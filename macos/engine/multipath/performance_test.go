package multipath

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type performanceResult struct {
	Name           string    `json:"name"`
	PayloadBytes   int       `json:"payload_bytes"`
	RelayRateBPS   []int64   `json:"relay_rate_bytes_per_second"`
	RelayOneWayMS  []float64 `json:"relay_one_way_delay_ms"`
	ElapsedSeconds float64   `json:"elapsed_seconds"`
	GoodputBPS     float64   `json:"goodput_bytes_per_second"`
	CPUSeconds     float64   `json:"test_process_cpu_seconds"`
	HeapPeakBytes  uint64    `json:"test_process_heap_peak_bytes"`
	RSSPeakBytes   int64     `json:"test_process_rss_peak_bytes"`
	Client         Stats     `json:"client"`
	Landing        []Stats   `json:"landing"`
}

func cpuSeconds(r syscall.Rusage) float64 {
	return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
}

func measureCase(t *testing.T, name string, rates []int64, latencies []time.Duration, payload int) performanceResult {
	t.Helper()
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
	addresses := make([]string, len(rates))
	for i, rate := range rates {
		r := newTestRelay(t, l.Addr().String(), rate, latencies[i])
		addresses[i] = r.listener.Addr().String()
	}
	s, err := DialClient(ctx, addresses, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wait, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if err = s.WaitPaths(wait, len(rates)); err != nil {
		t.Fatal(err)
	}
	// Warm-up is explicit and excluded from throughput timing. It lets the
	// adaptive scheduler observe RTT/ACK goodput instead of initial priors.
	if _, err = transfer(s, 1<<20); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var peak atomic.Uint64
	monitored := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitored:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				for old := peak.Load(); m.HeapAlloc > old; old = peak.Load() {
					if peak.CompareAndSwap(old, m.HeapAlloc) {
						break
					}
				}
			}
		}
	}()
	var before, after syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	elapsed, err := transfer(s, payload)
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	close(monitored)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	rss := after.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	result := performanceResult{Name: name, PayloadBytes: payload, RelayRateBPS: rates, ElapsedSeconds: elapsed.Seconds(), GoodputBPS: float64(payload) / elapsed.Seconds(), CPUSeconds: cpuSeconds(after) - cpuSeconds(before), HeapPeakBytes: peak.Load(), RSSPeakBytes: rss, Client: s.Snapshot(), Landing: srv.Snapshots()}
	for _, d := range latencies {
		result.RelayOneWayMS = append(result.RelayOneWayMS, float64(d)/float64(time.Millisecond))
	}
	raw, _ := json.Marshal(result)
	t.Logf("MEASUREMENT %s", raw)
	return result
}

func TestControlledAggregation(t *testing.T) {
	if os.Getenv("MPX_PERF") != "1" {
		t.Skip("set MPX_PERF=1 for real controlled TCP relay measurements")
	}
	var results []performanceResult
	for _, n := range []int{1, 2, 3, 6} {
		rates := make([]int64, n)
		delays := make([]time.Duration, n)
		for i := range rates {
			rates[i] = 2 << 20
			delays[i] = 5 * time.Millisecond
		}
		var result performanceResult
		t.Run(string(rune('0'+n))+"-equal-paths", func(t *testing.T) { result = measureCase(t, "equal-"+string(rune('0'+n)), rates, delays, 8<<20) })
		if result.ElapsedSeconds == 0 {
			t.Fatal("measurement missing")
		}
		results = append(results, result)
	}
	var asymmetric performanceResult
	t.Run("asymmetric", func(t *testing.T) {
		asymmetric = measureCase(t, "asymmetric-2MiB-256KiB-1MiB", []int64{2 << 20, 256 << 10, 1 << 20}, []time.Duration{4 * time.Millisecond, 70 * time.Millisecond, 15 * time.Millisecond}, 8<<20)
	})
	results = append(results, asymmetric)
	if output := os.Getenv("MPX_PERF_REPORT"); output != "" {
		report := struct {
			Schema      int                 `json:"schema"`
			Environment string              `json:"environment"`
			Scope       string              `json:"scope"`
			Generated   string              `json:"generated"`
			Cases       []performanceResult `json:"cases"`
		}{1, runtime.GOOS + "/" + runtime.GOARCH, "Loopback TCP proxies with independent serialization limits and bounded delay pipelines; single logical stream echo; CPU/RSS cover client, Landing, relays and test payloads in one process; not a WAN or isolated app benchmark", time.Now().UTC().Format(time.RFC3339), results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	baseline := results[0].GoodputBPS
	for i := 1; i < 4; i++ {
		r := results[i]
		ratio := r.GoodputBPS / baseline
		sum := float64(len(r.RelayRateBPS) * (2 << 20))
		efficiency := r.GoodputBPS / sum
		t.Logf("%s speedup=%.3fx serialization-limit efficiency=%.2f%%", r.Name, ratio, 100*efficiency)
		if ratio < 1.35 {
			t.Errorf("single-stream aggregation not sufficiently above fastest path: %s %.2fx", r.Name, ratio)
		}
		if efficiency < .70 {
			t.Errorf("less than 70%% of controlled path sum: %s %.2f%%", r.Name, 100*efficiency)
		}
		for _, p := range r.Client.PathStats {
			if p.Sent == 0 || p.Received == 0 {
				t.Errorf("unused carrier: %+v", p)
			}
		}
	}
	if asymmetric.GoodputBPS < .85*baseline {
		t.Errorf("slow path severely degraded transfer: %.2fx baseline", asymmetric.GoodputBPS/baseline)
	}
}

func TestSustainedTransferAndPathChurn(t *testing.T) {
	if os.Getenv("MPX_SOAK") != "1" {
		t.Skip("set MPX_SOAK=1 for a bounded sustained transfer and carrier churn check")
	}
	s, _, relays, _ := testTCP(t, 3, 2<<20, 3*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r := relays[i%len(relays)]
				r.fail()
				timer := time.NewTimer(300 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				r.recover()
				i++
			}
		}
	}()
	total := 0
	started := time.Now()
	for time.Since(started) < 60*time.Second {
		if _, err := transfer(s, 4<<20); err != nil {
			t.Fatal(err)
		}
		total += 4 << 20
	}
	cancel()
	<-done
	stats := s.Snapshot()
	t.Logf("SOAK bytes=%d duration=%v stats=%+v", total, time.Since(started), stats)
	if stats.PendingBytes != 0 || stats.BufferedBytes != 0 || stats.Connections != 0 {
		t.Fatalf("post-transfer state not reclaimed: %+v", stats)
	}
}
