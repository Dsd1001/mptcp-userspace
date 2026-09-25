package multipath

import (
	"context"
	"testing"
	"time"
)

// The thresholded WINDOW refill can keep outstanding credit nonzero even
// while an application consumes an entire bootstrap's worth of real data.
// Growth must not depend on catching exactly zero remaining credit.
func TestSmallConsumerGrowsWithoutDrainingToExactZero(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	now := time.Now()
	st := s.newStreamLocked(1)
	st.open = true
	defer st.Close()
	st.advertiseCreditLocked(now)
	for i := 0; i < 2; i++ {
		consumeWindowFixture(t, st, StreamWindow/2, now.Add(time.Duration(i+1)*time.Millisecond))
	}
	if st.windowTarget != 2*StreamWindow {
		t.Fatalf("bootstrap demand cannot grow through early refills: target=%d demand=%d outstanding=%d", st.windowTarget, st.demandBytes, st.rxLimit-st.rxRead)
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
		t.Fatal("idle inherited warm bulk")
	}
	consumeWindowFixture(t, st, 1, now.Add(time.Millisecond))
	if st.windowTarget != StreamWindow {
		t.Fatal("keepalive inherited warm bulk")
	}
	consumeWindowFixture(t, st, StreamWindow-1, now.Add(2*time.Millisecond))
	if st.windowTarget != MaxStreamWindow || st.rxLimit-st.rxRead != MaxStreamWindow {
		t.Fatal("real bootstrap consumption did not use bounded recent demand")
	}
	if s.receiveCredit > SessionCreditLimit || s.receiveGrowth > GrowthCreditLimit {
		t.Fatal("warm growth unbounded")
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
	if st.windowTarget > SmallStreamWindow || st.rxLimit-st.rxRead > SmallStreamWindow {
		t.Fatal("concurrent medium inherited speculative bulk grant")
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
