package multipath

import (
	"context"
	"testing"
	"time"
)

func TestRC7LoadRTTIsTelemetryNotStreamCreditGate(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.initScheduler(SchedulerWeighted)

	c := schedulerPath(1)
	c.minRTT = 30 * time.Millisecond
	c.rtt = 100 * time.Millisecond
	c.configuredRateBPS = 92e6 / 8
	s.paths[1] = c

	now := time.Now()
	st := s.newStreamLocked(1)
	st.open = true
	defer st.Close()
	st.advertiseCreditLocked(now)

	base, load := s.creditRTTsLocked()
	if base != 30*time.Millisecond || load != 100*time.Millisecond {
		t.Fatalf("load RTT telemetry mismatch: base=%v load=%v", base, load)
	}
	if st.windowTarget != MaxStreamWindow || st.rxLimit != MaxStreamWindow {
		t.Fatalf("RTT gated optimistic Stream allowance: target=%d limit=%d", st.windowTarget, st.rxLimit)
	}
	consumeWindowFixture(t, st, StandbyStreamWindow/2, now.Add(time.Millisecond))
	if st.windowTarget != MaxStreamWindow {
		t.Fatalf("consumption reintroduced RTT/BDP Stream gating: %d", st.windowTarget)
	}
}

func TestRC6BulkFairShareUsesActiveDemand(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.initScheduler(SchedulerWeighted)
	now := time.Now()

	const streams = 53
	for i := 0; i < streams; i++ {
		st := s.newStreamLocked(uint64(1 + 2*i))
		st.open = true
		st.initialWindow = StandbyStreamWindow
		st.demandBytes = StandbyStreamWindow / 2
		st.lastRead = now
		st.bulkActive = true
	}

	want := GrowthCreditLimit / streams
	if want < BulkWindowFloor {
		want = BulkWindowFloor
	}
	if want > BulkWindowMaxFloor {
		want = BulkWindowMaxFloor
	}
	if got := s.bulkWindowFloorLocked(now); got != want {
		t.Fatalf("53-Stream bulk floor mismatch: got=%d want=%d", got, want)
	}
	if got := s.activeBulkStreamsLocked(now); got != streams {
		t.Fatalf("active bulk telemetry mismatch: got=%d want=%d", got, streams)
	}
}

func TestRC6BulkFloorFallsBackUnderConnectionStorm(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.initScheduler(SchedulerWeighted)
	now := time.Now()

	for i := 0; i < 2048; i++ {
		st := s.newStreamLocked(uint64(1 + 2*i))
		st.open = true
		st.initialWindow = StreamWindow
		st.demandBytes = StreamWindow
		st.lastRead = now
		st.bulkActive = true
	}
	if got := s.bulkWindowFloorLocked(now); got != StreamWindow {
		t.Fatalf("2048-Stream storm floor=%d want=%d", got, StreamWindow)
	}
}

func TestRC6BulkFloorPressureGovernor(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.initScheduler(SchedulerWeighted)
	now := time.Now()
	st := s.newStreamLocked(1)
	st.open = true
	st.demandBytes = StandbyStreamWindow
	st.lastRead = now
	st.bulkActive = true

	if got := s.bulkWindowFloorLocked(now); got != BulkWindowMaxFloor {
		t.Fatalf("unpressured bulk floor=%d want=%d", got, BulkWindowMaxFloor)
	}

	s.receiveCredit = 60 * SessionCreditLimit / 100
	if got := s.bulkWindowFloorLocked(now); got != 3*BulkWindowMaxFloor/4 {
		t.Fatalf("60%% pressure floor=%d want=%d", got, 3*BulkWindowMaxFloor/4)
	}

	s.receiveCredit = 80 * SessionCreditLimit / 100
	if got := s.bulkWindowFloorLocked(now); got != BulkWindowMaxFloor/2 {
		t.Fatalf("80%% pressure floor=%d want=%d", got, BulkWindowMaxFloor/2)
	}

	s.receiveCredit = 95 * SessionCreditLimit / 100
	if got := s.bulkWindowFloorLocked(now); got != 0 {
		t.Fatalf("95%% pressure should stop extra fair-share growth: got=%d", got)
	}
}

func TestRC6LoadRTTNeverFallsBelowHighBaseRTT(t *testing.T) {
	s := schedulerFixture()
	s.scheduler.configured = SchedulerWeighted
	c := schedulerPath(1)
	c.minRTT = 200 * time.Millisecond
	c.rtt = 800 * time.Millisecond
	c.configuredRateBPS = 92e6 / 8
	s.paths[1] = c

	base, load := s.creditRTTsLocked()
	if base != 200*time.Millisecond || load != 200*time.Millisecond {
		t.Fatalf("high propagation floor was clipped by absolute load cap: base=%v load=%v", base, load)
	}
}
