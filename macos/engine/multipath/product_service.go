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
	productCarrierPreface      = "MPTU\x00\x00\x00\x01"
	productServiceProbe   byte = 0
	productServiceTCP     byte = 1
	productServiceUOT     byte = 2
	productMaxUOTFlows         = 512
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

// OpenTCP preserves raw byte-transparent Streams for legacy sessions.
func (s *Session) OpenTCP(ctx context.Context) (*Stream, error) {
	if !s.productMux {
		return s.Open(ctx)
	}
	return s.openProductService(ctx, productServiceTCP)
}

func (s *Session) openProductService(parent context.Context, kind byte) (*Stream, error) {
	if !s.productMux {
		return nil, ErrUOTUnsupported
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	st, err := s.Open(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { st.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	st.SetDeadline(deadline)
	request := []byte{'M', 'P', 'S', '1', kind, 0}
	var response [6]byte
	if err = writeAll(st, request); err == nil {
		_, err = io.ReadFull(st, response[:])
	}
	if err == nil && (string(response[:4]) != "MPA1" || response[4] != kind) {
		err = errors.New("invalid Landing product service response")
	}
	if err == nil && response[5] != 0 {
		if response[5] == 1 {
			err = ErrUOTUnsupported
		} else {
			err = fmt.Errorf("Landing rejected product service %d (status %d)", kind, response[5])
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		st.Close()
		return nil, err
	}
	stop()
	st.SetDeadline(time.Time{})
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
	st.SetDeadline(time.Now().Add(5 * time.Second))
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
