package multipath

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// Deliberately abortive native TCP Close is NOT a valid performance backend
// lifecycle. It is only a fault-injection control validating that our test
// distinguishes truncated TCP delivery from MPX/4 DATA corruption.
func TestWO01AbortiveTCPBackendIsFaultInjectionOnly(t *testing.T) {
	listener, err := PlainListen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	finish := make(chan error, 1)
	go func() {
		peer, err := listener.Accept()
		if err != nil {
			finish <- err
			return
		}
		tcp, ok := peer.(*net.TCPConn)
		if !ok {
			peer.Close()
			finish <- errors.New("expected loopback TCP socket")
			return
		}
		// SO_LINGER=0 intentionally aborts pending bytes. Unlike production
		// fixture, this must NOT be reported as normal full delivery.
		if err = tcp.SetLinger(0); err != nil {
			tcp.Close()
			finish <- err
			return
		}
		_, err = tcp.Write([]byte{0x41})
		tcp.Close()
		finish <- err
	}()
	client, err := PlainDial(context.Background(), listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var expected [2]byte
	_, readErr := io.ReadFull(client, expected[:])
	if readErr == nil {
		t.Fatal("abortive producer passed complete-byte accounting")
	}
	if err = <-finish; err != nil {
		t.Fatalf("fault source could not send prefix: %v", err)
	}
}
