package multipath

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// DialClient creates one persistent session, then maintains one ordinary TCP
// carrier per configured Relay. No stream is silently re-opened after restart.
func DialClient(ctx context.Context, addresses []string, token string) (*Session, error) {
	return DialClientWithScheduler(ctx, addresses, token, SchedulerAuto)
}

// DialClientWithScheduler authenticates one immutable configured policy for
// both directions. Auto may choose different effective roles per direction.
func DialClientWithScheduler(ctx context.Context, addresses []string, token string, requested SchedulerMode) (*Session, error) {
	return DialClientWithPolicy(ctx, addresses, token, requested, nil)
}

func DialClientWithPolicy(ctx context.Context, addresses []string, token string, requested SchedulerMode, capacities []PathCapacity) (*Session, error) {
	mode, modeErr := ParseSchedulerMode(string(requested))
	if modeErr != nil {
		return nil, modeErr
	}
	if len(addresses) < 1 || len(addresses) > 8 {
		return nil, errors.New("require 1-8 carrier addresses")
	}
	if mode == SchedulerWeighted {
		if len(capacities) != len(addresses) {
			return nil, errors.New("weighted scheduler requires one capacity record per carrier")
		}
		for _, capacity := range capacities {
			if err := capacity.validateWeighted(); err != nil {
				return nil, err
			}
		}
	} else {
		capacities = nil
	}
	key, err := ParseKey(token)
	if err != nil {
		return nil, err
	}
	var id sessionID
	if _, err = rand.Read(id[:]); err != nil {
		return nil, err
	}
	s := newSession(ctx, id, false, nil, mode)
	if mode == SchedulerWeighted {
		for i, capacity := range capacities {
			s.pathCapacities[i+1] = capacity
		}
	}
	first := -1
	var last error
	for i, address := range addresses {
		s.recordDialAttempt(byte(i+1), address)
		c, e := PlainDial(ctx, address)
		if e == nil {
			stopClose := context.AfterFunc(ctx, func() { c.Close() })
			capacity := PathCapacity{}
			if mode == SchedulerWeighted {
				capacity = capacities[i]
			}
			sc, he := clientHandshakePolicy(c, key, id, byte(i+1), true, mode, capacity)
			stopClose()
			if he == nil {
				e = s.addCarrier(byte(i+1), address, sc)
				if e == nil {
					first = i
					break
				}
			} else {
				e = he
			}
			if e != nil {
				c.Close()
			}
		}
		last = e
		s.recordDialError(byte(i+1), address, e)
		if ctx.Err() != nil {
			break
		}
	}
	if first < 0 {
		s.Close()
		return nil, fmt.Errorf("no authenticated Landing carrier: %w", last)
	}
	for i, address := range addresses {
		go s.maintainCarrier(byte(i+1), address, key)
	}
	return s, nil
}

func (s *Session) maintainCarrier(id byte, address string, key []byte) {
	delay := 200 * time.Millisecond
	for {
		s.mu.Lock()
		c := s.paths[id]
		closed := s.closed
		active := c != nil && c.active
		s.mu.Unlock()
		if closed {
			return
		}
		if active {
			select {
			case <-s.ctx.Done():
				return
			case <-c.done:
			}
			delay = 200 * time.Millisecond
		}
		if s.ctx.Err() != nil {
			return
		}
		s.recordDialAttempt(id, address)
		conn, err := PlainDial(s.ctx, address)
		if err == nil {
			var sc *secureConn
			stopClose := context.AfterFunc(s.ctx, func() { conn.Close() })
			capacity := PathCapacity{}
			if s.scheduler.configured == SchedulerWeighted {
				capacity = s.pathCapacities[id]
			}
			sc, err = clientHandshakePolicy(conn, key, s.id, id, false, s.scheduler.configured, capacity)
			stopClose()
			if err == nil {
				err = s.addCarrier(id, address, sc)
			}
			if err != nil {
				conn.Close()
			}
		}
		if err == nil {
			delay = 200 * time.Millisecond
			continue
		}
		if errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrSchedulerMismatch) {
			s.stop(err)
			return
		}
		s.recordDialError(id, address, err)
		timer := time.NewTimer(delay)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(5*time.Second, 2*delay)
	}
}

// Server owns a session registry shared with the independent UDP listener.
type Server struct {
	udpStarted                           bool
	ctx                                  context.Context
	cancel                               context.CancelFunc
	key                                  []byte
	backend                              string
	maxSessions                          int
	mu                                   sync.Mutex
	sessions                             map[sessionID]*Session
	handshakes                           map[net.Conn]bool
	sources                              map[string]*handshakeSource
	admissionAccepted, admissionRejected uint64
	admissionReasons                     map[string]uint64
	handshakeOutcomes                    map[string]uint64
	acceptRetries                        uint64
	lastAcceptError                      string
	listener                             net.Listener
	started                              bool
	wg                                   sync.WaitGroup
}

func NewServer(ctx context.Context, token, backend string, maxSessions int) (*Server, error) {
	key, err := ParseKey(token)
	if err != nil {
		return nil, err
	}
	if maxSessions < 1 || maxSessions > 16 {
		return nil, errors.New("max_sessions must be 1-16")
	}
	if _, _, err = net.SplitHostPort(backend); err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	return &Server{ctx: child, cancel: cancel, key: key, backend: backend, maxSessions: maxSessions, sessions: make(map[sessionID]*Session), handshakes: make(map[net.Conn]bool)}, nil
}

func (srv *Server) session(id sessionID) *Session {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	s := srv.sessions[id]
	if s != nil {
		select {
		case <-s.Done():
			return nil
		default:
		}
	}
	return s
}

func (srv *Server) Serve(listener net.Listener) error {
	srv.mu.Lock()
	if srv.started {
		srv.mu.Unlock()
		listener.Close()
		return errors.New("server already started")
	}
	srv.started = true
	srv.listener = listener
	srv.mu.Unlock()
	defer srv.Close()
	monitor := make(chan struct{})
	go func() {
		select {
		case <-srv.ctx.Done():
			listener.Close()
		case <-monitor:
		}
	}()
	defer close(monitor)
	for {
		c, err := listener.Accept()
		if err != nil {
			if srv.ctx.Err() != nil {
				return nil
			}
			if TemporaryAcceptError(err) {
				srv.mu.Lock()
				srv.acceptRetries++
				srv.lastAcceptError = err.Error()
				srv.mu.Unlock()
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-srv.ctx.Done():
					timer.Stop()
					return nil
				case <-timer.C:
				}
				continue
			}
			return err
		}
		if t, ok := c.(*net.TCPConn); ok {
			_ = t.SetNoDelay(true)
		}
		source := handshakeSourceKey(c.RemoteAddr())
		srv.mu.Lock()
		if srv.ctx.Err() != nil {
			srv.mu.Unlock()
			c.Close()
			return nil
		}
		if !srv.admitLocked(source, time.Now()) {
			srv.mu.Unlock()
			c.Close()
			continue
		}
		srv.handshakes[c] = true
		srv.wg.Add(1)
		srv.mu.Unlock()
		go func() {
			defer srv.wg.Done()
			defer func() { srv.mu.Lock(); delete(srv.handshakes, c); srv.releaseAdmissionLocked(source); srv.mu.Unlock() }()
			if err := srv.attach(c); err != nil {
				c.Close()
			}
		}()
	}
}

func (srv *Server) attach(c net.Conn) error {
	h, err := readHandshake(c, srv.key)
	if err != nil {
		reason := "io_error"
		var ne net.Error
		if errors.Is(err, ErrAuthentication) {
			reason = "authentication_failed"
		} else if errors.Is(err, ErrProtocol) {
			reason = "protocol_rejected"
		} else if errors.As(err, &ne) && ne.Timeout() {
			reason = "handshake_timeout"
		}
		srv.mu.Lock()
		srv.handshakeOutcomeLocked(reason)
		srv.mu.Unlock()
		return err
	}
	srv.mu.Lock()
	// Prune only terminated sessions, never an idle but connected session.
	for id, s := range srv.sessions {
		select {
		case <-s.Done():
			delete(srv.sessions, id)
		default:
		}
	}
	s := srv.sessions[h.id]
	status := byte(0)
	if srv.ctx.Err() != nil {
		status = 3
	} else if h.create {
		if s != nil || len(srv.sessions) >= srv.maxSessions {
			status = 3
		} else {
			s = newSession(srv.ctx, h.id, true, srv.openBackend, h.scheduler)
			srv.sessions[h.id] = s
		}
	} else if s == nil {
		status = 2
	} else if s.scheduler.configured != h.scheduler {
		status = 4
	}
	if status == 0 {
		if h.create {
			srv.handshakeOutcomeLocked("session_created")
		} else {
			srv.handshakeOutcomeLocked("session_joined")
		}
	} else if status == 4 {
		srv.handshakeOutcomeLocked("scheduler_mode_conflict")
	} else if status == 2 {
		srv.handshakeOutcomeLocked("session_missing")
	} else {
		srv.handshakeOutcomeLocked("session_capacity_or_conflict")
	}
	srv.mu.Unlock()
	sc, err := h.finish(srv.key, status)
	if err != nil {
		return err
	}
	return s.addCarrier(h.carrier, c.RemoteAddr().String(), sc)
}

func (srv *Server) openBackend(st *Stream) {
	ctx, cancel := context.WithTimeout(st.s.ctx, 5*time.Second)
	c, err := PlainDial(ctx, srv.backend)
	cancel()
	if err != nil {
		st.s.mu.Lock()
		st.s.resetLocked(st, 3, true)
		st.s.mu.Unlock()
		return
	}
	defer c.Close()
	if !st.s.accept(st) {
		return
	}
	Bridge(st.s.ctx, st, c.(HalfConn), 15*time.Minute)
}

func (srv *Server) Close() error {
	srv.cancel()
	srv.mu.Lock()
	if srv.listener != nil {
		srv.listener.Close()
	}
	for c := range srv.handshakes {
		c.Close()
	}
	sessions := make([]*Session, 0, len(srv.sessions))
	for _, s := range srv.sessions {
		sessions = append(sessions, s)
	}
	srv.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
	srv.wg.Wait()
	return nil
}

func (srv *Server) Snapshots() []Stats {
	srv.mu.Lock()
	ss := make([]*Session, 0, len(srv.sessions))
	for _, s := range srv.sessions {
		ss = append(ss, s)
	}
	srv.mu.Unlock()
	out := make([]Stats, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Snapshot())
	}
	return out
}

type HalfConn interface {
	net.Conn
	CloseWrite() error
}

type timedWriter struct {
	dst  HalfConn
	last *atomic.Int64
}

func (w timedWriter) Write(p []byte) (int, error) {
	if err := w.dst.SetWriteDeadline(time.Now().Add(45 * time.Second)); err != nil {
		return 0, err
	}
	n, err := w.dst.Write(p)
	if n > 0 {
		w.last.Store(time.Now().UnixNano())
	}
	return n, err
}

// Bridge is byte-transparent and supports independent half-close directions.
// Idle and cancellation close both sockets and wait for both copy workers.
func Bridge(ctx context.Context, a, b HalfConn, idle time.Duration) {
	defer a.Close()
	defer b.Close()
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	done := make(chan error, 2)
	copyOne := func(dst, src HalfConn) {
		_, err := io.CopyBuffer(timedWriter{dst: dst, last: &last}, src, make([]byte, MaxPayload))
		if err == nil {
			err = dst.CloseWrite()
		}
		done <- err
	}
	go copyOne(a, b)
	go copyOne(b, a)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	cancelled := ctx.Done()
	for completed := 0; completed < 2; {
		select {
		case err := <-done:
			completed++
			if err != nil {
				a.Close()
				b.Close()
			}
		case <-cancelled:
			a.Close()
			b.Close()
			cancelled = nil
		case now := <-ticker.C:
			if idle > 0 && now.Sub(time.Unix(0, last.Load())) >= idle {
				a.Close()
				b.Close()
			}
		}
	}
}
