package multipath

// This file is a product service envelope, not an MPX/4 extension. Legacy
// connections still begin with MPX\x00. Opt-in connections put this envelope
// outside the unchanged MPX handshake and carry service headers as Stream DATA.
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	productCarrierPreface              = "MPTU\x00\x00\x00\x01"
	productServiceProbe           byte = 0
	productServiceTCP             byte = 1
	productServiceUOT             byte = 2
	productMaxUOTFlows                 = 512
	productPreopenPoolSize             = 16
	productPreopenMaxAge               = 2 * time.Minute
	productPreopenRefillInterval       = 5 * time.Second
	productServiceSelectTimeout        = 3 * time.Minute
	productServiceResponseTimeout      = 5 * time.Second
)

var ErrUOTUnsupported = errors.New("Landing UoT capability is disabled or unsupported")

// Domain separation binds service selection to MPX's authenticated handshake.
// Stripping/inserting the public envelope cannot convert a product session into
// a raw TCP session: those sessions use different authentication keys.
func productServiceKey(key []byte) []byte {
	return mac(key, "mptcp-userspace/product-services/v1/mpx-key")
}

func DialClientWithUOTPolicy(ctx context.Context, addresses []string, token string, mode SchedulerMode, capacities []PathCapacity) (*Session, error) {
	s, err := dialClientPolicy(ctx, addresses, token, mode, capacities, true)
	if err != nil {
		return nil, fmt.Errorf("UoT requires a compatible updated Landing: %w", err)
	}
	return s, nil
}

func dialProductCarrier(ctx context.Context, address string, productMux bool) (net.Conn, error) {
	c, err := PlainDial(ctx, address)
	if err != nil || !productMux {
		return c, err
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if err = c.SetWriteDeadline(time.Now().Add(handshakeTimeout)); err == nil {
		err = writeAll(c, []byte(productCarrierPreface))
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

type productReadConn struct {
	net.Conn
	reader io.Reader
}

func (c *productReadConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func readProductCarrier(c net.Conn) (net.Conn, bool, error) {
	if err := c.SetReadDeadline(time.Now().Add(preHandshakeTimeout)); err != nil {
		return nil, false, err
	}
	var first [4]byte
	if _, err := io.ReadFull(c, first[:]); err != nil {
		return nil, false, err
	}
	switch string(first[:]) {
	case "MPX\x00":
		return &productReadConn{Conn: c, reader: io.MultiReader(bytes.NewReader(first[:]), c)}, false, nil
	case productCarrierPreface[:4]:
		var version [4]byte
		if _, err := io.ReadFull(c, version[:]); err != nil {
			return nil, false, err
		}
		if string(version[:]) != productCarrierPreface[4:] {
			return nil, false, ErrProtocol
		}
		return c, true, nil
	default:
		return nil, false, ErrProtocol
	}
}

// EnableUOT configures a fixed UDP destination. It opens no public UDP socket.
// Native UDP is an independent Landing capability and may remain enabled.
func (srv *Server) EnableUOT(backend string) error {
	a, err := net.ResolveUDPAddr("udp", backend)
	if err != nil {
		return err
	}
	if a.Port < 1 || a.IP == nil || a.IP.IsUnspecified() || a.IP.IsMulticast() {
		return errors.New("UoT backend requires a unicast IP and nonzero port")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.started || srv.ctx.Err() != nil {
		return errors.New("configure UoT before starting Landing")
	}
	srv.uotBackend = a
	return nil
}

func (s *Session) startProductStreamPool() {
	s.mu.Lock()
	if s.server || !s.productMux || s.closed || s.productPoolKick != nil {
		s.mu.Unlock()
		return
	}
	kick := make(chan struct{}, 1)
	s.productPoolKick = kick
	s.mu.Unlock()
	go s.productStreamPoolLoop(kick)
	select {
	case kick <- struct{}{}:
	default:
	}
}

func (s *Session) productStreamPoolLoop(kick <-chan struct{}) {
	ticker := time.NewTicker(productPreopenRefillInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-kick:
			s.refillProductStreamPool(time.Now())
		case now := <-ticker.C:
			s.refillProductStreamPool(now)
		}
	}
}

func (s *Session) refillProductStreamPool(now time.Time) {
	var stale []*Stream
	s.mu.Lock()
	if s.closed || s.server || !s.productMux {
		s.mu.Unlock()
		return
	}
	fresh := s.productPool[:0]
	for _, st := range s.productPool {
		if st == nil || st.closed || now.Sub(st.createdAt) >= productPreopenMaxAge {
			if st != nil && !st.closed {
				stale = append(stale, st)
			}
			continue
		}
		fresh = append(fresh, st)
	}
	s.productPool = fresh
	missing := max(0, productPreopenPoolSize-len(s.productPool)-s.productPoolOpening)
	s.productPoolOpening += missing
	s.mu.Unlock()

	for _, st := range stale {
		st.Close()
	}
	for i := 0; i < missing; i++ {
		go s.openProductPoolStream()
	}
}

func (s *Session) openProductPoolStream() {
	ctx, cancel := context.WithTimeout(s.ctx, productServiceResponseTimeout)
	st, err := s.Open(ctx)
	cancel()

	keep := false
	s.mu.Lock()
	s.productPoolOpening = max(0, s.productPoolOpening-1)
	if err == nil && !s.closed && s.productMux && !s.server && st != nil && !st.closed && len(s.productPool) < productPreopenPoolSize {
		s.productPool = append(s.productPool, st)
		keep = true
	}
	s.mu.Unlock()

	if err == nil && st != nil && !keep {
		st.Close()
	}
	// Failed pre-opens retry on the bounded periodic refill. Immediate
	// self-kicking here could turn a hard Stream/admission limit into a tight
	// retry loop.
}

func (s *Session) takeProductStream(ctx context.Context) (*Stream, error) {
	s.startProductStreamPool()
	for {
		s.mu.Lock()
		var st *Stream
		if len(s.productPool) > 0 {
			st = s.productPool[0]
			copy(s.productPool, s.productPool[1:])
			s.productPool = s.productPool[:len(s.productPool)-1]
		}
		kick := s.productPoolKick
		valid := st != nil && !st.closed && time.Since(st.createdAt) < productPreopenMaxAge
		s.mu.Unlock()

		if kick != nil {
			select {
			case kick <- struct{}{}:
			default:
			}
		}
		if st == nil {
			return s.Open(ctx)
		}
		if valid {
			return st, nil
		}
		st.Close()
	}
}

// OpenTCP preserves raw byte-transparent Streams for legacy sessions.
func (s *Session) OpenTCP(ctx context.Context) (*Stream, error) {
	if !s.productMux {
		return s.Open(ctx)
	}
	return s.openProductService(ctx, productServiceTCP)
}

func productServiceResponseError(kind byte, response []byte) error {
	if len(response) != 6 || string(response[:4]) != "MPA1" || response[4] != kind {
		return errors.New("invalid Landing product service response")
	}
	if response[5] == 0 {
		return nil
	}
	if response[5] == 1 && (kind == productServiceProbe || kind == productServiceUOT) {
		return ErrUOTUnsupported
	}
	return fmt.Errorf("Landing rejected product service %d (status %d)", kind, response[5])
}

func (s *Session) openProductService(parent context.Context, kind byte) (*Stream, error) {
	if !s.productMux {
		return nil, ErrUOTUnsupported
	}
	ctx, cancel := context.WithTimeout(parent, productServiceResponseTimeout)
	defer cancel()
	st, err := s.takeProductStream(ctx)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	replyDeadline := time.Time{}
	if kind == productServiceProbe {
		replyDeadline = deadline
	}
	st.armProductReply(kind, replyDeadline)
	st.SetWriteDeadline(deadline)
	request := []byte{'M', 'P', 'S', '1', kind, 0}
	err = writeAll(st, request)
	st.SetWriteDeadline(time.Time{})
	if err == nil && kind == productServiceProbe {
		err = st.waitProductReply()
	}
	if err != nil {
		st.Close()
		return nil, err
	}
	// TCP/UoT payload may now be sent immediately. The first Stream.Read
	// consumes and validates MPA1 internally before exposing backend bytes.
	return st, nil
}

func productServiceReply(st *Stream, kind, status byte) error {
	return writeAll(st, []byte{'M', 'P', 'A', '1', kind, status})
}

func (srv *Server) openProductBackend(st *Stream) {
	defer st.Close()
	if !st.s.accept(st) {
		return
	}
	st.SetDeadline(time.Now().Add(productServiceSelectTimeout))
	var request [6]byte
	if _, err := io.ReadFull(st, request[:]); err != nil || string(request[:4]) != "MPS1" || request[5] != 0 {
		return
	}
	kind := request[4]
	finish := func(status byte) {
		if productServiceReply(st, kind, status) == nil {
			st.CloseWrite()
			// Wait for the client to consume the reply before resetting/closing.
			var end [1]byte
			st.Read(end[:])
		}
	}
	switch kind {
	case productServiceProbe:
		if srv.uotBackend == nil {
			finish(1)
		} else {
			finish(0)
		}
	case productServiceTCP:
		ctx, cancel := context.WithTimeout(st.s.ctx, 5*time.Second)
		c, err := PlainDial(ctx, srv.backend)
		cancel()
		if err != nil {
			finish(3)
			return
		}
		defer c.Close()
		if productServiceReply(st, kind, 0) != nil {
			return
		}
		st.SetDeadline(time.Time{})
		Bridge(st.s.ctx, st, c.(HalfConn), 15*time.Minute)
	case productServiceUOT:
		if srv.uotBackend == nil {
			finish(1)
			return
		}
		st.s.mu.Lock()
		if st.s.uotActiveFlows >= productMaxUOTFlows {
			st.s.mu.Unlock()
			finish(4)
			return
		}
		st.s.uotActiveFlows++
		st.s.mu.Unlock()
		defer func() { st.s.mu.Lock(); st.s.uotActiveFlows--; st.s.mu.Unlock() }()
		c, err := net.DialUDP("udp", nil, srv.uotBackend)
		if err != nil {
			finish(3)
			return
		}
		defer c.Close()
		if productServiceReply(st, kind, 0) != nil {
			return
		}
		st.SetDeadline(time.Time{})
		serveUOTBackend(st.s.ctx, st, c)
	default:
		finish(2)
	}
}
