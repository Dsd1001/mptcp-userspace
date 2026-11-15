package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"mptcp-desktop/engine/multipath"
	"mptcp-desktop/engine/tunnelconfig"
)

// Public v1.2 command surface. Legacy helpers remain regression fixtures for
// Core transport behavior but are not reachable from the shipped executable.
func runTunnelCommand(ctx context.Context, args []string, input io.Reader) error {
	if len(args) == 0 {
		return errors.New("usage: mptcp-client run [inlet-id...] | validate [inlet-id...] | run-managed | validate-managed | version | doctor (JSON stdin)")
	}
	command := args[0]
	switch command {
	case "version":
		if len(args) != 1 {
			return errors.New("version takes no arguments")
		}
		emit(Event{Kind: "ready", Mode: sharedTunnelKind, Version: multipath.Version, SourceID: multipath.SourceID, WireProtocol: multipath.WireProtocol,
			Message: "MPTCP Tunnel " + multipath.Version + "; one Session, shared Carriers; tunnel-only; MPX/4 Stable"})
		return nil
	case "doctor", "doctor-userspace":
		if len(args) != 1 {
			return errors.New("doctor takes no arguments")
		}
		if err := ensureUserspaceFileLimit(); err != nil {
			return err
		}
		emit(Event{Kind: "ready", Mode: sharedTunnelKind, Message: userspaceEnvironmentMessage()})
		return nil
	case "run", "validate", "run-tunnel", "validate-tunnel":
		c, err := tunnelconfig.Decode(input)
		if err != nil {
			return err
		}
		c, err = c.Select(args[1:])
		if err != nil {
			return err
		}
		if command == "validate" || command == "validate-tunnel" {
			emit(Event{Kind: "ready", Mode: sharedTunnelKind, TotalProfiles: len(c.Inlets), Message: "共享隧道与业务入口配置有效"})
			return nil
		}
		return runSharedTunnel(ctx, SharedTunnelConfig(c))
	case "run-managed", "validate-managed":
		if len(args) != 1 {
			return errors.New("managed selection belongs in inlet_ids in JSON stdin")
		}
		in, err := tunnelconfig.DecodeManagedInput(input)
		if err != nil {
			return err
		}
		profile, err := tunnelconfig.Fetch(ctx, in.URL)
		if err != nil {
			return err
		}
		c, err := profile.Config.Select(in.InletIDs)
		if err != nil {
			return err
		}
		if command == "validate-managed" {
			emit(Event{Kind: "ready", Mode: sharedTunnelKind, ProfileID: profile.ProfileID, ProfileName: profile.DisplayName, TotalProfiles: len(c.Inlets), Message: "远端共享隧道配置有效"})
			return nil
		}
		return runSharedTunnel(ctx, SharedTunnelConfig(c))
	case "run-bundle", "validate-bundle":
		return errors.New("1.2.1 已移除独立 Profile/多组 MPX Bundle；请配置一个共享隧道和多个业务入口，不能自动合并旧的 Landing 密钥")
	default:
		return fmt.Errorf("unsupported tunnel command %q", command)
	}
}
