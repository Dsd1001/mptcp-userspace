package multipath

import (
	"context"
	"testing"
	"time"
)

func seedFixture(t *testing.T) (*Session, *Stream, time.Time) {
	t.Helper()
	s := schedulerFixture()
	s.ctx = context.Background()
	now := time.Now()
	s.windowSeed = 8 << 20
	s.windowSeedAt = now
	st := s.newStreamLocked(1)
	st.open = true
	st.advertiseCreditLocked(now)
	return s, st, now
}
func TestWarmSeedIdleBackgroundNeedsRealConsumption(t *testing.T) {
	s, st, now := seedFixture(t)
	for i := 0; i < 128; i++ {
		other := s.newStreamLocked(uint64(3 + 2*i))
		other.open = true
		other.advertiseCreditLocked(now)
		consumeWindowFixture(t, other, 1, now.Add(time.Millisecond))
	}
	if st.windowTarget != StreamWindow || st.rxLimit != StreamWindow {
		t.Fatal("implicit idle grant")
	}
	consumeWindowFixture(t, st, 1, now.Add(2*time.Millisecond))
	if st.windowTarget != StreamWindow {
		t.Fatal("one-byte warm seed")
	}
	consumeWindowFixture(t, st, StreamWindow-1, now.Add(3*time.Millisecond))
	if st.windowTarget != 8<<20 {
		t.Fatalf("idle background disabled measured seed: %d", st.windowTarget)
	}
	if s.receiveAllocated != 0 || s.receiveCredit > SessionCreditLimit || s.receiveGrowth > GrowthCreditLimit {
		t.Fatal("allocation or credit escaped bounds")
	}
	for id, other := range s.streams {
		if id != 1 && other.windowTarget != StreamWindow {
			t.Fatal("background was enlarged")
		}
	}
}
func TestWarmSeedKeepsActiveAndUnreadBulkProtected(t *testing.T) {
	for _, mode := range []string{"active", "unread", "expired-seed"} {
		t.Run(mode, func(t *testing.T) {
			s, st, now := seedFixture(t)
			other := s.newStreamLocked(3)
			other.open = true
			switch mode {
			case "active":
				other.windowTarget = MaxStreamWindow
				other.lastRead = now
			case "unread":
				other.rxHigh = StreamWindow
				other.lastRead = now.Add(-10 * time.Second)
			case "expired-seed":
				s.windowSeedAt = now.Add(-10 * time.Second)
			}
			consumeWindowFixture(t, st, StreamWindow, now.Add(time.Millisecond))
			if st.windowTarget > SmallStreamWindow {
				t.Fatalf("unsafe warm seed for %s: %d", mode, st.windowTarget)
			}
		})
	}
}
func TestWarmSeedConcurrentDemandStillProtected(t *testing.T) {
	s, st, now := seedFixture(t)
	other := s.newStreamLocked(3)
	other.open = true
	other.advertiseCreditLocked(now)
	consumeWindowFixture(t, st, StreamWindow, now.Add(time.Millisecond))
	consumeWindowFixture(t, other, StreamWindow, now.Add(2*time.Millisecond))
	if st.windowTarget != 8<<20 || other.windowTarget > SmallStreamWindow {
		t.Fatal("competing bulk inherited speculative seed")
	}
}
