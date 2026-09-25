package multipath

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type sustainedSample struct {
	Seconds float64 `json:"seconds"`
	Mbps    float64 `json:"mbps"`
	Client  Stats   `json:"client"`
	Landing []Stats `json:"landing"`
	Heap    uint64  `json:"test_process_heap_bytes"`
}

// Exactly one logical stream remains open for the entire interval, including
// faults. Expected and received SHA-256 cover every byte, without retaining a
// multi-gigabyte test payload. This is separate from short-connection stress.
func sustainedStream(t *testing.T, s *Session, srv *Server, duration time.Duration, name string, enforce bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), duration+45*time.Second)
	defer cancel()
	st, err := s.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(duration + 35*time.Second))
	type written struct {
		n    int64
		hash []byte
		err  error
	}
	sent := make(chan written, 1)
	readDone := make(chan written, 1)
	var received atomic.Int64
	start := time.Now()
	var before, after syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	go func() {
		h := sha256.New()
		data := make([]byte, 1<<20)
		var n int64
		var e error
		for time.Since(start) < duration {
			if _, e = rand.Read(data); e != nil {
				break
			}
			var count int
			count, e = st.Write(data)
			h.Write(data[:count])
			n += int64(count)
			if e != nil {
				break
			}
		}
		if e == nil {
			e = st.CloseWrite()
		}
		sent <- written{n, h.Sum(nil), e}
	}()
	go func() {
		h := sha256.New()
		data := make([]byte, 64<<10)
		for {
			n, e := st.Read(data)
			if n > 0 {
				h.Write(data[:n])
				received.Add(int64(n))
			}
			if e != nil {
				if e == io.EOF {
					e = nil
				}
				readDone <- written{received.Load(), h.Sum(nil), e}
				return
			}
		}
	}()
	samples := []sustainedSample{}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastAt, lastBytes := start, int64(0)
	var got written
loop:
	for {
		select {
		case got = <-readDone:
			break loop
		case now := <-ticker.C:
			n := received.Load()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			samples = append(samples, sustainedSample{now.Sub(start).Seconds(), float64(n-lastBytes) * 8 / now.Sub(lastAt).Seconds() / 1e6, s.Snapshot(), srv.Snapshots(), m.HeapAlloc})
			lastAt, lastBytes = now, n
		case <-ctx.Done():
			st.Close()
			t.Fatal("sustained stream deadline")
		}
	}
	elapsed := time.Since(start)
	w := <-sent
	st.Close()
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		complete := s.Snapshot().Connections == 0
		for _, p := range srv.Snapshots() {
			complete = complete && p.Connections == 0
		}
		if complete {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	client, landing := s.Snapshot(), srv.Snapshots()
	result := map[string]any{"name": name, "version": Version, "scope": "One logical TCP stream throughout; random SHA-256 streaming echo through real loopback shaped TCP proxies; process CPU/RSS include all test components", "seconds": elapsed.Seconds(), "bytes": got.n, "mbps": float64(got.n) * 8 / elapsed.Seconds() / 1e6, "samples": samples, "client_after": client, "landing_after": landing, "cpu_seconds": cpuSeconds(after) - cpuSeconds(before), "rss_highwater": after.Maxrss, "rss_unit": "bytes on Darwin, KiB on Linux", "sha256": fmt.Sprintf("%x", got.hash), "integrity": w.err == nil && got.err == nil && w.n == got.n && string(w.hash) == string(got.hash)}
	if path := os.Getenv("MPX_LONG_REPORT"); path != "" {
		data, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(path+"-"+name+".json", append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if w.err != nil || got.err != nil || w.n != got.n || string(w.hash) != string(got.hash) {
		t.Fatalf("stream integrity: write=%v read=%v bytes=%d/%d", w.err, got.err, w.n, got.n)
	}
	for _, stats := range append([]Stats{client}, landing...) {
		if stats.Connections != 0 || stats.PendingBytes != 0 || stats.BufferedBytes != 0 || stats.ReceiveAllocated != 0 || stats.ReceiveCredit != 0 || stats.ReadyFrames != 0 {
			t.Fatalf("state not reclaimed: %+v", stats)
		}
	}
	for _, sample := range samples {
		for _, stats := range append([]Stats{sample.Client}, sample.Landing...) {
			if stats.PendingBytes > MaxDataPendingBytes+MaxControlBytes || stats.ReceiveAllocated > MaxBuffered || stats.ReceiveCredit > SessionCreditLimit || stats.ReceiveCredit < 0 || stats.BufferedBytes < 0 {
				t.Fatalf("resource bound violated: %+v", stats)
			}
		}
	}
	mbps := float64(got.n) * 8 / elapsed.Seconds() / 1e6
	if enforce {
		if mbps < 300 {
			t.Errorf("sustained single stream below 300 Mbps: %.3f", mbps)
		}
		for i, sample := range samples {
			if i > 0 && sample.Mbps < 270 {
				t.Errorf("5-second interval below 270 Mbps: %.3f at %.1fs", sample.Mbps, sample.Seconds)
			}
		}
	}
	t.Logf("SUSTAINED %s seconds=%.3f bytes=%d Mbps=%.3f cpu=%.3f intervals=%d integrity=true", name, elapsed.Seconds(), got.n, mbps, cpuSeconds(after)-cpuSeconds(before), len(samples))
}

func TestHighBDPSustainedMargin(t *testing.T) {
	if os.Getenv("MPX_LONG_BDP") != "1" {
		t.Skip("set MPX_LONG_BDP=1")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend, _ := echoBackend(t)
	srv, err := NewServer(ctx, testToken, backend, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	l, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	addresses := []string{}
	for i := 0; i < 6; i++ {
		addresses = append(addresses, highBDPRelay(t, l.Addr().String(), 62500000/6, 25*time.Millisecond))
	}
	s, err := DialClient(ctx, addresses, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.WaitPaths(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if _, err = transfer(s, 16<<20); err != nil {
		t.Fatal(err)
	}
	// The regular echo fixture has a 60s connection deadline; use 45s here.
	sustainedStream(t, s, srv, 45*time.Second, "500Mbps-6paths-50ms", true)
}

func TestSingleLongStreamPathChurn(t *testing.T) {
	if os.Getenv("MPX_LONG_BDP") != "1" {
		t.Skip("set MPX_LONG_BDP=1")
	}
	s, srv, relays, _ := testTCP(t, 3, 2<<20, 3*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			r := relays[i%len(relays)]
			r.fail()
			timer := time.NewTimer(300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				r.recover()
				return
			case <-timer.C:
			}
			r.recover()
			i++
		}
	}()
	defer func() { cancel(); <-stopped }()
	sustainedStream(t, s, srv, 35*time.Second, "single-stream-path-churn", false)
}
