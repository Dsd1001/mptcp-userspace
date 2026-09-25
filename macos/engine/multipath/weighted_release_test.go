package multipath

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"
)

var weightedReleaseRTTs = []time.Duration{
	28700 * time.Microsecond,
	27500 * time.Microsecond,
	32800 * time.Microsecond,
	31700 * time.Microsecond,
	27500 * time.Microsecond,
	30800 * time.Microsecond,
}

type weightedReleaseTrial struct {
	Trial       int       `json:"trial"`
	Mbps        float64   `json:"mbps"`
	Seconds     float64   `json:"seconds"`
	Efficiency  float64   `json:"efficiency"`
	Retransmits uint64    `json:"retransmits"`
	ReorderPeak int       `json:"reorder_peak"`
	Shares      []float64 `json:"path_shares"`
}

type weightedFaultResult struct {
	Mbps                   float64 `json:"mbps"`
	RetransmitsDelta       uint64  `json:"retransmits_delta"`
	Path2PreShare          float64 `json:"path2_pre_share"`
	Path2PenaltyShare      float64 `json:"path2_penalty_incremental_share"`
	Path2RecoveryShare     float64 `json:"path2_recovery_incremental_share"`
	Path2ConfiguredRateBPS float64 `json:"path2_configured_rate_bps"`
	Path2Connected         bool    `json:"path2_connected"`
	Path2LastError         string  `json:"path2_last_error"`
}

type weightedReleaseReport struct {
	Version            string                 `json:"version"`
	SourceID           string                 `json:"source_id"`
	WireProtocol       int                    `json:"wire_protocol"`
	CapabilityRevision int                    `json:"capability_revision"`
	Mode               SchedulerMode          `json:"scheduler_mode"`
	BaseRTTMS          []float64              `json:"base_rtt_ms"`
	CapacityMbps       []float64              `json:"capacity_mbps"`
	Trials             []weightedReleaseTrial `json:"trials"`
	MedianMbps         float64                `json:"median_mbps"`
	MedianEfficiency   float64                `json:"median_efficiency"`
	Fault              weightedFaultResult    `json:"single_timeout_fault"`
}

func weightedReleaseFixture(t *testing.T) (*Session, *Server, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	backend, _ := echoBackend(t)
	server, err := NewServer(ctx, testToken, backend, 2)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	l, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		server.Close()
		cancel()
		t.Fatal(err)
	}
	go server.Serve(l)
	addresses := make([]string, 6)
	capacities := make([]PathCapacity, 6)
	for i := range addresses {
		addresses[i] = highBDPRelay(t, l.Addr().String(), 50_000_000/8, weightedReleaseRTTs[i]/2)
		capacities[i] = PathCapacity{DownloadMbps: 50, UploadMbps: 50}
	}
	client, err := DialClientWithPolicy(ctx, addresses, testToken, SchedulerWeighted, capacities)
	if err != nil {
		l.Close()
		server.Close()
		cancel()
		t.Fatal(err)
	}
	wait, stop := context.WithTimeout(ctx, 10*time.Second)
	if err := client.WaitPaths(wait, 6); err != nil {
		stop()
		client.Close()
		l.Close()
		server.Close()
		cancel()
		t.Fatal(err)
	}
	stop()
	cleanup := func() {
		client.Close()
		l.Close()
		server.Close()
		cancel()
	}
	return client, server, cleanup
}

func weightedReleaseQuiesce(t *testing.T, s *Session) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !capacityReclaimed(s.Snapshot()) {
		if time.Now().After(deadline) {
			t.Fatal("weighted release session did not quiesce")
		}
		time.Sleep(time.Millisecond)
	}
}

func weightedSentSnapshot(s *Session) map[int]uint64 {
	out := map[int]uint64{}
	for _, p := range s.Snapshot().PathStats {
		out[p.ID] = p.Sent
	}
	return out
}

func weightedShares(st Stats, before map[int]uint64) []float64 {
	total := uint64(0)
	delta := map[int]uint64{}
	for _, p := range st.PathStats {
		d := p.Sent - before[p.ID]
		delta[p.ID] = d
		total += d
	}
	shares := make([]float64, 0, len(st.PathStats))
	for _, p := range st.PathStats {
		shares = append(shares, float64(delta[p.ID])/float64(max(uint64(1), total)))
	}
	return shares
}

func weightedTotals(st Stats, before map[int]uint64) (uint64, uint64) {
	total := uint64(0)
	p2 := uint64(0)
	for _, p := range st.PathStats {
		d := p.Sent - before[p.ID]
		total += d
		if p.ID == 2 {
			p2 = d
		}
	}
	return total, p2
}

func weightedIncrementalShare(aTotal, aPath, bTotal, bPath uint64) float64 {
	return float64(bPath-aPath) / float64(max(uint64(1), bTotal-aTotal))
}

func injectWeightedReleaseTimeout(t *testing.T, s *Session) time.Time {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		c := s.paths[2]
		if c != nil && c.active {
			for _, p := range s.pending {
				if p != nil && p.f.kind == kindData && p.path == c && !p.sentAt.IsZero() {
					now := time.Now()
					rto := max(500*time.Millisecond, 4*c.rtt)
					p.sentAt = now.Add(-rto - time.Millisecond)
					s.lastSweep = time.Time{}
					before := s.retransmits
					s.sweepLocked(now)
					ok := s.retransmits == before+1 &&
						c.lastError == "delivery timeout; temporarily deprioritized" &&
						c.penaltyUntil.After(now)
					s.mu.Unlock()
					if !ok {
						t.Fatal("weighted timeout did not enter production delivery-timeout protection")
					}
					return now
				}
			}
		}
		s.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("could not find in-flight DATA on weighted path 2")
	return time.Time{}
}

func TestWeightedReleaseHighBDP(t *testing.T) {
	if os.Getenv("MPX_WEIGHTED_RELEASE") == "" {
		t.Skip("set MPX_WEIGHTED_RELEASE=1")
	}
	report := weightedReleaseReport{
		Version: Version, SourceID: SourceID, WireProtocol: WireProtocol,
		CapabilityRevision: CapabilityRevision, Mode: SchedulerWeighted,
		BaseRTTMS:    []float64{28.7, 27.5, 32.8, 31.7, 27.5, 30.8},
		CapacityMbps: []float64{50, 50, 50, 50, 50, 50},
	}
	values := make([]float64, 0, 3)
	for trial := 1; trial <= 3; trial++ {
		client, _, cleanup := weightedReleaseFixture(t)
		if _, err := transfer(client, 16<<20); err != nil {
			cleanup()
			t.Fatal(err)
		}
		weightedReleaseQuiesce(t, client)
		before := weightedSentSnapshot(client)
		const size = 64 << 20
		elapsed, err := transfer(client, size)
		if err != nil {
			cleanup()
			t.Fatal(err)
		}
		weightedReleaseQuiesce(t, client)
		st := client.Snapshot()
		shares := weightedShares(st, before)
		active := 0
		for _, share := range shares {
			if share > 0.01 {
				active++
			}
		}
		mbps := float64(size) * 8 / elapsed.Seconds() / 1e6
		report.Trials = append(report.Trials, weightedReleaseTrial{
			Trial: trial, Mbps: mbps, Seconds: elapsed.Seconds(),
			Efficiency: mbps / 300, Retransmits: st.Retransmits,
			ReorderPeak: st.ReorderPeak, Shares: shares,
		})
		values = append(values, mbps)
		cleanup()
		if active != 6 {
			t.Fatalf("trial %d used %d/6 weighted paths", trial, active)
		}
		if st.Retransmits != 0 {
			t.Fatalf("trial %d had unexpected retransmits=%d", trial, st.Retransmits)
		}
	}
	sort.Float64s(values)
	report.MedianMbps = values[len(values)/2]
	report.MedianEfficiency = report.MedianMbps / 300
	if report.MedianMbps < 255 {
		t.Errorf("weighted median %.3f Mbps is below 85%% of configured 300 Mbps", report.MedianMbps)
	}

	// One source-matched production-semantic timeout run.
	client, _, cleanup := weightedReleaseFixture(t)
	defer cleanup()
	if _, err := transfer(client, 16<<20); err != nil {
		t.Fatal(err)
	}
	weightedReleaseQuiesce(t, client)
	before := weightedSentSnapshot(client)
	base := client.Snapshot()
	const faultSize = 192 << 20
	type transferDone struct {
		elapsed time.Duration
		err     error
	}
	done := make(chan transferDone, 1)
	go func() {
		elapsed, err := transfer(client, faultSize)
		done <- transferDone{elapsed: elapsed, err: err}
	}()
	time.Sleep(250 * time.Millisecond)
	pre := client.Snapshot()
	preTotal, preP2 := weightedTotals(pre, before)
	injected := injectWeightedReleaseTimeout(t, client)
	just := client.Snapshot()
	justTotal, justP2 := weightedTotals(just, before)
	wait := injected.Add(1500 * time.Millisecond).Sub(time.Now())
	if wait > 0 {
		time.Sleep(wait)
	}
	during := client.Snapshot()
	duringTotal, duringP2 := weightedTotals(during, before)
	wait = injected.Add(2300 * time.Millisecond).Sub(time.Now())
	if wait > 0 {
		time.Sleep(wait)
	}
	afterPenalty := client.Snapshot()
	afterTotal, afterP2 := weightedTotals(afterPenalty, before)
	dr := <-done
	if dr.err != nil {
		t.Fatal(dr.err)
	}
	weightedReleaseQuiesce(t, client)
	final := client.Snapshot()
	finalTotal, finalP2 := weightedTotals(final, before)
	var p2 PathStats
	for _, p := range final.PathStats {
		if p.ID == 2 {
			p2 = p
			break
		}
	}
	report.Fault = weightedFaultResult{
		Mbps:                   float64(faultSize) * 8 / dr.elapsed.Seconds() / 1e6,
		RetransmitsDelta:       final.Retransmits - base.Retransmits,
		Path2PreShare:          float64(preP2) / float64(max(uint64(1), preTotal)),
		Path2PenaltyShare:      weightedIncrementalShare(justTotal, justP2, duringTotal, duringP2),
		Path2RecoveryShare:     weightedIncrementalShare(afterTotal, afterP2, finalTotal, finalP2),
		Path2ConfiguredRateBPS: p2.ConfiguredRateBPS,
		Path2Connected:         p2.Connected,
		Path2LastError:         p2.LastError,
	}
	if report.Fault.RetransmitsDelta != 1 {
		t.Errorf("single injected timeout produced retransmits=%d, want 1", report.Fault.RetransmitsDelta)
	}
	if report.Fault.Path2PenaltyShare >= 0.08 {
		t.Errorf("path2 was not sufficiently avoided during penalty: %.3f", report.Fault.Path2PenaltyShare)
	}
	if report.Fault.Path2RecoveryShare <= 0.08 || !p2.Connected {
		t.Errorf("path2 did not recover after penalty: share=%.3f connected=%v", report.Fault.Path2RecoveryShare, p2.Connected)
	}
	if p2.ConfiguredRateBPS != configuredRateBPS(50) {
		t.Errorf("path2 configured rate changed after timeout: %.0f", p2.ConfiguredRateBPS)
	}

	if path := os.Getenv("MPX_WEIGHTED_RELEASE_REPORT"); path != "" {
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("WEIGHTED_RELEASE median=%.3fMbps efficiency=%.3f fault=%.3fMbps p2 penalty=%.3f recovery=%.3f",
		report.MedianMbps, report.MedianEfficiency, report.Fault.Mbps,
		report.Fault.Path2PenaltyShare, report.Fault.Path2RecoveryShare)
}
