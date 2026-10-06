package multipath

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func weightedDownloadBackend(t *testing.T, bytesPerStream int) string {
	t.Helper()
	l, err := PlainListen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	conns := make(map[net.Conn]bool)
	payload := make([]byte, 32<<10)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns[c] = true
			mu.Unlock()
			go func() {
				defer c.Close()
				defer func() {
					mu.Lock()
					delete(conns, c)
					mu.Unlock()
				}()
				_ = c.SetDeadline(time.Now().Add(60 * time.Second))
				var trigger [1]byte
				if _, err := io.ReadFull(c, trigger[:]); err != nil {
					return
				}
				for left := bytesPerStream; left > 0; {
					n := min(left, len(payload))
					if err := writeAll(c, payload[:n]); err != nil {
						return
					}
					left -= n
				}
				_ = c.(HalfConn).CloseWrite()
			}()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		for c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})
	return l.Addr().String()
}

func TestWeighted92x6ConcurrentStartup(t *testing.T) {
	if os.Getenv("MPX_WEIGHTED_STARTUP") != "1" {
		t.Skip("set MPX_WEIGHTED_STARTUP=1 for 92 Mbps x 6 concurrent startup validation")
	}
	const pathCount = 6
	streamCount, bytesPerStream := 150, 2<<20
	if raw := os.Getenv("MPX_WEIGHTED_STREAMS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > MaxStreams {
			t.Fatalf("invalid MPX_WEIGHTED_STREAMS=%q", raw)
		}
		streamCount = n
		if streamCount <= 64 {
			bytesPerStream = 4 << 20
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	backend := weightedDownloadBackend(t, bytesPerStream)
	server, err := NewServer(ctx, testToken, backend, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	l, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go server.Serve(l)

	addresses := make([]string, pathCount)
	capacities := make([]PathCapacity, pathCount)
	for i := 0; i < pathCount; i++ {
		addresses[i] = highBDPRelay(t, l.Addr().String(), 11_500_000, 25*time.Millisecond)
		capacities[i] = PathCapacity{DownloadMbps: 92, UploadMbps: 92}
	}
	client, err := DialClientWithPolicy(ctx, addresses, testToken, SchedulerWeighted, capacities)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	wait, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if err := client.WaitPaths(wait, pathCount); err != nil {
		t.Fatal(err)
	}

	streams := make([]*Stream, streamCount)
	for i := range streams {
		st, err := client.Open(ctx)
		if err != nil {
			t.Fatalf("open stream %d: %v", i, err)
		}
		st.SetDeadline(time.Now().Add(45 * time.Second))
		streams[i] = st
	}

	var received atomic.Int64
	start := make(chan struct{})
	errs := make(chan error, streamCount)
	var wg sync.WaitGroup
	for i, st := range streams {
		wg.Add(1)
		go func(i int, st *Stream) {
			defer wg.Done()
			defer st.Close()
			<-start
			if _, err := st.Write([]byte{1}); err != nil {
				errs <- fmt.Errorf("stream %d trigger: %w", i, err)
				return
			}
			if err := st.CloseWrite(); err != nil {
				errs <- fmt.Errorf("stream %d closewrite: %w", i, err)
				return
			}
			buf := make([]byte, 32<<10)
			got := 0
			for got < bytesPerStream {
				n, err := st.Read(buf)
				if n > 0 {
					got += n
					received.Add(int64(n))
				}
				if err != nil {
					if err == io.EOF && got == bytesPerStream {
						break
					}
					errs <- fmt.Errorf("stream %d read %d/%d: %w", i, got, bytesPerStream, err)
					return
				}
			}
			if got != bytesPerStream {
				errs <- fmt.Errorf("stream %d short read %d/%d", i, got, bytesPerStream)
			}
		}(i, st)
	}

	totalBytes := int64(streamCount * bytesPerStream)
	started := time.Now()
	close(start)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	lastAt, lastBytes := started, int64(0)
	peakMbps := 0.0
	minAfterRamp := math.Inf(1)
	ramped := false
	var samples []float64
	for {
		select {
		case now := <-ticker.C:
			current := received.Load()
			dt := now.Sub(lastAt).Seconds()
			mbps := float64(current-lastBytes) * 8 / dt / 1e6
			samples = append(samples, mbps)
			peakMbps = max(peakMbps, mbps)
			if mbps >= 200 {
				ramped = true
			}
			if ramped && totalBytes-current > totalBytes/10 {
				minAfterRamp = min(minAfterRamp, mbps)
			}
			lastAt, lastBytes = now, current
		case <-done:
			elapsed := time.Since(started)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := received.Load(); got != totalBytes {
				t.Fatalf("download total mismatch %d/%d", got, totalBytes)
			}
			avgMbps := float64(totalBytes) * 8 / elapsed.Seconds() / 1e6
			stats := client.Snapshot()
			t.Logf("WEIGHTED_STARTUP streams=%d paths=%d avg=%.3fMbps peak200ms=%.3fMbps min_after_200=%.3fMbps seconds=%.3f window_waits=%d retransmits=%d samples=%v",
				streamCount, pathCount, avgMbps, peakMbps, minAfterRamp, elapsed.Seconds(), stats.WindowWaits, stats.Retransmits, samples)
			if !ramped || peakMbps < 300 {
				t.Fatalf("weighted startup never reached useful aggregate rate: peak=%.3f", peakMbps)
			}
			if avgMbps < 250 {
				t.Fatalf("weighted aggregate average too low: %.3f Mbps", avgMbps)
			}
			if !math.IsInf(minAfterRamp, 1) && minAfterRamp < 100 {
				t.Fatalf("weighted startup collapsed after ramp: %.3f Mbps", minAfterRamp)
			}
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
