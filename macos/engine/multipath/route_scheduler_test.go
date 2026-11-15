package multipath

import (
	"testing"
)

func routeSchedulingFixture(t *testing.T, weights ...int) *Session {
	t.Helper()
	s := schedulerFixture()
	s.productMux = true
	var policies []RoutePolicy
	for i, w := range weights {
		policies = append(policies, RoutePolicy{ID: uint32(i + 1), Weight: w})
	}
	if err := s.ConfigureRoutePolicies(policies); err != nil {
		t.Fatal(err)
	}
	return s
}

func routeTestQueue(t *testing.T, s *Session, stream uint64, route uint32, frames, size int) *Stream {
	t.Helper()
	st := s.newStreamLocked(stream)
	if err := s.bindRouteLocked(st, route); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < frames; i++ {
		if s.queueLocked(frame{kind: kindData, stream: stream, offset: uint64(i * size), data: make([]byte, size)}) == nil {
			t.Fatal("enqueue")
		}
	}
	return st
}

func takeRouteFrame(t *testing.T, s *Session) (uint32, int) {
	t.Helper()
	rq := s.nextDataReadyLocked()
	if rq == nil {
		t.Fatal("no eligible data")
	}
	p := rq.frames.Front().Value.(*outbound)
	id, cost := rq.route.id, p.cost
	s.unreadyLocked(p)
	s.routeDispatchedLocked(rq, p)
	s.removePendingLocked(p)
	return id, cost
}

func TestRouteDRRFairnessIndependentOfConnectionCount(t *testing.T) {
	for _, weight := range []int{1, 2} {
		s := routeSchedulingFixture(t, 1, weight)
		for i := 0; i < 16; i++ {
			routeTestQueue(t, s, uint64(2*i+1), 1, 32, MaxPayload)
		}
		routeTestQueue(t, s, 101, 2, 256, MaxPayload)
		counts := map[uint32]int{}
		turns := 96
		for i := 0; i < turns; i++ {
			id, _ := takeRouteFrame(t, s)
			counts[id]++
		}
		want := turns / (1 + weight)
		if counts[1] != want || counts[2] != turns-want {
			t.Fatalf("weight %d: got %v; connection-count bias", weight, counts)
		}
	}
}

func TestRouteDRRChargesBytesAndBorrowsIdleCapacity(t *testing.T) {
	s := routeSchedulingFixture(t, 1, 1, 1)
	routeTestQueue(t, s, 1, 1, 1024, 512)
	routeTestQueue(t, s, 3, 2, 128, MaxPayload)
	totals := map[uint32]int{}
	for i := 0; i < 400; i++ {
		id, cost := takeRouteFrame(t, s)
		totals[id] += cost
	}
	delta := totals[1] - totals[2]
	if delta < 0 {
		delta = -delta
	}
	if delta > 2*(MaxPayload+64) {
		t.Fatalf("not byte-fair: %v", totals)
	}
	s = routeSchedulingFixture(t, 1, 1, 1)
	routeTestQueue(t, s, 1, 1, 32, MaxPayload)
	for i := 0; i < 32; i++ {
		if id, _ := takeRouteFrame(t, s); id != 1 {
			t.Fatal("idle route held capacity")
		}
	}
	if s.routeReady.Len() != 0 || s.routeReadyWeight != 0 || s.readyDataBytes != 0 {
		t.Fatal("ready accounting leaked")
	}
}

func TestRouteQueueAdmissionAndIdentity(t *testing.T) {
	s := routeSchedulingFixture(t, 1, 1)
	s.queueAdmissionLimit = 1 << 20
	a := routeTestQueue(t, s, 1, 1, 20, MaxPayload)
	b := routeTestQueue(t, s, 3, 2, 1, MaxPayload)
	a.txNext, b.txNext = StreamWindow, StreamWindow
	if room := s.routeAdmissionRoomLocked(a); room != 0 {
		t.Fatalf("bulk route can monopolize queue: %d", room)
	}
	if room := s.routeAdmissionRoomLocked(b); room < MaxPayload {
		t.Fatalf("small route starved: %d", room)
	}
	if err := s.bindRouteLocked(a, 2); err == nil {
		t.Fatal("route changed after DATA commitment")
	}
	before := s.credit.txCommitted
	takeRouteFrame(t, s)
	if s.credit.txCommitted != before {
		t.Fatal("route scheduling created a second credit pool")
	}
}

func TestRoutedEarlyDataGateDoesNotOverridePeerWindow(t *testing.T) {
	s, _ := rev2Fixture()
	s.productMux = true
	if err := s.ConfigureRoutePolicies([]RoutePolicy{{ID: 1}}); err != nil {
		t.Fatal(err)
	}
	st := rev2Stream(s, 1)
	if err := s.bindRouteLocked(st, 1); err != nil {
		t.Fatal(err)
	}
	st.writeRemaining = MaxPayload
	st.txNext = routedEarlyDataLimit
	if n, reason := st.writeAllowanceLocked(); n != 0 || reason != waitStreamWindow {
		t.Fatalf("early gate: %d %d", n, reason)
	}
	st.routeConfirmed = true
	if n, _ := st.writeAllowanceLocked(); n <= 0 {
		t.Fatal("confirmation did not release early gate")
	}
	s.credit.txCommitted = s.credit.peerLimit
	if n, reason := st.writeAllowanceLocked(); n != 0 || reason != waitSessionWindow {
		t.Fatalf("peer credit bypass: %d %d", n, reason)
	}
}
