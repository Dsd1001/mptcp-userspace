package multipath

import (
	"fmt"
	"testing"
	"time"
)

func TestRoutedWeightedAvailableCarrierWithManyConnections(t *testing.T) {
	for _, streams := range []int{32, 63, 64, 97, 150} {
		t.Run(fmt.Sprintf("streams_%d", streams), func(t *testing.T) {
			s := schedulerFixture()
			s.initScheduler(SchedulerWeighted)
			s.routeGroups = map[uint32]*routeGroup{1: {id: 1, weight: 1}}
			near, far := schedulerPath(1), schedulerPath(2)
			near.minRTT, near.rtt = 5*time.Millisecond, 5*time.Millisecond
			far.minRTT, far.rtt = 500*time.Millisecond, 500*time.Millisecond
			near.configuredRateBPS = configuredRateBPS(94)
			far.configuredRateBPS = configuredRateBPS(94)
			near.outstanding = legacyWeightedFlightBudgetForTest(near, near.configuredRateBPS)
			s.paths[1], s.paths[2] = near, far
			for i := uint64(1); i <= uint64(streams); i++ {
				s.streams[i] = &Stream{s: s, id: i}
			}
			if s.pathLocked(time.Now()) == nil {
				t.Fatalf("no carrier with %d Streams", streams)
			}
			if s.pathSelection.CostDeferral != 0 {
				t.Fatal("weighted cost deferral")
			}
		})
	}
}
