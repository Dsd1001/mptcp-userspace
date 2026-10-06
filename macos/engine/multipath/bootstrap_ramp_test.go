package multipath

import (
	"context"
	"testing"
	"time"
)

func TestOptimisticWindowRefreshesAfterConsumptionBatch(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	st := s.newStreamLocked(1)
	st.open = true
	defer st.Close()
	now := time.Now()
	st.advertiseCreditLocked(now)

	if st.rxLimit-st.rxRead != MaxStreamWindow {
		t.Fatalf("initial allowance=%d want=%d", st.rxLimit-st.rxRead, MaxStreamWindow)
	}

	consumeWindowFixture(t, st, streamCreditRefreshBatch-1, now.Add(time.Millisecond))
	if st.rxLimit-st.rxRead != uint64(MaxStreamWindow-(streamCreditRefreshBatch-1)) {
		t.Fatalf("sub-batch consumption unexpectedly refreshed WINDOW: remaining=%d", st.rxLimit-st.rxRead)
	}

	consumeWindowFixture(t, st, 1, now.Add(2*time.Millisecond))
	if st.rxLimit-st.rxRead != MaxStreamWindow {
		t.Fatalf("batch consumption did not roll full allowance: remaining=%d", st.rxLimit-st.rxRead)
	}
	if s.receiveCredit != 0 || s.receiveGrowth != 0 || s.receiveAllocated != 0 {
		t.Fatalf("consumed optimistic allowance left actual resource usage: credit=%d growth=%d allocated=%d", s.receiveCredit, s.receiveGrowth, s.receiveAllocated)
	}
}

func TestIdleAndKeepaliveDoNotShrinkOptimisticAllowance(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	st := s.newStreamLocked(1)
	st.open = true
	defer st.Close()
	now := time.Now()
	st.advertiseCreditLocked(now)

	for i := 0; i < 100; i++ {
		oldRead := st.rxRead
		if err := st.receiveCommitLocked(st.rxRead + 1); err != nil {
			t.Fatal(err)
		}
		st.rxRead++
		st.releaseReadCreditLocked(oldRead)
		st.consumeCreditLocked(1, now.Add(time.Duration(i+1)*100*time.Millisecond))
	}
	st.advertiseCreditLocked(now.Add(creditIdle + time.Second))
	if st.windowTarget != MaxStreamWindow || st.rxLimit-st.rxRead != MaxStreamWindow {
		t.Fatalf("idle/keepalive shrank optimistic allowance: target=%d remaining=%d", st.windowTarget, st.rxLimit-st.rxRead)
	}
	if s.receiveCredit != 0 || s.receiveAllocated != 0 {
		t.Fatalf("keepalive entitlement consumed actual resources: credit=%d allocated=%d", s.receiveCredit, s.receiveAllocated)
	}
}

func TestConcurrentOptimisticEntitlementsDoNotReserveSessionCredit(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	now := time.Now()
	for i := 0; i < 256; i++ {
		st := s.newStreamLocked(uint64(1 + 2*i))
		st.open = true
		st.advertiseCreditLocked(now)
		if st.rxLimit != MaxStreamWindow {
			t.Fatalf("stream %d allowance=%d", i, st.rxLimit)
		}
	}
	if s.receiveCredit != 0 || s.receiveGrowth != 0 || s.receiveAllocated != 0 {
		t.Fatalf("optimistic entitlements reserved actual Session resources: credit=%d growth=%d allocated=%d", s.receiveCredit, s.receiveGrowth, s.receiveAllocated)
	}
	if got := s.sharedCreditSnapshotLocked().WindowEntitlements; got != uint64(256*MaxStreamWindow) {
		t.Fatalf("entitlement telemetry=%d want=%d", got, uint64(256*MaxStreamWindow))
	}
}

func consumeWindowFixture(t *testing.T, st *Stream, n int, now time.Time) {
	t.Helper()
	old := st.rxRead
	if e := st.receiveCommitLocked(old + uint64(n)); e != nil {
		t.Fatal(e)
	}
	st.rxRead += uint64(n)
	st.releaseReadCreditLocked(old)
	st.consumeCreditLocked(n, now)
}
