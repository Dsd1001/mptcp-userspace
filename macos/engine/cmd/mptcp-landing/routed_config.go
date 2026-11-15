package main

import (
	"errors"
	"mptcp-desktop/engine/multipath"
	"net"
)

func (c Config) validateRouted() error {
	if c.SchemaVersion != 1 {
		return errors.New("routed egress requires schema_version=1")
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
	for _, route := range c.TCPRoutes {
		bh, bp, _ := net.SplitHostPort(route.Backend)
		ip := net.ParseIP(bh)
		if lp == bp && (lh == bh || ip != nil && ip.IsLoopback() && net.ParseIP(lh).IsUnspecified()) {
			return errors.New("TCP route loops back to the tunnel listener")
		}
	}
	return nil
}
