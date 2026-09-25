package multipath

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Deterministic model of two high-BDP receive grants. These are real, already
// authenticated carrier/session/backend connections; only the measured target
// is seeded so the pressure is repeatable rather than scheduler-speed dependent.
// No physical receive pages are invented. This tests irrevocable credit, not RSS.
func TestWarmCreditMustLeaveNewStreamAdmission(t *testing.T) {
	s, srv, _, _ := testTCP(t, 6, 32<<20, 25*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := s.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := s.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	before := srv.AdmissionSnapshot()
	s.mu.Lock()
	a.windowTarget = MaxStreamWindow
	a.advertiseCreditLocked(time.Now())
	b.windowTarget = MaxStreamWindow
	b.advertiseCreditLocked(time.Now())
	credit := s.receiveCredit
	s.mu.Unlock()
	started := time.Now()
	short, err := s.Open(ctx)
	if short != nil {
		short.Close()
	}
	time.Sleep(150 * time.Millisecond)
	after := srv.AdmissionSnapshot()
	snapshot := s.Snapshot()
	closed := false
	select {
	case <-s.Done():
		closed = true
	default:
	}
	raw, _ := json.Marshal(map[string]any{"credit_before_open": credit, "open_error": errString(err), "elapsed_ms": time.Since(started).Milliseconds(), "paths_after": snapshot.Paths, "session_done": closed, "session_error": errString(s.Err()), "handshake_before": before, "handshake_after": after, "scope": "Deterministic credit pressure on six real loopback carriers; not attribution of a historical production disconnect"})
	t.Logf("CREDIT_CAUSAL_BASELINE %s", raw)
	if err != nil {
		t.Fatalf("new business stream was rejected by warm receive credit: %v", err)
	}
	if closed || snapshot.Paths != 6 {
		t.Fatal("stream admission pressure terminated shared transport")
	}
	if after.Accepted != before.Accepted || after.Rejected != before.Rejected {
		t.Fatal("stream admission caused carrier re-authentication")
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestIdleNewStreamsMustNotInheritLargeGrant(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.windowSeed = MaxStreamWindow
	s.windowSeedAt = time.Now()
	a := s.newStreamLocked(1)
	a.open = true
	a.windowTarget = MaxStreamWindow
	a.advertiseCreditLocked(time.Now())
	b := s.newStreamLocked(3)
	b.open = true
	b.advertiseCreditLocked(time.Now())
	defer a.Close()
	defer b.Close()
	if b.rxLimit > StreamWindow {
		t.Fatalf("idle new stream inherited %d bytes without consumption", b.rxLimit)
	}
	if s.receiveCredit > SessionCreditLimit {
		t.Fatal("credit ledger exceeded hard bound")
	}
}

// A limit on one logical flow must remain local to that flow. This is evidence
// about this controlled cause, not a claim that the user's EOFs were unrelated.
func TestHardStreamLimitDoesNotCloseCarriers(t *testing.T) {
	s, srv, _, _ := testTCP(t, 6, 32<<20, 5*time.Millisecond)
	before := srv.AdmissionSnapshot()
	s.mu.Lock()
	for id := uint64(1); len(s.streams) < MaxStreams; id += 2 {
		s.newStreamLocked(id)
	}
	s.mu.Unlock()
	_, err := s.Open(context.Background())
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("expected bounded stream refusal: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if s.Snapshot().Paths != 6 {
		t.Fatal("hard stream limit killed a carrier")
	}
	select {
	case <-s.Done():
		t.Fatal("hard stream limit killed the session")
	default:
	}
	s.mu.Lock()
	for _, st := range s.streams {
		s.resetLocked(st, 1, false)
	}
	s.mu.Unlock()
	if _, err = transfer(s, 32769); err != nil {
		t.Fatal(err)
	}
	after := srv.AdmissionSnapshot()
	if after.Accepted != before.Accepted || after.Rejected != before.Rejected {
		t.Fatal("hard stream limit triggered handshakes")
	}
}
