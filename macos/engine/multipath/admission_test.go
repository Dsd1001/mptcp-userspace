package multipath

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestAdmissionPerSourceBurstRefillAndBoundedTable(t *testing.T) {
	srv := &Server{handshakes: make(map[net.Conn]bool)}
	now := time.Now()
	for i := 0; i < handshakeSourceLimit; i++ {
		if !srv.admitLocked("source-a", now) {
			t.Fatal("early concurrency rejection")
		}
	}
	if srv.admitLocked("source-a", now) {
		t.Fatal("source monopolized admission")
	}
	if !srv.admitLocked("source-b", now) {
		t.Fatal("unrelated source blocked")
	}
	for i := 0; i < handshakeSourceLimit; i++ {
		srv.releaseAdmissionLocked("source-a")
	}
	for i := 0; i < 8; i++ {
		if !srv.admitLocked("source-a", now) {
			t.Fatal("burst unexpectedly exhausted")
		}
		srv.releaseAdmissionLocked("source-a")
	}
	if srv.admitLocked("source-a", now) {
		t.Fatal("unbounded source attempt rate")
	}
	if !srv.admitLocked("source-a", now.Add(time.Second)) {
		t.Fatal("token bucket did not recover")
	}
	for i := 0; len(srv.sources) < handshakeSourceMax; i++ {
		srv.admitLocked(fmt.Sprintf("source-%d", i), now)
	}
	if srv.admitLocked("table-overflow", now) || len(srv.sources) != handshakeSourceMax {
		t.Fatal("source table unbounded")
	}
	for _, b := range srv.sources {
		b.active = 0
		b.updated = now.Add(-2 * time.Minute)
	}
	if !srv.admitLocked("fresh-after-expiry", now) || len(srv.sources) != 1 {
		t.Fatal("source entries not reclaimed")
	}
}

type admissionStubConn struct {
	net.Conn
	id int
}

func TestAdmissionGlobalCapAndAddressNormalization(t *testing.T) {
	srv := &Server{handshakes: make(map[net.Conn]bool)}
	for i := 0; i < handshakeGlobalLimit; i++ {
		srv.handshakes[&admissionStubConn{id: i}] = true
	}
	if srv.admitLocked("fresh", time.Now()) {
		t.Fatal("global handshake cap ignored")
	}
	key := func(ip string, port int) string {
		return handshakeSourceKey(&net.TCPAddr{IP: net.ParseIP(ip), Port: port})
	}
	if key("192.0.2.1", 1) != key("::ffff:192.0.2.1", 65000) {
		t.Fatal("IPv4-mapped or port quota bypass")
	}
	if key("2001:db8::1", 1) != key("2001:db8::abcd", 2) {
		t.Fatal("IPv6 /64 quota bypass")
	}
	if key("2001:db8:0:1::1", 1) == key("2001:db8:0:2::1", 1) {
		t.Fatal("different /64 sources conflated")
	}
}

// All sockets are local. IPv4 slow clients and an IPv6 legitimate client
// exercise actual independent source quotas, without changing host aliases.
func TestSlowHandshakesDoNotStarveOtherSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend, count := echoBackend(t)
	srv, err := NewServer(ctx, testToken, backend, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	l, err := PlainListen(ctx, "[::]:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var slow []net.Conn
	defer func() {
		for _, c := range slow {
			c.Close()
		}
	}()
	for i := 0; i < 80; i++ {
		c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		slow = append(slow, c)
	}
	deadline := time.Now().Add(time.Second)
	var before AdmissionStats
	for time.Now().Before(deadline) {
		before = srv.AdmissionSnapshot()
		if before.Rejected >= 72 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if before.Active != handshakeSourceLimit || before.Rejected < 72 {
		t.Fatalf("slow source not bounded: %+v", before)
	}
	if count.Load() != 0 || len(srv.Snapshots()) != 0 {
		t.Fatal("unauthenticated backend/session allocation")
	}
	start := time.Now()
	client, err := DialClient(ctx, []string{net.JoinHostPort("::1", port)}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err = transfer(client, 32769); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) >= time.Second {
		t.Fatal("legitimate source waited for attack deadlines")
	}
	if count.Load() != 1 {
		t.Fatal("wrong backend allocation count")
	}
	t.Logf("legitimate transfer=%v admission=%+v", time.Since(start), srv.AdmissionSnapshot())
	deadline = time.Now().Add(2500 * time.Millisecond)
	for srv.AdmissionSnapshot().Active != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if srv.AdmissionSnapshot().Active != 0 {
		t.Fatal("expired slow hello not reclaimed")
	}
}

func TestPartialHelloDeadlineAndOldWireRejection(t *testing.T) {
	key, _ := ParseKey(testToken)
	for _, legacy := range []bool{false, true} {
		a, b := net.Pipe()
		result := make(chan error, 1)
		started := time.Now()
		go func() { _, err := readHandshake(b, key); b.Close(); result <- err }()
		if legacy {
			h := make([]byte, helloSize)
			copy(h, "MPX1")
			h[4] = 1
			h[5] = 1
			h[6] = 1
			binary.BigEndian.PutUint32(h[40:44], 1048576)
			binary.BigEndian.PutUint32(h[44:], MaxPayload)
			a.Write(h)
		} else {
			a.Write([]byte("MPX2"))
		}
		err := <-result
		a.Close()
		if legacy {
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("old wire accepted: %v", err)
			}
		} else {
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() || time.Since(started) > 2500*time.Millisecond {
				t.Fatalf("partial hello timeout: %v %v", time.Since(started), err)
			}
		}
	}
}
