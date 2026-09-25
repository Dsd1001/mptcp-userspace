package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"mptcp-desktop/engine/multipath"
)

func TestSchedulerConfigCompatibility(t *testing.T) {
	base := validProfile()
	base.SchemaVersion, base.Mode = 3, "userspace_multipath"
	base.TransportKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, value := range []string{"auto", "aggregate", "protect", "weighted", "", "AUTO", "other"} {
		c := base
		c.SchedulerMode = &value
		if value == "weighted" {
			down := 50.0
			for i := range c.Relays {
				c.Relays[i].DownloadMbps = &down
			}
		}
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		_, err = readConfig(bytes.NewReader(raw))
		valid := value == "auto" || value == "aggregate" || value == "protect" || value == "weighted"
		if (err == nil) != valid {
			t.Fatal("scheduler validation", value, err)
		}
		c.Mode = "native_mptcp"
		raw, _ = json.Marshal(c)
		if _, err := readConfig(bytes.NewReader(raw)); err != nil {
			t.Fatal("Native must ignore scheduler string", value, err)
		}
	}
	base.SchedulerMode = nil
	raw, _ := json.Marshal(base)
	c, err := readConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if mode, err := c.schedulerMode(); err != nil || mode != multipath.SchedulerAuto {
		t.Fatal("missing mode did not default Auto")
	}
}

// A real child invokes the production main/readConfig/runClient path. The
// synthetic key only enters stdin; no Keychain or user profile is accessed.
func TestSchedulerProcessHelper(t *testing.T) {
	if os.Getenv("MPX_SCHEDULER_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	os.Args = []string{"mptcp-desktop-engine", "run"}
	main()
	os.Exit(0)
}

func TestSchedulerModesThroughActualStdin(t *testing.T) {
	for _, mode := range []string{"auto", "aggregate", "protect", "weighted"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			backend, err := multipath.PlainListen(ctx, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			go func() {
				for {
					c, err := backend.Accept()
					if err != nil {
						return
					}
					go func() {
						defer c.Close()
						c.SetDeadline(time.Now().Add(10 * time.Second))
						io.Copy(c, c)
						c.(multipath.HalfConn).CloseWrite()
					}()
				}
			}()
			key := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
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
			relay, err := multipath.PlainListen(ctx, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer relay.Close()
			go func() {
				for {
					a, err := relay.Accept()
					if err != nil {
						return
					}
					go func() {
						b, err := multipath.PlainDial(ctx, landing.Addr().String())
						if err != nil {
							a.Close()
							return
						}
						multipath.Bridge(ctx, a.(multipath.HalfConn), b.(multipath.HalfConn), time.Minute)
					}()
				}
			}()
			reserve, err := multipath.PlainListen(ctx, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := reserve.Addr().(*net.TCPAddr).Port
			reserve.Close()
			relays := []Relay{{Host: "127.0.0.1", Port: landing.Addr().(*net.TCPAddr).Port}, {Host: "127.0.0.1", Port: relay.Addr().(*net.TCPAddr).Port}}
			if mode == "weighted" {
				down, up := 50.0, 50.0
				for i := range relays {
					relays[i].DownloadMbps = &down
					relays[i].UploadMbps = &up
				}
			}
			config := Config{SchemaVersion: 3, Mode: "userspace_multipath", SchedulerMode: &mode, ListenPort: port, TransportKey: key, Relays: relays}
			input, _ := json.Marshal(config)
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSchedulerProcessHelper$")
			cmd.Env = append(os.Environ(), "MPX_SCHEDULER_CHILD=1")
			cmd.Stdin = bytes.NewReader(input)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			events := make(chan Event, 64)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					cmd.Process.Kill()
					cmd.Wait()
				}
			}()
			go func() {
				defer close(events)
				scanner := bufio.NewScanner(stdout)
				scanner.Buffer(make([]byte, 8192), 131072)
				for scanner.Scan() {
					var e Event
					if json.Unmarshal(scanner.Bytes(), &e) == nil && e.Kind != "" {
						select {
						case events <- e:
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			waitEvent := func(match func(Event) bool) Event {
				t.Helper()
				for {
					select {
					case e, ok := <-events:
						if !ok {
							t.Fatal("child exited before event")
						}
						if e.Kind == "error" {
							t.Fatal("child error", e.Message)
						}
						if match(e) {
							return e
						}
					case <-ctx.Done():
						t.Fatal("real stdin engine test timed out")
					}
				}
			}
			waitEvent(func(e Event) bool { return e.Kind == "listening" })
			conn, err := multipath.PlainDial(ctx, fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				t.Fatal(err)
			}
			conn.SetDeadline(time.Now().Add(8 * time.Second))
			payload := bytes.Repeat([]byte{0, 255, 31, 128, 5, 7, 9, 10}, 128<<10)
			writeDone := make(chan error, 1)
			go func() {
				_, err := conn.Write(payload)
				if err == nil {
					err = conn.(multipath.HalfConn).CloseWrite()
				}
				writeDone <- err
			}()
			got, err := io.ReadAll(conn)
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := <-writeDone; err != nil || !bytes.Equal(got, payload) {
				t.Fatal("real stdin transfer integrity", err)
			}
			e := waitEvent(func(e Event) bool { return e.Kind == "stats" && e.Received >= int64(len(payload)) && e.Paths == 2 })
			if string(e.ConfiguredSchedulerMode) != mode {
				t.Fatal("actual child ignored stdin scheduler")
			}
			snapshots := server.Snapshots()
			if len(snapshots) != 1 || string(snapshots[0].ConfiguredSchedulerMode) != mode || snapshots[0].Sent < uint64(len(payload)) {
				t.Fatal("Landing direction ignored selected policy")
			}
			if mode != "auto" && (string(e.EffectiveSchedulerMode) != mode || string(snapshots[0].EffectiveSchedulerMode) != mode) {
				t.Fatal("forced mode changed")
			}
			if dir := os.Getenv("MPX_ENGINE_MODES_REPORT"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(e)
				if err := os.WriteFile(filepath.Join(dir, "engine-"+mode+".json"), append(data, '\n'), 0644); err != nil {
					t.Fatal(err)
				}
				data, _ = json.Marshal(snapshots[0])
				if err := os.WriteFile(filepath.Join(dir, "landing-"+mode+".json"), append(data, '\n'), 0644); err != nil {
					t.Fatal(err)
				}
			}
			cmd.Process.Signal(os.Interrupt)
			if err := cmd.Wait(); err != nil {
				t.Fatal("child did not stop cleanly", err)
			}
			t.Logf("REAL_STDIN mode=%s paths=%d bytes=%d client_effective=%s landing_effective=%s exit=0", mode, e.Paths, len(got), e.EffectiveSchedulerMode, snapshots[0].EffectiveSchedulerMode)
		})
	}
}
