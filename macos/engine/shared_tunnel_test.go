package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mptcp-desktop/engine/multipath"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sharedTestConfig() SharedTunnelConfig {
	return SharedTunnelConfig{SchemaVersion: 1, Kind: sharedTunnelKind,
		Tunnel: SharedTunnelTransport{TransportKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Relays: []Relay{{Host: "127.0.0.1", Port: 21001}, {Host: "127.0.0.1", Port: 21002}}},
		Inlets: []SharedTunnelInlet{{ID: "hkbn", ListenPort: 1081, RouteID: 1}, {ID: "hkt", ListenPort: 1082, RouteID: 2}, {ID: "icable", ListenPort: 1083, RouteID: 3}}}
}

func TestSharedTunnelConfigStrictAndNoImplicitMigration(t *testing.T) {
	valid := sharedTestConfig()
	raw, _ := json.Marshal(valid)
	if _, err := readSharedTunnel(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*SharedTunnelConfig){
		func(c *SharedTunnelConfig) { c.Kind = "bundle" },
		func(c *SharedTunnelConfig) { c.Inlets[1].ListenPort = c.Inlets[0].ListenPort },
		func(c *SharedTunnelConfig) { c.Inlets[1].RouteID = c.Inlets[0].RouteID },
		func(c *SharedTunnelConfig) { c.Inlets[1].ID = c.Inlets[0].ID },
		func(c *SharedTunnelConfig) { c.Inlets[0].Weight = 17 },
		func(c *SharedTunnelConfig) { c.Inlets[0].MaxConnections = -1 },
		func(c *SharedTunnelConfig) { c.Tunnel.TransportKey = "invalid" },
		func(c *SharedTunnelConfig) { c.Tunnel.Relays[0].Port = c.Inlets[0].ListenPort },
	} {
		var c SharedTunnelConfig
		json.Unmarshal(raw, &c)
		mutate(&c)
		if c.validate() == nil {
			t.Fatal("invalid shared tunnel accepted")
		}
	}
	if _, err := readSharedTunnel(bytes.NewReader(append(raw, []byte(" {}")...))); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if _, err := readSharedTunnel(bytes.NewReader(bytes.Replace(raw, []byte(`"kind"`), []byte(`"unknown"`), 1))); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := readSharedTunnel(bytes.NewReader(bytes.Repeat([]byte{'x'}, 65537))); err == nil {
		t.Fatal("oversized JSON accepted")
	}
	legacy := validProfile()
	legacyRaw, _ := json.Marshal(legacy)
	if _, err := readSharedTunnel(bytes.NewReader(legacyRaw)); err == nil {
		t.Fatal("legacy profile silently migrated")
	}
}

func sharedFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func TestSharedTunnelAtomicPortBinding(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	c := sharedTestConfig()
	c.Inlets = c.Inlets[:2]
	c.Inlets[0].ListenPort = sharedFreePort(t)
	c.Inlets[1].ListenPort = occupied.Addr().(*net.TCPAddr).Port
	if r, err := newSharedTunnelRuntime(context.Background(), c); err == nil {
		r.closeSockets()
		t.Fatal("conflicting port accepted")
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Inlets[0].ListenPort)))
	if err != nil {
		t.Fatalf("partial listener leaked after rollback: %v", err)
	}
	l.Close()
}

func sharedBackend(t *testing.T, ctx context.Context, label string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				stop := context.AfterFunc(ctx, func() { c.Close() })
				defer stop()
				io.WriteString(c, label)
				io.Copy(c, c)
				c.(multipath.HalfConn).CloseWrite()
			}()
		}
	}()
	return l.Addr().String()
}

func sharedCountedRelay(t *testing.T, ctx context.Context, target string) (Relay, *atomic.Int64) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	count := new(atomic.Int64)
	go func() {
		for {
			a, e := l.Accept()
			if e != nil {
				return
			}
			count.Add(1)
			go func() {
				b, e := multipath.PlainDial(ctx, target)
				if e != nil {
					a.Close()
					return
				}
				multipath.Bridge(ctx, a.(multipath.HalfConn), b.(multipath.HalfConn), time.Minute)
			}()
		}
	}()
	return Relay{Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port}, count
}

func TestSharedTunnelRuntimeThreeListenersSixConnections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := sharedTestConfig()
	var routes []multipath.TCPRoute
	for i := range c.Inlets {
		c.Inlets[i].ListenPort = sharedFreePort(t)
		routes = append(routes, multipath.TCPRoute{ID: uint32(i + 1), Backend: sharedBackend(t, ctx, fmt.Sprintf("target-%d\n", i+1))})
	}
	srv, err := multipath.NewRoutedServerWithScheduler(ctx, c.Tunnel.TransportKey, routes, 2, multipath.SchedulerAggregate)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	c.Tunnel.Relays = nil
	c.Tunnel.SchedulerMode = "aggregate"
	var counters []*atomic.Int64
	for i := 0; i < 6; i++ {
		relay, count := sharedCountedRelay(t, ctx, l.Addr().String())
		c.Tunnel.Relays = append(c.Tunnel.Relays, relay)
		counters = append(counters, count)
	}
	done := make(chan error, 1)
	go func() { done <- runSharedTunnel(ctx, c) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("shared runtime shutdown timed out")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ss := srv.Snapshots()
		if len(ss) == 1 && ss[0].Paths == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shared tunnel failed to become ready")
		}
		select {
		case err := <-done:
			t.Fatalf("runtime exited: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Ensure runtime has published the capability-probed Session before dialing.
	time.Sleep(30 * time.Millisecond)
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for _, inlet := range c.Inlets {
		wg.Add(1)
		go func(inlet SharedTunnelInlet) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(inlet.ListenPort)), time.Second)
			if err != nil {
				errs <- err
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			payload := bytes.Repeat([]byte{byte(inlet.RouteID), 0, 255, 'M', 'P', 'S'}, 128<<10)
			if _, err = conn.Write(payload); err != nil {
				errs <- err
				return
			}
			conn.(multipath.HalfConn).CloseWrite()
			got, err := io.ReadAll(conn)
			if err == nil && !bytes.Equal(got, append([]byte(fmt.Sprintf("target-%d\n", inlet.RouteID)), payload...)) {
				err = fmt.Errorf("inlet %s: target mismatch", inlet.ID)
			}
			errs <- err
		}(inlet)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ss := srv.Snapshots(); len(ss) != 1 || ss[0].Paths != 6 {
		t.Fatal("inlets created separate Sessions")
	}
	for _, count := range counters {
		if count.Load() != 1 {
			t.Fatalf("Relay accepted %d TCP connections, wanted 1", count.Load())
		}
	}
}
