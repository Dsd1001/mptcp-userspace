package multipath

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type capacitySample struct {
	Seconds float64 `json:"seconds"`
	Client  Stats   `json:"client"`
	Server  Stats   `json:"server"`
}
type capacityBulk struct {
	Stream uint64  `json:"stream"`
	Start  float64 `json:"start_seconds"`
	End    float64 `json:"end_seconds"`
	Bytes  int     `json:"bytes"`
	SHA256 string  `json:"sha256"`
}
type capacityResult struct {
	SchedulerMode       SchedulerMode       `json:"scheduler_mode"`
	SmallExchangeTiming smallExchangeTiming `json:"small_exchange_timing"`
	Version             string              `json:"version"`
	SourceID            string              `json:"source_id"`
	Wire                int                 `json:"wire_protocol"`
	Target              int                 `json:"target_streams"`
	Round               int                 `json:"round"`
	RequestedSeconds    int                 `json:"requested_seconds"`
	ObservedSeconds     float64             `json:"observed_seconds"`
	PeakClient          int                 `json:"peak_client_streams"`
	PeakServer          int                 `json:"peak_server_streams"`
	Exchanges           int64               `json:"exchanges"`
	Churn               int64               `json:"churn_reopens"`
	Bulk                []capacityBulk      `json:"bulk_segments"`
	BoundaryNext        bool                `json:"typed_next_stream_rejection"`
	Samples             []capacitySample    `json:"samples"`
	BeforeAdmission     AdmissionStats      `json:"admission_before"`
	AfterAdmission      AdmissionStats      `json:"admission_after"`
	ClientAfter         Stats               `json:"client_after"`
	ServerAfter         Stats               `json:"server_after"`
	Failures            []string            `json:"failures"`
	Passed              bool                `json:"passed"`
	Eligible            bool                `json:"acceptance_eligible"`
	Scope               string              `json:"scope"`
}

// Both directions run concurrently; a test must not introduce an artificial
// write-then-read deadlock when the explicit initial WINDOW is only 16 KiB.
func capacityExchange(st *Stream, size, seed int) error {
	return capacityExchangeWithin(st, size, seed, 5*time.Second)
}

func capacityExchangeWithin(st *Stream, size, seed int, timeout time.Duration) error {
	began := time.Now()
	defer func() { noteSchedulerSmallTiming(size, time.Since(began)) }()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*31 + (i >> 8) + seed) % 256)
	}
	st.SetDeadline(time.Now().Add(timeout))
	done := make(chan error, 1)
	go func() { done <- writeAll(st, data) }()
	got := make([]byte, size)
	_, readErr := io.ReadFull(st, got)
	if readErr != nil {
		st.Close()
	}
	writeErr := <-done
	if readErr != nil {
		return readErr
	}
	if writeErr != nil {
		return writeErr
	}
	if !bytes.Equal(data, got) {
		return fmt.Errorf("binary mismatch on stream %d", st.id)
	}
	return nil
}
func capacityClose(st *Stream, peer *Session) error {
	if st == nil {
		return nil
	}
	st.SetDeadline(time.Now().Add(5 * time.Second))
	if e := st.CloseWrite(); e != nil {
		st.Close()
		return e
	}
	rest, e := io.ReadAll(st)
	st.Close()
	if e != nil {
		return e
	}
	if len(rest) != 0 {
		return fmt.Errorf("unexpected trailing bytes")
	}
	// Do not count an OPEN racing a not-yet-retired peer stream as the next hard-boundary admission.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		peer.mu.Lock()
		old := peer.streamForCreditLocked(st.id)
		peer.mu.Unlock()
		st.s.mu.Lock()
		local := st.s.streamForCreditLocked(st.id)
		st.s.mu.Unlock()
		if old == nil && local == nil {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("peer did not retire stream %d", st.id)
}
func capacityBounds(st Stats) error {
	r := st.Resources
	if r.OccupiedSlots > MaxStreams || r.TXUsed < 0 || r.TXUsed > SessionCreditLimit || r.TXGrowth < 0 || r.TXGrowth > GrowthCreditLimit || r.TXBootstrap < 0 || r.TXBootstrap > BootstrapCreditLimit {
		return fmt.Errorf("sender/closing resource bound")
	}
	for name, pair := range map[string][2]int{"streams": {r.ActiveStreams, MaxStreams}, "credit": {r.ReceiveCredit, SessionCreditLimit}, "bootstrap": {r.BootstrapCredit, BootstrapCreditLimit}, "growth": {r.GrowthCredit, GrowthCreditLimit}, "pages": {r.ReceiveAllocated, MaxBuffered}, "data_frames": {r.DataPendingFrames, MaxDataPending}, "data_bytes": {r.DataPendingBytes, MaxDataPendingBytes}, "control_frames": {r.ControlPendingFrames, MaxControlPending}, "control_bytes": {r.ControlPendingBytes, MaxControlBytes}} {
		if pair[0] < 0 || pair[0] > pair[1] {
			return fmt.Errorf("%s=%d outside 0..%d", name, pair[0], pair[1])
		}
	}
	if r.ReceiveCredit != r.BootstrapCredit+r.GrowthCredit {
		return fmt.Errorf("credit subledger mismatch")
	}
	if r.PendingFrames != r.DataPendingFrames+r.ControlPendingFrames || r.PendingBytes != r.DataPendingBytes+r.ControlPendingBytes {
		return fmt.Errorf("pending subledger mismatch")
	}
	if r.OpenReceiveCreditWaits != 0 || r.Waits[LimitReceiveCredit] != 0 {
		return fmt.Errorf("receive-credit OPEN wait")
	}
	return nil
}
func capacityReclaimed(st Stats) bool {
	r := st.Resources
	return r.ClosingStreams == 0 && r.TXUsed == 0 && r.TXGrowth == 0 && st.Connections == 0 && st.PendingBytes == 0 && st.ReadyFrames == 0 && st.ReceiveCredit == 0 && st.ReceiveAllocated == 0 && st.BufferedBytes == 0 && r.GrowthCredit == 0 && r.ControlPendingFrames == 0 && r.DataPendingFrames == 0 && r.WindowBlockedWriters == 0
}

func capacityCase(t *testing.T, target, round, seconds int) {
	t.Helper()
	resetSchedulerSmallTiming()
	result := capacityResult{SchedulerMode: testSchedulerMode(t), Version: Version, SourceID: SourceID, Wire: WireProtocol, Target: target, Round: round, RequestedSeconds: seconds, Scope: "Actual simultaneous MPX streams over six shaped loopback TCP carriers (500Mbps aggregate, 50ms RTT), not HTTP request totals or an App/WAN measurement"}
	defer func() {
		result.SmallExchangeTiming = getSchedulerSmallTiming()
		result.Passed = !t.Failed()
		result.Eligible = result.Passed && seconds == 30 && result.PeakClient == target && result.PeakServer == target
		if dir := os.Getenv("MPX_CAPACITY_REPORT"); dir != "" {
			if e := os.MkdirAll(dir, 0755); e != nil {
				t.Error(e)
				return
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			if e := os.WriteFile(filepath.Join(dir, fmt.Sprintf("capacity-%d-%d.json", target, round)), append(raw, '\n'), 0644); e != nil {
				t.Error(e)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds+35)*time.Second)
	defer cancel()
	backend, _ := echoBackend(t)
	srv, e := NewServer(ctx, testToken, backend, 2)
	if e != nil {
		t.Fatal(e)
	}
	defer srv.Close()
	listener, e := PlainListen(ctx, "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go srv.Serve(listener)
	addresses := make([]string, 6)
	for i := range addresses {
		addresses[i] = highBDPRelay(t, listener.Addr().String(), 62500000/6, 25*time.Millisecond)
	}
	s, e := DialClientWithScheduler(ctx, addresses, testToken, testSchedulerMode(t))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.WaitPaths(ctx, 6); e != nil {
		t.Fatal(e)
	}
	peer := srv.session(s.id)
	if peer == nil {
		t.Fatal("no matching peer session")
	}
	slots := make([]*Stream, target)
	errs := make(chan error, target*2+32)
	openOne := func() (*Stream, error) {
		c, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		return s.Open(c)
	}
	var wg sync.WaitGroup
	for base := 0; base < target; base += 32 {
		for index := base; index < min(target, base+32); index++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				st, e := openOne()
				if e != nil {
					errs <- e
					return
				}
				slots[i] = st
			}(index)
		}
		wg.Wait()
	}
	if len(errs) > 0 {
		t.Fatal(<-errs)
	}
	initialClient, initialServer := s.Snapshot(), peer.Snapshot()
	if initialClient.Connections != target || initialServer.Connections != target {
		t.Fatal("target never reached", initialClient.Connections, initialServer.Connections)
	}
	if target == MaxStreams {
		st, e := s.Open(ctx)
		if st != nil {
			st.Close()
		}
		if ResourceReason(e) != LimitStreams {
			t.Fatalf("%dth not typed: %v", MaxStreams+1, e)
		}
		result.BoundaryNext = true
	}
	clientRefusals, serverRefusals := s.Snapshot().Resources.Rejections, peer.Snapshot().Resources.Rejections
	result.BeforeAdmission = srv.AdmissionSnapshot()
	began := time.Now()
	until := began.Add(time.Duration(seconds) * time.Second)
	result.Samples = append(result.Samples, capacitySample{0, initialClient, initialServer})
	stop := make(chan struct{})
	var exchanges, churn atomic.Int64
	var eventMu sync.Mutex
	pause := func(d time.Duration) bool {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-stop:
			return false
		case <-ctx.Done():
			return false
		case <-timer.C:
			return time.Now().Before(until)
		}
	}
	nIdle, nSmall := target*65/100, target*25/100
	for index := range slots {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fail := func(e error) {
				if e != nil {
					errs <- fmt.Errorf("slot %d: %w", i, e)
				}
			}
			if i >= target-3 {
				// The bulk flows repeatedly enter and leave; no full-round download.
				for phase, fraction := range []float64{.07, .28, .53, .77} {
					delay := time.Until(began.Add(time.Duration(float64(seconds)*fraction*float64(time.Second)) + time.Duration(i%3)*45*time.Millisecond))
					if delay > 0 && !pause(delay) {
						return
					}
					if !time.Now().Before(until) {
						return
					}
					st := slots[i]
					size := (1 + (i+round+phase)%3) * 2 << 20
					start := time.Since(began).Seconds()
					if e := capacityExchangeWithin(st, size, round+i+phase, 12*time.Second); e != nil {
						fail(e)
						return
					}
					exchanges.Add(1)
					data := make([]byte, size)
					for j := range data {
						data[j] = byte((j*31 + (j >> 8) + round + i + phase) % 256)
					}
					event := capacityBulk{st.id, start, time.Since(began).Seconds(), size, fmt.Sprintf("%x", sha256.Sum256(data))}
					eventMu.Lock()
					result.Bulk = append(result.Bulk, event)
					eventMu.Unlock()
					if e := capacityClose(st, peer); e != nil {
						fail(e)
						return
					}
					slots[i] = nil
					if !time.Now().Before(until) {
						return
					}
					replacement, e := openOne()
					if e != nil {
						fail(e)
						return
					}
					slots[i] = replacement
					churn.Add(1)
				}
				return
			}
			if i < nIdle && i%5 != 0 {
				<-stop
				return
			}
			iteration := 0
			for time.Now().Before(until) {
				size, delay := 1537, 150*time.Millisecond+time.Duration(i%7)*17*time.Millisecond
				if i < nIdle {
					size = 1 + i%31
					delay = 2100*time.Millisecond + time.Duration(i%13)*23*time.Millisecond
				} else if i >= nIdle+nSmall {
					size = 32769 + (i%2)*65536
					delay = 500*time.Millisecond + time.Duration(i%5)*41*time.Millisecond
				} else {
					size = []int{1, 1537, 8193}[iteration%3]
				}
				if i >= target-5 {
					size = 257
					delay = 700 * time.Millisecond
				}
				if e := capacityExchange(slots[i], size, round+i+iteration); e != nil {
					fail(e)
					return
				}
				exchanges.Add(1)
				iteration++
				if i >= target-5 {
					if e := capacityClose(slots[i], peer); e != nil {
						fail(e)
						return
					}
					slots[i] = nil
					if !time.Now().Before(until) {
						return
					}
					st, e := openOne()
					if e != nil {
						fail(e)
						return
					}
					slots[i] = st
					churn.Add(1)
				}
				if !pause(delay) {
					return
				}
			}
		}(index)
	}
	sample := func() {
		client, server := s.Snapshot(), peer.Snapshot()
		result.PeakClient = max(result.PeakClient, client.Connections)
		result.PeakServer = max(result.PeakServer, server.Connections)
		result.Samples = append(result.Samples, capacitySample{time.Since(began).Seconds(), client, server})
		for _, current := range []Stats{client, server} {
			if e := capacityBounds(current); e != nil {
				errs <- e
			}
			if current.Paths != 6 || current.Lifecycle.Closed {
				errs <- fmt.Errorf("shared transport changed")
			}
		}
		if !reflect.DeepEqual(client.Resources.Rejections, clientRefusals) || !reflect.DeepEqual(server.Resources.Rejections, serverRefusals) {
			errs <- fmt.Errorf("unexpected hard refusal")
		}
		for i, p := range client.PathStats {
			if p.Connections != initialClient.PathStats[i].Connections || p.DialAttempts != initialClient.PathStats[i].DialAttempts {
				errs <- fmt.Errorf("carrier %d reconnected under stream pressure", p.ID)
			}
		}
	}
	// Sample the fully populated identities before any churn closes a slot.
	result.PeakClient = initialClient.Connections
	result.PeakServer = initialServer.Connections
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	sample()
	for time.Now().Before(until) {
		select {
		case <-ctx.Done():
			errs <- ctx.Err()
		case <-ticker.C:
			sample()
		}
		if len(errs) > 0 {
			break
		}
	}
	result.ObservedSeconds = time.Since(began).Seconds()
	close(stop)
	wg.Wait()
	result.Exchanges = exchanges.Load()
	result.Churn = churn.Load()
	result.AfterAdmission = srv.AdmissionSnapshot()
	for len(errs) > 0 {
		result.Failures = append(result.Failures, (<-errs).Error())
	}
	for i := range slots {
		if slots[i] != nil {
			wg.Add(1)
			go func(st *Stream) {
				defer wg.Done()
				if e := capacityClose(st, peer); e != nil {
					errs <- e
				}
			}(slots[i])
		}
	}
	wg.Wait()
	for len(errs) > 0 {
		result.Failures = append(result.Failures, (<-errs).Error())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result.ClientAfter = s.Snapshot()
		result.ServerAfter = peer.Snapshot()
		if capacityReclaimed(result.ClientAfter) && capacityReclaimed(result.ServerAfter) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(result.Failures) > 0 {
		t.Errorf("capacity failures: %v", result.Failures)
	}
	if !capacityReclaimed(result.ClientAfter) || !capacityReclaimed(result.ServerAfter) {
		t.Error("stream/credit/page/pending did not return to baseline")
	}
	if result.BeforeAdmission.Accepted != result.AfterAdmission.Accepted || result.BeforeAdmission.Rejected != result.AfterAdmission.Rejected {
		t.Error("stream pressure caused carrier handshakes")
	}
	if seconds == 30 && (len(result.Bulk) < 6 || result.Churn < 8 || result.ObservedSeconds < 30) {
		t.Error("insufficient mixed traffic, churn or duration")
	}
	t.Logf("CAPACITY streams=%d round=%d seconds=%.3f peak=%d/%d exchanges=%d churn=%d bulk_segments=%d failures=%d reclaimed=%v", target, round, result.ObservedSeconds, result.PeakClient, result.PeakServer, result.Exchanges, result.Churn, len(result.Bulk), len(result.Failures), capacityReclaimed(result.ClientAfter) && capacityReclaimed(result.ServerAfter))
}

func TestShortCapacityMatrix(t *testing.T) {
	mode := os.Getenv("MPX_CAPACITY")
	if mode == "" {
		t.Skip("set MPX_CAPACITY=enforce for 128/256/512/1024/2048 streams, 30 seconds x 2")
	}
	counts, rounds, seconds := []int{128, 256, 512, 1024, 2048}, 2, 30
	if mode == "smoke" {
		counts = []int{MaxStreams}
		rounds = 1
		seconds = 5
	} else if mode != "enforce" {
		t.Fatal("invalid capacity mode")
	}
	for _, n := range counts {
		for round := 1; round <= rounds; round++ {
			t.Run(fmt.Sprintf("%d-streams-round-%d", n, round), func(t *testing.T) { capacityCase(t, n, round, seconds) })
		}
	}
}
