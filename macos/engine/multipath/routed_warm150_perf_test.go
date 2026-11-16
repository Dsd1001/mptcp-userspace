package multipath

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// A controlled Routed-only throughput acceptance fixture. It pre-establishes
// all 150 backend TCP connections before releasing one shared start gate.
// This separates the route's 8 concurrent-dial limit from actual synchronized
// DATA dispatch, unlike 150 simultaneous OpenRoutedTCP operations.
func TestDiagnoseRoutedWarmSynchronized150(t *testing.T) {
	if os.Getenv("MPX_DIAG_WARM150") != "1" {
		t.Skip("set MPX_DIAG_WARM150=1")
	}
	const paths = 6
	const totalMbps = 564
	const count = 150
	const perStream = 4 << 20
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	startGate := make(chan struct{})
	go func() {
		for {
			c, e := backend.Accept()
			if e != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				select {
				case <-startGate:
				case <-ctx.Done():
					return
				}
				data := make([]byte, MaxPayload)
				for i := 0; i < perStream; i += len(data) {
					if _, e := c.Write(data); e != nil {
						return
					}
				}
				if tcp, ok := c.(*net.TCPConn); ok {
					_ = tcp.CloseWrite()
				}
			}(c)
		}
	}()
	srv, e := NewRoutedServerWithScheduler(ctx, testToken, []TCPRoute{{ID: 1, Backend: backend.Addr().String()}}, 2, SchedulerAuto)
	if e != nil {
		t.Fatal(e)
	}
	defer srv.Close()
	listener, e := PlainListen(ctx, "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go srv.Serve(listener)
	capacities := make([]PathCapacity, paths)
	addresses := make([]string, paths)
	for i := range addresses {
		rate := int64(totalMbps) * 1_000_000 / 8 / paths
		addresses[i] = highBDPRelay(t, listener.Addr().String(), rate, 25*time.Millisecond)
		cap := math.Round(float64(totalMbps)/paths*10) / 10
		capacities[i] = PathCapacity{DownloadMbps: cap, UploadMbps: cap}
	}
	client, e := DialClientWithRoutedPolicy(ctx, addresses, testToken, SchedulerWeighted, capacities, []RoutePolicy{{ID: 1, Weight: 1}})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	wait, wcancel := context.WithTimeout(ctx, 8*time.Second)
	if e = client.WaitPaths(wait, paths); e != nil {
		t.Fatal(e)
	}
	wcancel()
	openStreams := make([]*Stream, 0, count)
	defer func() {
		for _, st := range openStreams {
			_ = st.Close()
		}
	}()
	for i := 0; i < count; i++ {
		oc, ocancel := context.WithTimeout(ctx, 10*time.Second)
		st, err := client.OpenRoutedTCP(oc, 1)
		ocancel()
		if err != nil {
			t.Fatalf("warm stream %d OpenRoutedTCP: %v", i, err)
		}
		openStreams = append(openStreams, st)
		if err := st.waitProductReply(); err != nil {
			t.Fatalf("warm stream %d backend dial: %v", i, err)
		}
	}
	if len(openStreams) != count {
		t.Fatal("not all streams opened")
	}
	// Let backend handshakes settle before releasing synchronized DATA.
	time.Sleep(900 * time.Millisecond)
	var serverSession *Session
	srv.mu.Lock()
	for _, ss := range srv.sessions {
		serverSession = ss
	}
	srv.mu.Unlock()
	if serverSession == nil {
		t.Fatal("missing server Session")
	}
	serverSession.mu.Lock()
	established := len(serverSession.streams)
	serverSession.mu.Unlock()
	if established < count {
		t.Fatalf("expected %d warmed streams; only %d", count, established)
	}
	started := time.Now()
	close(startGate)
	var wg sync.WaitGroup
	errors := make(chan error, count)
	for _, st := range openStreams {
		wg.Add(1)
		go func(st *Stream) {
			defer wg.Done()
			_ = st.SetReadDeadline(time.Now().Add(90 * time.Second))
			n, e := io.CopyN(io.Discard, st, perStream)
			if e != nil {
				errors <- fmt.Errorf("read %d/%d: %w", n, perStream, e)
			} else {
				errors <- nil
			}
		}(st)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(started)
	snapshot := serverSession.Snapshot()
	t.Logf("WARM150_DIAG established=%d total_mib=%d seconds=%.4f mbps=%.3f priority=%d flight_budget=%d cost_deferral=%d retransmits=%d",
		established, count*perStream/(1<<20), elapsed.Seconds(),
		float64(count*perStream)*8/elapsed.Seconds()/1e6,
		snapshot.FirstDataPriorityFrames, snapshot.Resources.PathSelection.FlightBudget,
		snapshot.Resources.PathSelection.CostDeferral, snapshot.Retransmits)
}
