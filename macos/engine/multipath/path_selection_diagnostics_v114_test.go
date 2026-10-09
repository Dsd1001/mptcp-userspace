package multipath

import (
	"testing"
	"time"
)

func TestV114PathSelectionDiagnosticsExclusiveReasons(t *testing.T) {
	s := schedulerFixture()
	now := time.Now()
	expect := func(what string, selected bool, reason uint64) {
		t.Helper()
		c := s.pathLocked(now)
		if (c != nil) != selected {
			t.Fatalf("%s selected=%v wanted=%v", what, c != nil, selected)
		}
		a := s.pathSelection
		failed := a.FlightBudget + a.CarrierQueue + a.RoleRestricted + a.Penalty + a.NoActive + a.CostDeferral + a.Other
		if a.Attempts != a.Selected+failed {
			t.Fatalf("%s: attempts=%d selected=%d failed=%d", what, a.Attempts, a.Selected, failed)
		}
		if !selected && failed != reason {
			t.Fatalf("%s: failed count=%d expected=%d", what, failed, reason)
		}
	}
	expect("no path", false, 1)
	c := schedulerPath(1)
	s.paths[c.id] = c
	c.outstanding = c.flightBudget()
	expect("budget saturated", false, 2)
	if s.pathSelection.FlightBudget != 1 {
		t.Fatalf("wrong budget count: %+v", s.pathSelection)
	}
	c.outstanding = 0
	for i := 0; i < carrierQueue; i++ {
		c.queue <- sendTask{}
	}
	expect("full carrier queue", false, 3)
	if s.pathSelection.CarrierQueue != 1 {
		t.Fatalf("wrong queue count: %+v", s.pathSelection)
	}
	<-c.queue
	expect("path available", true, 3)
	r := s.resourceSnapshotLocked()
	if r.PathSelection.Attempts != 4 || r.PathSelection.Selected != 1 || r.PathSelection.NoActive != 1 {
		t.Fatalf("snapshot missing counters: %+v", r.PathSelection)
	}
}

func TestV114PathCostDeferralAttribution(t *testing.T) {
	s := schedulerFixture()
	fast, slow := schedulerPath(1), schedulerPath(2)
	slow.rtt = 200 * time.Millisecond
	slow.goodput = 128 << 10
	for i := 0; i < carrierQueue; i++ {
		fast.queue <- sendTask{}
	}
	s.paths[1] = fast
	s.paths[2] = slow
	if s.pathLocked(time.Now()) != nil {
		t.Fatal("expected cost deferral")
	}
	if s.pathSelection.CostDeferral != 1 || s.pathSelection.Attempts != 1 {
		t.Fatalf("cost deferral not identified: %+v", s.pathSelection)
	}
}
