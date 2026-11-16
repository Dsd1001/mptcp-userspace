package multipath

// Ad-hoc A/B fixture: archived v1.2.2 sources only; not a release change.
// Identical 6-loopback-carrier shaping, backend and transfer payload for plain
// versus shared-routed MPX/4, including one-stream and eight-stream traffic.
import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"testing"
	"time"
)

func diagRoutedTransfer(s *Session, size int) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	st, err := s.OpenRoutedTCP(ctx, 1)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(60 * time.Second))
	data := make([]byte, size)
	if _, err = rand.Read(data); err != nil {
		return 0, err
	}
	result := make(chan error, 1)
	started := time.Now()
	go func() {
		for pos := 0; pos < len(data); {
			n := min(len(data)-pos, 1+(pos*17+7919)%65003)
			written, e := st.Write(data[pos : pos+n])
			pos += written
			if e != nil {
				result <- e
				return
			}
		}
		result <- st.CloseWrite()
	}()
	got, err := io.ReadAll(io.LimitReader(st, int64(size)+1))
	if err != nil {
		st.Close()
		<-result
		return 0, err
	}
	if err = <-result; err != nil {
		return 0, err
	}
	if len(got) != size || sha256.Sum256(got) != sha256.Sum256(data) {
		return 0, fmt.Errorf("routed integrity mismatch: %d/%d", len(got), size)
	}
	return time.Since(started), nil
}

func diagRates(a, b Stats) ([]uint64, int) {
	prior := map[int]uint64{}
	for _, p := range a.PathStats {
		prior[p.ID] = p.Sent
	}
	out := make([]uint64, 0, len(b.PathStats))
	active := 0
	for _, p := range b.PathStats {
		n := p.Sent - prior[p.ID]
		out = append(out, n)
		if n > (32 << 10) {
			active++
		}
	}
	return out, active
}

func diagRoutedCase(t *testing.T, routed bool, mbps, rttMS int, mode SchedulerMode) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend, _ := echoBackend(t)
	var srv *Server
	var err error
	if routed {
		srv, err = NewRoutedServerWithScheduler(ctx, testToken, []TCPRoute{
			{ID: 1, Backend: backend},
			{ID: 2, Backend: backend},
			{ID: 3, Backend: backend},
		}, 2, SchedulerAuto)
	} else {
		srv, err = NewServer(ctx, testToken, backend, 2)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	listener, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(listener)
	const paths = 6
	addresses := make([]string, paths)
	capacities := make([]PathCapacity, paths)
	for i := range addresses {
		rate := int64(mbps) * 1000000 / 8 / paths
		addresses[i] = highBDPRelay(t, listener.Addr().String(), rate, time.Duration(rttMS/2)*time.Millisecond)
		configured := math.Round(float64(mbps)/paths*10) / 10
		capacities[i] = PathCapacity{DownloadMbps: configured, UploadMbps: configured}
	}
	if mode != SchedulerWeighted {
		capacities = nil
	}
	var client *Session
	if routed {
		client, err = DialClientWithRoutedPolicy(ctx, addresses, testToken, mode, capacities,
			[]RoutePolicy{{ID: 1, Weight: 1}, {ID: 2, Weight: 1}, {ID: 3, Weight: 1}})
	} else {
		client, err = DialClientWithPolicy(ctx, addresses, testToken, mode, capacities)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	wait, done := context.WithTimeout(ctx, 12*time.Second)
	err = client.WaitPaths(wait, paths)
	done()
	if err != nil {
		t.Fatal(err)
	}
	send := func(size int) (time.Duration, error) {
		if routed {
			return diagRoutedTransfer(client, size)
		}
		return transfer(client, size)
	}
	coldStarted := time.Now()
	if _, err = send(1 << 20); err != nil {
		t.Fatal(err)
	}
	cold := time.Since(coldStarted)
	if _, err = send(16 << 20); err != nil {
		t.Fatal(err)
	}
	clientBefore := client.Snapshot()
	peers := srv.Snapshots()
	if len(peers) != 1 {
		t.Fatalf("expected one server Session, got %d", len(peers))
	}
	serverBefore := peers[0]
	cpuBefore := readTestProcessUsage()
	one, err := send(64 << 20)
	cpuAfter := readTestProcessUsage()
	if err != nil {
		t.Fatal(err)
	}
	clientAfter := client.Snapshot()
	peers = srv.Snapshots()
	if len(peers) != 1 {
		t.Fatal("lost Session during throughput")
	}
	serverAfter := peers[0]
	sender, used := diagRates(serverBefore, serverAfter)

	var wg sync.WaitGroup
	errors := make(chan error, 8)
	parallelStarted := time.Now()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := send(8 << 20)
			errors <- e
		}()
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatalf("parallel transfer: %v", e)
		}
	}
	parallel := time.Since(parallelStarted)
	both := client.Snapshot()
	routeDispatched := make(map[uint32]uint64)
	for _, route := range both.Routes {
		routeDispatched[route.ID] = route.DispatchedPayloadBytes
	}
	report := map[string]any{
		"type":           map[bool]string{true: "routed", false: "raw"}[routed],
		"scheduler_mode": mode, "path_count": paths, "total_config_mbps": mbps, "rtt_ms": rttMS,
		"cold_1m_wall_seconds": cold.Seconds(),
		"single_64m_seconds":   one.Seconds(), "single_64m_mbps": float64(64<<20) * 8 / one.Seconds() / 1e6,
		"parallel_8x8m_seconds": parallel.Seconds(), "parallel_8x8m_mbps": float64(64<<20) * 8 / parallel.Seconds() / 1e6,
		"single_server_send_bytes_by_path": sender, "single_server_paths_used": used,
		"single_client_window_waits_delta":         clientAfter.WindowWaits - clientBefore.WindowWaits,
		"single_server_flight_budget_blocks_delta": serverAfter.Resources.PathSelection.FlightBudget - serverBefore.Resources.PathSelection.FlightBudget,
		"single_server_carrier_queue_blocks_delta": serverAfter.Resources.PathSelection.CarrierQueue - serverBefore.Resources.PathSelection.CarrierQueue,
		"single_server_cost_deferrals_delta":       serverAfter.Resources.PathSelection.CostDeferral - serverBefore.Resources.PathSelection.CostDeferral,
		"single_server_selected_delta":             serverAfter.Resources.PathSelection.Selected - serverBefore.Resources.PathSelection.Selected,
		"single_cpu_seconds":                       cpuAfter.CPUSeconds - cpuBefore.CPUSeconds,
		"active_routes":                            routeDispatched,
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PERF_DIAG %s", b)
}

func TestDiagnoseRoutedVsRaw(t *testing.T) {
	if os.Getenv("MPX_DIAG_ROUTED") != "1" {
		t.Skip("set MPX_DIAG_ROUTED=1 for private A/B fixture")
	}
	mode := testSchedulerMode(t)
	mbps, rttMS := 500, 50
	for _, field := range []struct {
		name string
		dst  *int
	}{{"MPX_DIAG_MBPS", &mbps}, {"MPX_DIAG_RTT", &rttMS}} {
		if raw := os.Getenv(field.name); raw != "" {
			var n int
			if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
				t.Fatal(err)
			}
			*field.dst = n
		}
	}
	if mbps < 12 || mbps > 2000 || rttMS < 0 || rttMS > 400 {
		t.Fatal("invalid private A/B shaping")
	}
	for _, routed := range []bool{false, true} {
		name := "raw"
		if routed {
			name = "routed"
		}
		t.Run(name, func(t *testing.T) {
			diagRoutedCase(t, routed, mbps, rttMS, mode)
		})
	}
}
