package multipath

import (
	"testing"
	"time"
)

func TestWO01WeightedHerdGuardStopsSynchronizedBulkCostReordering(t *testing.T) {
	s := schedulerFixture()
	s.initScheduler(SchedulerWeighted)
	c1, c2 := schedulerPath(1), schedulerPath(2)
	c1.minRTT = 5 * time.Millisecond
	c2.minRTT = 500 * time.Millisecond
	c1.configuredRateBPS = configuredRateBPS(92)
	c2.configuredRateBPS = configuredRateBPS(92)
	c1.outstanding = weightedFlightBudget(c1, c1.configuredRateBPS)
	s.paths[1] = c1
	s.paths[2] = c2
	for i := 0; i < 64; i++ {
		id := uint64(1 + 2*i)
		s.streams[id] = &Stream{s: s, id: id}
	}
	now := time.Now()
	if got := s.pathLocked(now); got != nil {
		t.Fatalf("large-fan-in guard failed to preserve legacy cost deferral: %v", got)
	}
	if !s.weightedHerdGuard || s.pathSelection.CostDeferral == 0 {
		t.Fatal("guard did not latch")
	}
	for i := 0; i < 33; i++ {
		delete(s.streams, uint64(1+2*i))
	}
	if got := s.pathLocked(now); got != c2 {
		t.Fatalf("small fan-in never returned to work-conserving: %v", got)
	}
	if s.weightedHerdGuard {
		t.Fatal("guard did not clear after cooldown to 31 streams")
	}
}
