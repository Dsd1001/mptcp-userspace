package multipath

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProductWeightedConcurrentStartup(t *testing.T) {
	if os.Getenv("MPX_PRODUCT_STARTUP") != "1" {
		t.Skip("set MPX_PRODUCT_STARTUP=1 for UoT-enabled product-mux concurrent startup validation")
	}
	const pathCount = 6
	streamCount, bytesPerStream := 53, 4<<20
	if raw := os.Getenv("MPX_PRODUCT_STREAMS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > MaxStreams-productPreopenPoolSize {
			t.Fatalf("invalid MPX_PRODUCT_STREAMS=%q", raw)
		}
		streamCount = n
		if streamCount > 64 {
			bytesPerStream = 2 << 20
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	backend := weightedDownloadBackend(t, bytesPerStream)
	server, err := NewServer(ctx, testToken, backend, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = server.EnableUOT(udpEchoBackend(t)); err != nil {
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
	client, err := DialClientWithUOTPolicy(ctx, addresses, testToken, SchedulerWeighted, capacities)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	wait, stop := context.WithTimeout(ctx, 10*time.Second)
	if err = client.WaitPaths(wait, pathCount); err != nil {
		stop()
		t.Fatal(err)
	}
	stop()

	uot, err := StartClientUOT(client, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer uot.Close()
	waitProductPool(t, client, productPreopenPoolSize)

	var received atomic.Int64
	start := make(chan struct{})
	errs := make(chan error, streamCount)
	var wg sync.WaitGroup
	for i := 0; i < streamCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			st, err := client.OpenTCP(ctx)
			if err != nil {
				errs <- fmt.Errorf("stream %d open: %w", i, err)
				return
			}
			defer st.Close()
			st.SetDeadline(time.Now().Add(45 * time.Second))
			if _, err = st.Write([]byte{1}); err != nil {
				errs <- fmt.Errorf("stream %d trigger: %w", i, err)
				return
			}
			if err = st.CloseWrite(); err != nil {
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
		}(i)
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
			t.Logf("PRODUCT_WEIGHTED_STARTUP streams=%d paths=%d avg=%.3fMbps peak200ms=%.3fMbps min_after_200=%.3fMbps seconds=%.3f window_waits=%d retransmits=%d samples=%v",
				streamCount, pathCount, avgMbps, peakMbps, minAfterRamp, elapsed.Seconds(), stats.WindowWaits, stats.Retransmits, samples)
			if !ramped || peakMbps < 300 {
				t.Fatalf("product startup never reached useful aggregate rate: peak=%.3f", peakMbps)
			}
			if avgMbps < 250 {
				t.Fatalf("product aggregate average too low: %.3f Mbps", avgMbps)
			}
			if !math.IsInf(minAfterRamp, 1) && minAfterRamp < 100 {
				t.Fatalf("product startup collapsed after ramp: %.3f Mbps", minAfterRamp)
			}
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
