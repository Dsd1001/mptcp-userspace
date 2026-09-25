package multipath

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Unlike the old 256 KiB relay queue, this fixture has room for the complete
// high-BDP flight. The exact same fixture is used against the frozen 0.6.0.
// Each direction is a fixed serialization-rate + propagation-delay pipeline;
// it never grants a token-bucket startup burst. At most 8 MiB is queued per pipe.
func highBDPRelay(t *testing.T, target string, rate int64, delay time.Duration) string {
	t.Helper()
	l, err := PlainListen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	conns := make(map[net.Conn]bool)
	var wg sync.WaitGroup
	pipe := func(dst, src net.Conn) {
		q := make(chan relayChunk, 256)
		finished := make(chan struct{})
		go func() {
			defer close(q)
			defer close(finished)
			buf := make([]byte, 32768)
			for {
				n, e := src.Read(buf)
				if n > 0 {
					chunk := relayChunk{data: append([]byte(nil), buf[:n]...), ready: time.Now().Add(delay)}
					select {
					case q <- chunk:
					case <-ctx.Done():
						return
					}
				}
				if e != nil {
					return
				}
			}
		}()
		defer func() { src.Close(); <-finished }()
		next := time.Now()
		timer := time.NewTimer(time.Hour)
		timer.Stop()
		defer timer.Stop()
		for {
			var chunk relayChunk
			select {
			case <-ctx.Done():
				return
			case c, ok := <-q:
				if !ok {
					return
				}
				chunk = c
			}
			if chunk.ready.After(next) {
				next = chunk.ready
			}
			next = next.Add(time.Duration(float64(time.Second) * float64(len(chunk.data)) / float64(rate)))
			if wait := time.Until(next); wait > 0 {
				timer.Reset(wait)
				select {
				case <-timer.C:
				case <-ctx.Done():
					return
				}
			}
			if e := writeAll(dst, chunk.data); e != nil {
				return
			}
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			a, e := l.Accept()
			if e != nil {
				return
			}
			b, e := PlainDial(ctx, target)
			if e != nil {
				a.Close()
				continue
			}
			mu.Lock()
			if ctx.Err() != nil {
				mu.Unlock()
				a.Close()
				b.Close()
				return
			}
			conns[a] = true
			conns[b] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				done := make(chan struct{}, 2)
				go func() { pipe(a, b); done <- struct{}{} }()
				go func() { pipe(b, a); done <- struct{}{} }()
				<-done
				a.Close()
				b.Close()
				<-done
				mu.Lock()
				delete(conns, a)
				delete(conns, b)
				mu.Unlock()
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		l.Close()
		mu.Lock()
		for c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return l.Addr().String()
}

type highBDPResult struct {
	RequestedSchedulerMode SchedulerMode     `json:"requested_scheduler_mode"`
	WarmClient             Stats             `json:"client_after_warmup"`
	WarmServer             Stats             `json:"server_after_warmup"`
	ModeTimeline           []schedulerSample `json:"scheduler_timeline"`

	Name       string    `json:"name"`
	Rates      []int64   `json:"rates_bytes_per_second"`
	RTT        []float64 `json:"base_rtt_ms"`
	Bytes      int       `json:"bytes"`
	Seconds    float64   `json:"seconds"`
	Mbps       float64   `json:"mbps"`
	CPU        float64   `json:"test_process_cpu_seconds"`
	HeapPeak   uint64    `json:"test_process_heap_peak_bytes"`
	RSSPeak    int64     `json:"test_process_rss_highwater_bytes"`
	PeakFlight Stats     `json:"peak_client_flight"`
	Client     Stats     `json:"client_after"`
	Landing    []Stats   `json:"landing_after"`
}

func highBDPCase(t *testing.T, name string, rates []int64, rtts []time.Duration, size int) highBDPResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend, _ := echoBackend(t)
	server, err := NewServer(ctx, testToken, backend, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	l, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(l)
	addresses := make([]string, len(rates))
	for i := range rates {
		addresses[i] = highBDPRelay(t, l.Addr().String(), rates[i], rtts[i]/2)
	}
	client, err := DialClientWithScheduler(ctx, addresses, testToken, testSchedulerMode(t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	wait, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if err = client.WaitPaths(wait, len(rates)); err != nil {
		t.Fatal(err)
	}
	// Real data warmup only. No injected rate/RTT priors or testing-only scheduler.
	if _, err = transfer(client, 16<<20); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	done, joined := make(chan struct{}), make(chan struct{})
	result := highBDPResult{Name: name, Rates: rates, Bytes: size}
	result.RequestedSchedulerMode = testSchedulerMode(t)
	peer := server.session(client.id)
	result.WarmClient = client.Snapshot()
	result.WarmServer = peer.Snapshot()
	measuredSetup := time.Now()
	result.ModeTimeline = append(result.ModeTimeline, schedulerSample{0, result.WarmClient, result.WarmServer})

	for _, rtt := range rtts {
		result.RTT = append(result.RTT, float64(rtt)/float64(time.Millisecond))
	}
	go func() {
		defer close(joined)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		maxFlight := 0
		sampleTick := 0
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			stats := client.Snapshot()
			sampleTick++
			if sampleTick%5 == 0 && len(result.ModeTimeline) < 512 {
				result.ModeTimeline = append(result.ModeTimeline, schedulerSample{time.Since(measuredSetup).Seconds(), stats, peer.Snapshot()})
			}

			flight := 0
			for _, p := range stats.PathStats {
				flight += p.Outstanding
			}
			if flight > maxFlight {
				maxFlight = flight
				result.PeakFlight = stats
			}
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			result.HeapPeak = max(result.HeapPeak, mem.HeapAlloc)
		}
	}()
	var before, after syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	elapsed, err := transfer(client, size)
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	close(done)
	<-joined
	if err != nil {
		t.Fatal(err)
	}
	result.Seconds = elapsed.Seconds()
	result.Mbps = float64(size) * 8 / elapsed.Seconds() / 1e6
	result.CPU = cpuSeconds(after) - cpuSeconds(before)
	result.RSSPeak = after.Maxrss
	if runtime.GOOS == "linux" {
		result.RSSPeak *= 1024
	}
	// The original payload/FIN timing above is unchanged. Rev2 adds a reliable
	// final-consumption settlement; validate cleanup separately after timing.
	cleanupDeadline := time.Now().Add(5 * time.Second)
	for {
		result.Client = client.Snapshot()
		peerState := peer.Snapshot()
		if capacityReclaimed(result.Client) && capacityReclaimed(peerState) {
			break
		}
		if time.Now().After(cleanupDeadline) {
			t.Fatal("post-transfer final-credit settlement did not quiesce")
		}
		time.Sleep(time.Millisecond)
	}
	result.Landing = server.Snapshots()
	t.Logf("HIGH_BDP %s %.3f Mbps cpu=%.3fs window_waits=%d", name, result.Mbps, result.CPU, result.Client.WindowWaits)
	if result.Client.PendingBytes != 0 || result.Client.BufferedBytes != 0 || result.Client.ReceiveAllocated != 0 || result.Client.Connections != 0 {
		t.Fatalf("state not reclaimed: %+v", result.Client)
	}
	for _, p := range result.Client.PathStats {
		if p.Sent == 0 || p.Received == 0 {
			t.Errorf("unused path %d", p.ID)
		}
	}
	return result
}

func TestHighBDPMatrix(t *testing.T) {
	mode := os.Getenv("MPX_HIGH_BDP")
	if mode == "" {
		t.Skip("set MPX_HIGH_BDP=baseline, quick or enforce")
	}
	var results []highBDPResult
	defer func() {
		if path := os.Getenv("MPX_HIGH_BDP_REPORT"); path != "" {
			report := map[string]any{"source_id": SourceID, "wire_protocol": WireProtocol, "scheduler_mode": testSchedulerMode(t), "timeline_scope": "100ms samples from post-warmup measurement setup through original transfer return; original transfer timer unchanged", "version": Version, "platform": runtime.GOOS + "/" + runtime.GOARCH, "scope": "Single logical stream random echo, real loopback TCP shaped proxies; CPU/RSS include client, Landing, relay and test payloads; not an App/WAN benchmark", "warmup_bytes": 16 << 20, "cases": results}
			data, _ := json.MarshalIndent(report, "", "  ")
			if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
				t.Error(err)
			}
		}
	}()
	counts, capacities := []int{2, 3, 6}, []int{300, 500}
	if mode == "baseline" || mode == "quick" {
		counts = []int{6}
		capacities = []int{500}
	}
	for _, capacity := range capacities {
		for _, n := range counts {
			for _, ms := range []int{30, 50, 100} {
				name := fmt.Sprintf("%dMbps-%dpaths-%dms", capacity, n, ms)
				t.Run(name, func(t *testing.T) {
					rates, rtts := make([]int64, n), make([]time.Duration, n)
					for i := range rates {
						rates[i] = int64(capacity) * 1000000 / 8 / int64(n)
						rtts[i] = time.Duration(ms) * time.Millisecond
					}
					r := highBDPCase(t, name, rates, rtts, 64<<20)
					results = append(results, r)
					if mode == "enforce" && capacity == 500 && ms <= 50 && r.Mbps < 300 {
						t.Errorf("single stream below required 300Mbps: %.3f", r.Mbps)
					}
					if mode == "enforce" && capacity == 300 && ms <= 50 && r.Mbps < 240 {
						t.Errorf("single stream below 80%% of 300Mbps tier: %.3f", r.Mbps)
					}
				})
			}
		}
	}
	if mode == "enforce" {
		var fastest float64
		for _, mixed := range []bool{false, true} {
			name := "asymmetric-fastest-only"
			rates := []int64{37500000}
			rtts := []time.Duration{30 * time.Millisecond}
			if mixed {
				name = "asymmetric-300+20+180Mbps"
				rates = append(rates, 2500000, 22500000)
				rtts = append(rtts, 150*time.Millisecond, 50*time.Millisecond)
			}
			t.Run(name, func(t *testing.T) {
				r := highBDPCase(t, name, rates, rtts, 64<<20)
				results = append(results, r)
				if !mixed {
					fastest = r.Mbps
				} else if r.Mbps < fastest*.95 {
					t.Errorf("slow path regression: %.3f vs fastest %.3f", r.Mbps, fastest)
				}
			})
		}
	}
}

func BenchmarkReceiveReorder(b *testing.B) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.mu.Lock()
	st := s.newStreamLocked(1)
	st.open = true
	st.windowTarget = MaxPayload
	st.advertiseCreditLocked(time.Now())
	s.mu.Unlock()
	data := make([]byte, MaxPayload)
	out := make([]byte, len(data))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.mu.Lock()
		err := st.receiveLocked(uint64(i)*MaxPayload, data)
		s.mu.Unlock()
		if err != nil {
			b.Fatal(err)
		}
		if n, e := st.Read(out); e != nil || n != len(data) {
			b.Fatal(n, e)
		}
	}
}
