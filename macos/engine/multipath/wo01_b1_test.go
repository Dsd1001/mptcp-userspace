package multipath

import (
	"testing"
	"time"
)

func wo01B1Session() (*Session, *carrier) {
	s := schedulerFixture()
	s.initScheduler(SchedulerWeighted)
	c := schedulerPath(1)
	c.budget = maxPathBudget
	s.paths[1] = c
	return s, c
}

func TestWO01B1Synchronous150SharedBudgetAndCancellationCleanup(t *testing.T) {
	s, c := wo01B1Session()
	for i := 0; i < 150; i++ {
		id := uint64(1 + i*2)
		s.streams[id] = &Stream{s: s, id: id}
		if p := s.queueLocked(frame{kind: kindData, stream: id, offset: 0, data: make([]byte, MaxPayload)}); p == nil {
			t.Fatalf("queue %d failed", i)
		}
	}
	if s.firstDataTokenCapLocked() != firstDataTokenCapBusy {
		t.Fatal("unbounded concurrent OPEN burst")
	}
	if s.fastReady.Len() != 150 {
		t.Fatalf("fast ring len=%d", s.fastReady.Len())
	}
	s.dispatchLocked(time.Now())
	if s.firstDataPromoted > 1 {
		t.Fatalf("first DATA storm promoted=%d", s.firstDataPromoted)
	}
	if s.firstDataTokens < 0 || s.firstDataTokens > firstDataTokenCapBusy {
		t.Fatalf("shared tokens out of bound %d", s.firstDataTokens)
	}
	// Cancel all unsent data; neither the normal nor fast ring retains any
	// stale stream, and queued-but-unsent priority grants are reclaimed.
	for _, p := range s.pending {
		s.removePendingLocked(p)
	}
	if s.fastReady.Len() != 0 || s.readyStreams.Len() != 0 || s.readyFrames != 0 || len(s.pending) != 0 {
		t.Fatalf("leaked ready queues: fast=%d fair=%d ready=%d pending=%d",
			s.fastReady.Len(), s.readyStreams.Len(), s.readyFrames, len(s.pending))
	}
	if s.firstDataTokens < 0 || s.firstDataTokens > firstDataTokenCapBusy {
		t.Fatalf("cancellation overflowed shared tokens %d", s.firstDataTokens)
	}
	_ = c
}

func TestWO01B1FairFallbackWhenNoPriorityTokens(t *testing.T) {
	s, c := wo01B1Session()
	s.streams[1] = &Stream{s: s, id: 1}
	s.firstDataTokensInitialized = true
	s.firstDataTokens = 0
	p := s.queueLocked(frame{kind: kindData, stream: 1, offset: 0, data: make([]byte, MaxPayload)})
	s.dispatchLocked(time.Now())
	if s.firstDataPromoted != 0 {
		t.Fatal("tokens exhausted but frame promoted")
	}
	if len(c.queue) != 1 || s.firstDataFairFallbacks == 0 {
		t.Fatal("empty tokens blocked normal fair DATA")
	}
	if p.ready != nil {
		t.Fatal("normal DATA still waiting after successful fair fallback")
	}
}
func TestWO01B1ACKBasedTokenRefillAndNoDuplicate(t *testing.T) {
	s, c := wo01B1Session()
	s.streams[1] = &Stream{s: s, id: 1}
	p := s.queueLocked(frame{kind: kindData, stream: 1, offset: 0, data: make([]byte, MaxPayload)})
	s.dispatchLocked(time.Now())
	if !p.firstDataTokenCharged || s.firstDataPromoted != 1 {
		t.Fatal("first frame never charged")
	}
	before := s.firstDataTokens
	p.attempts = 1
	p.sentAt = time.Now().Add(-20 * time.Millisecond)
	ack := frame{kind: kindACK, stream: 1, id: p.f.id}
	if err := s.ackLocked(c, ack); err != nil {
		t.Fatal(err)
	}
	expected := min(firstDataTokenCapNormal, before+max(1, p.cost/firstDataTokenRefillDiv))
	if s.firstDataTokens != expected {
		t.Fatalf("ACK tokens got=%d want=%d", s.firstDataTokens, expected)
	}
	if err := s.ackLocked(c, ack); err != nil {
		t.Fatal(err)
	}
	if s.firstDataTokens != expected {
		t.Fatal("duplicate ACK minted tokens")
	}
}
func TestWO01B1ReinjectNeverGetsAnotherPriority(t *testing.T) {
	s, c := wo01B1Session()
	s.streams[1] = &Stream{s: s, id: 1}
	p := s.queueLocked(frame{kind: kindData, stream: 1, offset: 0, data: make([]byte, MaxPayload)})
	s.dispatchLocked(time.Now())
	if s.fastReady.Len() != 0 || p.generation == 0 {
		t.Fatal("original first DATA still in priority ring")
	}
	// Real detach detaches the previous transmission and calls readyLocked
	// with a higher generation. Priority must not be reacquired.
	p.path = nil
	s.readyLocked(p, true)
	if s.fastReady.Len() != 0 {
		t.Fatal("retransmission reacquired priority")
	}
	s.removePendingLocked(p)
	if s.fastReady.Len() != 0 || len(s.pending) != 0 {
		t.Fatal("retransmission cancellation leaked")
	}
	_ = c
}
func TestWO01B1OtherModesNeverPromote(t *testing.T) {
	for _, mode := range []SchedulerMode{SchedulerAggregate, SchedulerProtect, SchedulerAuto} {
		s := schedulerFixture()
		s.initScheduler(mode)
		s.streams[1] = &Stream{s: s, id: 1}
		s.queueLocked(frame{kind: kindData, stream: 1, offset: 0, data: []byte{42}})
		if s.fastReady.Len() != 0 || s.firstDataPromoted != 0 {
			t.Fatalf("mode %s unexpectedly joined Weighted fast lane", mode)
		}
	}
}
