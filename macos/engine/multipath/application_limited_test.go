package multipath

import (
	"testing"
	"time"
)

func TestSparseUnfilledCarrierDoesNotCollapseCapacity(t *testing.T) {
	c := schedulerPath(1)
	c.goodput = 4 << 20
	c.startupDone = true
	now := time.Now()
	p := &outbound{f: frame{data: make([]byte, 1024)}, sentAt: now.Add(-50 * time.Millisecond)}
	c.observeDelivery(now, p, uint64(time.Second))
	c.observeDelivery(now.Add(time.Second), p, uint64(2*time.Second))
	if c.goodput != 4<<20 {
		t.Fatalf("sparse allocation was mistaken for capacity: got %.0f expected %d", c.goodput, 4<<20)
	}
}

func TestBusyCarrierStillLearnsSustainedLowerCapacity(t *testing.T) {
	s := schedulerFixture()
	c := schedulerPath(1)
	s.paths[1] = c
	c.goodput = 4 << 20
	c.startupDone = true
	now := time.Now()
	stamp := uint64(time.Second)
	p := &outbound{f: frame{data: make([]byte, MaxPayload)}, sentAt: now.Add(-50 * time.Millisecond)}
	c.observeDelivery(now, p, stamp)
	for i := 0; i < 10; i++ {
		// Show actual offered-load pressure through the production dispatcher gate,
		// not a testing-only injected bandwidth/capacity prior.
		c.outstanding = c.flightBudget()
		s.pathLocked(now)
		stamp += uint64(200 * time.Millisecond)
		now = now.Add(200 * time.Millisecond)
		p.sentAt = now.Add(-50 * time.Millisecond)
		c.observeDelivery(now, p, stamp)
	}
	expected := float64(MaxPayload) / .2
	if c.goodput > expected*1.01 || c.goodput < expected*.99 {
		t.Fatalf("busy slow carrier retained stale rate: %.0f expected %.0f", c.goodput, expected)
	}
	if c.flightBudget() > maxPathBudget {
		t.Fatal("unbounded budget")
	}
}
