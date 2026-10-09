package multipath

import (
	"testing"
	"time"
)

func setupV114QueueWaiter(t *testing.T, s *Session, id uint64, peerRoom int) (*Stream, <-chan struct{}) {
	t.Helper()
	st := rev2Stream(s, id)
	st.txNext = StreamWindow
	st.peerLimit = st.txNext + uint64(peerRoom)
	st.writeRemaining = MaxPayload
	cleanup := attachPendingWriter(s, st)
	t.Cleanup(cleanup)
	ch := s.beginWriterWaitLocked(st, waitQueueAdmission)
	t.Cleanup(func() { s.endWriterWaitLocked(st) })
	return st, ch
}

func TestV114QueueAdmissionWakesPartialStreamCredit(t *testing.T) {
	s, _ := rev2Fixture()
	s.queueAdmissionLimit = 32 << 20
	st := rev2Stream(s, 23)
	st.txNext = StreamWindow
	st.peerLimit = st.txNext + 8<<10
	st.writeRemaining = MaxPayload
	ceiling := s.queueAdmissionCeilingLocked(st)
	s.readyDataBytes = ceiling - (4 << 10) - 64
	if n, r := st.writeAllowanceLocked(); n != 0 || r != waitQueueAdmission {
		t.Fatalf("expected initial queue block: %d/%d", n, r)
	}
	cleanup := attachPendingWriter(s, st)
	defer cleanup()
	ch := s.beginWriterWaitLocked(st, waitQueueAdmission)
	defer s.endWriterWaitLocked(st)
	s.readyDataBytes -= 8 << 10
	if n, r := st.writeAllowanceLocked(); n != 8<<10 || r != waitNone {
		t.Fatalf("8KiB peer credit is admissible: %d/%d", n, r)
	}
	s.signalQueueAdmissionLocked(8 << 10)
	select {
	case <-ch:
	default:
		t.Fatal("admissible partial-credit writer remained asleep")
	}
}

func TestV114QueueAdmissionWakesNegotiatedSmallFrame(t *testing.T) {
	s, _ := rev2Fixture()
	s.queueAdmissionLimit = 32 << 20
	s.peerMaxFrame = 1024
	st := rev2Stream(s, 25)
	st.txNext = StreamWindow
	st.peerLimit = st.txNext + MaxStreamWindow/2
	st.writeRemaining = MaxPayload
	ceiling := s.queueAdmissionCeilingLocked(st)
	s.readyDataBytes = ceiling - 512 - 64
	if n, r := st.writeAllowanceLocked(); n != 0 || r != waitQueueAdmission {
		t.Fatalf("expected initial queue block: %d/%d", n, r)
	}
	cleanup := attachPendingWriter(s, st)
	defer cleanup()
	ch := s.beginWriterWaitLocked(st, waitQueueAdmission)
	defer s.endWriterWaitLocked(st)
	s.readyDataBytes -= 2048
	if n, r := st.writeAllowanceLocked(); n != 1024 || r != waitNone {
		t.Fatalf("1KiB frame should fit now: %d/%d", n, r)
	}
	s.signalQueueAdmissionLocked(2048)
	select {
	case <-ch:
	default:
		t.Fatal("admissible 1KiB peer-frame writer remained asleep")
	}
}

func TestV114QueueAdmissionWakeUsesVirtualRoom(t *testing.T) {
	s, _ := rev2Fixture()
	s.queueAdmissionLimit = 32 << 20
	s.peerMaxFrame = 1024
	s.readyDataBytes = s.queueAdmissionLimit - queueAdmissionBootstrapReserve - 32*1024 - 128
	var channels []<-chan struct{}
	for i := 0; i < 40; i++ {
		_, ch := setupV114QueueWaiter(t, s, uint64(101+2*i), MaxStreamWindow/2)
		channels = append(channels, ch)
	}
	s.signalQueueAdmissionLocked(32 << 10)
	woke := 0
	for _, ch := range channels {
		select {
		case <-ch:
			woke++
		default:
		}
	}
	// A bounded 32-writer batch is allowed. The available room should be
	// shared by multiple 1KiB writers rather than notifying exactly one.
	if woke < 15 || woke > queueAdmissionWakeBatch {
		t.Fatalf("expected multiple bounded small-frame wakes; got %d", woke)
	}
	_ = time.Now()
}

func TestV114SplitOpenAndCreditReasonsAndLegacyAlias(t *testing.T) {
	s, _ := rev2Fixture()
	st := rev2Stream(s, 7)
	st.writeRemaining = MaxPayload
	st.open = false
	if n, r := st.writeAllowanceLocked(); n != 0 || r != waitStreamOpen {
		t.Fatalf("pre-open DATA should be stream_open: %d/%d", n, r)
	}
	st.open = true
	st.peerLimit = st.txNext
	if n, r := st.writeAllowanceLocked(); n != 0 || r != waitStreamWindow {
		t.Fatalf("exhausted Stream WINDOW should be stream_window: %d/%d", n, r)
	}
	s.credit.waits[waitStreamOpen] = creditWait{Count: 7, NS: 7000}
	s.credit.waits[waitStreamWindow] = creditWait{Count: 5, NS: 5000}
	reasons := s.sharedCreditSnapshotLocked().WriteWaits
	if reasons["stream_open"].Count != 7 || reasons["stream_window"].Count != 5 {
		t.Fatalf("missing disjoint fields: %#v", reasons)
	}
	if legacy := reasons["stream_window_or_open"]; legacy.Count != 12 || legacy.NS != 12000 {
		t.Fatalf("legacy combined alias changed semantics: %#v", legacy)
	}
}
