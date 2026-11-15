package multipath

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// This is a controlled loopback experiment, NOT a WAN bandwidth guarantee.
// Each TCP forwarding path has its own 2 MiB/s serializer and 5 ms delay.
func TestRoutedControlledAggregation(t *testing.T) {
	if os.Getenv("MPX_ROUTED_PERF") != "1" {
		t.Skip("set MPX_ROUTED_PERF=1 for controlled routed TCP aggregation")
	}
	rates := make(map[int]float64)
	for _, paths := range []int{1, 2, 6} {
		t.Run(fmt.Sprintf("paths_%d", paths), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			backend, _ := routedBannerBackend(t, "")
			srv, err := NewRoutedServerWithScheduler(ctx, testToken, []TCPRoute{{ID: 1, Backend: backend}}, 1, SchedulerAggregate)
			if err != nil {
				t.Fatal(err)
			}
			defer srv.Close()
			l, err := PlainListen(ctx, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			go srv.Serve(l)
			var addresses []string
			for i := 0; i < paths; i++ {
				r := newTestRelay(t, l.Addr().String(), 2<<20, 5*time.Millisecond)
				addresses = append(addresses, r.listener.Addr().String())
			}
			s, err := DialClientWithRoutedPolicy(ctx, addresses, testToken, SchedulerAggregate, nil, []RoutePolicy{{ID: 1}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			wait, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()
			if err = s.WaitPaths(wait, paths); err != nil {
				t.Fatal(err)
			}
			if err = routedRoundtrip(s, 1, "", 1<<20); err != nil {
				t.Fatal(err)
			}
			const payload = 8 << 20
			started := time.Now()
			if err = routedRoundtrip(s, 1, "", payload); err != nil {
				t.Fatal(err)
			}
			seconds := time.Since(started).Seconds()
			rates[paths] = payload / seconds
			t.Logf("ROUTED_MEASUREMENT paths=%d payload_bytes=%d elapsed_seconds=%.6f goodput_mbps=%.3f per_path_limit_mibps=2 scope=single-stream-loopback-echo", paths, payload, seconds, rates[paths]*8/1e6)
		})
	}
	if rates[1] <= 0 || rates[2] <= 0 || rates[6] <= 0 {
		t.Fatal("missing measurements")
	}
	speedup2, speedup6 := rates[2]/rates[1], rates[6]/rates[1]
	t.Logf("ROUTED_SPEEDUP two_paths=%.3fx six_paths=%.3fx", speedup2, speedup6)
	if speedup2 < 1.3 || speedup6 < 2.5 {
		t.Fatalf("controlled aggregation regression: 2=%.3fx 6=%.3fx", speedup2, speedup6)
	}
}
