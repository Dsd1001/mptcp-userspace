package multipath

import (
	"context"
	"testing"
	"time"
)

// The thresholded WINDOW refill can keep outstanding credit nonzero even
// while an application consumes an entire bootstrap's worth of real data.
// Growth must not depend on catching exactly zero remaining credit.
func TestHalfWindowConsumptionTriggersEarlyAutotune(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.initScheduler(SchedulerWeighted)
	now := time.Now()
	st := s.newStreamLocked(1)
	st.open = true
	defer st.Close()
	st.advertiseCreditLocked(now)

	consumeWindowFixture(t, st, StreamWindow/2, now.Add(time.Millisecond))
	if st.windowTarget <= StreamWindow {
		t.Fatalf("half-window consumption did not trigger early autotune: target=%d", st.windowTarget)
	}
	if st.rxLimit-st.rxRead != uint64(st.windowTarget) {
		t.Fatalf("autotuned credit was not immediately advertised: target=%d remaining=%d", st.windowTarget, st.rxLimit-st.rxRead)
	}
	first := st.windowTarget
	consumeWindowFixture(t, st, StreamWindow/2, now.Add(2*time.Millisecond))
	if st.windowTarget <= first {
		t.Fatalf("continued demand did not continuously enlarge target: first=%d second=%d", first, st.windowTarget)
	}
	if s.receiveCredit > SessionCreditLimit || s.receiveGrowth > GrowthCreditLimit {
		t.Fatal("growth escaped bound")
	}
}

func TestWarmSeedRequiresRealBootstrapConsumption(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	now := time.Now()
	s.windowSeed = MaxStreamWindow
	s.windowSeedAt = now
	st := s.newStreamLocked(1)
	st.open = true
	defer st.Close()
	st.advertiseCreditLocked(now)
	if st.rxLimit != StreamWindow || st.windowTarget != StreamWindow {
		t.Fatalf("single non-Weighted Stream lost cold bootstrap: target=%d limit=%d", st.windowTarget, st.rxLimit)
	}
	consumeWindowFixture(t, st, 1, now.Add(time.Millisecond))
	if st.windowTarget != StreamWindow {
		t.Fatal("keepalive inherited warm bulk")
	}
	consumeWindowFixture(t, st, StreamWindow-1, now.Add(2*time.Millisecond))
	if st.windowTarget != MaxStreamWindow || int(st.rxLimit-st.rxRead) != st.windowTarget {
		t.Fatalf("real bootstrap consumption did not reuse uncontended warm seed: target=%d remaining=%d", st.windowTarget, st.rxLimit-st.rxRead)
	}
	if s.receiveCredit > SessionCreditLimit || s.receiveGrowth > GrowthCreditLimit {
		t.Fatal("warm growth unbounded")
	}
}

func TestWarmSeedRetriesAfterTemporaryContentionEnds(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	now := time.Now()
	s.windowSeed = 8 << 20
	s.windowSeedAt = now

	blocker := s.newStreamLocked(1)
	blocker.open = true
	blocker.windowTarget = 8 << 20
	blocker.demandBytes = 8 << 20
	blocker.lastRead = now

	st := s.newStreamLocked(3)
	st.open = true
	st.advertiseCreditLocked(now)
	defer st.Close()

	consumeWindowFixture(t, st, StreamWindow, now.Add(time.Millisecond))
	if st.warmSeedUsed {
		t.Fatal("temporary contention permanently consumed warm-seed eligibility")
	}
	if st.windowTarget >= 8<<20 {
		t.Fatal("contended Stream inherited warm seed")
	}

	blocker.closed = true
	consumeWindowFixture(t, st, StreamWindow/2, now.Add(2*time.Millisecond))
	if !st.warmSeedUsed || st.windowTarget != 8<<20 {
		t.Fatalf("uncontended retry did not inherit warm seed: used=%v target=%d", st.warmSeedUsed, st.windowTarget)
	}
}

func TestConcurrentMediumDoesNotInheritBulkSeed(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	now := time.Now()
	bulk := s.newStreamLocked(1)
	bulk.open = true
	bulk.windowTarget = MaxStreamWindow
	bulk.advertiseCreditLocked(now)
	defer bulk.Close()
	s.windowSeed = MaxStreamWindow
	s.windowSeedAt = now
	st := s.newStreamLocked(3)
	st.open = true
	defer st.Close()
	st.advertiseCreditLocked(now)
	for i := 0; i < 2; i++ {
		consumeWindowFixture(t, st, StreamWindow, now.Add(time.Duration(i+1)*time.Millisecond))
	}
	ceiling := s.streamWindowCeilingLocked(st, now.Add(2*time.Millisecond))
	if st.windowTarget > ceiling || st.windowTarget >= MaxStreamWindow {
		t.Fatalf("concurrent medium inherited speculative bulk grant: target=%d ceiling=%d", st.windowTarget, ceiling)
	}
}

func TestConcurrentBulkGetsBoundedRecentSeedAfterSmallWindowDemand(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.initScheduler(SchedulerWeighted)
	now := time.Now()
	s.windowSeed = 8 << 20
	s.windowSeedAt = now

	other := s.newStreamLocked(1)
	other.open = true
	other.windowTarget = MaxStreamWindow
	other.lastRead = now
	defer other.Close()

	st := s.newStreamLocked(3)
	st.open = true
	st.advertiseCreditLocked(now)
	defer st.Close()

	// The first bootstrap cannot inherit the full seed while another bulk
	// Stream is active. Continued real consumption may grow continuously, but
	// only within this Stream's demand/fair-share ceiling.
	consumeWindowFixture(t, st, StreamWindow, now.Add(time.Millisecond))
	consumeWindowFixture(t, st, 2*StreamWindow, now.Add(2*time.Millisecond))
	consumeWindowFixture(t, st, 4*StreamWindow, now.Add(3*time.Millisecond))
	consumeWindowFixture(t, st, StreamWindow, now.Add(4*time.Millisecond))

	if st.demandBytes < SmallStreamWindow {
		t.Fatalf("fixture did not establish bulk demand: %d", st.demandBytes)
	}
	ceiling := s.streamWindowCeilingLocked(st, now.Add(4*time.Millisecond))
	if st.windowTarget <= SmallStreamWindow || st.windowTarget > ceiling {
		t.Fatalf("contended bulk did not continuously grow within its ceiling: target=%d ceiling=%d", st.windowTarget, ceiling)
	}
	if st.windowTarget >= 8<<20 {
		t.Fatal("contended bulk inherited the full measured seed")
	}
	if s.receiveCredit > SessionCreditLimit || s.receiveGrowth > GrowthCreditLimit {
		t.Fatal("bounded warm window escaped Session credit limits")
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
