package main

import (
	"errors"
	"mptcp-desktop/engine/multipath"
	"net"
	"strings"
)

func (c Config) validateRouted() error {
	if c.SchemaVersion != 2 || c.Kind != "mpx4_tunnel_egress" {
		return errors.New("1.2.0 requires schema_version=2, kind=mpx4_tunnel_egress; legacy fixed-backend configurations are not converted")
	}
	if _, err := multipath.ParseKey(c.TransportKey); err != nil {
		return err
	}
	if c.MaxSessions < 1 || c.MaxSessions > 16 {
		return errors.New("max_sessions must be 1..16")
	}
	if _, err := multipath.ParseSchedulerMode(c.SchedulerMode); err != nil {
		return err
	}
	if err := endpoint(c.ListenTCP, true); err != nil {
		return err
	}
	if c.BackendTCP != "" || c.BackendUDP != "" || c.ListenUDP != "" || c.UDPEnabled || c.UOTEnabled {
		return errors.New("tcp_routes is routed-only: omit fixed backends and UDP/UoT settings")
	}
	if err := multipath.ValidateTCPRoutes(c.TCPRoutes); err != nil {
		return err
	}
	lh, lp, _ := net.SplitHostPort(c.ListenTCP)
	listenIP := net.ParseIP(lh)
	for _, route := range c.TCPRoutes {
		if route.Backend == "" {
			continue
		}
		bh, bp, _ := net.SplitHostPort(route.Backend)
		ip := net.ParseIP(bh)
		localName := strings.EqualFold(strings.TrimSuffix(bh, "."), "localhost")
		sameHost := bh == lh || ip != nil && listenIP != nil && ip.Equal(listenIP)
		if lp == bp && (sameHost || (localName || ip != nil && ip.IsLoopback()) && (listenIP.IsUnspecified() || listenIP.IsLoopback())) {
			return errors.New("route backend loops back to the TCP tunnel listener")
		}
	}
	return nil
}
