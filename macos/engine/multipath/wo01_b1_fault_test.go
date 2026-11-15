package multipath

import (
	"errors"
	"net"
	"testing"
	"time"
)

// The dedicated DATA channel is an extra queue, not an extra Transmission
// identity. A failed Carrier must not replay an old task after reinjection.
func TestWO01B1CarrierPriorityQueuedFailureReinjectsWithoutReboost(t *testing.T) {
	s, failed := wo01B1Session()
	s.streams[1] = &Stream{s: s, id: 1}
	failed.minRTT = 5 * time.Millisecond
	failed.rtt = 10 * time.Millisecond
	backup := schedulerPath(2)
	backup.minRTT = 60 * time.Millisecond
	backup.rtt = 120 * time.Millisecond
	backup.configuredRateBPS = configuredRateBPS(92)
	backup.priorityDATA = make(chan sendTask, 2)
	s.paths[2] = backup
	a, b := net.Pipe()
	defer b.Close()
	key, err := ParseKey(testToken)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := newSecure(a, key, []byte("wo01-reinjection-priority"), true)
	if err != nil {
		t.Fatal(err)
	}
	failed.conn = tx
	failed.done = make(chan struct{})
	p := s.queueLocked(frame{kind: kindData, stream: 1, offset: 0, data: []byte("no-loss-reinject")})
	if p == nil {
		t.Fatal("cannot queue first DATA")
	}
	id := p.f.id
	s.dispatchLocked(time.Now())
	if p.path != failed || len(failed.priorityDATA) != 1 || !p.firstDataTokenCharged {
		t.Fatalf("first DATA not on primary priority queue path=%v len=%d", p.path, len(failed.priorityDATA))
	}
	beforeGen := p.generation
	s.detachLocked(failed, errors.New("synthetic Carrier failure"))
	if failed.active || p.path != nil || p.f.id != id || p.generation <= beforeGen || p.ready == nil {
		t.Fatalf("carrier detach lost identity or queued reinjection: active=%v path=%v gen=%d", failed.active, p.path, p.generation)
	}
	if s.fastReady.Len() != 0 {
		t.Fatal("reinjecting first DATA reacquired priority")
	}
	s.dispatchLocked(time.Now())
	if p.path != backup || len(backup.queue) != 1 || len(backup.priorityDATA) != 0 || p.f.id != id {
		t.Fatalf("reinject failed: path=%v queue=%d priority=%d id=%d", p.path, len(backup.queue), len(backup.priorityDATA), p.f.id)
	}
	// A stale old priority channel task may exist, but must have an obsolete
	// generation and the old writer is already stopped. It cannot be reused.
	stale := <-failed.priorityDATA
	if stale.generation == p.generation {
		t.Fatal("stale priority task shares current generation")
	}
	s.removePendingLocked(p)
	if len(s.pending) != 0 || backup.outstanding != 0 || backup.queued != 0 {
		t.Fatal("reinject accounting not released")
	}
}

// Cancellation of a priority DATA that has not reached Socket writer must
// refund at most its originally charged token; duplicate cancel is harmless.
func TestWO01B1CancelQueuedPriorityReturnsTokenOnlyOnce(t *testing.T) {
	s, c := wo01B1Session()
	s.streams[1] = &Stream{s: s, id: 1}
	p := s.queueLocked(frame{kind: kindData, stream: 1, offset: 0, data: make([]byte, MaxPayload)})
	if p == nil {
		t.Fatal("queue first DATA failed")
	}
	s.dispatchLocked(time.Now())
	if !p.firstDataTokenCharged || len(c.priorityDATA) != 1 {
		t.Fatal("first priority frame not charged")
	}
	remaining := s.firstDataTokens
	if remaining >= firstDataTokenCapNormal {
		t.Fatalf("unbounded grant after dispatch %d", remaining)
	}
	s.removePendingLocked(p)
	if s.firstDataTokens != firstDataTokenCapNormal || p.firstDataTokenCharged || c.outstanding != 0 || c.queued != 0 {
		t.Fatalf("cancel leak or wrong refund tokens=%d outstanding=%d queued=%d", s.firstDataTokens, c.outstanding, c.queued)
	}
	s.removePendingLocked(p)
	if s.firstDataTokens != firstDataTokenCapNormal || len(s.pending) != 0 || s.fastReady.Len() != 0 {
		t.Fatal("duplicate cancel changed token reserve or ready ring")
	}
}
