package multipath

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestWeightedGenericControlAvoidsBlockedSocketWriter(t *testing.T) {
	s, stalled, ready := modeFixture(SchedulerWeighted)
	stalled.writeStartedNS.Store(time.Now().Add(-2 * time.Second).UnixNano())
	if !s.controlWriterStalled(stalled, time.Now()) {
		t.Fatal("missed blocked writer")
	}
	if s.controlWriterStalled(ready, time.Now()) {
		t.Fatal("idle writer appears blocked")
	}

	if !s.controlLocked(nil, frame{kind: kindWindow, stream: 1, id: StreamWindow}) {
		t.Fatal("no control carrier")
	}
	if len(stalled.control) != 0 || len(ready.control) != 1 {
		t.Fatalf("WINDOW was queued behind blocked DATA: stalled=%d ready=%d", len(stalled.control), len(ready.control))
	}
	open := s.queueLocked(frame{kind: kindOpen, stream: 1})
	s.dispatchControlsLocked()
	if open.path != ready || open.ready != nil {
		t.Fatal("reliable OPEN queued on blocked Carrier")
	}

	// An authenticated DATA ACK carries a per-Carrier delivery timestamp.
	// It must not be silently switched to another path for route preference.
	if !s.controlLocked(stalled, frame{kind: kindACK, stream: 1, id: 17}) {
		t.Fatal("directed ACK failed")
	}
	if len(stalled.control) != 1 || len(ready.control) != 1 {
		t.Fatal("directed ACK was moved off its active owning Carrier")
	}

	stalled.writeStartedNS.Store(0)
	if !s.controlLocked(nil, frame{kind: kindWindow, stream: 3, id: StreamWindow}) {
		t.Fatal("no destination after writer recovered")
	}
	if len(stalled.control) != 2 {
		t.Fatal("healthy recovered carrier is wrongly excluded")
	}
}

func TestWeightedControlStillHasFallbackWhenEveryWriterStalled(t *testing.T) {
	s, first, second := modeFixture(SchedulerWeighted)
	ago := time.Now().Add(-2 * time.Second).UnixNano()
	first.writeStartedNS.Store(ago)
	second.writeStartedNS.Store(ago)
	if !s.controlLocked(nil, frame{kind: kindWindow, stream: 1, id: StreamWindow}) {
		t.Fatal("must not drop generic control when every writer is busy")
	}
	if len(first.control)+len(second.control) != 1 {
		t.Fatal("no control fallback")
	}
	open := s.queueLocked(frame{kind: kindOpen, stream: 1})
	s.dispatchControlsLocked()
	if open.path == nil || open.ready != nil {
		t.Fatal("reliable OPEN starved when all writers busy")
	}
}

func TestControlWriteStallHintDoesNotChangeOtherSchedulerModes(t *testing.T) {
	for _, mode := range []SchedulerMode{SchedulerAuto, SchedulerAggregate, SchedulerProtect} {
		t.Run(string(mode), func(t *testing.T) {
			s, first, second := modeFixture(mode)
			if mode == SchedulerProtect {
				s.setSchedulerRoleLocked(second, RoleBackup, "test", time.Now())
			}
			first.writeStartedNS.Store(time.Now().Add(-2 * time.Second).UnixNano())
			if s.controlWriterStalled(first, time.Now()) {
				t.Fatal("Weighted-only hint unexpectedly applies to another scheduler")
			}
			if !s.controlLocked(nil, frame{kind: kindWindow, stream: 1, id: StreamWindow}) {
				t.Fatal("control unrouteable")
			}
			if len(first.control) != 1 || len(second.control) != 0 {
				t.Fatal("existing non-Weighted control routing changed")
			}
		})
	}
}

func TestCarrierWriteStallTimestampFollowsActualEncryptedIO(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	key, e := ParseKey(testToken)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := newSecure(a, key, []byte("carrier-stalled-control-test"), true)
	if e != nil {
		t.Fatal(e)
	}
	rx, e := newSecure(b, key, []byte("carrier-stalled-control-test"), false)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := schedulerFixture()
	s.ctx = ctx
	s.initScheduler(SchedulerWeighted)
	c := schedulerPath(1)
	c.conn = tx
	c.done = make(chan struct{})
	c.control <- frame{kind: kindPing, offset: 1}
	ended := make(chan struct{})
	go func() { s.writeCarrier(c); close(ended) }()
	defer func() { cancel(); _ = a.Close(); _ = b.Close(); <-ended }()

	deadline := time.Now().Add(2 * time.Second)
	for c.writeStartedNS.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("writer failed to advertise blocked encrypted IO")
		}
		time.Sleep(time.Millisecond)
	}
	if !s.controlWriterStalled(c, time.Now().Add(150*time.Millisecond)) {
		t.Fatal("generic controls would not avoid a Socket blocked 150ms")
	}
	_ = b.SetReadDeadline(deadline)
	f, e := rx.readFrame()
	if e != nil || f.kind != kindPing {
		t.Fatalf("encrypted data did not emerge: %v", e)
	}
	for c.writeStartedNS.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("writer stall indicator leaked after successful write")
		}
		time.Sleep(time.Millisecond)
	}
}
