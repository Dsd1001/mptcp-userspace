package multipath

import (
	"testing"
	"time"
)

func TestRecentCapacityExpiresAfterLowerRateSamples(t *testing.T) {
	c := schedulerPath(1)
	c.startupDone = true
	now := time.Now()
	stamp := uint64(time.Second)
	p := &outbound{f: frame{data: make([]byte, MaxPayload)}, sentAt: now.Add(-time.Millisecond)}
	c.observeDelivery(now, p, stamp)
	// One faster measured epoch seeds capacity, then eight slower epochs must
	// replace it. The filter is not a permanent high-watermark or fixed weight.
	stamp += uint64(100 * time.Millisecond)
	c.sampleBudgetLimited = true // this fixture models continuously offered work
	c.observeDelivery(now, p, stamp)
	high := c.goodput
	p.f.data = p.f.data[:1024]
	for i := 0; i < 8; i++ {
		c.sampleBudgetLimited = true
		stamp += uint64(100 * time.Millisecond)
		c.observeDelivery(now, p, stamp)
	}
	if c.goodput >= high || c.goodput != 65536 {
		t.Fatalf("old capacity never expired: high=%f now=%f", high, c.goodput)
	}
	if c.capacityIndex < 0 || c.capacityIndex >= len(c.capacitySamples) {
		t.Fatal("capacity history unbounded")
	}
}

func TestFeedbackBudgetHasQueueInflationCap(t *testing.T) {
	c := schedulerPath(1)
	c.minRTT = 30 * time.Millisecond
	c.rtt = 30 * time.Second
	c.goodput = 6e6
	c.startupDone = true
	for i := 0; i < 20; i++ {
		c.updateBudget()
	}
	// The measured thirty-second RTT cannot create a thirty-second send queue.
	maximum := int(6e6*(1.25*.120+.015)) + 2*MaxPayload
	if c.flightBudget() > maximum || c.flightBudget() > maxPathBudget {
		t.Fatalf("queue feedback inflated budget: %d", c.flightBudget())
	}
}
