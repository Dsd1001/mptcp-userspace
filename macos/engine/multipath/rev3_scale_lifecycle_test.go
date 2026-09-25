package multipath

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRev4ScaleConstantsAndNextRefusal(t *testing.T) {
	if MaxStreams != 2048 || SessionCreditLimit != 128<<20 {
		t.Fatalf("unexpected scale: streams=%d credit=%d", MaxStreams, SessionCreditLimit)
	}
	if BootstrapCreditLimit != 32<<20 || GrowthCreditLimit != 96<<20 {
		t.Fatalf("unexpected pools: bootstrap=%d growth=%d", BootstrapCreditLimit, GrowthCreditLimit)
	}
	if MaxDataPending != 8192 || MaxDataPendingBytes != 128<<20 || MaxBuffered != 128<<20 {
		t.Fatalf("pending/physical limits not separated: pending=%d physical=%d", MaxDataPendingBytes, MaxBuffered)
	}
	s := schedulerFixture()
	s.ctx = context.Background()
	s.nextStream = 1
	for id := uint64(1); len(s.streams) < MaxStreams; id += 2 {
		st := s.newStreamLocked(id)
		st.open = true
	}
	if reason := s.openBlockReasonLocked(); reason != LimitStreams {
		t.Fatalf("%dth identity not refused by stream limit: %q", MaxStreams+1, reason)
	}
	if _, err := s.Open(context.Background()); !errors.Is(err, ErrResourceLimit) || ResourceReason(err) != LimitStreams {
		t.Fatalf("%dth refusal not typed: %v", MaxStreams+1, err)
	}
	r := s.Snapshot().Resources
	if r.StreamLimit != 2048 || r.ReceiveCreditLimit != 128<<20 || r.BootstrapLimit != 32<<20 || r.GrowthLimit != 96<<20 {
		t.Fatalf("telemetry limits mismatch: %+v", r)
	}
	if r.DataPendingLimit != 8192 || r.DataPendingByteLimit != 128<<20 || r.ReceiveAllocatedLimit != 128<<20 {
		t.Fatalf("pending/physical telemetry conflated: %+v", r)
	}
}

func TestLifecycleTelemetrySeparatesLocalSocketsFromMPXSlots(t *testing.T) {
	s := schedulerFixture()
	now := time.Now()
	mk := func(id uint64) *Stream {
		st := s.newStreamLocked(id)
		st.open = true
		return st
	}
	open := mk(1)
	open.createdAt = now.Add(-11 * time.Minute)
	open.lastActivity = now.Add(-11 * time.Minute)

	half := mk(3)
	half.hasFIN = true
	half.rxFIN = 0

	waitACK := mk(5)
	waitACK.writeFIN = true

	waitPeer := mk(7)
	waitPeer.writeFIN = true
	waitPeer.finACK = true

	both := mk(9)
	both.writeFIN = true
	both.finACK = true
	both.hasFIN = true
	both.rxFIN = 0

	waitConsumed := mk(11)
	waitConsumed.closed = true
	waitConsumed.writeFIN = true
	waitConsumed.finACK = true
	waitConsumed.hasFIN = true
	waitConsumed.rxFIN = 0
	delete(s.streams, waitConsumed.id)
	s.closing[waitConsumed.id] = waitConsumed
	s.pending[9001] = &outbound{f: frame{kind: kindFinalConsumed, stream: waitConsumed.id, id: 9001}, cost: 64}

	closingOther := mk(13)
	closingOther.closed = true
	closingOther.writeFIN = true
	closingOther.finACK = true
	closingOther.hasFIN = true
	closingOther.rxFIN = 0
	delete(s.streams, closingOther.id)
	s.closing[closingOther.id] = closingOther

	opening := s.newStreamLocked(15)
	opening.open = false

	s.NoteLocalConnection(2)
	s.NoteLocalConnection(-1)
	r := s.Snapshot().Resources

	if r.LocalConnections != 1 || r.OccupiedSlots != 8 || r.ActiveStreams != 6 || r.ClosingStreams != 2 {
		t.Fatalf("socket/slot accounting mismatch: %+v", r)
	}
	if r.LifecycleOpening != 1 || r.LifecycleOpen != 1 || r.LifecycleHalfClosed != 1 ||
		r.LifecycleWaitFinalACK != 1 || r.LifecycleWaitPeerFinal != 1 || r.LifecycleBothFinal != 1 ||
		r.LifecycleWaitConsumed != 1 || r.LifecycleClosingOther != 1 {
		t.Fatalf("lifecycle buckets mismatch: %+v", r)
	}
	accounted := r.LifecycleOpening + r.LifecycleOpen + r.LifecycleHalfClosed + r.LifecycleWaitFinalACK +
		r.LifecycleWaitPeerFinal + r.LifecycleBothFinal + r.LifecycleWaitConsumed + r.LifecycleClosingOther
	if accounted != r.OccupiedSlots {
		t.Fatalf("lifecycle buckets do not cover occupied slots: %d/%d", accounted, r.OccupiedSlots)
	}
	if r.DataIdleOver30s < 1 || r.DataIdleOver1m < 1 || r.DataIdleOver5m < 1 || r.DataIdleOver10m < 1 ||
		r.OldestDataIdleSeconds < 10*60 || r.OldestStreamAgeSeconds < 10*60 {
		t.Fatalf("idle-age telemetry missing: %+v", r)
	}
}
