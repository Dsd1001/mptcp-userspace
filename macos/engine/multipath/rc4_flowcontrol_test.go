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

func TestRC7OptimisticAllowanceSupersedesWarmHistoryGate(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	now := time.Now()
	st := s.newStreamLocked(1)
	st.open = true
	defer st.Close()

	st.warmTarget = 4 << 20 // historical state must not cap the RC7 allowance.
	st.warmAt = now
	st.advertiseCreditLocked(now)
	if st.windowTarget != MaxStreamWindow || st.rxLimit != MaxStreamWindow {
		t.Fatalf("warm history capped optimistic allowance: target=%d limit=%d", st.windowTarget, st.rxLimit)
	}

	consumeWindowFixture(t, st, StreamWindow, now.Add(time.Millisecond))
	if st.windowTarget != MaxStreamWindow || st.rxLimit-st.rxRead > MaxStreamWindow {
		t.Fatalf("real consumption changed optimistic allowance semantics: target=%d remaining=%d", st.windowTarget, st.rxLimit-st.rxRead)
	}
	if s.receiveCredit != 0 || s.receiveGrowth != 0 {
		t.Fatalf("consumed allowance pinned actual Session credit: credit=%d growth=%d", s.receiveCredit, s.receiveGrowth)
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

func TestRC7OptimisticInitialAllowanceDoesNotCommitCredit(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	st := s.newStreamLocked(1)
	st.open = true
	st.advertiseCreditLocked(time.Now())
	if st.rxLimit != MaxStreamWindow {
		t.Fatalf("unexpected optimistic initial grant: %d", st.rxLimit)
	}
	if s.receiveCredit != 0 || s.receiveGrowth != 0 || s.receiveAllocated != 0 {
		t.Fatalf("WINDOW entitlement reserved receive resources: credit=%d growth=%d pages=%d", s.receiveCredit, s.receiveGrowth, s.receiveAllocated)
	}
}
