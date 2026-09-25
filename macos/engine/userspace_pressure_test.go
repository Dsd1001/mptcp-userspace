package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"mptcp-desktop/engine/multipath"
)

type pressuredListener struct {
	net.Listener
	armed atomic.Bool
}

func (l *pressuredListener) Accept() (net.Conn, error) {
	if l.armed.Swap(false) {
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: &os.SyscallError{Syscall: "accept", Err: syscall.EMFILE}}
	}
	return l.Listener.Accept()
}
func TestLocalAcceptPressurePreservesExistingForwarding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backend, err := multipath.PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		for {
			c, e := backend.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c); c.(multipath.HalfConn).CloseWrite() }()
		}
	}()
	key, err := multipath.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	server, err := multipath.NewServer(ctx, key, backend.Addr().String(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	landing, err := multipath.PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(landing)
	session, err := multipath.DialClient(ctx, []string{landing.Addr().String(), landing.Addr().String()}, key)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err = session.WaitPaths(ctx, 2); err != nil {
		t.Fatal(err)
	}
	local, err := multipath.PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &pressuredListener{Listener: local}
	done := make(chan error, 1)
	go func() { done <- serveUserspace(ctx, listener, session) }()
	c, err := multipath.PlainDial(ctx, local.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(4 * time.Second))
	exchange := func() {
		t.Helper()
		payload := bytes.Repeat([]byte{0, 255, 17, 41}, 257)
		if _, e := c.Write(payload); e != nil {
			t.Fatal(e)
		}
		got := make([]byte, len(payload))
		if _, e := io.ReadFull(c, got); e != nil || !bytes.Equal(payload, got) {
			t.Fatalf("existing stream failed: %v", e)
		}
	}
	exchange()
	listener.armed.Store(true)
	probe, err := multipath.PlainDial(ctx, local.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	probe.Close()
	deadline := time.Now().Add(time.Second)
	for session.Snapshot().Resources.Waits[multipath.LimitLocalAccept] == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	select {
	case err = <-done:
		t.Fatalf("local accept ended forwarding: %v", err)
	default:
	}
	exchange()
	if session.Snapshot().Paths != 2 || session.Snapshot().Resources.Waits[multipath.LimitLocalAccept] == 0 {
		t.Fatal("missing pressure evidence or lost carriers")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("forwarding did not shut down on explicit cancellation")
	}
}
