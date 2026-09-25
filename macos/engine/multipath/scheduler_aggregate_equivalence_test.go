package multipath

import (
	"math/rand"
	"testing"
	"time"
)

func (s *Session) reference258cPathLocked(now time.Time) *carrier {
	var best *carrier
	bestScore, earliest := 1e30, 1e30
	healthy := false
	for _, c := range s.paths {
		if c.active && !now.Before(c.penaltyUntil) {
			healthy = true
			break
		}
	}
	for _, c := range s.paths {
		if !c.active || (healthy && now.Before(c.penaltyUntil)) {
			continue
		}
		rate := max(c.goodput, 65536)
		// Flight bytes already include serialization queueing. Do not charge
		// measured queue-inflated RTT a second time when choosing a path.
		baseRTT := c.minRTT
		if baseRTT <= 0 {
			baseRTT = c.rtt
		}
		score := baseRTT.Seconds()/2 + float64(c.outstanding+MaxPayload)/rate
		earliest = min(earliest, score)
		// Cap application-layer in-flight work to an estimated bandwidth-delay
		// budget. Filling a slow carrier's OS send buffer creates avoidable HOL.
		budget := c.flightBudget()
		if c.outstanding >= budget {
			c.budgetLimited = true
			c.sampleBudgetLimited = true
		}
		if len(c.queue) >= carrierQueue || c.outstanding >= budget {
			continue
		}
		if best == nil || score < bestScore || (score == bestScore && c.id < best.id) {
			best = c
			bestScore = score
		}
	}
	// Waiting briefly for the earliest route can beat sending immediately over
	// a high-delay path merely because another writer's tiny queue is full.
	if best != nil && bestScore > earliest+.025 {
		return nil
	}
	return best
}

func TestSchedulerAggregateMatchesFrozenSelection(t *testing.T) {
	rng := rand.New(rand.NewSource(689))
	for trial := 0; trial < 2000; trial++ {
		s := schedulerFixture()
		s.initScheduler(SchedulerAggregate)
		now := time.Now()
		for id := byte(1); id <= 6; id++ {
			c := schedulerPath(id)
			c.active = rng.Intn(5) != 0
			c.minRTT = time.Duration(1+rng.Intn(200)) * time.Millisecond
			c.rtt = c.minRTT * 2
			c.goodput = float64(65536 + rng.Intn(50000000))
			c.outstanding = rng.Intn(4 << 20)
			c.budget = 65536 + rng.Intn(3<<20)
			c.scheduler.role = RoleActive
			if rng.Intn(6) == 0 {
				c.penaltyUntil = now.Add(time.Second)
			}
			for n := rng.Intn(carrierQueue + 1); n > 0; n-- {
				c.queue <- sendTask{}
			}
			s.paths[id] = c
		}
		expected := s.reference258cPathLocked(now)
		got := s.pathLocked(now)
		if got != expected {
			t.Fatalf("forced Aggregate changed frozen algorithm at trial %d", trial)
		}
		s.initScheduler(SchedulerProtect)
		if got = s.pathLocked(now); got != expected {
			t.Fatalf("Protect ACTIVE set changed Aggregate selection at trial %d", trial)
		}
	}
}
