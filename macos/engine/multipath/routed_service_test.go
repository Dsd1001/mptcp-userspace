package multipath

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func routedBannerBackend(t *testing.T, banner string) (string, *atomic.Int64) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	listener, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	count := new(atomic.Int64)
	t.Cleanup(func() { cancel(); listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			count.Add(1)
			go func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				if _, err := io.WriteString(conn, banner); err != nil {
					return
				}
				_, _ = io.Copy(conn, conn)
				conn.(HalfConn).CloseWrite()
			}()
		}
	}()
	return listener.Addr().String(), count
}

func routedFixture(t *testing.T, routes []TCPRoute, policies []RoutePolicy, paths int) (*Server, *Session, []*testRelay) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := NewRoutedServerWithScheduler(ctx, testToken, routes, 2, SchedulerAggregate)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(listener)
	t.Cleanup(func() { srv.Close() })
	var relays []*testRelay
	var addresses []string
	for i := 0; i < paths; i++ {
		relay := newTestRelay(t, listener.Addr().String(), 8<<20, 2*time.Millisecond)
		relays = append(relays, relay)
		addresses = append(addresses, relay.listener.Addr().String())
	}
	s, err := DialClientWithRoutedPolicy(ctx, addresses, testToken, SchedulerAggregate, nil, policies)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wait, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if err := s.WaitPaths(wait, paths); err != nil {
		t.Fatal(err)
	}
	return srv, s, relays
}

func routedRoundtrip(s *Session, id uint32, banner string, size int) error {
	st, err := s.OpenRoutedTCP(context.Background(), id)
	if err != nil {
		return err
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(15 * time.Second))
	// Write more than the early allowance BEFORE calling Read. The internal
	// service response pump must permit progress without an application Read.
	payload := bytes.Repeat([]byte{byte(id), 0, 255, 'M', 'P', 'S', '1', 3}, (size+7)/8)[:size]
	if _, err = st.Write(payload); err != nil {
		return err
	}
	if err = st.CloseWrite(); err != nil {
		return err
	}
	got, err := io.ReadAll(st)
	if err != nil {
		return err
	}
	expected := append([]byte(banner), payload...)
	if !bytes.Equal(got, expected) {
		return fmt.Errorf("route %d payload mismatch: %d/%d", id, len(got), len(expected))
	}
	return nil
}

func TestRoutedThreeTargetsOneSessionSixCarriers(t *testing.T) {
	var routes []TCPRoute
	var policies []RoutePolicy
	var counts []*atomic.Int64
	for id := uint32(1); id <= 3; id++ {
		backend, count := routedBannerBackend(t, fmt.Sprintf("route-%d\n", id))
		counts = append(counts, count)
		routes = append(routes, TCPRoute{ID: id, Backend: backend})
		policies = append(policies, RoutePolicy{ID: id})
	}
	srv, s, relays := routedFixture(t, routes, policies, 6)
	waitProductPool(t, s, productPreopenPoolSize)
	for _, count := range counts {
		if count.Load() != 0 {
			t.Fatal("pre-open pool dialed a backend")
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for id := uint32(1); id <= 3; id++ {
		wg.Add(1)
		go func(id uint32) { defer wg.Done(); errs <- routedRoundtrip(s, id, fmt.Sprintf("route-%d\n", id), 1<<20) }(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshots := srv.Snapshots()
	if len(snapshots) != 1 || snapshots[0].Paths != 6 || s.Snapshot().Paths != 6 {
		t.Fatalf("expected one Session and six Carriers: server=%d client=%d", len(snapshots), s.Snapshot().Paths)
	}
	for _, relay := range relays {
		relay.mu.Lock()
		n := len(relay.conns)
		relay.mu.Unlock()
		if n != 2 {
			t.Fatalf("expected one incoming/outgoing TCP pair per Relay, got %d sockets", n)
		}
	}
	before := s.Snapshot()
	if err := routedRoundtrip(s, 1, "route-1\n", 8<<20); err != nil {
		t.Fatal(err)
	}
	after := s.Snapshot()
	used := 0
	for i, path := range after.PathStats {
		if path.Sent > before.PathStats[i].Sent+32<<10 {
			used++
		}
	}
	if used != 6 {
		t.Fatalf("single routed stream used %d/6 carriers", used)
	}
	t.Logf("verified: 3 targets, 1 Session, 6 TCP Carrier pairs, single Stream DATA used %d paths", used)
}

func TestRoutedServerFirstFailureAndCapacityIsolation(t *testing.T) {
	good, _ := routedBannerBackend(t, "hello\n")
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bad := reserve.Addr().String()
	reserve.Close()
	_, s, _ := routedFixture(t, []TCPRoute{{ID: 1, Backend: good, MaxConnections: 1}, {ID: 2, Backend: good}, {ID: 3, Backend: bad}}, []RoutePolicy{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 99}}, 2)
	first, err := s.OpenRoutedTCP(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	first.SetDeadline(time.Now().Add(5 * time.Second))
	greeting := make([]byte, 6)
	if _, err = io.ReadFull(first, greeting); err != nil || string(greeting) != "hello\n" {
		t.Fatalf("server-first: %q %v", greeting, err)
	}
	for _, tc := range []struct {
		id   uint32
		want error
	}{{1, ErrRouteBusy}, {3, ErrRouteDial}, {99, ErrRouteNotFound}} {
		st, err := s.OpenRoutedTCP(context.Background(), tc.id)
		if err != nil {
			t.Fatal(err)
		}
		st.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = st.Read(make([]byte, 1))
		st.Close()
		if !errors.Is(err, tc.want) {
			t.Fatalf("route %d: got %v want %v", tc.id, err, tc.want)
		}
		if err = routedRoundtrip(s, 2, "hello\n", 128<<10); err != nil {
			t.Fatalf("healthy route affected: %v", err)
		}
	}
	if _, err = s.OpenRoutedTCP(context.Background(), 100); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("unknown local route: %v", err)
	}
	if s.Err() != nil {
		t.Fatalf("route failure ended Session: %v", s.Err())
	}
}

func TestRoutedCarrierFailureKeepsExistingStream(t *testing.T) {
	backend, _ := routedBannerBackend(t, "")
	_, s, relays := routedFixture(t, []TCPRoute{{ID: 1, Backend: backend}}, []RoutePolicy{{ID: 1}}, 6)
	st, err := s.OpenRoutedTCP(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(15 * time.Second))
	payload := bytes.Repeat([]byte("carrier-reinjection\x00"), 400000)
	written := make(chan error, 1)
	go func() {
		_, e := st.Write(payload)
		if e == nil {
			e = st.CloseWrite()
		}
		written <- e
	}()
	timer := time.AfterFunc(20*time.Millisecond, func() { relays[0].fail() })
	defer timer.Stop()
	got, err := io.ReadAll(st)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("existing Stream corrupted after Carrier failure: len=%d err=%v", len(got), err)
	}
	if err = <-written; err != nil {
		t.Fatal(err)
	}
	if s.Err() != nil {
		t.Fatalf("Session failed: %v", s.Err())
	}
}

func TestRoutedUnsupportedPeerAndInvalidTables(t *testing.T) {
	_, target, _ := productTestServer(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s, err := DialClientWithRoutedPolicy(ctx, []string{target}, testToken, SchedulerAuto, nil, []RoutePolicy{{ID: 1}}); !errors.Is(err, ErrRoutedUnsupported) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("legacy peer did not fail closed: %v", err)
	}
	for _, routes := range [][]TCPRoute{nil, {{ID: 0, Backend: "127.0.0.1:1"}}, {{ID: 1, Backend: "0.0.0.0:1"}}, {{ID: 1, Backend: "bad/name:80"}}, {{ID: 1, Backend: "127.0.0.1:1", Weight: 17}}, {{ID: 1, Backend: "127.0.0.1:1"}, {ID: 1, Backend: "127.0.0.1:2"}}} {
		if ValidateTCPRoutes(routes) == nil {
			t.Fatalf("invalid table accepted: %#v", routes)
		}
	}
	if err := ValidateTCPRoutes([]TCPRoute{{ID: 1, Backend: "landing.example.test:443"}, {ID: 2, Backend: "[::1]:8443"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRoutedRawCarrierRejected(t *testing.T) {
	backend, _ := routedBannerBackend(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv, err := NewRoutedServerWithScheduler(ctx, testToken, []TCPRoute{{ID: 1, Backend: backend}}, 1, SchedulerAuto)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	l, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	if s, err := DialClient(ctx, []string{l.Addr().String()}, testToken); err == nil {
		s.Close()
		t.Fatal("raw fixed-backend protocol accepted by routed-only egress")
	}
}
