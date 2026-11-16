package multipath

import (
	"testing"
	"time"
)

func TestWO01Weighted150ConnectedStreamsRemainWorkConserving(t *testing.T) {
	s := schedulerFixture()
	s.initScheduler(SchedulerWeighted)
	near, far := schedulerPath(1), schedulerPath(2)
	near.minRTT, near.rtt = 5*time.Millisecond, 5*time.Millisecond
	far.minRTT, far.rtt = 500*time.Millisecond, 500*time.Millisecond
	near.configuredRateBPS = configuredRateBPS(92)
	far.configuredRateBPS = configuredRateBPS(92)
	near.outstanding = legacyWeightedFlightBudgetForTest(near, near.configuredRateBPS)
	s.paths[1], s.paths[2] = near, far
	for i := 0; i < 150; i++ {
		id := uint64(1 + i*2)
		s.streams[id] = &Stream{s: s, id: id}
	}
	if got := s.pathLocked(time.Now()); got == nil {
		t.Fatal("weighted path stalled at 150 connections")
	}
	if s.pathSelection.CostDeferral != 0 {
		t.Fatal("weighted unexpectedly deferred cost")
	}
}
