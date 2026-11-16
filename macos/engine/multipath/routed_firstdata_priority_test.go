package multipath

import (
	"testing"
	"time"
)

// The old B1 first-DATA lane was skipped for all Routed Sessions. Restore
// priority ONLY inside the Route chosen by the DRR ring and its byte deficit.
func TestRoutedFirstDataPriorityStaysInsideDRRRoute(t *testing.T) {
	s := routeSchedulingFixture(t, 1, 1)
	s.initScheduler(SchedulerWeighted)
	c := schedulerPath(1)
	c.priorityDATA = make(chan sendTask, 2)
	c.configuredRateBPS = configuredRateBPS(94)
	c.budget = maxPathBudget
	s.paths[1] = c
	// A previously active bulk Stream is first in route 1.
	bulk := s.newStreamLocked(1)
	bulk.routeID = 1
	pBulk := s.queueLocked(frame{kind: kindData, stream: 1, offset: MaxPayload, data: []byte{1}})
	// A small new request waiting behind the bulk inside route 1.
	small := s.newStreamLocked(3)
	small.routeID = 1
	pSmall := s.queueLocked(frame{kind: kindData, stream: 3, offset: 0, data: []byte{2}})
	// Different route must still receive its normal DRR turn.
	other := s.newStreamLocked(5)
	other.routeID = 2
	pOther := s.queueLocked(frame{kind: kindData, stream: 5, offset: 0, data: []byte{3}})
	if pBulk == nil || pSmall == nil || pOther == nil {
		t.Fatal("unable to queue cross-route fixture")
	}
	s.dispatchLocked(time.Now())
	if s.firstDataPromoted != 2 {
		t.Fatalf("two routed small flows should be prioritized, promoted=%d", s.firstDataPromoted)
	}
	if !pSmall.firstDataTokenCharged || !pOther.firstDataTokenCharged || pBulk.firstDataTokenCharged {
		t.Fatal("wrong Stream granted DATA priority")
	}
	if len(c.priorityDATA) != 2 || len(c.queue) != 1 {
		t.Fatalf("carrier normal/priority queues %d/%d", len(c.queue), len(c.priorityDATA))
	}
	if s.routeGroups[1].dispatched != 2 || s.routeGroups[2].dispatched != 1 {
		t.Fatalf("DRR starved another route: dispatched=%d/%d", s.routeGroups[1].dispatched, s.routeGroups[2].dispatched)
	}
	if s.routeGroups[1].ready.Len() != 0 || s.routeGroups[1].firstReady.Len() != 0 ||
		s.routeGroups[2].ready.Len() != 0 || s.routeGroups[2].firstReady.Len() != 0 {
		t.Fatal("stale Route fast-ready or normal ring entries")
	}
	if s.fastReady.Len() != 0 {
		t.Fatal("legacy fast-ring stale references")
	}
	// ACK must refill, not duplicate mint; cancelled work clears both rings.
	before := s.firstDataTokens
	pSmall.attempts = 1
	pSmall.sentAt = time.Now().Add(-10 * time.Millisecond)
	if e := s.ackLocked(c, frame{kind: kindACK, stream: pSmall.f.stream, id: pSmall.f.id}); e != nil {
		t.Fatal(e)
	}
	if s.firstDataTokens <= before {
		t.Fatal("ACK failed to refill priority tokens")
	}
	after := s.firstDataTokens
	if e := s.ackLocked(c, frame{kind: kindACK, stream: pSmall.f.stream, id: pSmall.f.id}); e != nil {
		t.Fatal(e)
	}
	if s.firstDataTokens != after {
		t.Fatal("duplicate ACK minted second priority allowance")
	}
}

func TestRoutedFirstDataBypassesFullNormalCarrierQueue(t *testing.T) {
	s := routeSchedulingFixture(t, 1)
	s.initScheduler(SchedulerWeighted)
	c := schedulerPath(1)
	c.configuredRateBPS = configuredRateBPS(94)
	c.priorityDATA = make(chan sendTask, 2)
	c.budget = maxPathBudget
	s.paths[1] = c
	// A blocked ordinary writer must not prevent bounded first DATA from using
	// the separate two-slot priority queue selected within the Route DRR turn.
	for i := 0; i < carrierQueue; i++ {
		c.queue <- sendTask{p: &outbound{f: frame{kind: kindData, stream: 99}}}
	}
	st := s.newStreamLocked(1)
	st.routeID = 1
	p := s.queueLocked(frame{kind: kindData, stream: st.id, offset: 0, data: make([]byte, MaxPayload)})
	if p == nil {
		t.Fatal("cannot queue routed small response")
	}
	s.dispatchLocked(time.Now())
	if p.ready != nil || !p.firstDataTokenCharged || s.firstDataPromoted != 1 {
		t.Fatalf("routed B1 priority failed: ready=%v charged=%v count=%d", p.ready, p.firstDataTokenCharged, s.firstDataPromoted)
	}
	if len(c.queue) != carrierQueue || len(c.priorityDATA) != 1 || s.routeGroups[1].dispatched != uint64(MaxPayload) {
		t.Fatal("priority bypass changed normal queue or DRR charge")
	}
}

func TestRoutedFirstDataCancelledEntryDoesNotLeakRouteRing(t *testing.T) {
	s := routeSchedulingFixture(t, 1)
	s.initScheduler(SchedulerWeighted)
	st := s.newStreamLocked(1)
	st.routeID = 1
	p := s.queueLocked(frame{kind: kindData, stream: 1, offset: 0, data: []byte{1}})
	if p == nil || s.routeGroups[1].firstReady.Len() != 1 {
		t.Fatal("not queued")
	}
	s.removePendingLocked(p)
	if s.routeGroups[1].firstReady.Len() != 0 || s.fastReady.Len() != 0 || s.routeReady.Len() != 0 || s.readyFrames != 0 {
		t.Fatal("cancelled routed first DATA leaked priority/DRR entry")
	}
}

func TestRoutedFirstDataRespectsRouteDeficitWith150Streams(t *testing.T) {
	s := routeSchedulingFixture(t, 1, 1)
	s.initScheduler(SchedulerWeighted)
	a := s.newStreamLocked(1)
	a.routeID = 1
	s.queueLocked(frame{kind: kindData, stream: a.id, offset: 0, data: make([]byte, MaxPayload)})
	r := s.routeGroups[1]
	if r.firstReady.Len() != 1 {
		t.Fatal("missing bounded route first-DATA ring")
	}
	// Do not overspend route's DRR deficit even if token cap has room.
	r.deficit = MaxPayload - 1
	if q, _ := s.routedFirstDataCandidateLocked(r); q != nil {
		t.Fatal("priority bypassed byte deficit")
	}
	r.deficit = MaxPayload + 64
	if q, charge := s.routedFirstDataCandidateLocked(r); q == nil || charge != MaxPayload+64 {
		t.Fatalf("eligible route first data missing %v %d", q, charge)
	}
	// Cross-route: another route's first DATA cannot be selected here.
	b := s.newStreamLocked(3)
	b.routeID = 2
	s.queueLocked(frame{kind: kindData, stream: b.id, offset: 0, data: []byte{1}})
	if s.routeGroups[2].firstReady.Len() != 1 || s.routeGroups[1].firstReady.Len() != 1 {
		t.Fatal("route first-DATA queues leaked across routes")
	}
	for i := 0; i < 150; i++ {
		id := uint64(1000 + i)
		st := s.newStreamLocked(id)
		st.routeID = 2
		if s.queueLocked(frame{kind: kindData, stream: st.id, offset: 0, data: []byte{1}}) == nil {
			t.Fatal("large-Stream fixture")
		}
	}
	if q, charge := s.routedFirstDataCandidateLocked(r); q == nil || charge != MaxPayload+64 {
		t.Fatal("150 concurrent DATA frames incorrectly disabled route-local priority")
	}
	if s.firstDataTokens > firstDataTokenCapNormal {
		t.Fatal("route-local first DATA exceeded shared bounded tokens")
	}
}
