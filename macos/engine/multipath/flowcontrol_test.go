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

func TestAdaptiveCreditGrowthShrinkAndMonotonicGrant(t *testing.T) {
	s, st := creditFixture()
	now := time.Now()
	st.readSampleAt = now.Add(-100 * time.Millisecond)
	for i := 0; i < 12; i++ {
		before := st.windowTarget
		n := int(st.rxLimit - st.rxRead)
		oldRead := st.rxRead
		if err := st.receiveCommitLocked(st.rxRead + uint64(n)); err != nil {
			t.Fatal(err)
		}
		st.rxRead += uint64(n)
		st.releaseReadCreditLocked(oldRead)
		st.consumeCreditLocked(n, now)
		if st.windowTarget > before*2 || st.windowTarget > MaxStreamWindow || s.receiveCredit > SessionCreditLimit || s.receiveCredit < 0 {
			t.Fatal("unbounded credit growth")
		}
		now = now.Add(100 * time.Millisecond)
	}
	if st.windowTarget <= 1<<20 {
		t.Fatalf("window did not adapt beyond old fixed window: %d", st.windowTarget)
	}
	limit := st.rxLimit
	st.advertiseCreditLocked(now.Add(creditIdle + time.Second))
	if st.windowTarget != StreamWindow || st.rxLimit != limit {
		t.Fatal("idle shrink retracted already advertised bytes")
	}
	n := int(st.rxLimit - st.rxRead)
	oldRead := st.rxRead
	if err := st.receiveCommitLocked(st.rxRead + uint64(n)); err != nil {
		t.Fatal(err)
	}
	st.rxRead += uint64(n)
	st.releaseReadCreditLocked(oldRead)
	st.advertiseCreditLocked(now.Add(creditIdle + time.Second))
	if st.rxLimit-st.rxRead != StreamWindow || s.receiveCredit != 0 {
		t.Fatal("old grant did not drain into small target")
	}
	st.Close()
	if s.receiveCredit != 0 {
		t.Fatal("credit leak on close")
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

func TestTimeoutShrinksBudgetWithoutDroppingPending(t *testing.T) {
	s, st := creditFixture()
	c := s.paths[1]
	c.budget = maxPathBudget
	c.goodput = 20e6
	now := time.Now()
	p := s.queueLocked(frame{kind: kindData, stream: st.id, data: []byte("preserved")})
	s.unreadyLocked(p)
	p.path = c
	p.sentAt = now.Add(-time.Second)
	p.created = now.Add(-2 * time.Second)
	p.attempts = 1
	c.outstanding = p.cost
	s.sweepLocked(now)
	if c.flightBudget() != maxPathBudget/2 || p.path != nil || s.pending[p.f.id] != p || p.ready == nil {
		t.Fatal("timeout lost payload or failed to shrink")
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
