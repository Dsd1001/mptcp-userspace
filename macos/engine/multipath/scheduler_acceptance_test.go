package multipath

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// This environment variable is test-only. Product policy always comes from
// the validated profile and authenticated immutable session hello.
func testSchedulerMode(t *testing.T) SchedulerMode {
	t.Helper()
	mode, err := ParseSchedulerMode(os.Getenv("MPX_TEST_SCHEDULER"))
	if err != nil {
		t.Fatal(err)
	}
	return mode
}

type schedulerSample struct {
	Seconds float64 `json:"seconds"`
	Client  Stats   `json:"client"`
	Server  Stats   `json:"server"`
}

type smallExchangeTiming struct {
	Count uint64 `json:"count"`
	NS    int64  `json:"total_ns"`
	MaxNS int64  `json:"max_ns"`
}

var schedulerSmallTiming struct {
	sync.Mutex
	smallExchangeTiming
}

func resetSchedulerSmallTiming() {
	schedulerSmallTiming.Lock()
	schedulerSmallTiming.smallExchangeTiming = smallExchangeTiming{}
	schedulerSmallTiming.Unlock()
}

func noteSchedulerSmallTiming(size int, elapsed time.Duration) {
	if size > 8193 {
		return
	}
	schedulerSmallTiming.Lock()
	schedulerSmallTiming.Count++
	schedulerSmallTiming.NS += int64(elapsed)
	schedulerSmallTiming.MaxNS = max(schedulerSmallTiming.MaxNS, int64(elapsed))
	schedulerSmallTiming.Unlock()
}

func getSchedulerSmallTiming() smallExchangeTiming {
	schedulerSmallTiming.Lock()
	defer schedulerSmallTiming.Unlock()
	return schedulerSmallTiming.smallExchangeTiming
}

// Exact same 64MiB high-BDP helper and 16MiB warmup, with a same-mode paired
// fastest-only reference. No test-only rate priors, skipped hash or new timer.
func TestSchedulerExtremePathProtection(t *testing.T) {
	path := os.Getenv("MPX_SCHEDULER_EXTREME_REPORT")
	if path == "" {
		t.Skip("opt-in scheduler extreme path review")
	}
	var results []highBDPResult
	defer func() {
		data, err := json.MarshalIndent(map[string]any{"version": Version, "wire_protocol": WireProtocol, "source_id": SourceID, "scheduler_mode": testSchedulerMode(t), "warmup_bytes": 16 << 20, "cases": results}, "", "  ")
		if err == nil {
			err = os.WriteFile(path, append(data, '\n'), 0644)
		}
		if err != nil {
			t.Error(err)
		}
	}()
	var fastest float64
	for _, mixed := range []bool{false, true} {
		name := "extreme-fastest-300Mbps"
		rates, rtts := []int64{37500000}, []time.Duration{30 * time.Millisecond}
		if mixed {
			name = "extreme-300+5Mbps-30+200ms"
			rates = append(rates, 625000)
			rtts = append(rtts, 200*time.Millisecond)
		}
		t.Run(name, func(t *testing.T) {
			r := highBDPCase(t, name, rates, rtts, 64<<20)
			results = append(results, r)
			if !mixed {
				fastest = r.Mbps
			} else if r.Mbps < .95*fastest {
				t.Errorf("extreme path protection below 95%%: %.3f vs fastest %.3f", r.Mbps, fastest)
			}
		})
	}
}

func TestSchedulerForcedModesSurviveCarrierRejoin(t *testing.T) {
	for _, mode := range []SchedulerMode{SchedulerAggregate, SchedulerProtect} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
			var relays []*testRelay
			var addresses []string
			for i := 0; i < 2; i++ {
				r := newTestRelay(t, l.Addr().String(), 2<<20, 3*time.Millisecond)
				relays = append(relays, r)
				addresses = append(addresses, r.listener.Addr().String())
			}
			client, err := DialClientWithScheduler(ctx, addresses, testToken, mode)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err = client.WaitPaths(ctx, 2); err != nil {
				t.Fatal(err)
			}
			failed := time.AfterFunc(150*time.Millisecond, relays[0].fail)
			recovered := time.AfterFunc(600*time.Millisecond, relays[0].recover)
			defer failed.Stop()
			defer recovered.Stop()
			if _, err = transfer(client, 4<<20); err != nil {
				t.Fatal("mode path failure", mode, err, client.Snapshot())
			}
			if err = client.WaitPaths(ctx, 2); err != nil {
				t.Fatal(err)
			}
			peer := srv.session(client.id)
			for _, side := range []*Session{client, peer} {
				st := side.Snapshot()
				if st.ConfiguredSchedulerMode != mode || st.EffectiveSchedulerMode != mode || st.ModeSwitches != 0 {
					t.Fatal("forced policy changed on rejoin", st.SchedulerStats)
				}
			}
			snapshot := client.Snapshot()
			if snapshot.PathStats[0].Connections < 2 {
				t.Fatal("path failure/rejoin not exercised")
			}
			t.Log(fmt.Sprintf("mode=%s rejoined=%d established policy unchanged", mode, snapshot.PathStats[0].Connections))
		})
	}
}
