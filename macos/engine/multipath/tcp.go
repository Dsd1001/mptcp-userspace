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

// DialClientWithScheduler selects this endpoint's local sending policy. MPX/4
// Stable does not negotiate scheduler modes in the Core handshake.
func DialClientWithScheduler(ctx context.Context, addresses []string, token string, requested SchedulerMode) (*Session, error) {
	return DialClientWithPolicy(ctx, addresses, token, requested, nil)
}

func DialClientWithPolicy(ctx context.Context, addresses []string, token string, requested SchedulerMode, capacities []PathCapacity) (*Session, error) {
	return dialClientPolicy(ctx, addresses, token, requested, capacities, false)
}

func dialClientPolicy(ctx context.Context, addresses []string, token string, requested SchedulerMode, capacities []PathCapacity, productMux bool) (*Session, error) {
	mode, modeErr := ParseSchedulerMode(string(requested))
	if modeErr != nil {
		return nil, modeErr
	}
	if len(addresses) < 1 || len(addresses) > MaxCarriers {
		return nil, fmt.Errorf("require 1-%d carrier addresses", MaxCarriers)
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
	if productMux {
		key = productServiceKey(key)
	}
	var id sessionID
	if _, err = rand.Read(id[:]); err != nil {
		return nil, err
	}
	s := newSession(ctx, id, false, nil, mode)
	s.productMux = productMux
	if mode == SchedulerWeighted {
		for i, capacity := range capacities {
			s.pathCapacities[uint64(i+1)] = capacity
		}
	}
	first := -1
	var last error
	for i, address := range addresses {
		s.recordDialAttempt(uint64(i+1), address)
		c, e := dialProductCarrier(ctx, address, productMux)
		if e == nil {
			stopClose := context.AfterFunc(ctx, func() { c.Close() })
			capacity := PathCapacity{}
			if mode == SchedulerWeighted {
				capacity = capacities[i]
			}
			sc, he := clientHandshakePolicy(c, key, id, uint64(i+1), true, mode, capacity)
			stopClose()
			if he == nil {
				e = s.addCarrier(uint64(i+1), address, sc)
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
		s.recordDialError(uint64(i+1), address, e)
		if ctx.Err() != nil {
			break
		}
	}
	if first < 0 {
		s.Close()
		return nil, fmt.Errorf("no authenticated Landing carrier: %w", last)
	}
	for i, address := range addresses {
		go s.maintainCarrier(uint64(i+1), address, key)
	}
	if productMux {
		s.startProductStreamPool()
	}
	return s, nil
}

func (s *Session) maintainCarrier(id uint64, address string, key []byte) {
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

		// Candidate Generations are allocated independently from Highest Accepted
		// Generation. Failed candidates do not advance accepted Session state.
		s.mu.Lock()
		generation, ok := s.nextCarrierCandidateGenerationLocked(id)
		s.mu.Unlock()
		if !ok {
			s.recordDialError(id, address, fmt.Errorf("%w: carrier generation exhausted", ErrCarrierConflict))
			return
		}

		s.recordDialAttempt(id, address)
		conn, err := dialProductCarrier(s.ctx, address, s.productMux)
		if err == nil {
			var sc *secureConn
			stopClose := context.AfterFunc(s.ctx, func() { conn.Close() })
			capacity := PathCapacity{}
			if s.scheduler.configured == SchedulerWeighted {
				capacity = s.pathCapacities[id]
			}
			expected, haveExpected := s.expectedPeerLimits()
			var expectedPtr *handshakeLimits
			if haveExpected {
				expectedPtr = &expected
			}
			sc, err = clientHandshakePolicyGenerationExpected(conn, key, s.id, id, generation, false, s.scheduler.configured, capacity, expectedPtr)
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
		// Candidate JOIN rejection is Carrier-scoped and does not mutate the live
		// Session. Local scheduler policy is intentionally absent from Core wire state.
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
	uotBackend                           *net.UDPAddr
	maxSessions                          int
	schedulerDefault                     SchedulerMode
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
	return NewServerWithScheduler(ctx, token, backend, maxSessions, SchedulerAuto)
}

// NewServerWithScheduler selects the Landing endpoint's local sending policy.
// MPX/4 Protocol Version 4 Stable deliberately does not negotiate scheduler
// modes in Core. A RECEIVE_CAPACITY_HINT may still let Auto select Weighted
// behavior for server-to-client traffic without changing Core semantics.
func NewServerWithScheduler(ctx context.Context, token, backend string, maxSessions int, requested SchedulerMode) (*Server, error) {
	mode, err := ParseSchedulerMode(string(requested))
	if err != nil {
		return nil, err
	}
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
	return &Server{ctx: child, cancel: cancel, key: key, backend: backend, maxSessions: maxSessions, schedulerDefault: mode, sessions: make(map[sessionID]*Session), handshakes: make(map[net.Conn]bool)}, nil
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
	carrier, productMux, err := readProductCarrier(c)
	key := srv.key
	if productMux {
		key = productServiceKey(key)
	}
	var h incomingHandshake
	if err == nil {
		h, err = readHandshake(carrier, key)
	}
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

	// Admission lookup is read-only until CLIENT_FINISHED has authenticated the
	// peer. MPX/4 forbids attaching unauthenticated Carrier state to a live Session.
	srv.mu.Lock()
	for id, existing := range srv.sessions {
		select {
		case <-existing.Done():
			delete(srv.sessions, id)
		default:
		}
	}
	existing := srv.sessions[h.id]
	rejectCode := uint64(0)
	if srv.ctx.Err() != nil {
		rejectCode = mpx4ErrInternal
	} else if h.create {
		switch {
		case h.generation != 0:
			rejectCode = mpx4ErrCarrierConflict
		case existing != nil:
			rejectCode = mpx4ErrSessionConflict
		case len(srv.sessions) >= srv.maxSessions:
			rejectCode = mpx4ErrResourceLimit
		}
	} else if existing == nil {
		rejectCode = mpx4ErrSessionNotFound
	} else if existing.productMux != productMux {
		rejectCode = mpx4ErrSessionConflict
	} else if err := existing.validateCarrierAdmission(h.carrier, h.generation, h.client.limits()); err != nil {
		rejectCode = mpx4ErrCarrierConflict
		var failure *mpx4Failure
		if errors.As(err, &failure) {
			rejectCode = failure.code
		}
	}
	srv.mu.Unlock()

	sc, err := h.finish(key, rejectCode)
	if err != nil {
		srv.mu.Lock()
		switch rejectCode {
		case mpx4ErrCarrierConflict:
			srv.handshakeOutcomeLocked("carrier_generation_conflict")
		case mpx4ErrSessionNotFound:
			srv.handshakeOutcomeLocked("session_missing")
		case mpx4ErrSessionConflict:
			srv.handshakeOutcomeLocked("session_conflict")
		case mpx4ErrResourceLimit:
			srv.handshakeOutcomeLocked("session_or_carrier_capacity")
		case mpx4ErrInternal:
			srv.handshakeOutcomeLocked("server_closing")
		default:
			if errors.Is(err, ErrAuthentication) {
				srv.handshakeOutcomeLocked("authentication_failed")
			} else {
				srv.handshakeOutcomeLocked("handshake_failed")
			}
		}
		srv.mu.Unlock()
		return err
	}

	// Authentication is complete. Re-check under the registry lock to close the
	// race between simultaneous CREATE/JOIN handshakes, then mutate Session state.
	srv.mu.Lock()
	s := srv.sessions[h.id]
	if h.create {
		if s != nil || len(srv.sessions) >= srv.maxSessions || srv.ctx.Err() != nil {
			srv.handshakeOutcomeLocked("session_capacity_or_conflict")
			srv.mu.Unlock()
			sc.Close()
			return &ResourceLimitError{Reason: "session_capacity_or_conflict"}
		}
		serverMode := srv.schedulerDefault
		if serverMode == SchedulerAuto && h.client.hasReceiveCapacityHint {
			// The published Capacity Hint extension is unilateral and safely
			// ignorable. Auto may use it as local evidence and select Weighted.
			serverMode = SchedulerWeighted
		}
		handler := srv.openBackend
		if productMux {
			handler = srv.openProductBackend
		}
		s = newSession(srv.ctx, h.id, true, handler, serverMode)
		s.productMux = productMux
		srv.sessions[h.id] = s
		srv.handshakeOutcomeLocked("session_created")
	} else {
		if s == nil || s.productMux != productMux {
			srv.handshakeOutcomeLocked("session_missing")
			srv.mu.Unlock()
			sc.Close()
			return ErrSessionExpired
		}
		srv.handshakeOutcomeLocked("session_joined")
	}
	srv.mu.Unlock()

	if err := s.addCarrier(h.carrier, c.RemoteAddr().String(), sc); err != nil {
		if errors.Is(err, ErrCarrierConflict) {
			srv.mu.Lock()
			srv.handshakeOutcomeLocked("carrier_generation_conflict")
			srv.mu.Unlock()
		}
		return err
	}
	return nil
}

func (srv *Server) openBackend(st *Stream) {
	ctx, cancel := context.WithTimeout(st.s.ctx, 5*time.Second)
	c, err := PlainDial(ctx, srv.backend)
	cancel()
	if err != nil {
		st.s.mu.Lock()
		st.s.resetLocked(st, mpx4ErrInternal, true)
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
