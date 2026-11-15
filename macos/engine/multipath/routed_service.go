package multipath

// Routed TCP is a product service carried as ordinary MPX/4 Stream DATA.
// It does not add a second transport, change Core frames, or expose arbitrary
// destination dialing to the client. A route is selected once per connection.
import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	productServiceRoutedUOT  byte = 5
	productServiceRoutedTCP  byte = 3
	productServiceRouteProbe byte = 4
	MaxTCPRoutes                  = 64
	routedEarlyDataLimit          = 64 << 10 // includes the ten-byte service request
)

var (
	ErrRouteNotFound     = errors.New("routed TCP destination is not allowed")
	ErrRouteBusy         = errors.New("routed TCP destination is at capacity")
	ErrRouteDial         = errors.New("routed TCP destination connection failed")
	ErrRoutedUnsupported = errors.New("peer does not support routed TCP service")
)

// TCPRoute is an operator-controlled allowlist entry on the tunnel egress.
// Backend may be an IP or DNS name; it is resolved for each new connection.
// Existing connections are never moved or replayed after a DNS change.
type TCPRoute struct {
	UDPBackend     string `json:"udp_backend,omitempty"`
	ID             uint32 `json:"id"`
	Name           string `json:"name,omitempty"`
	Backend        string `json:"backend"`
	Weight         int    `json:"weight,omitempty"`
	MaxConnections int    `json:"max_connections,omitempty"`
}

// RoutePolicy is endpoint-local scheduling policy, not a Core wire parameter.
type RoutePolicy struct {
	ID     uint32 `json:"id"`
	Weight int    `json:"weight,omitempty"`
}

type routedBackend struct {
	config      TCPRoute
	connections chan struct{}
	dialing     chan struct{}
}

func normalizedRouteWeight(weight int) (int, error) {
	if weight == 0 {
		return 1, nil
	}
	if weight < 1 || weight > 16 {
		return 0, errors.New("route weight must be 1..16")
	}
	return weight, nil
}

func validateRouteBackend(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("route backend must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || host == "" || len(host) > 253 {
		return errors.New("invalid route backend host or port")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
			return errors.New("route backend must be unicast")
		}
		return nil
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("invalid route backend DNS name")
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return errors.New("invalid route backend DNS name")
			}
		}
	}
	return nil
}

func ValidateTCPRoutes(routes []TCPRoute) error {
	if len(routes) < 1 || len(routes) > MaxTCPRoutes {
		return errors.New("require 1..64 TCP routes")
	}
	seen := make(map[uint32]bool, len(routes))
	for _, route := range routes {
		if route.ID == 0 || seen[route.ID] {
			return errors.New("route IDs must be nonzero and unique")
		}
		seen[route.ID] = true
		if len(route.Name) > 128 {
			return errors.New("route name is too long")
		}
		if _, err := normalizedRouteWeight(route.Weight); err != nil {
			return err
		}
		if route.MaxConnections < 0 || route.MaxConnections > MaxStreams {
			return errors.New("route max_connections must be 1..2048, or 0 for default 512")
		}
		if route.Backend == "" && route.UDPBackend == "" {
			return errors.New("route requires a TCP backend or udp_backend")
		}
		if route.Backend != "" {
			if err := validateRouteBackend(route.Backend); err != nil {
				return fmt.Errorf("route %d TCP: %w", route.ID, err)
			}
		}
		if route.UDPBackend != "" {
			if err := validateRouteBackend(route.UDPBackend); err != nil {
				return fmt.Errorf("route %d UDP: %w", route.ID, err)
			}
		}
	}
	return nil
}

// NewRoutedServerWithScheduler creates a routed-only egress. It deliberately
// has no legacy fallback backend, no native UDP listener, and no open proxy.
func NewRoutedServerWithScheduler(ctx context.Context, token string, routes []TCPRoute, maxSessions int, mode SchedulerMode) (*Server, error) {
	if err := ValidateTCPRoutes(routes); err != nil {
		return nil, err
	}
	srv, err := newServerWithScheduler(ctx, token, "", maxSessions, mode)
	if err != nil {
		return nil, err
	}
	srv.tcpRoutes = make(map[uint32]*routedBackend, len(routes))
	for _, config := range routes {
		config.Weight, _ = normalizedRouteWeight(config.Weight)
		if config.MaxConnections == 0 {
			config.MaxConnections = 512
		}
		srv.tcpRoutes[config.ID] = &routedBackend{
			config:      config,
			connections: make(chan struct{}, config.MaxConnections),
			dialing:     make(chan struct{}, min(8, config.MaxConnections)),
		}
	}
	return srv, nil
}

// DialClientWithRoutedPolicy maintains exactly one Session over the supplied
// Carrier paths. The capability probe fails closed against legacy-only peers.
func DialClientWithRoutedPolicy(ctx context.Context, addresses []string, token string, mode SchedulerMode, capacities []PathCapacity, routes []RoutePolicy) (*Session, error) {
	if err := ValidateRoutePolicies(routes); err != nil {
		return nil, err
	}
	s, err := dialClientPolicy(ctx, addresses, token, mode, capacities, true, routes)
	if err != nil {
		return nil, err
	}
	st, err := s.openProductService(ctx, productServiceRouteProbe)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("routed TCP capability probe: %w", err)
	}
	st.Close()
	return s, nil
}

// OpenRoutedTCP returns an independent byte stream. It sends the route request
// immediately (including for server-first protocols). At most 64 KiB may be
// committed before the private reply pump validates the egress connection result.
func (s *Session) OpenRoutedTCP(parent context.Context, routeID uint32) (*Stream, error) {
	return s.openRoutedService(parent, routeID, productServiceRoutedTCP)
}

func (s *Session) OpenRoutedUOT(parent context.Context, routeID uint32) (*Stream, error) {
	return s.openRoutedService(parent, routeID, productServiceRoutedUOT)
}

func (s *Session) openRoutedService(parent context.Context, routeID uint32, kind byte) (*Stream, error) {
	s.mu.Lock()
	allowed := s.productMux && !s.server && routeID != 0 && s.routeGroups[routeID] != nil
	s.mu.Unlock()
	if !allowed {
		return nil, ErrRouteNotFound
	}
	ctx, cancel := context.WithTimeout(parent, productServiceResponseTimeout)
	defer cancel()
	st, err := s.takeProductStream(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	err = s.bindRouteLocked(st, routeID)
	s.mu.Unlock()
	if err != nil {
		st.Close()
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	st.armProductReply(kind, deadline)
	st.SetWriteDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { st.SetWriteDeadline(time.Now()) })
	request := [10]byte{'M', 'P', 'S', '1', kind, 0}
	binary.BigEndian.PutUint32(request[6:], routeID)
	err = writeAll(st, request[:])
	if !stop() || ctx.Err() != nil {
		st.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, context.Canceled
	}
	st.SetWriteDeadline(time.Time{})
	if err != nil {
		st.Close()
		return nil, err
	}
	// Do not require the application to call Read before a large Write can
	// progress. This pump consumes exactly the service reply, never payload.
	go func() { _ = st.waitProductReply() }()
	return st, nil
}

func (srv *Server) openRoutedBackend(st *Stream, kind byte, finish func(byte)) {
	// Unlike an unused pre-open, a partially supplied route header has a short
	// deadline. The bounded early data is left in the existing MPX receive pages.
	st.SetDeadline(time.Now().Add(productServiceResponseTimeout))
	var raw [4]byte
	if _, err := io.ReadFull(st, raw[:]); err != nil {
		return
	}
	id := binary.BigEndian.Uint32(raw[:])
	route := srv.tcpRoutes[id]
	if route == nil {
		finish(5)
		return
	}
	target := route.config.Backend
	if kind == productServiceRoutedUOT {
		target = route.config.UDPBackend
	}
	if target == "" {
		finish(2)
		return
	}

	st.s.mu.Lock()
	err := st.s.bindRouteLocked(st, id)
	st.s.mu.Unlock()
	if err != nil {
		finish(5)
		return
	}
	select {
	case route.connections <- struct{}{}:
		defer func() { <-route.connections }()
	default:
		finish(4)
		return
	}
	ctx, cancel := context.WithTimeout(st.s.ctx, productServiceResponseTimeout)
	defer cancel()
	// Connection slots bound the waiting population; a healthy connection
	// burst waits for a dial permit instead of being rejected immediately.
	select {
	case route.dialing <- struct{}{}:
	case <-ctx.Done():
		finish(4)
		return
	}
	var c net.Conn
	if kind == productServiceRoutedUOT {
		c, err = (&net.Dialer{}).DialContext(ctx, "udp", target)
	} else {
		c, err = PlainDial(ctx, target)
	}
	cancel()
	<-route.dialing
	if err != nil {
		finish(3)
		return
	}
	defer c.Close()
	// Re-arm after dialing so a slow but successful dial can still report its
	// result. A failed route never replays bytes to another backend.
	st.SetDeadline(time.Now().Add(productServiceResponseTimeout))
	if productServiceReply(st, kind, 0) != nil {
		return
	}
	st.SetDeadline(time.Time{})
	if kind == productServiceRoutedUOT {
		udp, ok := c.(*net.UDPConn)
		if !ok {
			return
		}
		serveUOTBackend(st.s.ctx, st, udp)
		return
	}
	Bridge(st.s.ctx, st, c.(HalfConn), 15*time.Minute)
}
