package multipath

import (
	"fmt"
	"testing"
	"time"
)

// Shared Tunnel Weighted must dispatch to an available Carrier even after a
// 64-Stream fan-in activates the legacy herd guard.
func TestRoutedWeightedHerdGuardUsesAvailableCarrier(t *testing.T) {
	for _, streams := range []int{32, 63, 64, 97} {
		t.Run(fmt.Sprintf("streams_%d", streams), func(t *testing.T) {
			s := schedulerFixture()
			s.initScheduler(SchedulerWeighted)
			s.routeGroups = map[uint32]*routeGroup{1: {id: 1, weight: 1}}
			near, far := schedulerPath(1), schedulerPath(2)
			near.minRTT, near.rtt = 5*time.Millisecond, 5*time.Millisecond
			far.minRTT, far.rtt = 500*time.Millisecond, 500*time.Millisecond
			near.configuredRateBPS = configuredRateBPS(94)
			far.configuredRateBPS = configuredRateBPS(94)
			near.outstanding = weightedFlightBudget(near, near.configuredRateBPS)
			s.paths[1], s.paths[2] = near, far
			for i := uint64(1); i <= uint64(streams); i++ {
				s.streams[i] = &Stream{s: s, id: i}
			}
			got := s.pathLocked(time.Now())
			if s.weightedHerdGuard != (streams >= 64) {
				t.Fatalf("herd guard state %t at %d streams", s.weightedHerdGuard, streams)
			}
			if got == nil {
				t.Fatalf("routed Weighted must find an available Carrier: streams=%d cost_deferral=%d", streams, s.pathSelection.CostDeferral)
			}
			if streams >= 64 && got != far {
				t.Fatalf("herd guard skipped only budget-eligible Carrier: got=%p want=%p streams=%d", got, far, streams)
			}
			if s.pathSelection.CostDeferral != 0 {
				t.Fatalf("routed Weighted must remain work-conserving: cost_deferral=%d", s.pathSelection.CostDeferral)
			}
		})
	}
}
