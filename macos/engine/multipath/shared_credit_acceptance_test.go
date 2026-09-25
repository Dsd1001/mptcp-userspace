package multipath

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type sharedCreditAcceptance struct {
	Version          string  `json:"version"`
	SourceID         string  `json:"source_id"`
	Count            int     `json:"history_streams"`
	Retained         bool    `json:"retained_idle"`
	WarmBytes        int     `json:"session_warmup_bytes"`
	EachHistoryBytes int     `json:"each_history_stream_bytes"`
	IdleSeconds      float64 `json:"idle_seconds"`
	Bytes            int     `json:"measured_bytes"`
	Seconds          float64 `json:"measured_seconds"`
	Mbps             float64 `json:"mbps"`
	Error            string  `json:"error,omitempty"`
	Before           Stats   `json:"client_before"`
	PeerBefore       Stats   `json:"server_before"`
	After            Stats   `json:"client_after"`
	PeerAfter        Stats   `json:"server_after"`
	Cleanup          bool    `json:"cleanup_reclaimed"`
	Scope            string  `json:"scope"`
}

func sharedCreditAcceptanceCase(t *testing.T, count int, keep bool) (r sharedCreditAcceptance) {
	t.Helper()
	r = sharedCreditAcceptance{Version: Version, SourceID: SourceID, Count: count, Retained: keep, WarmBytes: 16 << 20, EachHistoryBytes: 2 << 20, IdleSeconds: 5.5, Bytes: 32 << 20, Scope: "Six equal-capacity 50Mbps/50ms real loopback TCP relays. Both members of each pair have the same N warmed stream history and 5.5s quiet period; only keeping versus closing those streams differs. Original transfer timer and SHA check unchanged. Not an App/WAN speed test."}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	backend, _ := echoBackend(t)
	srv, err := NewServer(ctx, testToken, backend, 2)
	if err != nil {
		r.Error = err.Error()
		return
	}
	defer srv.Close()
	l, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		r.Error = err.Error()
		return
	}
	go srv.Serve(l)
	addresses := make([]string, 6)
	for i := range addresses {
		addresses[i] = highBDPRelay(t, l.Addr().String(), 6250000, 25*time.Millisecond)
	}
	s, err := DialClientWithScheduler(ctx, addresses, testToken, SchedulerAggregate)
	if err != nil {
		r.Error = err.Error()
		return
	}
	defer s.Close()
	if err = s.WaitPaths(ctx, 6); err != nil {
		r.Error = err.Error()
		return
	}
	peer := srv.session(s.id)
	if _, err = transfer(s, r.WarmBytes); err != nil {
		r.Error = "session warmup: " + err.Error()
		return
	}
	streams := make([]*Stream, 0, count)
	for i := 0; i < count; i++ {
		st, e := s.Open(ctx)
		if e != nil {
			r.Error = "history OPEN: " + e.Error()
			return
		}
		streams = append(streams, st)
		if e = sharedHistoryExchange(st, r.EachHistoryBytes, 700+i); e != nil {
			r.Error = fmt.Sprintf("history stream %d: %v", i, e)
			return
		}
	}
	if !keep {
		for _, st := range streams {
			if e := capacityClose(st, peer); e != nil {
				r.Error = "history close: " + e.Error()
				return
			}
		}
	}
	select {
	case <-time.After(5500 * time.Millisecond):
	case <-ctx.Done():
		r.Error = ctx.Err().Error()
		return
	}
	r.Before, r.PeerBefore = s.Snapshot(), peer.Snapshot()
	elapsed, e := transfer(s, r.Bytes)
	if e != nil {
		r.Error = "measured transfer: " + e.Error()
	} else {
		r.Seconds = elapsed.Seconds()
		r.Mbps = float64(r.Bytes) * 8 / r.Seconds / 1e6
	}
	r.After, r.PeerAfter = s.Snapshot(), peer.Snapshot()
	for _, st := range streams {
		st.Close()
	}
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if capacityReclaimed(s.Snapshot()) && capacityReclaimed(peer.Snapshot()) {
			r.Cleanup = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return
}

func TestSharedCreditIdleAcceptance(t *testing.T) {
	if os.Getenv("MPX_SHARED_CREDIT_AB") == "" {
		t.Skip("set MPX_SHARED_CREDIT_AB=1")
	}
	counts := []int{0, 8, 16, 32}
	if value := os.Getenv("MPX_SHARED_CREDIT_COUNTS"); value != "" {
		counts = nil
		for _, v := range strings.Split(value, ",") {
			n, e := strconv.Atoi(v)
			if e != nil || n < 0 || n > 32 {
				t.Fatal("invalid history count")
			}
			counts = append(counts, n)
		}
	}
	results := []sharedCreditAcceptance{}
	defer func() {
		if p := os.Getenv("MPX_SHARED_CREDIT_REPORT"); p != "" {
			b, e := json.MarshalIndent(results, "", "  ")
			if e != nil {
				t.Error(e)
				return
			}
			if e = os.WriteFile(p, append(b, '\n'), 0644); e != nil {
				t.Error(e)
			}
		}
	}()
	order := []bool{false, true}
	if os.Getenv("MPX_SHARED_CREDIT_REVERSE") == "1" {
		order = []bool{true, false}
	}
	for _, n := range counts {
		for _, keep := range order {
			t.Run(fmt.Sprintf("history-%d-retain-%t", n, keep), func(t *testing.T) {
				r := sharedCreditAcceptanceCase(t, n, keep)
				results = append(results, r)
				t.Logf("SHARED_CREDIT history=%d retain=%t mbps=%.3f used=%d growth=%d pages=%d cleanup=%t error=%s", n, keep, r.Mbps, r.Before.ReceiveCredit, r.Before.Resources.GrowthCredit, r.Before.ReceiveAllocated, r.Cleanup, r.Error)
				if r.Error != "" {
					t.Error(r.Error)
				}
				if !r.Cleanup {
					t.Error("actual credit/pages/pending cleanup incomplete")
				}
			})
		}
	}
}

// The baseline can already slow during history preparation. Use the same
// bounded 20-second exchange deadline on A and B; this is not the measured timer.
func sharedHistoryExchange(st *Stream, size, seed int) error {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*31 + (i >> 8) + seed) % 256)
	}
	st.SetDeadline(time.Now().Add(20 * time.Second))
	done := make(chan error, 1)
	go func() { done <- writeAll(st, data) }()
	got := make([]byte, size)
	_, err := io.ReadFull(st, got)
	if err != nil {
		st.Close()
	}
	writeErr := <-done
	if err != nil {
		return err
	}
	if writeErr != nil {
		return writeErr
	}
	if !bytes.Equal(data, got) {
		return fmt.Errorf("history payload mismatch")
	}
	st.SetDeadline(time.Time{})
	return nil
}
