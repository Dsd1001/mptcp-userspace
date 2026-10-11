package multipath

import (
	"testing"
	"time"
)

func TestWO01WeightedWorkConservingSkipsUnavailableLowCostCarrier(t *testing.T) {
	s := schedulerFixture()
	s.initScheduler(SchedulerWeighted)
	now := time.Now()
	near := schedulerPath(1)
	far := schedulerPath(2)
	near.minRTT = 5 * time.Millisecond
	far.minRTT = 100 * time.Millisecond
	for _, c := range []*carrier{near, far} {
		c.configuredRateBPS = configuredRateBPS(92)
		c.budget = 4 << 20
		c.outstanding = 0
	}
	s.paths = map[uint64]*carrier{1: near, 2: far}
	if got := s.pathLocked(now); got != near {
		t.Fatalf("cost-rank nearest=%v", got)
	}
	near.outstanding = maxPathBudget // Saturated preferable carrier cannot defer usable second carrier
	if got := s.pathLocked(now); got != far {
		t.Fatalf("budget-limited nearest blocked healthy fallback=%v", got)
	}
	near.outstanding = 0
	near.writeStartedNS.Store(now.Add(-1500 * time.Millisecond).UnixNano())
	if got := s.pathLocked(now); got != far {
		t.Fatalf("stalled nearest blocked fallback=%v", got)
	}
	far.active = false
	if got := s.pathLocked(now); got != nil {
		t.Fatalf("stalled sole path must not receive DATA: %v", got)
	}
	if s.pathSelection.WriterStall == 0 {
		t.Fatal("writer stall not attributed")
	}
	far.active = true
	near.writeStartedNS.Store(0)
	if got := s.pathLocked(now); got != near {
		t.Fatalf("recovering nearest was starved: %v", got)
	}
	near.penaltyUntil = now.Add(time.Second)
	if got := s.pathLocked(now); got != far {
		t.Fatalf("penalized nearest not excluded: %v", got)
	}
}
func TestWO01AggregateKeepsLegacyCostDeferralUnderWriterStall(t *testing.T) {
	s := schedulerFixture()
	s.initScheduler(SchedulerAggregate)
	now := time.Now()
	near, far := schedulerPath(1), schedulerPath(2)
	near.minRTT = 5 * time.Millisecond
	far.minRTT = 500 * time.Millisecond
	near.outstanding = near.flightBudget()
	near.writeStartedNS.Store(now.Add(-time.Second).UnixNano())
	s.paths = map[uint64]*carrier{1: near, 2: far}
	if c := s.pathLocked(now); c != nil {
		t.Fatalf("aggregate should retain frozen deferral: %v", c)
	}
	if s.pathSelection.CostDeferral != 1 {
		t.Fatal("legacy deferral missing")
	}
}
