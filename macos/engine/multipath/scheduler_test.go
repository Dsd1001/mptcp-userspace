package multipath

import (
	"testing"
	"time"
)

func schedulerFixture() *Session {
	s := &Session{scheduler: schedulerState{configured: SchedulerAggregate, effective: SchedulerAggregate, reason: "forced_aggregate"}, changed: make(chan struct{}), kick: make(chan struct{}, 1), streams: make(map[uint64]*Stream), pending: make(map[uint64]*outbound), paths: make(map[byte]*carrier)}
	s.initCreditLocked()
	s.credit.peerLimit = SessionCreditLimit // fixture models an authenticated initial grant
	return s
}
func schedulerPath(id byte) *carrier {
	return &carrier{id: id, active: true, rtt: 20 * time.Millisecond, goodput: 2 << 20, queue: make(chan sendTask, carrierQueue), control: make(chan frame, 512), reliableControl: make(chan sendTask, controlCarrierQueue), sampleAt: time.Now()}
}
func TestGoodputExcludesApplicationIdle(t *testing.T) {
	s := schedulerFixture()
	c := schedulerPath(1)
	s.paths[1] = c
	c.sampleAt = time.Now().Add(-time.Second)
	c.ackBytes = 123
	s.queueLocked(frame{kind: kindData, stream: 1, data: make([]byte, MaxPayload)})
	before := time.Now()
	s.dispatchLocked(before)
	if c.sampleAt.Before(before) || c.ackBytes != 0 {
		t.Fatal("idle time would contaminate rate sample")
	}
}
func TestLateACKDoesNotCreditReassignedCarrier(t *testing.T) {
	s := schedulerFixture()
	a, b := schedulerPath(1), schedulerPath(2)
	s.paths[1] = a
	s.paths[2] = b
	a.sampleAt = time.Now().Add(-time.Second)
	p := s.queueLocked(frame{kind: kindData, stream: 1, data: make([]byte, MaxPayload)})
	p.path = b
	p.attempts = 2
	p.sentAt = time.Now()
	b.outstanding = p.cost
	initial := a.goodput
	s.ackLocked(a, frame{kind: kindACK, stream: 1, id: p.f.id})
	if a.goodput != initial || a.ackBytes != 0 || a.deliverySamples != 0 {
		t.Fatal("late ACK polluted another path estimate")
	}
	if len(s.pending) != 0 || b.outstanding != 0 {
		t.Fatal("ACK did not release retransmitted pending work")
	}
}
func TestSchedulerWaitsRatherThanFillingSlowPath(t *testing.T) {
	s := schedulerFixture()
	fast, slow := schedulerPath(1), schedulerPath(2)
	slow.rtt = 200 * time.Millisecond
	slow.goodput = 128 << 10
	for i := 0; i < carrierQueue; i++ {
		fast.queue <- sendTask{}
	}
	s.paths[1] = fast
	s.paths[2] = slow
	if c := s.pathLocked(time.Now()); c != nil {
		t.Fatal("full fast writer queue forced traffic onto much slower route")
	}
	fast.active = false
	if c := s.pathLocked(time.Now()); c != slow {
		t.Fatal("only surviving slow route must remain usable")
	}
}
