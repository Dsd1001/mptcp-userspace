package multipath

import (
	"context"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type oneAcceptExhaustion struct {
	net.Listener
	armed atomic.Bool
}

func (l *oneAcceptExhaustion) Accept() (net.Conn, error) {
	if l.armed.Swap(false) {
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: &os.SyscallError{Syscall: "accept", Err: syscall.EMFILE}}
	}
	return l.Listener.Accept()
}

// Explicit fault injection, not a claim that production encountered EMFILE.
// No host fd limit is changed and no host-wide exhaustion is attempted.
func TestTemporaryAcceptExhaustionMustPreserveSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backend, _ := echoBackend(t)
	server, err := NewServer(ctx, testToken, backend, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	raw, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &oneAcceptExhaustion{Listener: raw}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	addresses := make([]string, 6)
	for i := range addresses {
		addresses[i] = raw.Addr().String()
	}
	s, err := DialClient(ctx, addresses, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.WaitPaths(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if _, err = transfer(s, 32769); err != nil {
		t.Fatal(err)
	}
	listener.armed.Store(true)
	probe, err := PlainDial(ctx, raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	time.Sleep(250 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("temporary accept error shut down every carrier: serve_error=%v client=%+v admission=%+v", err, s.Snapshot(), server.AdmissionSnapshot())
	default:
	}
	if _, err = transfer(s, 32769); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Paths != 6 {
		t.Fatal("temporary exhaustion closed an established carrier")
	}
	t.Log("temporary accept exhaustion preserved six carriers and subsequent business transfer")
}
