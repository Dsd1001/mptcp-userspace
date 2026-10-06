package multipath

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func productTestServer(t *testing.T, udpBackend string) (*Server, string, *atomic.Int64) {
	t.Helper()
	backend, count := echoBackend(t)
	srv, err := NewServer(context.Background(), testToken, backend, 4)
	if err != nil {
		t.Fatal(err)
	}
	if udpBackend != "" {
		if err = srv.EnableUOT(udpBackend); err != nil {
			t.Fatal(err)
		}
	}
	l, err := PlainListen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return srv, l.Addr().String(), count
}

func productTestClient(t *testing.T, addresses ...string) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Session lifetime is separate from the bounded path wait.
	s, err := DialClientWithUOTPolicy(context.Background(), addresses, testToken, SchedulerAggregate, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.WaitPaths(ctx, len(addresses)); err != nil {
		t.Fatal(err)
	}
	return s
}

func productTestTCPRoundtrip(t *testing.T, s *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := s.OpenTCP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(3 * time.Second))
	// Even a payload resembling the product header must remain opaque here.
	payload := []byte("MPS1\x02\x00opaque TCP bytes")
	if _, err = st.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(st, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("TCP changed: %q, %v", got, err)
	}
}

func TestProductUOTOverTCPRelaysAndCarrierRecovery(t *testing.T) {
	_, target, _ := productTestServer(t, udpEchoBackend(t))
	r1 := newTestRelay(t, target, 0, 0)
	r2 := newTestRelay(t, target, 0, 0)
	s := productTestClient(t, r1.listener.Addr().String(), r2.listener.Addr().String())
	u, err := StartClientUOT(s, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	apps := make([]*net.UDPConn, 2)
	for i := range apps {
		apps[i], err = net.DialUDP("udp", nil, u.Addr().(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		defer apps[i].Close()
	}
	productTestTCPRoundtrip(t, s)
	var wg sync.WaitGroup
	errorsOut := make(chan error, 2)
	for i, app := range apps {
		wg.Add(1)
		go func(i int, app *net.UDPConn) {
			defer wg.Done()
			for _, n := range []int{0, 1, 1200, 8000} {
				if err := udpRoundtrip(app, bytes.Repeat([]byte{byte(i + 1)}, n)); err != nil {
					errorsOut <- err
					return
				}
			}
		}(i, app)
	}
	wg.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Fatal(err)
	}
	if got := u.Snapshot(); got.Connections != 2 || got.Paths != 2 || got.Dropped != 0 {
		t.Fatalf("UoT snapshot: %+v", got)
	}
	// The association is attached to the two-carrier Session. The scheduler may
	// keep a small burst on one path; larger workloads use the same shared path
	// scheduler and do not require a per-packet path guarantee.
	if len(s.Snapshot().PathStats) != 2 {
		t.Fatalf("UoT did not retain both shared carriers: %+v", s.Snapshot().PathStats)
	}
	// Reuse the same UDP socket/association as a carrier leaves and rejoins.
	r1.fail()
	if err = udpRoundtrip(apps[0], []byte("one path remains")); err != nil {
		t.Fatal(err)
	}
	r1.recover()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = s.WaitPaths(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err = udpRoundtrip(apps[0], []byte("rejoined product carrier")); err != nil {
		t.Fatal(err)
	}
	if got := u.Snapshot().Connections; got != 2 {
		t.Fatalf("flows changed on reconnect: %d", got)
	}
	productTestTCPRoundtrip(t, s)
}

func TestProductUOTBackendSocketIsolation(t *testing.T) {
	backend, err := listenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		b := make([]byte, udpMax)
		for {
			_, addr, err := backend.ReadFromUDP(b)
			if err != nil {
				return
			}
			backend.WriteToUDP([]byte(addr.String()), addr)
		}
	}()
	_, target, _ := productTestServer(t, backend.LocalAddr().String())
	s := productTestClient(t, target)
	u, err := StartClientUOT(s, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	seen := make(map[string]bool)
	for i := 0; i < 2; i++ {
		app, err := net.DialUDP("udp", nil, u.Addr().(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		defer app.Close()
		app.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err = app.Write([]byte("source")); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 100)
		n, err := app.Read(b)
		if err != nil {
			t.Fatal(err)
		}
		if seen[string(b[:n])] {
			t.Fatal("local sources shared a Landing UDP socket")
		}
		seen[string(b[:n])] = true
	}
}

func TestProductLegacyAndDisabledUOT(t *testing.T) {
	_, target, count := productTestServer(t, "")
	legacy, err := DialClient(context.Background(), []string{target}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	productTestTCPRoundtrip(t, legacy)
	s := productTestClient(t, target)
	if _, err = StartClientUOT(s, "127.0.0.1:0"); !errors.Is(err, ErrUOTUnsupported) {
		t.Fatalf("disabled Landing not reported: %v", err)
	}
	productTestTCPRoundtrip(t, s)
	if count.Load() != 2 {
		t.Fatalf("UoT probe reached TCP backend: %d", count.Load())
	}
}

func TestProductEnvelopeCannotBeStrippedOrInjected(t *testing.T) {
	key, _ := ParseKey(testToken)
	for _, productOnWire := range []bool{false, true} {
		t.Run(map[bool]string{false: "stripped", true: "injected"}[productOnWire], func(t *testing.T) {
			srv, _, count := productTestServer(t, udpEchoBackend(t))
			a, b := net.Pipe()
			defer a.Close()
			done := make(chan error, 1)
			go func() { defer b.Close(); done <- srv.attach(b) }()
			a.SetDeadline(time.Now().Add(3 * time.Second))
			clientKey := key
			if productOnWire {
				if err := writeAll(a, []byte(productCarrierPreface)); err != nil {
					t.Fatal(err)
				}
			} else {
				clientKey = productServiceKey(key)
			}
			if c, err := clientHandshake(a, clientKey, sessionID{1}, 1, true); err == nil {
				c.Close()
				t.Fatal("service binding bypassed")
			}
			a.Close()
			if err := <-done; err == nil {
				t.Fatal("Landing accepted mismatched service key")
			}
			if len(srv.Snapshots()) != 0 || count.Load() != 0 {
				t.Fatal("failed auth mutated sessions/backend")
			}
		})
	}
}

func TestProductMalformedServiceIsStreamScoped(t *testing.T) {
	_, target, count := productTestServer(t, udpEchoBackend(t))
	s := productTestClient(t, target)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := s.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = st.Write([]byte("MPS1\x02\x01")); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err = st.Read(b[:]); err == nil {
		t.Fatal("reserved flags accepted")
	}
	st.Close()
	if count.Load() != 0 {
		t.Fatal("malformed service reached TCP backend")
	}
	productTestTCPRoundtrip(t, s)
}

func TestProductUOTRejectsOldLandingBeforePayload(t *testing.T) {
	key, _ := ParseKey(testToken)
	l, err := PlainListen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		// Pre-UoT Landing reads the MPX handshake directly.
		_, err = readHandshake(c, key)
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if s, err := DialClientWithUOTPolicy(ctx, []string{l.Addr().String()}, testToken, SchedulerAuto, nil); err == nil {
		s.Close()
		t.Fatal("legacy Landing accepted UoT")
	}
	if err := <-done; !errors.Is(err, ErrProtocol) {
		t.Fatalf("old Landing: %v", err)
	}
}
