package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"mptcp-desktop/engine/multipath"
)

const sharedTunnelKind = "shared_mpx4_tunnel"

var errSharedListener = errors.New("shared inlet listener stopped")

type SharedTunnelTransport struct {
	TransportKey  string  `json:"transport_key"`
	Relays        []Relay `json:"relays"`
	SchedulerMode string  `json:"scheduler_mode,omitempty"`
}

type SharedTunnelInlet struct {
	ID             string `json:"id"`
	Name           string `json:"name,omitempty"`
	ListenPort     int    `json:"listen_port"`
	RouteID        uint32 `json:"route_id"`
	Weight         int    `json:"weight,omitempty"`
	MaxConnections int    `json:"max_connections,omitempty"`
}

type SharedTunnelConfig struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	Tunnel        SharedTunnelTransport `json:"tunnel"`
	Inlets        []SharedTunnelInlet   `json:"inlets"`
}

func (c SharedTunnelConfig) transportConfig(port int) Config {
	mode := c.Tunnel.SchedulerMode
	if mode == "" {
		mode = string(multipath.SchedulerAuto)
	}
	return Config{SchemaVersion: 3, Mode: "userspace_multipath", ListenPort: port, Relays: c.Tunnel.Relays, TransportKey: c.Tunnel.TransportKey, SchedulerMode: &mode}
}

func (c SharedTunnelConfig) policies() []multipath.RoutePolicy {
	out := make([]multipath.RoutePolicy, 0, len(c.Inlets))
	for _, inlet := range c.Inlets {
		out = append(out, multipath.RoutePolicy{ID: inlet.RouteID, Weight: inlet.Weight})
	}
	return out
}

func (c SharedTunnelConfig) validate() error {
	if c.SchemaVersion != 1 || c.Kind != sharedTunnelKind {
		return errors.New("shared tunnel requires schema_version=1 and kind=shared_mpx4_tunnel")
	}
	if len(c.Inlets) < 1 || len(c.Inlets) > multipath.MaxTCPRoutes {
		return errors.New("require 1..64 tunnel inlets")
	}
	ids, ports := make(map[string]bool), make(map[int]bool)
	for _, inlet := range c.Inlets {
		if inlet.ID == "" || len(inlet.ID) > 64 || ids[inlet.ID] {
			return errors.New("inlet IDs must be nonempty, unique, and at most 64 bytes")
		}
		if len(inlet.Name) > 128 {
			return errors.New("inlet name is too long")
		}
		for _, ch := range inlet.ID {
			if ch <= ' ' || ch == '/' || ch == '\\' {
				return errors.New("invalid inlet ID")
			}
		}
		ids[inlet.ID] = true
		if ports[inlet.ListenPort] {
			return errors.New("inlet listen ports must be unique")
		}
		ports[inlet.ListenPort] = true
		if inlet.MaxConnections < 0 || inlet.MaxConnections > multipath.MaxStreams {
			return errors.New("inlet max_connections must be 1..2048, or 0 for default 512")
		}
		if err := c.transportConfig(inlet.ListenPort).validate(); err != nil {
			return fmt.Errorf("inlet %s: %w", inlet.ID, err)
		}
	}
	return multipath.ValidateRoutePolicies(c.policies())
}

func readSharedTunnel(reader io.Reader) (SharedTunnelConfig, error) {
	var c SharedTunnelConfig
	data, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil {
		return c, err
	}
	if len(data) > 64<<10 {
		return c, errors.New("shared tunnel configuration exceeds 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return c, errors.New("configuration must contain one JSON object")
	}
	return c, c.validate()
}

type SharedInletStats struct {
	ID         string `json:"id"`
	RouteID    uint32 `json:"route_id"`
	ListenPort int    `json:"listen_port"`
	Active     int64  `json:"active_connections"`
	Accepted   uint64 `json:"accepted_connections"`
	Rejected   uint64 `json:"rejected_connections"`
}

type sharedInletRuntime struct {
	config   SharedTunnelInlet
	listener net.Listener
	slots    chan struct{}
	active   atomic.Int64
	accepted atomic.Uint64
	rejected atomic.Uint64
}

type sharedTunnelRuntime struct {
	ctx     context.Context
	mu      sync.Mutex
	session *multipath.Session
	locals  map[net.Conn]bool
	inlets  []*sharedInletRuntime
	slots   chan struct{}
	accepts sync.WaitGroup
	workers sync.WaitGroup
	fatal   chan error
}

// Bind every port before opening a single Carrier. A conflict rolls back all
// listeners, without a partially running inlet set or a remote Session.
func newSharedTunnelRuntime(ctx context.Context, c SharedTunnelConfig) (*sharedTunnelRuntime, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	r := &sharedTunnelRuntime{ctx: ctx, locals: make(map[net.Conn]bool), slots: make(chan struct{}, multipath.MaxStreams-32), fatal: make(chan error, len(c.Inlets))}
	for _, inlet := range c.Inlets {
		listener, err := multipath.PlainListen(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(inlet.ListenPort)))
		if err != nil {
			for _, opened := range r.inlets {
				opened.listener.Close()
			}
			return nil, fmt.Errorf("inlet %s listen: %w", inlet.ID, err)
		}
		limit := inlet.MaxConnections
		if limit == 0 {
			limit = 512
		}
		r.inlets = append(r.inlets, &sharedInletRuntime{config: inlet, listener: listener, slots: make(chan struct{}, limit)})
	}
	return r, nil
}

func (r *sharedTunnelRuntime) closeSockets() {
	for _, inlet := range r.inlets {
		inlet.listener.Close()
	}
	r.mu.Lock()
	connections := make([]net.Conn, 0, len(r.locals))
	for c := range r.locals {
		connections = append(connections, c)
	}
	r.mu.Unlock()
	for _, c := range connections {
		c.Close()
	}
}

func (r *sharedTunnelRuntime) inletStats() []SharedInletStats {
	out := make([]SharedInletStats, 0, len(r.inlets))
	for _, inlet := range r.inlets {
		out = append(out, SharedInletStats{ID: inlet.config.ID, RouteID: inlet.config.RouteID, ListenPort: inlet.config.ListenPort, Active: inlet.active.Load(), Accepted: inlet.accepted.Load(), Rejected: inlet.rejected.Load()})
	}
	return out
}

func (r *sharedTunnelRuntime) serve(inlet *sharedInletRuntime) {
	defer r.accepts.Done()
	for {
		conn, err := inlet.listener.Accept()
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			if multipath.TemporaryAcceptError(err) {
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-r.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				continue
			}
			select {
			case r.fatal <- fmt.Errorf("%w: inlet %s: %v", errSharedListener, inlet.config.ID, err):
			default:
			}
			return
		}
		inlet.accepted.Add(1)
		r.mu.Lock()
		s := r.session
		if s == nil || r.ctx.Err() != nil {
			r.mu.Unlock()
			inlet.rejected.Add(1)
			conn.Close()
			continue
		}
		select {
		case r.slots <- struct{}{}:
		default:
			r.mu.Unlock()
			inlet.rejected.Add(1)
			conn.Close()
			continue
		}
		select {
		case inlet.slots <- struct{}{}:
		default:
			<-r.slots
			r.mu.Unlock()
			inlet.rejected.Add(1)
			conn.Close()
			continue
		}
		r.locals[conn] = true
		r.mu.Unlock()
		inlet.active.Add(1)
		r.workers.Add(1)
		go func() {
			defer r.workers.Done()
			defer func() {
				conn.Close()
				r.mu.Lock()
				delete(r.locals, conn)
				r.mu.Unlock()
				inlet.active.Add(-1)
				<-inlet.slots
				<-r.slots
			}()
			local, ok := conn.(multipath.HalfConn)
			if !ok {
				inlet.rejected.Add(1)
				return
			}
			stream, err := s.OpenRoutedTCP(r.ctx, inlet.config.RouteID)
			if err != nil {
				inlet.rejected.Add(1)
				return
			}
			multipath.Bridge(r.ctx, local, stream, 15*time.Minute)
		}()
	}
}

func (r *sharedTunnelRuntime) runSession(s *multipath.Session) error {
	r.mu.Lock()
	r.session = s
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.session == s {
			r.session = nil
		}
		r.mu.Unlock()
		s.Close()
	}()
	for _, inlet := range r.inlets {
		emit(Event{Kind: "listening", Mode: sharedTunnelKind, ProfileID: inlet.config.ID, ProfileName: inlet.config.Name, ListenPort: inlet.config.ListenPort, Message: fmt.Sprintf("shared route %d", inlet.config.RouteID)})
	}
	stats := func() {
		v := s.Snapshot()
		emit(Event{Kind: "stats", Mode: sharedTunnelKind, Paths: v.Paths, Connections: int64(v.Connections), Sent: int64(v.Sent), Received: int64(v.Received), PathStats: v.PathStats, Routes: v.Routes, Inlets: r.inletStats(), SchedulerStats: v.SchedulerStats, Resources: &v.Resources, Lifecycle: &v.Lifecycle, Version: multipath.Version, SourceID: multipath.SourceID, WireProtocol: multipath.WireProtocol})
	}
	stats()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case err := <-r.fatal:
			return err
		case <-s.Done():
			return fmt.Errorf("shared Session ended; existing connections will not be replayed: %w", s.Err())
		case <-ticker.C:
			stats()
		}
	}
}

func runSharedTunnel(parent context.Context, c SharedTunnelConfig) error {
	if err := ensureUserspaceFileLimit(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	r, err := newSharedTunnelRuntime(ctx, c)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, r.closeSockets)
	defer func() {
		cancel()
		r.closeSockets()
		r.accepts.Wait()
		r.workers.Wait()
		stop()
	}()
	for _, inlet := range r.inlets {
		r.accepts.Add(1)
		go r.serve(inlet)
	}
	transport := c.transportConfig(c.Inlets[0].ListenPort)
	mode, _ := transport.schedulerMode()
	capacities, _ := transport.weightedCapacities()
	addresses := make([]string, len(c.Tunnel.Relays))
	for i, relay := range c.Tunnel.Relays {
		addresses[i] = net.JoinHostPort(relay.Host, strconv.Itoa(relay.Port))
	}
	failures := 0
	for ctx.Err() == nil {
		select {
		case err := <-r.fatal:
			return err
		default:
		}
		emit(Event{Kind: "connecting", Mode: sharedTunnelKind, Message: "one shared MPX/4 Session for all inlets"})
		s, dialErr := multipath.DialClientWithRoutedPolicy(ctx, addresses, c.Tunnel.TransportKey, mode, capacities, c.policies())
		if dialErr == nil {
			started := time.Now()
			dialErr = r.runSession(s)
			if time.Since(started) >= 30*time.Second {
				failures = 0
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		// Unsupported peers and invalid keys are not hidden by infinite retries.
		if errors.Is(dialErr, errSharedListener) || errors.Is(dialErr, multipath.ErrRoutedUnsupported) || errors.Is(dialErr, multipath.ErrAuthentication) {
			return dialErr
		}
		select {
		case err := <-r.fatal:
			return err
		default:
		}
		delay := bundleProfileRetryDelay(failures)
		failures++
		emit(Event{Kind: "retrying", Mode: sharedTunnelKind, RetryAttempt: failures, RetryAfterSeconds: int(delay / time.Second), Message: dialErr.Error(), Inlets: r.inletStats()})
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case err := <-r.fatal:
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
	return nil
}
