package multipath

import "testing"

func attachPendingWriter(s *Session, st *Stream) func() {
	st.writeRemaining = MaxPayload
	st.writeEntry = s.writerReady.PushBack(st)
	return func() {
		if st.writeEntry != nil {
			s.writerReady.Remove(st.writeEntry)
			st.writeEntry = nil
		}
		st.writeRemaining = 0
	}
}

func TestPendingReserveTracksActiveBootstrapWriters(t *testing.T) {
	s := schedulerFixture()
	for id := uint64(1); len(s.streams) < MaxStreams; id += 2 {
		st := s.newStreamLocked(id)
		st.open = true
	}
	if frames, bytes := s.pendingBootstrapReserveLocked(nil); frames != 0 || bytes != 0 {
		t.Fatalf("idle Streams reserved pending storage: frames=%d bytes=%d", frames, bytes)
	}

	a := s.streams[1]
	b := s.streams[3]
	cleanupA := attachPendingWriter(s, a)
	defer cleanupA()
	cleanupB := attachPendingWriter(s, b)
	defer cleanupB()

	if frames, bytes := s.pendingBootstrapReserveLocked(nil); frames != 2 || bytes != 2*(MaxPayload+64) {
		t.Fatalf("dynamic reserve mismatch: frames=%d bytes=%d", frames, bytes)
	}
	a.txNext = StreamWindow
	if frames, bytes := s.pendingBootstrapReserveLocked(nil); frames != 1 || bytes != MaxPayload+64 {
		t.Fatalf("completed bootstrap still reserved storage: frames=%d bytes=%d", frames, bytes)
	}
}

func TestGrowthUsesSharedPendingPoolAndLeavesDynamicBootstrapRoom(t *testing.T) {
	s, _ := rev2Fixture()
	growth := rev2Stream(s, 1)
	growth.txNext = StreamWindow
	growth.peerConsumed = 0
	s.credit.txCommitted = StreamWindow
	s.credit.txUsed = StreamWindow
	growthCleanup := attachPendingWriter(s, growth)
	defer growthCleanup()

	// The old static bootstrap partition stopped growth around 64 MiB.
	// With no active bootstrap writer, growth may use the shared 1 GiB pool.
	s.dataPendingBytes = 128 << 20
	s.dataPendingFrames = 4096
	if n, reason := growth.writeAllowanceLocked(); n != MaxPayload || reason != waitNone {
		t.Fatalf("shared pending pool still has old static ceiling: n=%d reason=%d", n, reason)
	}

	bootstrap := rev2Stream(s, 3)
	bootstrapCleanup := attachPendingWriter(s, bootstrap)
	defer bootstrapCleanup()

	// Leave exactly one physical DATA frame of hard room. Growth must preserve
	// it for the active bootstrap writer, while that bootstrap writer may use it.
	s.dataPendingBytes = MaxDataPendingBytes - (MaxPayload + 64)
	s.dataPendingFrames = 0
	if n, reason := growth.writeAllowanceLocked(); n != 0 || reason != waitPendingBytes {
		t.Fatalf("growth consumed dynamic bootstrap reserve: n=%d reason=%d", n, reason)
	}
	if n, reason := bootstrap.writeAllowanceLocked(); n != MaxPayload || reason != waitNone {
		t.Fatalf("bootstrap could not consume reserved pending room: n=%d reason=%d", n, reason)
	}
}

func TestDataACKSignalsOnePendingWriterWithoutGlobalBroadcast(t *testing.T) {
	s, c := rev2Fixture()
	st := rev2Stream(s, 1)
	p := s.queueLocked(frame{kind: kindData, stream: st.id, data: make([]byte, MaxPayload)})
	if p == nil {
		t.Fatal("failed to queue DATA")
	}
	p.path = c
	c.outstanding = p.cost

	waiter := rev2Stream(s, 3)
	cleanup := attachPendingWriter(s, waiter)
	defer cleanup()
	global := s.changed
	pending := s.beginWriterWaitLocked(waiter, waitPendingBytes)
	if err := s.ackLocked(c, frame{kind: kindACK, stream: st.id, id: p.f.id}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-global:
		t.Fatal("ordinary DATA ACK broadcast global changed channel")
	default:
	}
	select {
	case <-pending:
	default:
		t.Fatal("DATA ACK did not release one pending writer")
	}
	s.endWriterWaitLocked(waiter)
}
