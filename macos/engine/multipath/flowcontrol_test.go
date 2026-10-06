package multipath

import (
	"context"
	"errors"
	"testing"
	"time"
)

func creditFixture() (*Session, *Stream) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.paths[1] = schedulerPath(1)
	s.paths[1].rtt = 100 * time.Millisecond
	st := s.newStreamLocked(1)
	st.open = true
	st.advertiseCreditLocked(time.Now())
	return s, st
}

func TestCreditRTTUsesBoundedWeightedLoadRTT(t *testing.T) {
	s := schedulerFixture()
	s.scheduler.configured = SchedulerWeighted
	fast := schedulerPath(1)
	fast.minRTT = 20 * time.Millisecond
	fast.rtt = 400 * time.Millisecond // capped at 4x base = 80 ms
	fast.configuredRateBPS = 30e6
	slow := schedulerPath(2)
	slow.minRTT = 100 * time.Millisecond
	slow.rtt = 500 * time.Millisecond // capped at 150 ms absolute ceiling
	slow.configuredRateBPS = 10e6
	s.paths[1], s.paths[2] = fast, slow
	base, load := s.creditRTTsLocked()
	if base != 40*time.Millisecond {
		t.Fatalf("weighted base RTT mismatch: got=%v want=40ms", base)
	}
	if load != 97500*time.Microsecond {
		t.Fatalf("weighted bounded load RTT mismatch: got=%v want=97.5ms", load)
	}
	if got := s.creditRTTLocked(); got != load {
		t.Fatalf("credit RTT did not use bounded load estimate: got=%v want=%v", got, load)
	}
}

func TestOptimisticCreditRemainsMonotonicAcrossIdle(t *testing.T) {
	s, st := creditFixture()
	now := time.Now()
	if st.windowTarget != MaxStreamWindow || st.rxLimit-st.rxRead != MaxStreamWindow {
		t.Fatalf("initial optimistic allowance missing: target=%d remaining=%d", st.windowTarget, st.rxLimit-st.rxRead)
	}

	consumeWindowFixture(t, st, streamCreditRefreshBatch, now.Add(time.Millisecond))
	limit := st.rxLimit
	st.advertiseCreditLocked(now.Add(creditIdle + time.Second))
	if st.windowTarget != MaxStreamWindow || st.rxLimit < limit || st.rxLimit-st.rxRead != MaxStreamWindow {
		t.Fatalf("idle changed optimistic allowance: target=%d limit=%d remaining=%d", st.windowTarget, st.rxLimit, st.rxLimit-st.rxRead)
	}
	if s.receiveCredit != 0 || s.receiveAllocated != 0 {
		t.Fatalf("consumed allowance left actual resources: credit=%d allocated=%d", s.receiveCredit, s.receiveAllocated)
	}
	st.Close()
	if s.receiveCredit != 0 {
		t.Fatal("credit leak on close")
	}
}

func TestPerStreamWindowDoesNotRateLimitBelowSessionPressure(t *testing.T) {
	s, st := creditFixture()
	s.initScheduler(SchedulerWeighted)
	now := time.Now()
	defer st.Close()

	for i := 0; i < 8; i++ {
		consumeWindowFixture(t, st, streamCreditRefreshBatch, now.Add(time.Duration(i+1)*time.Millisecond))
		if st.windowTarget != MaxStreamWindow || st.rxLimit-st.rxRead != MaxStreamWindow {
			t.Fatalf("per-Stream allowance became a rate gate: target=%d remaining=%d", st.windowTarget, st.rxLimit-st.rxRead)
		}
	}
	if s.receivePressurePercentLocked() != 0 || s.sessionRefillTargetLocked() != SessionCreditLimit {
		t.Fatalf("low-pressure Session unexpectedly throttled: pressure=%d refill=%d", s.receivePressurePercentLocked(), s.sessionRefillTargetLocked())
	}
	if s.receiveCredit > SessionCreditLimit || s.receiveGrowth > GrowthCreditLimit {
		t.Fatal("optimistic Stream allowance escaped Session hard bounds")
	}
}

func TestSessionCreditLedgerAndAdmission(t *testing.T) {
	s, a := creditFixture()
	a.windowTarget = MaxStreamWindow
	a.advertiseCreditLocked(time.Now())
	b := s.newStreamLocked(3)
	b.open = true
	b.windowTarget = MaxStreamWindow
	b.advertiseCreditLocked(time.Now())
	total := int(a.rxLimit - a.rxRead + b.rxLimit - b.rxRead)
	if total != 2*MaxStreamWindow || s.receiveCredit != 0 || s.admissionReserveLocked() != BootstrapCreditLimit {
		t.Fatalf("ledger/reserve mismatch %d %d", total, s.receiveCredit)
	}
	if reason := s.openBlockReasonLocked(); reason != "" {
		t.Fatalf("large grants consumed new-stream reserve: %s", reason)
	}
	a.Close()
	b.Close()
	if s.receiveCredit != 0 || s.receiveAllocated != 0 {
		t.Fatal("reset did not return credit")
	}
}

func TestAbsoluteCreditReorderingAndReceiptSeparation(t *testing.T) {
	s, st := creditFixture()
	c := s.paths[1]
	st.commitSendCreditLocked(MaxPayload)
	p := s.queueLocked(frame{kind: kindData, stream: 1, data: make([]byte, MaxPayload)})
	p.path = c
	p.attempts = 1
	p.sentAt = time.Now()
	c.outstanding = p.cost
	s.ackLocked(c, frame{kind: kindACK, stream: 1, id: p.f.id, offset: MaxPayload})
	if st.peerLimit != 0 {
		t.Fatal("receipt ACK invented send credit")
	}
	if err := st.receiveCreditLocked(frame{id: 2 << 20, offset: MaxPayload}); err != nil {
		t.Fatal(err)
	}
	if err := st.receiveCreditLocked(frame{id: StreamWindow, offset: 0}); err != nil {
		t.Fatal(err)
	}
	if st.peerLimit != 2<<20 || st.peerConsumed != MaxPayload {
		t.Fatal("stale WINDOW revoked credit or consumption")
	}
	for _, f := range []frame{{id: 1, offset: 2}, {id: MaxStreamWindow + 1}, {id: MaxPayload + 1, offset: MaxPayload + 1}} {
		if !errors.Is(st.receiveCreditLocked(f), ErrProtocol) {
			t.Fatal("invalid absolute credit accepted")
		}
	}
}

func TestCarrierBudgetBDPGrowthShrinkAndCap(t *testing.T) {
	c := schedulerPath(1)
	c.goodput = 40e6
	c.minRTT = 100 * time.Millisecond
	for i := 0; i < 12; i++ {
		old := c.flightBudget()
		c.updateBudget()
		if c.flightBudget() > old*2 || c.flightBudget() > maxPathBudget {
			t.Fatal("unbounded carrier growth")
		}
	}
	if c.flightBudget() <= 256<<10 {
		t.Fatal("old fixed carrier cap remains")
	}
	c.goodput = 65536
	for i := 0; i < 30; i++ {
		c.updateBudget()
	}
	if c.flightBudget() > initialPathBudget*2 {
		t.Fatalf("budget did not shrink: %d", c.flightBudget())
	}
	c.goodput = 1e12
	for i := 0; i < 20; i++ {
		c.updateBudget()
	}
	if c.flightBudget() != maxPathBudget {
		t.Fatal("carrier budget cap not enforced")
	}
}

func TestTimeoutNeedsTwoEpochsBeforePathPenalty(t *testing.T) {
	s, st := creditFixture()
	c := s.paths[1]
	c.budget = maxPathBudget
	c.goodput = 20e6
	now := time.Now()

	queueTimedOut := func(at time.Time, payload string) *outbound {
		p := s.queueLocked(frame{kind: kindData, stream: st.id, data: []byte(payload)})
		s.unreadyLocked(p)
		p.path = c
		p.sentAt = at.Add(-time.Second)
		p.created = at.Add(-2 * time.Second)
		p.attempts = 1
		c.outstanding = p.cost
		return p
	}

	first := queueTimedOut(now, "first")
	s.sweepLocked(now)
	if c.flightBudget() != maxPathBudget || !c.penaltyUntil.IsZero() || c.timeoutStreak != 1 {
		t.Fatalf("first timeout penalized healthy path: budget=%d penalty=%v streak=%d", c.flightBudget(), c.penaltyUntil, c.timeoutStreak)
	}
	if first.path != nil || s.pending[first.f.id] != first || first.ready == nil {
		t.Fatal("first timeout lost retransmission payload")
	}

	later := now.Add(600 * time.Millisecond)
	second := queueTimedOut(later, "second")
	s.sweepLocked(later)
	if c.flightBudget() != maxPathBudget/2 || !c.penaltyUntil.After(later) || c.timeoutStreak != 1 {
		t.Fatalf("repeated timeout did not apply bounded penalty: budget=%d penalty=%v streak=%d", c.flightBudget(), c.penaltyUntil, c.timeoutStreak)
	}
	if second.path != nil || s.pending[second.f.id] != second || second.ready == nil {
		t.Fatal("repeated timeout lost retransmission payload")
	}
}

func TestDataProgressClearsTimeoutSuspicion(t *testing.T) {
	s, st := creditFixture()
	c := s.paths[1]
	c.timeoutStreak = 1
	c.lastTimeoutAt = time.Now()

	p := s.queueLocked(frame{kind: kindData, stream: st.id, data: make([]byte, MaxPayload)})
	s.unreadyLocked(p)
	p.path = c
	p.sentAt = time.Now().Add(-10 * time.Millisecond)
	p.attempts = 1
	c.outstanding = p.cost
	if err := s.ackLocked(c, frame{kind: kindACK, stream: st.id, id: p.f.id, offset: receiverStampUS(100 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	if c.timeoutStreak != 0 || !c.lastTimeoutAt.IsZero() {
		t.Fatalf("DATA progress did not clear timeout suspicion: streak=%d at=%v", c.timeoutStreak, c.lastTimeoutAt)
	}
}

func TestReadyQueuesFairAndBoundedOnReset(t *testing.T) {
	s, st := creditFixture()
	s.paths[1].rtt = 20 * time.Millisecond
	other := s.newStreamLocked(3)
	other.open = true
	for i := 0; i < 30; i++ {
		s.queueLocked(frame{kind: kindData, stream: 1, data: []byte{1}})
	}
	s.queueLocked(frame{kind: kindData, stream: 3, data: []byte{3}})
	s.dispatchLocked(time.Now())
	found := false
	for len(s.paths[1].queue) > 0 {
		task := <-s.paths[1].queue
		if task.p.f.stream == 3 {
			found = true
		}
	}
	if !found {
		t.Fatal("large stream starved ready small stream")
	}
	s.resetLocked(st, 1, false)
	s.resetLocked(other, 1, false)
	if len(s.pending) != 0 || len(s.ready) != 0 || s.pendingBytes != 0 || s.receiveCredit != 0 {
		t.Fatal("ready/credit ledger leaked after reset")
	}
}
