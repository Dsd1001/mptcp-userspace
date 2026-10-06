package multipath

import (
	"context"
	"testing"
	"time"
)

func TestIdleBackgroundEntitlementsRemainAccountingOnly(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	now := time.Now()

	for i := 0; i < 129; i++ {
		st := s.newStreamLocked(uint64(1 + 2*i))
		st.open = true
		st.advertiseCreditLocked(now)
		if st.windowTarget != MaxStreamWindow || st.rxLimit != MaxStreamWindow {
			t.Fatalf("stream %d did not receive full entitlement: target=%d limit=%d", i, st.windowTarget, st.rxLimit)
		}
	}

	if s.receiveCredit != 0 || s.receiveGrowth != 0 || s.receiveAllocated != 0 {
		t.Fatalf("idle background entitlements consumed actual resources: credit=%d growth=%d allocated=%d", s.receiveCredit, s.receiveGrowth, s.receiveAllocated)
	}
}

func TestIdleDoesNotShrinkOptimisticEntitlement(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	now := time.Now()
	st := s.newStreamLocked(1)
	st.open = true
	st.advertiseCreditLocked(now)

	before := st.rxLimit
	st.advertiseCreditLocked(now.Add(10 * time.Second))
	if st.rxLimit < before || st.windowTarget != MaxStreamWindow {
		t.Fatalf("idle shrank entitlement: before=%d after=%d target=%d", before, st.rxLimit, st.windowTarget)
	}

	consumeWindowFixture(t, st, streamCreditRefreshBatch, now.Add(11*time.Second))
	if st.rxLimit-st.rxRead != MaxStreamWindow {
		t.Fatalf("post-idle consumption did not restore full rolling allowance: remaining=%d", st.rxLimit-st.rxRead)
	}
}

func TestConcurrentDemandStillBoundedBySessionLedger(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	now := time.Now()
	streams := make([]*Stream, 8)
	for i := range streams {
		st := s.newStreamLocked(uint64(1 + 2*i))
		st.open = true
		st.advertiseCreditLocked(now)
		streams[i] = st
	}

	// Commit real DATA, not merely WINDOW entitlement, until the Session hard
	// ledger is full. Each Stream has 16 MiB entitlement but the aggregate
	// unconsumed bytes still cannot exceed 128 MiB.
	for _, st := range streams {
		if err := st.receiveCommitLocked(st.rxHigh + MaxStreamWindow); err != nil {
			t.Fatal(err)
		}
	}
	if s.receiveCredit != SessionCreditLimit {
		t.Fatalf("actual Session credit=%d want=%d", s.receiveCredit, SessionCreditLimit)
	}
	extra := s.newStreamLocked(99)
	extra.open = true
	extra.advertiseCreditLocked(now)
	if err := extra.receiveCommitLocked(1); err == nil {
		t.Fatal("Session hard receive-credit limit accepted extra DATA")
	}

	for _, st := range streams {
		st.discardReceiveLocked()
	}
	if s.receiveCredit != 0 || s.receiveGrowth != 0 {
		t.Fatalf("actual receive credit did not drain: credit=%d growth=%d", s.receiveCredit, s.receiveGrowth)
	}
}
