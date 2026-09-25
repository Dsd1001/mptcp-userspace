package multipath

import (
	"testing"
	"time"
)

func TestReceiverClockIgnoresReverseACKCompression(t *testing.T) {
	c := schedulerPath(1)
	c.startupDone = true
	now := time.Now()
	p := &outbound{f: frame{data: make([]byte, MaxPayload)}, sentAt: now.Add(-50 * time.Millisecond)}
	c.observeDelivery(now, p, uint64(time.Second))
	// Local ACKs arrive together, but the remote bytes arrived 200 ms apart.
	c.sampleBudgetLimited = true // continuously loaded forward path
	c.observeDelivery(now.Add(time.Microsecond), p, uint64(1200*time.Millisecond))
	expected := float64(MaxPayload) / .2
	if c.goodput < expected*.99 || c.goodput > expected*1.01 {
		t.Fatalf("compressed ACK rate accepted: %f vs %f", c.goodput, expected)
	}
	previous := c.remoteSampleAt
	c.observeDelivery(now, p, uint64(500*time.Millisecond))
	if c.remoteSampleAt != previous || c.goodput != expected {
		t.Fatal("old timestamp changed monotonic sample")
	}
	c.observeDelivery(now, p, uint64(30*time.Second))
	if c.remoteSampleAt != uint64(30*time.Second) || c.ackBytes != 0 {
		t.Fatal("idle interval polluted delivery estimate")
	}
}

func TestBudgetProbeRequiresDemandAndConfirmedFlight(t *testing.T) {
	c := schedulerPath(1)
	now := time.Now()
	p := &outbound{f: frame{data: make([]byte, MaxPayload)}, sentAt: now.Add(50 * time.Millisecond)}
	c.observeDelivery(now, p, 0)
	c.observeDelivery(now, p, 0)
	if c.flightBudget() != initialPathBudget {
		t.Fatal("idle path increased budget")
	}
	c.budgetLimited = true
	c.observeDelivery(now, p, 0)
	if c.flightBudget() != initialPathBudget*2 {
		t.Fatal("confirmed constrained flight did not probe capacity")
	}
	c.budgetLimited = true
	c.observeDelivery(now, p, 0)
	if c.flightBudget() != initialPathBudget*2 {
		t.Fatal("probe doubled without a full confirmed flight")
	}
}
