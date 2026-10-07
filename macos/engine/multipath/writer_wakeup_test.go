package multipath

import (
	"context"
	"testing"
	"time"
)

func attachWriterWaiter(s *Session, st *Stream, reason int) func() {
	st.writeRemaining = MaxPayload
	st.writeEntry = s.writerReady.PushBack(st)
	s.beginWriterWaitLocked(st, reason)
	return func() {
		s.endWriterWaitLocked(st)
		if st.writeEntry != nil {
			s.writerReady.Remove(st.writeEntry)
			st.writeEntry = nil
		}
		st.writeRemaining = 0
	}
}

func signaledWriterCount(streams []*Stream) int {
	n := 0
	for _, st := range streams {
		if st != nil && st.writeWake != nil && len(st.writeWake) != 0 {
			n++
		}
	}
	return n
}

func TestSharedCreditReleaseSignalsOnlyReleasedFrameBudget(t *testing.T) {
	s, _ := rev2Fixture()
	source := rev2Stream(s, 1)
	source.commitSendCreditLocked(256 << 10)

	waiters := make([]*Stream, 0, 8)
	cleanups := make([]func(), 0, 8)
	for i := 0; i < 8; i++ {
		st := rev2Stream(s, uint64(2*i+3))
		waiters = append(waiters, st)
		cleanups = append(cleanups, attachWriterWaiter(s, st, waitGrowth))
	}
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()

	global := s.changed
	if err := source.releaseSendCreditLocked(128 << 10); err != nil {
		t.Fatal(err)
	}
	if got, want := signaledWriterCount(waiters), 4; got != want {
		t.Fatalf("128 KiB consumption signaled %d writers, want %d", got, want)
	}
	select {
	case <-global:
		t.Fatal("shared credit release broadcast global changed channel")
	default:
	}
}

func TestSessionWindowSignalsOnlyNewCommitBudget(t *testing.T) {
	s, _ := rev2Fixture()
	s.credit.txCommitted = 1 << 20
	s.credit.peerLimit = 1 << 20

	waiters := make([]*Stream, 0, 8)
	cleanups := make([]func(), 0, 8)
	for i := 0; i < 8; i++ {
		st := rev2Stream(s, uint64(2*i+1))
		waiters = append(waiters, st)
		cleanups = append(cleanups, attachWriterWaiter(s, st, waitSessionWindow))
	}
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()

	global := s.changed
	if err := s.receiveSessionCreditLocked(frame{kind: kindSessionWindow, offset: 0, id: (1 << 20) + (128 << 10)}); err != nil {
		t.Fatal(err)
	}
	if got, want := signaledWriterCount(waiters), 4; got != want {
		t.Fatalf("128 KiB SESSION_WINDOW signaled %d writers, want %d", got, want)
	}
	select {
	case <-global:
		t.Fatal("SESSION_WINDOW broadcast global changed channel")
	default:
	}
}

func TestStreamWindowTargetsOnlyAddressedWriter(t *testing.T) {
	s, c := rev2Fixture()
	a := rev2Stream(s, 1)
	b := rev2Stream(s, 3)
	a.peerLimit = 0
	b.peerLimit = 0
	cleanupA := attachWriterWaiter(s, a, waitStreamWindow)
	defer cleanupA()
	cleanupB := attachWriterWaiter(s, b, waitStreamWindow)
	defer cleanupB()

	global := s.changed
	if err := s.handleFrame(c, frame{kind: kindWindow, stream: a.id, offset: 0, id: OpenBootstrapWindow}); err != nil {
		t.Fatal(err)
	}
	if len(a.writeWake) != 1 {
		t.Fatal("addressed Stream writer was not signaled")
	}
	if len(b.writeWake) != 0 {
		t.Fatal("unrelated Stream writer was signaled")
	}
	select {
	case <-global:
		t.Fatal("Stream WINDOW broadcast global changed channel")
	default:
	}
}

func TestOpenOKTargetsOnlyOpeningWriter(t *testing.T) {
	s, c := rev2Fixture()
	a := s.newStreamLocked(1)
	b := s.newStreamLocked(3)
	p := s.queueLocked(frame{kind: kindOpen, stream: a.id})
	if p == nil {
		t.Fatal("failed to queue OPEN")
	}
	a.openID = p.f.id
	cleanupA := attachWriterWaiter(s, a, waitStreamWindow)
	defer cleanupA()
	cleanupB := attachWriterWaiter(s, b, waitStreamWindow)
	defer cleanupB()

	global := s.changed
	if err := s.ackLocked(c, frame{kind: kindOpenOK, stream: a.id, id: p.f.id}); err != nil {
		t.Fatal(err)
	}
	if !a.open {
		t.Fatal("OPEN_OK did not open Stream")
	}
	if len(a.openWake) != 1 {
		t.Fatal("OPEN_OK did not signal the Stream Open waiter")
	}
	if len(b.openWake) != 0 {
		t.Fatal("OPEN_OK signaled an unrelated Stream Open waiter")
	}
	if len(a.writeWake) != 1 {
		t.Fatal("opening Stream writer was not signaled")
	}
	if len(b.writeWake) != 0 {
		t.Fatal("unrelated opening writer was signaled")
	}
	select {
	case <-global:
		t.Fatal("OPEN_OK broadcast global changed channel")
	default:
	}
}

func TestIncomingDATATargetsOnlyAddressedReader(t *testing.T) {
	s, c := rev2Fixture()
	a := rev2Stream(s, 1)
	b := rev2Stream(s, 3)
	global := s.changed

	if err := s.handleFrame(c, frame{kind: kindData, stream: a.id, id: 1, offset: 0, data: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if len(a.readWake) != 1 {
		t.Fatal("addressed Stream reader was not signaled")
	}
	if len(b.readWake) != 0 {
		t.Fatal("unrelated Stream reader was signaled")
	}
	select {
	case <-global:
		t.Fatal("incoming DATA broadcast global changed channel")
	default:
	}
}

func TestOpenWaitCompletesFromTargetedOpenSignal(t *testing.T) {
	s, c := rev2Fixture()
	// schedulerFixture is a minimal scheduler-only fixture and leaves the
	// client Stream-ID allocator at zero; a real Session starts at Stream 1.
	s.nextStream = 1
	type result struct {
		st  *Stream
		err error
	}
	done := make(chan result, 1)
	go func() {
		st, err := s.Open(context.Background())
		done <- result{st: st, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	var p *outbound
	var global <-chan struct{}
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, candidate := range s.pending {
			if candidate.f.kind == kindOpen {
				p = candidate
				break
			}
		}
		if p != nil {
			global = s.changed
			err := s.ackLocked(c, frame{kind: kindOpenOK, stream: p.f.stream, id: p.f.id})
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		s.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if p == nil {
		t.Fatal("Open did not enqueue STREAM_OPEN")
	}
	select {
	case <-global:
		t.Fatal("OPEN_OK used global broadcast instead of targeted Open signal")
	default:
	}
	select {
	case got := <-done:
		if got.err != nil || got.st == nil || !got.st.open {
			t.Fatalf("Open result: stream=%v err=%v", got.st, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Open remained blocked after targeted OPEN_OK signal")
	}
}

func TestWriterTurnSignalsBoundedFIFOBatch(t *testing.T) {
	s, _ := rev2Fixture()
	waiters := make([]*Stream, 0, writerTurnBatch+2)
	cleanups := make([]func(), 0, writerTurnBatch+2)
	for i := 0; i < writerTurnBatch+2; i++ {
		st := rev2Stream(s, uint64(2*i+1))
		waiters = append(waiters, st)
		cleanups = append(cleanups, attachWriterWaiter(s, st, waitWriterTurn))
	}
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()

	s.signalWriterTurnLocked()
	for i, st := range waiters {
		want := 0
		if i < writerTurnBatch {
			want = 1
		}
		if got := len(st.writeWake); got != want {
			t.Fatalf("writer %d signal=%d want=%d", i, got, want)
		}
	}
}

func TestWriterTurnAllowsOnlyFirstBoundedBatchWhenCreditScarce(t *testing.T) {
	s, _ := rev2Fixture()
	waiters := make([]*Stream, 0, writerTurnBatch+2)
	for i := 0; i < writerTurnBatch+2; i++ {
		st := rev2Stream(s, uint64(2*i+1))
		st.txNext = StreamWindow
		st.writeRemaining = MaxPayload
		st.writeEntry = s.writerReady.PushBack(st)
		waiters = append(waiters, st)
		defer func(st *Stream) {
			s.writerReady.Remove(st.writeEntry)
			st.writeEntry = nil
		}(st)
	}

	room := writerTurnBatch * MaxPayload
	s.credit.txUsed = SessionCreditLimit - room
	s.credit.txGrowth = s.credit.txUsed
	s.credit.txCommitted = uint64(SessionCreditLimit - room)
	s.credit.peerLimit = SessionCreditLimit

	for i, st := range waiters {
		got := s.writerTurnLocked(st)
		want := i < writerTurnBatch
		if got != want {
			t.Fatalf("writer %d turn=%v want=%v", i, got, want)
		}
	}
}
