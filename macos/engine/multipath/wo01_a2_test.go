package multipath

import (
	"testing"
	"time"
)

func TestWO01A2LoadedACKRTTDoesNotInflateFlight(t *testing.T) {
	c := schedulerPath(1)
	c.minRTT = 30 * time.Millisecond
	c.rtt = 200 * time.Millisecond
	c.configuredRateBPS = configuredRateBPS(92)
	c.deliverySamples = 4
	c.goodput = c.configuredRateBPS
	now := time.Now()
	legacy := weightedFlightBudget(c, c.configuredRateBPS)
	adaptive := weightedAdaptiveFlightBudget(c, c.configuredRateBPS, now)
	if adaptive >= legacy {
		t.Fatalf("loaded ACK inflated budget: adaptive=%d legacy=%d", adaptive, legacy)
	}
	if adaptive < initialPathBudget {
		t.Fatal("undersized flight")
	}
}
func TestWO01A2HighRTTDoesNotStarveFlight(t *testing.T) {
	c := schedulerPath(1)
	c.minRTT = 450 * time.Millisecond
	c.rtt = c.minRTT
	c.configuredRateBPS = configuredRateBPS(92)
	got := weightedAdaptiveFlightBudget(c, c.configuredRateBPS, time.Now())
	if got < 6<<20 || got > maxPathBudget {
		t.Fatalf("highBDP adaptive budget=%d", got)
	}
}
func TestWO01A2CooldownRampAndRecovery(t *testing.T) {
	c := schedulerPath(1)
	now := time.Now()
	c.minRTT = 10 * time.Millisecond
	c.rtt = 10 * time.Millisecond
	rate := configuredRateBPS(92)
	start := weightedAdaptiveFlightBudget(c, rate, now)
	c.minRTT = 100 * time.Millisecond
	if got := weightedAdaptiveFlightBudget(c, rate, now.Add(20*time.Millisecond)); got != start {
		t.Fatalf("cooldown invalid: got %d want %d", got, start)
	}
	next := weightedAdaptiveFlightBudget(c, rate, now.Add(200*time.Millisecond))
	if next <= start || next > start+max(MaxPayload, start/4) {
		t.Fatalf("unbounded growth start=%d next=%d", start, next)
	}
	c.deliverySamples = 5
	c.rtt = 600 * time.Millisecond
	small := weightedAdaptiveFlightBudget(c, rate, now.Add(400*time.Millisecond))
	if small > next+max(MaxPayload, next/4) {
		t.Fatalf("loaded ACK causes unbounded increase: %d to %d", next, small)
	}
	// Reach a steady loaded-flight target before checking a penalty downshift.
	for i := 3; i < 18; i++ {
		weightedAdaptiveFlightBudget(c, rate, now.Add(time.Duration(i)*200*time.Millisecond))
	}
	steady := c.weightedAdaptiveBudget
	c.penaltyUntil = now.Add(10 * time.Second)
	shrunk := weightedAdaptiveFlightBudget(c, rate, now.Add(4*time.Second))
	if shrunk >= steady {
		t.Fatalf("degraded path failed to back off %d -> %d", steady, shrunk)
	}
}
func TestWO01A2SessionFlightCap(t *testing.T) {
	s := schedulerFixture()
	s.initScheduler(SchedulerWeighted)
	c := schedulerPath(1)
	c.configuredRateBPS = configuredRateBPS(92)
	c.outstanding = 48 << 20
	s.paths[1] = c
	if got := s.pathLocked(time.Now()); got != nil {
		t.Fatal("unbounded aggregate flight")
	}
	if s.pathSelection.FlightBudget != 1 {
		t.Fatal("missing cap attribution")
	}
}
func TestWO01A2AutoUploadPreservesLearnedBudget(t *testing.T) {
	c := schedulerPath(1)
	c.budget = 256 << 10
	if got := weightedAdaptiveFlightBudget(c, 0, time.Now()); got != c.flightBudget() {
		t.Fatalf("upload auto changed budget: got %d want %d", got, c.flightBudget())
	}
}
