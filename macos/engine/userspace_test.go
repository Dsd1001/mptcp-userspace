package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"mptcp-desktop/engine/multipath"
)

func TestUserspaceFileLimitSupportsMaxStreams(t *testing.T) {
	if err := ensureUserspaceFileLimit(); err != nil {
		t.Fatal(err)
	}
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		t.Fatal(err)
	}
	if lim.Cur < uint64(multipath.MaxStreams+512) {
		t.Fatalf("RLIMIT_NOFILE soft=%d too small for %d streams", lim.Cur, multipath.MaxStreams)
	}
}

func TestUserspaceProfileMigration(t *testing.T) {
	legacy := validProfile()
	if legacy.userspace() {
		t.Fatal("legacy silently switched protocol")
	}
	key, err := multipath.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	c := Config{SchemaVersion: 3, Mode: "userspace_multipath", ListenPort: 1081, Relays: []Relay{{Host: "127.0.0.1", Port: 21001}, {Host: "127.0.0.1", Port: 21002}}, TransportKey: key, UDPEnabled: true}
	raw, _ := json.Marshal(c)
	decoded, err := readConfig(bytes.NewReader(raw))
	if err != nil || !decoded.userspace() {
		t.Fatalf("schema 3: %v", err)
	}
	if !decoded.tcpEnabled() {
		t.Fatal("absent TCP switch should default enabled")
	}
	off := false
	c.TCPEnabled = &off
	if err = c.validate(); err != nil {
		t.Fatal(err)
	}
	c.UDPEnabled = false
	if c.validate() == nil {
		t.Fatal("both protocols disabled accepted")
	}
	c.TCPEnabled = nil
	c.Mode = "native_mptcp"
	if c.validate() == nil {
		t.Fatal("native duplicate-host guard changed")
	}
	c.Relays = legacy.Relays
	if err = c.validate(); err != nil {
		t.Fatal(err)
	}
	c.Mode = "userspace_multipath"
	c.TransportKey = "short"
	if c.validate() == nil {
		t.Fatal("weak transport key accepted")
	}
}

func engineTestRelay(t *testing.T, ctx context.Context, targetTCP, targetUDP string) Relay {
	t.Helper()
	l, err := multipath.PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	remoteAddr, err := net.ResolveUDPAddr("udp", targetUDP)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := net.DialUDP("udp", nil, remoteAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close(); udp.Close(); remote.Close() })
	go func() {
		for {
			a, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				b, e := multipath.PlainDial(ctx, targetTCP)
				if e != nil {
					a.Close()
					return
				}
				multipath.Bridge(ctx, a.(multipath.HalfConn), b.(multipath.HalfConn), time.Minute)
			}()
		}
	}()
	var mu sync.Mutex
	var client *net.UDPAddr
	go func() {
		buf := make([]byte, 2048)
		for {
			n, a, e := udp.ReadFromUDP(buf)
			if e != nil {
				return
			}
			mu.Lock()
			client = a
			mu.Unlock()
			remote.Write(buf[:n])
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, e := remote.Read(buf)
			if e != nil {
				return
			}
			mu.Lock()
			a := client
			mu.Unlock()
			if a != nil {
				udp.WriteToUDP(buf[:n], a)
			}
		}
	}()
	return Relay{Host: "127.0.0.1", Port: port}
}

func TestUserspaceEngineTCPUDPAndStop(t *testing.T) {
	for _, tcpEnabled := range []bool{true, false} {
		t.Run(strconv.FormatBool(tcpEnabled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
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
			ub, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer ub.Close()
			go func() {
				b := make([]byte, 65536)
				for {
					n, a, e := ub.ReadFromUDP(b)
					if e != nil {
						return
					}
					ub.WriteToUDP(b[:n], a)
				}
			}()
			key, _ := multipath.NewKey()
			server, err := multipath.NewServer(ctx, key, backend.Addr().String(), 4)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			landing, err := multipath.PlainListen(ctx, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			go server.Serve(landing)
			udp, err := multipath.StartUDPServer(server, "127.0.0.1:0", ub.LocalAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer udp.Close()
			relays := []Relay{engineTestRelay(t, ctx, landing.Addr().String(), udp.Addr().String()), engineTestRelay(t, ctx, landing.Addr().String(), udp.Addr().String())}
			reserve, err := multipath.PlainListen(ctx, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := reserve.Addr().(*net.TCPAddr).Port
			reserve.Close()
			config := Config{SchemaVersion: 3, Mode: "userspace_multipath", ListenPort: port, Relays: relays, TCPEnabled: &tcpEnabled, UDPEnabled: true, TransportKey: key}
			done := make(chan error, 1)
			go func() { done <- runClient(ctx, config) }()
			address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			deadline := time.Now().Add(5 * time.Second)
			if tcpEnabled {
				var conn net.Conn
				for time.Now().Before(deadline) {
					conn, err = multipath.PlainDial(ctx, address)
					if err == nil {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				payload := bytes.Repeat([]byte{0, 255, 31, 128, 4, 3, 2, 1}, 128<<10)
				writeDone := make(chan error, 1)
				go func() {
					_, e := conn.Write(payload)
					if e == nil {
						e = conn.(multipath.HalfConn).CloseWrite()
					}
					writeDone <- e
				}()
				got, e := io.ReadAll(conn)
				if e != nil {
					t.Fatal(e)
				}
				if e = <-writeDone; e != nil {
					t.Fatal(e)
				}
				if !bytes.Equal(got, payload) {
					t.Fatal("engine byte corruption")
				}
			}
			appAddr, _ := net.ResolveUDPAddr("udp", address)
			app, err := net.DialUDP("udp", nil, appAddr)
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			packet := []byte("engine UDP\x00\xff")
			received := false
			for time.Now().Before(deadline) {
				app.SetDeadline(time.Now().Add(200 * time.Millisecond))
				app.Write(packet)
				buf := make([]byte, 1024)
				n, e := app.Read(buf)
				if e == nil {
					if !bytes.Equal(buf[:n], packet) {
						t.Fatal("UDP mismatch")
					}
					received = true
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !received {
				select {
				case exitErr := <-done:
					t.Fatalf("engine UDP unavailable; engine exited: %v", exitErr)
				default:
					t.Fatalf("engine UDP unavailable; admission=%+v TCP=%+v UDP=%+v", server.AdmissionSnapshot(), server.Snapshots(), udp.Snapshots())
				}
			}
			if !tcpEnabled {
				if c, e := multipath.PlainDial(ctx, address); e == nil {
					c.Close()
					t.Fatal("TCP listener remained enabled")
				}
			}
			cancel()
			select {
			case err = <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("engine stop leaked workers")
			}
		})
	}
}
