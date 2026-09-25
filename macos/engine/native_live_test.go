//go:build darwin

package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// Opt-in handshake-only check. Deployment details stay outside the source tree.
func TestNativeLiveHandshake(t *testing.T) {
	path := os.Getenv("MPTCP_DIAGNOSTIC_PROFILE")
	if path == "" {
		t.Skip("set MPTCP_DIAGNOSTIC_PROFILE for a payload-free live test")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	config, err := readConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for i, relay := range config.Relays {
		start := time.Now()
		c, err := nativeDialPrimary(ctx, relay)
		if err != nil {
			t.Logf("Relay %d handshake failed: %v", i+1, err)
			continue
		}
		t.Logf("Relay %d initial handshake: %v", i+1, time.Since(start))
		c.Close()
	}
	start := time.Now()
	c, err := nativeDial(ctx, config.Relays)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	t.Logf("staggered handshake: %v", time.Since(start))
	raw, err := c.file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var nodelay int
	var optionErr error
	if err := raw.Control(func(fd uintptr) {
		nodelay, optionErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY)
	}); err != nil {
		t.Fatal(err)
	}
	if optionErr != nil || nodelay != 1 {
		t.Fatalf("TCP_NODELAY=%d err=%v", nodelay, optionErr)
	}
	if n, err := c.pathStatus(); err != nil || n < 1 {
		t.Fatalf("MPTCP paths=%d err=%v", n, err)
	}
	if _, err := readPCBWithLimit(1); !errors.Is(err, syscall.EOVERFLOW) {
		t.Fatalf("bounded snapshot should report overflow: %v", err)
	}
	if n, err := c.pathStatus(); err != nil || n < 1 {
		t.Fatalf("telemetry overflow affected the connection: paths=%d err=%v", n, err)
	}
	if data, err := readPCB(); err != nil {
		t.Fatal(err)
	} else {
		t.Logf("actual PCB snapshot: %d bytes", len(data))
	}
}
