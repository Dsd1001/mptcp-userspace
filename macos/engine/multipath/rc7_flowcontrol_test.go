package multipath

import (
	"context"
	"testing"
	"time"
)

func TestRC7OptimisticStreamEntitlementDoesNotReserveMemory(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	st := s.newStreamLocked(1)
	st.open = true
	st.advertiseCreditLocked(time.Now())

	if st.rxLimit != MaxStreamWindow || st.windowTarget != MaxStreamWindow {
		t.Fatalf("optimistic Stream entitlement mismatch: limit=%d target=%d", st.rxLimit, st.windowTarget)
	}
	if s.receiveCredit != 0 || s.receiveGrowth != 0 || s.receiveAllocated != 0 {
		t.Fatalf("WINDOW entitlement reserved actual resources: credit=%d growth=%d allocated=%d", s.receiveCredit, s.receiveGrowth, s.receiveAllocated)
	}
}

func TestRC7StreamConsumptionRollsFullAllowance(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	st := s.newStreamLocked(1)
	st.open = true
	now := time.Now()
	st.advertiseCreditLocked(now)

	consumeWindowFixture(t, st, streamCreditRefreshBatch, now.Add(time.Millisecond))
	if got := st.rxLimit - st.rxRead; got != MaxStreamWindow {
		t.Fatalf("rolling Stream allowance=%d want=%d", got, MaxStreamWindow)
	}
	if st.windowTarget != MaxStreamWindow {
		t.Fatalf("Stream target shrank under normal consumption: %d", st.windowTarget)
	}
}

func TestRC7SessionRefillGovernor(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()

	cases := []struct {
		pressure int
		want     int
	}{
		{0, SessionCreditLimit},
		{70, SessionCreditLimit},
		{80, SessionCreditLimit * 15 / 25},
		{90, SessionCreditLimit * 5 / 25},
		{95, 0},
		{100, 0},
	}
	for _, tc := range cases {
		s.receiveCredit = (tc.pressure*SessionCreditLimit + 99) / 100
		s.receiveAllocated = 0
		if got := s.sessionRefillTargetLocked(); got != tc.want {
			t.Fatalf("pressure=%d target=%d want=%d", tc.pressure, got, tc.want)
		}
	}
}

func TestRC7HighPressureDoesNotExtendSessionLimit(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.credit.rxLimit = SessionCreditLimit
	s.credit.rxCommitted = 95 * SessionCreditLimit / 100
	s.credit.rxConsumed = 0
	s.receiveCredit = 95 * SessionCreditLimit / 100
	before := s.credit.rxLimit
	s.advertiseSessionCreditLocked(time.Now(), true)
	if s.credit.rxLimit != before {
		t.Fatalf("95%% pressure extended Session WINDOW: before=%d after=%d", before, s.credit.rxLimit)
	}
}

func TestRC7ManyEntitlementsRemainAccountingOnly(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	for i := 0; i < MaxStreams; i++ {
		st := s.newStreamLocked(uint64(1 + 2*i))
		st.open = true
		st.advertiseCreditLocked(time.Now())
		if st.rxLimit != MaxStreamWindow {
			t.Fatalf("stream %d entitlement=%d", i, st.rxLimit)
		}
	}
	if s.receiveCredit != 0 || s.receiveAllocated != 0 {
		t.Fatalf("2048 optimistic entitlements consumed actual resources: credit=%d allocated=%d", s.receiveCredit, s.receiveAllocated)
	}
}
