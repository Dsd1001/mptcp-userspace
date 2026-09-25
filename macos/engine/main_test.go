package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"mptcp-desktop/engine/multipath"
)

func validProfile() Config {
	return Config{SchemaVersion: 2, Mode: "tcp_forward", ListenPort: 1081, Relays: []Relay{{Host: "192.0.2.1", Port: 21001}, {Host: "192.0.2.2", Port: 21002}}}
}
func TestProfileValidation(t *testing.T) {
	c := validProfile()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*Config){
		func(c *Config) { c.SchemaVersion = 1 },
		func(c *Config) { c.Mode = "socks5" },
		func(c *Config) { c.ListenPort = 80 },
		func(c *Config) { c.Relays = []Relay{c.Relays[0], c.Relays[0]} },
		func(c *Config) { c.Relays = []Relay{{Host: "127.0.0.1", Port: 1081}, {Host: "192.0.2.2", Port: 21002}} },
	} {
		invalid := validProfile()
		modify(&invalid)
		if invalid.validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
func TestUserspaceRaisesNOFILEFor2048Streams(t *testing.T) {
	if err := ensureUserspaceFileLimit(); err != nil {
		t.Fatal(err)
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	need := uint64(multipath.MaxStreams + 512)
	if limit.Cur < need {
		t.Fatalf("RLIMIT_NOFILE=%d, want >=%d", limit.Cur, need)
	}
}

func TestStrictConfig(t *testing.T) {
	raw, _ := json.Marshal(validProfile())
	if _, err := readConfig(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		string(raw) + "{}",
		strings.TrimSuffix(string(raw), "}") + ",\"tls_name\":\"old-backend\"}",
		strings.Repeat(" ", 32769),
		"{\"listen_port\":1080,\"relays\":[]}",
	} {
		if _, err := readConfig(strings.NewReader(data)); err == nil {
			t.Fatal("old or malformed config accepted")
		}
	}
}

type testTunnel struct{ *net.TCPConn }

func (c *testTunnel) paths() int { return 2 }
func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	a, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := listener.Accept()
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	a.SetDeadline(time.Now().Add(3 * time.Second))
	b.SetDeadline(time.Now().Add(3 * time.Second))
	return a.(*net.TCPConn), b.(*net.TCPConn)
}
func TestTransparentForwardingAndHalfClose(t *testing.T) {
	backend, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	entrance, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveForward(ctx, entrance, func(ctx context.Context) (forwardConn, error) {
			conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", backend.Addr().String())
			if err != nil {
				return nil, err
			}
			return &testTunnel{conn.(*net.TCPConn)}, nil
		})
	}()
	payload := make([]byte, 256*1024)
	for i := range payload {
		payload[i] = byte((i*73 + 19) % 256)
	}
	response := []byte{0xff, 0x00, 0x05, 0x16, 0x03, 0x01, 0x7f}
	serverResult := make(chan error, 1)
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		received, err := io.ReadAll(conn)
		if err != nil {
			serverResult <- err
			return
		}
		if !bytes.Equal(received, payload) {
			serverResult <- errors.New("bytes changed or a protocol greeting was injected")
			return
		}
		_, err = conn.Write(response)
		if err == nil {
			err = conn.(*net.TCPConn).CloseWrite()
		}
		serverResult <- err
	}()
	conn, err := net.DialTimeout("tcp4", entrance.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err = conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	received, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, response) {
		t.Fatal("response after half-close was changed")
	}
	if err = <-serverResult; err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("forwarder did not stop")
	}
}
func TestBridgeCancelAndIdle(t *testing.T) {
	for _, mode := range []string{"cancel", "idle"} {
		t.Run(mode, func(t *testing.T) {
			_, a := tcpPair(t)
			b, _ := tcpPair(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { bridge(ctx, a, b, &counters{}, 50*time.Millisecond); close(done) }()
			if mode == "cancel" {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("bridge did not close")
			}
		})
	}
}
func TestCancelDuringDial(t *testing.T) {
	entrance, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveForward(ctx, entrance, func(ctx context.Context) (forwardConn, error) { close(started); <-ctx.Done(); return nil, ctx.Err() })
	}()
	conn, err := net.DialTimeout("tcp4", entrance.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dial not started")
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending dial leaked")
	}
}
