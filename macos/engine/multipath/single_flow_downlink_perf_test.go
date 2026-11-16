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
	"testing"
	"time"
)

// Optional, controlled end-to-end download A/B. This models a TCP backend
// (iCable) sending a large response through the shared routed egress (HKBN)
// over six rate-limited TCP Carriers. Not a substitute for public WAN testing.
func TestDiagnoseRoutedDownlink(t *testing.T) {
	if os.Getenv("MPX_DIAG_DOWNLINK") != "1" {
		t.Skip("set MPX_DIAG_DOWNLINK=1")
	}
	const totalMbps = 564
	const paths = 6
	const delay = 25 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				const bufSize = 32 << 10
				chunk := make([]byte, bufSize)
				for n := 0; n < 128<<20; n += bufSize {
					if _, writeErr := c.Write(chunk); writeErr != nil {
						return
					}
				}
				if tcp, ok := c.(*net.TCPConn); ok {
					_ = tcp.CloseWrite()
				}
			}(c)
		}
	}()
	defer func() { l.Close(); wg.Wait() }()
	idleCount, _ := strconv.Atoi(os.Getenv("MPX_DIAG_IDLE_STREAMS"))
	if idleCount < 0 || idleCount > 150 {
		t.Fatal("invalid MPX_DIAG_IDLE_STREAMS")
	}
	idleBackend, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer idleBackend.Close()
	go func() {
		for {
			c, err := idleBackend.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(io.Discard, c)
			}(c)
		}
	}()
	srv, e := NewRoutedServerWithScheduler(ctx, testToken, []TCPRoute{
		{ID: 1, Backend: l.Addr().String()},
		{ID: 2, Backend: idleBackend.Addr().String()},
	}, 2, SchedulerAuto)
	if e != nil {
		t.Fatal(e)
	}
	defer srv.Close()
	listen, e := PlainListen(ctx, "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go srv.Serve(listen)
	var addresses []string
	capacities := make([]PathCapacity, paths)
	for i := 0; i < paths; i++ {
		rate := int64(totalMbps) * 1_000_000 / 8 / paths
		addresses = append(addresses, highBDPRelay(t, listen.Addr().String(), rate, delay))
		mbps := math.Round(float64(totalMbps)/paths*10) / 10
		capacities[i] = PathCapacity{DownloadMbps: mbps, UploadMbps: mbps}
	}
	mode := testSchedulerMode(t)
	if mode != SchedulerWeighted {
		capacities = nil
	}
	client, e := DialClientWithRoutedPolicy(ctx, addresses, testToken, mode, capacities, []RoutePolicy{{ID: 1, Weight: 1}, {ID: 2, Weight: 1}})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	wait, stop := context.WithTimeout(ctx, 8*time.Second)
	if e = client.WaitPaths(wait, paths); e != nil {
		t.Fatal(e)
	}

	stop()
	var idleOpen []*Stream
	defer func() {
		for _, st := range idleOpen {
			_ = st.Close()
		}
	}()
	if idleCount > 0 {
		for i := 0; i < idleCount; i++ {
			openCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			st, err := client.OpenRoutedTCP(openCtx, 2)
			cancel()
			if err != nil {
				t.Fatalf("idle route stream %d/%d: %v", i, idleCount, err)
			}
			idleOpen = append(idleOpen, st)
		}
		for _, st := range idleOpen {
			if err := st.waitProductReply(); err != nil {
				t.Fatal(err)
			}
		}
		// Allow handshake DATA/ACK to settle outside the active-TX grace
		// period. This models an actual long-lived, quiet background route.
		time.Sleep(900 * time.Millisecond)
		for _, snap := range srv.Snapshots() {
			t.Logf("IDLE_DIAG mode=%s requested_idle=%d established_streams=%d", mode, idleCount, snap.Connections)
		}
	}
	download := func(size int64) (time.Duration, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st, e := client.OpenRoutedTCP(ctx, 1)
		if e != nil {
			return 0, e
		}
		defer st.Close()
		started := time.Now()
		n, e := io.CopyN(io.Discard, st, size)
		if e != nil {
			return 0, fmt.Errorf("short routed response %d/%d: %w", n, size, e)
		}
		return time.Since(started), nil
	}
	for _, c := range []struct {
		label       string
		size        int64
		concurrency int
	}{
		{"single", 64 << 20, 1}, {"parallel", 8 << 20, 8},
	} {
		start := time.Now()
		errs := make(chan error, c.concurrency)
		for i := 0; i < c.concurrency; i++ {
			go func() { _, e := download(c.size); errs <- e }()
		}
		for i := 0; i < c.concurrency; i++ {
			if e := <-errs; e != nil {
				t.Fatal(e)
			}
		}
		duration := time.Since(start)
		ss := srv.Snapshots()
		var totalSelected, costDeferral, flightBudget uint64
		var serverStreams int
		var prio uint64
		var waitReasons map[string]creditWait
		var hot []StreamFlowStats
		if len(ss) == 1 {
			totalSelected = ss[0].Resources.PathSelection.Selected
			costDeferral = ss[0].Resources.PathSelection.CostDeferral
			flightBudget = ss[0].Resources.PathSelection.FlightBudget
			waitReasons = ss[0].Resources.WriteWaits
			hot = ss[0].HotStreams
			prio = ss[0].FirstDataPriorityFrames
			serverStreams = ss[0].Connections
		}
		t.Logf("DOWNLOAD_DIAG mode=%s buffer_kib=%s idle=%d established_streams=%d priority=%d type=%s total_mib=%d elapsed_seconds=%.4f mbps=%.2f selected=%d cost_deferral=%d flight_budget=%d wait_reasons=%v hot_streams=%d", mode, os.Getenv("MPX_ROUTED_BACKEND_RCVBUF_KIB"), idleCount, serverStreams, prio, c.label, c.size*int64(c.concurrency)/(1<<20), duration.Seconds(), float64(c.size*int64(c.concurrency))*8/duration.Seconds()/1e6, totalSelected, costDeferral, flightBudget, waitReasons, len(hot))
	}
}
