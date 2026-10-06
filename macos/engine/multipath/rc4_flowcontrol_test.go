package multipath

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRC4StandbyWindowScalesWithLiveStreams(t *testing.T) {
	cases := []struct {
		streams int
		want    int
	}{
		{1, 192 << 10},
		{512, 128 << 10},
		{1024, 64 << 10},
		{2048, 32 << 10},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.streams), func(t *testing.T) {
			s := schedulerFixture()
			s.ctx = context.Background()
			for i := 0; i < tc.streams; i++ {
				st := s.newStreamLocked(uint64(1 + 2*i))
				st.open = true
			}
			if got := s.standbyWindowLocked(); got != tc.want {
				t.Fatalf("standby window mismatch for %d Streams: got=%d want=%d", tc.streams, got, tc.want)
			}
		})
	}
}

func TestRC4InitialWindowScalesUnderConnectionStorm(t *testing.T) {
	cases := []struct {
		streams int
		want    int
	}{
		{1, 192 << 10},
		{53, 192 << 10},
		{128, 128 << 10},
		{256, 64 << 10},
		{512, 32 << 10},
		{2048, 32 << 10},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.streams), func(t *testing.T) {
			s := schedulerFixture()
			s.ctx = context.Background()
			s.initScheduler(SchedulerWeighted)
			for i := 0; i < tc.streams; i++ {
				st := s.newStreamLocked(uint64(1 + 2*i))
				st.open = true
			}
			if got := s.initialWindowLocked(); got != tc.want {
				t.Fatalf("initial window mismatch for %d Streams: got=%d want=%d", tc.streams, got, tc.want)
			}
		})
	}
}

func TestRC4IdleBurstRestoresPerStreamWarmHistory(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.initScheduler(SchedulerWeighted)
	now := time.Now()
	st := s.newStreamLocked(1)
	st.open = true
	defer st.Close()

	st.advertiseCreditLocked(now)
	if st.windowTarget != StandbyStreamWindow {
		t.Fatalf("initial standby target=%d want=%d", st.windowTarget, StandbyStreamWindow)
	}

	const proven = 4 << 20
	st.windowTarget = proven
	st.warmTarget = proven
	st.warmAt = now
	st.lastRead = now
	st.advertiseCreditLocked(now)
	if st.rxLimit < proven {
		t.Fatalf("fixture did not advertise proven target: %d", st.rxLimit)
	}

	idleAt := now.Add(creditIdle + time.Second)
	st.advertiseCreditLocked(idleAt)
	if st.windowTarget != StandbyStreamWindow {
		t.Fatalf("idle established Stream did not settle at standby: %d", st.windowTarget)
	}
	if st.warmTarget != proven {
		t.Fatalf("idle lost warm history: %d", st.warmTarget)
	}

	consumeWindowFixture(t, st, StreamWindow, idleAt.Add(time.Millisecond))
	if !st.warmHistoryUsed {
		t.Fatal("burst did not consume warm-history eligibility")
	}
	if st.windowTarget != proven {
		t.Fatalf("burst did not rapidly restore warm target: got=%d want=%d", st.windowTarget, proven)
	}
}

func TestRC4WarmHistoryDecaysAndExpires(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	st := s.newStreamLocked(1)
	st.open = true
	now := time.Now()
	st.warmTarget = 4 << 20
	st.warmAt = now

	if got := st.warmHistoryTargetLocked(now.Add(45 * time.Second)); got != 2<<20 {
		t.Fatalf("warm history did not decay by one half-life: %d", got)
	}
	if got := st.warmHistoryTargetLocked(now.Add(95 * time.Second)); got != 512<<10 {
		t.Fatalf("warm history multi-step decay mismatch: %d", got)
	}
	if got := st.warmHistoryTargetLocked(now.Add(warmHistoryTTL)); got != 0 {
		t.Fatalf("expired warm history remained eligible: %d", got)
	}
}

func TestRC4InitialStandbyDoesNotCommitCredit(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.initScheduler(SchedulerWeighted)
	st := s.newStreamLocked(1)
	st.open = true
	st.advertiseCreditLocked(time.Now())
	if st.rxLimit != StandbyStreamWindow {
		t.Fatalf("unexpected initial standby grant: %d", st.rxLimit)
	}
	if s.receiveCredit != 0 || s.receiveGrowth != 0 || s.receiveAllocated != 0 {
		t.Fatalf("WINDOW entitlement reserved receive resources: credit=%d growth=%d pages=%d", s.receiveCredit, s.receiveGrowth, s.receiveAllocated)
	}
}
