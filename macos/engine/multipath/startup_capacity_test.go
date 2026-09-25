package multipath

import (
	"testing"
	"time"
)

// A single receiver-limited startup epoch must not erase every unfilled entry
// in the capacity history. The prior expires after eight actual loaded epochs;
// this is not a permanent minimum rate or an injected link-capacity benchmark.
func TestNewCarrierPriorSurvivesOneLowEpochThenExpires(t *testing.T) {
	s, _, _, _ := testTCP(t, 2, 16<<20, 5*time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.paths[1]
	initial := c.goodput
	c.startupDone = true
	now := time.Now()
	p := &outbound{f: frame{data: make([]byte, MaxPayload)}, sentAt: now.Add(-20 * time.Millisecond)}
	stamp := uint64(time.Second)
	c.observeDelivery(now, p, stamp)
	c.sampleBudgetLimited = true
	stamp += uint64(200 * time.Millisecond)
	c.observeDelivery(now.Add(200*time.Millisecond), p, stamp)
	if c.goodput != initial {
		t.Fatalf("one low startup epoch collapsed capacity: got %.0f prior %.0f", c.goodput, initial)
	}
	for i := 0; i < 7; i++ {
		c.sampleBudgetLimited = true
		stamp += uint64(200 * time.Millisecond)
		c.observeDelivery(now.Add(time.Duration(i+2)*200*time.Millisecond), p, stamp)
	}
	expected := float64(MaxPayload) / .2
	if c.goodput < expected*.99 || c.goodput > expected*1.01 {
		t.Fatalf("bounded startup prior did not expire: got %.0f want %.0f", c.goodput, expected)
	}
}
