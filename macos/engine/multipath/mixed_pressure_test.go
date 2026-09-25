package multipath

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestHighBDPMixedLongStreamAndShortBursts(t *testing.T) {
	if os.Getenv("MPX_MIXED") != "1" {
		t.Skip("set MPX_MIXED=1 for real shaped mixed-stream pressure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
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
	addresses := make([]string, 6)
	for i := range addresses {
		addresses[i] = highBDPRelay(t, l.Addr().String(), 62500000/6, 25*time.Millisecond)
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
	before := srv.AdmissionSnapshot()
	done := make(chan struct{})
	var mu sync.Mutex
	durations := []float64{}
	failures := []string{}
	peakStreams, peakCredit, peakPending := 0, 0, 0
	go func() {
		defer close(done)
		time.Sleep(2 * time.Second)
		for round := 0; round < 9; round++ {
			burst := []int{8, 16, 32}[round%3]
			var wg sync.WaitGroup
			for i := 0; i < burst; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					start := time.Now()
					_, e := transfer(s, []int{1, 1537, 32769}[i%3])
					st := s.Snapshot()
					mu.Lock()
					durations = append(durations, time.Since(start).Seconds()*1000)
					peakStreams = max(peakStreams, st.Connections)
					peakCredit = max(peakCredit, st.ReceiveCredit)
					peakPending = max(peakPending, st.PendingBytes)
					if e != nil {
						failures = append(failures, e.Error())
					}
					mu.Unlock()
				}(i)
			}
			wg.Wait()
			time.Sleep(2 * time.Second)
		}
	}()
	sustainedStream(t, s, srv, 35*time.Second, "500Mbps-six-paths-mixed-short", true)
	<-done
	sort.Float64s(durations)
	after := srv.AdmissionSnapshot()
	result := map[string]any{"version": Version, "scope": "One 35-second logical flow plus nine 8/16/32-stream bursts on six real shaped loopback carriers, 500 Mbps sum and 50 ms RTT; not a WAN or standalone App benchmark", "short_attempts": len(durations), "short_errors": failures, "max_short_ms": durations[len(durations)-1], "p95_short_ms": durations[int(float64(len(durations))*.95)], "peak_sampled_streams": peakStreams, "peak_sampled_credit": peakCredit, "peak_sampled_pending_bytes": peakPending, "admission_before": before, "admission_after": after, "client_after": s.Snapshot(), "landing_after": srv.Snapshots()}
	if path := os.Getenv("MPX_MIXED_REPORT"); path != "" {
		data, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if len(failures) > 0 {
		t.Fatalf("short stream failures alongside big flow: %v", failures)
	}
	if len(durations) != 168 {
		t.Fatalf("missing burst work: %d", len(durations))
	}
	if after.Accepted != before.Accepted || after.Rejected != before.Rejected || s.Snapshot().Paths != 6 {
		t.Fatal("mixed pressure restarted or reauthenticated shared carriers")
	}
	if len(s.Snapshot().Resources.Rejections) != 0 {
		t.Fatal("mixed pressure produced resource refusals")
	}
	t.Logf("MIXED_SHORTS success=%d/%d p95_ms=%.2f max_ms=%.2f handshake_delta=0", len(durations), len(durations), durations[int(float64(len(durations))*.95)], durations[len(durations)-1])
}

func TestSixCarrierReconnectBudgetAndReasonCounters(t *testing.T) {
	s, srv, relays, _ := testTCP(t, 6, 32<<20, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	before := srv.AdmissionSnapshot()
	for cycle := 0; cycle < 3; cycle++ {
		for _, r := range relays {
			r.fail()
		}
		time.Sleep(20 * time.Millisecond)
		for _, r := range relays {
			r.recover()
		}
		if err := s.WaitPaths(ctx, 6); err != nil {
			t.Fatal(err)
		}
		if _, err := transfer(s, 32769); err != nil {
			t.Fatal(err)
		}
	}
	after := srv.AdmissionSnapshot()
	if after.Accepted < before.Accepted+18 {
		t.Fatalf("not all six carriers actually reauthenticated: %+v", after)
	}
	for reason, count := range after.RejectionReasons {
		if count > 0 && reason != "source_rate" && reason != "source_concurrency" {
			t.Fatalf("unexpected rejection class %s", reason)
		}
	}
	if after.Active > handshakeGlobalLimit || s.Snapshot().Paths != 6 {
		t.Fatal("admission/transport failed bounded recovery")
	}
	raw, _ := json.Marshal(map[string]any{"before": before, "after": after, "client": s.Snapshot(), "scope": "Controlled six-carrier churn from one loopback source. Does not attribute historic production rejected counts."})
	t.Logf("RECONNECT_ADMISSION %s", raw)
}
