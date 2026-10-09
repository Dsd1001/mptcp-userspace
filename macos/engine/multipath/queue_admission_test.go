package multipath

import (
	"testing"
	"time"
)

func TestQueueAdmissionEnvOptIn(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want int
	}{
		{"", 32 << 20},
		{"0", 0},
		{"16", 16 << 20},
		{"32", 32 << 20},
		{"64", 64 << 20},
		{"65", 0},
		{"-1", 0},
		{"invalid", 0},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("MPX_QUEUE_ADMISSION_MIB", tc.env)
			if got := queueAdmissionLimitFromEnv(); got != tc.want {
				t.Fatalf("limit for %q = %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}

func TestQueueAdmissionBulkStopsBeforeSessionCreditAndBootstrapCanProceed(t *testing.T) {
	s, _ := rev2Fixture()
	s.queueAdmissionLimit = 32 << 20
	bulk := rev2Stream(s, 1)
	bulk.txNext = StreamWindow
	bulk.writeRemaining = MaxPayload
	s.credit.txCommitted = StreamWindow
	bootstrap := rev2Stream(s, 3)
	bootstrap.writeRemaining = MaxPayload
	s.readyDataBytes = s.queueAdmissionLimit - queueAdmissionBootstrapReserve

	if n, why := bulk.writeAllowanceLocked(); n != 0 || why != waitQueueAdmission {
		t.Fatalf("bulk admitted into a saturated queue: n=%d reason=%d", n, why)
	}
	if n, why := bootstrap.writeAllowanceLocked(); n != MaxPayload || why != waitNone {
		t.Fatalf("bootstrap Stream starved behind bulk queue: n=%d reason=%d", n, why)
	}
	if s.credit.txCommitted != StreamWindow || s.dataPendingBytes != 0 {
		t.Fatal("local queue admission mutated protocol credit or reliable pending state")
	}
	s.readyDataBytes = s.queueAdmissionLimit + queueAdmissionBootstrapReserve
	if n, why := bootstrap.writeAllowanceLocked(); n != 0 || why != waitQueueAdmission {
		t.Fatalf("bootstrap bypassed its bounded limit: n=%d reason=%d", n, why)
	}
	s.queueAdmissionLimit = 0
	if n, why := bulk.writeAllowanceLocked(); n != MaxPayload || why != waitNone {
		t.Fatalf("disabled admission did not restore normal v1.1.1 behavior: n=%d reason=%d", n, why)
	}
}

func TestQueueAdmissionTracksOnlyUnscheduledDataAndWakesTargetedWaiters(t *testing.T) {
	s, carrier := rev2Fixture()
	// A tiny test cap lets three ordinary DATA frames saturate the bulk band.
	one := MaxPayload + 64
	s.queueAdmissionLimit = 4 * one
	producer := rev2Stream(s, 1)
	var packets []*outbound
	for i := 0; i < 3; i++ {
		p := s.queueLocked(frame{kind: kindData, stream: producer.id, data: make([]byte, MaxPayload)})
		if p == nil {
			t.Fatal("failed to enqueue test DATA")
		}
		packets = append(packets, p)
	}
	if s.readyDataBytes != 3*one || s.pendingBytes != 3*one || s.readyFrames != 3 {
		t.Fatalf("queue accounting mismatch: ready=%d pending=%d frames=%d", s.readyDataBytes, s.pendingBytes, s.readyFrames)
	}

	waiter := rev2Stream(s, 3)
	waiter.txNext = StreamWindow
	cleanup := attachPendingWriter(s, waiter)
	defer cleanup()
	waitChannel := s.beginWriterWaitLocked(waiter, waitQueueAdmission)
	defer s.endWriterWaitLocked(waiter)
	globalChannel := s.changed
	if n, why := waiter.writeAllowanceLocked(); n != 0 || why != waitQueueAdmission {
		t.Fatalf("waiter was not queue limited: n=%d reason=%d", n, why)
	}
	s.dispatchLocked(time.Now())
	if s.readyDataBytes >= 3*one {
		t.Fatalf("Carrier dispatch did not free ready queue bytes: %d", s.readyDataBytes)
	}
	if s.pendingBytes != 3*one || len(s.pending) != 3 {
		t.Fatal("dispatch incorrectly released reliable DATA before ACK")
	}
	select {
	case <-waitChannel:
	default:
		t.Fatal("Carrier dispatch did not signal queue admission writer")
	}
	select {
	case <-globalChannel:
		t.Fatal("queue admission caused global writer thundering-herd broadcast")
	default:
	}
	if carrier.outstanding == 0 {
		t.Fatal("DATA was not assigned to the Carrier")
	}

	// A reinjected frame re-enters the ready queue without consuming another
	// logical Session credit or introducing a duplicate reliable pending entry.
	beforeCredit := s.credit.txCommitted
	beforePending := len(s.pending)
	p := packets[0]
	s.readyLocked(p, true)
	if s.readyDataBytes < one {
		t.Fatal("reinjected DATA not charged to unscheduled ready queue")
	}
	if s.credit.txCommitted != beforeCredit || len(s.pending) != beforePending {
		t.Fatal("reinject duplicated credit or reliable identity")
	}
	s.unreadyLocked(p)
	if s.credit.txCommitted != beforeCredit || len(s.pending) != beforePending {
		t.Fatal("unready operation modified credit or reliable identity")
	}
}

func TestQueueAdmissionCancellationWakesAndBalancesAccounting(t *testing.T) {
	s, _ := rev2Fixture()
	one := MaxPayload + 64
	s.queueAdmissionLimit = 4 * one
	producer := rev2Stream(s, 1)
	var packets []*outbound
	for i := 0; i < 3; i++ {
		p := s.queueLocked(frame{kind: kindData, stream: producer.id, data: make([]byte, MaxPayload)})
		if p == nil {
			t.Fatal("queue returned nil")
		}
		packets = append(packets, p)
	}
	waiter := rev2Stream(s, 3)
	waiter.txNext = StreamWindow
	cleanup := attachPendingWriter(s, waiter)
	defer cleanup()
	targeted := s.beginWriterWaitLocked(waiter, waitQueueAdmission)
	s.removePendingLocked(packets[0])
	select {
	case <-targeted:
	default:
		t.Fatal("removing unscheduled DATA did not wake blocked writer")
	}
	s.endWriterWaitLocked(waiter)
	if s.queueAdmissionWaiters != 0 || s.readyDataBytes != 2*one || len(s.pending) != 2 {
		t.Fatalf("cancellation accounting failed: waiters=%d ready=%d pending=%d", s.queueAdmissionWaiters, s.readyDataBytes, len(s.pending))
	}
}

func TestQueueAdmissionDoesNotBypassPeerSessionCredit(t *testing.T) {
	s, _ := rev2Fixture()
	s.queueAdmissionLimit = 32 << 20
	st := rev2Stream(s, 1)
	st.writeRemaining = MaxPayload
	s.credit.txCommitted = s.credit.peerLimit
	if n, why := st.writeAllowanceLocked(); n != 0 || why != waitSessionWindow {
		t.Fatalf("Session WINDOW bypassed by queue admission: n=%d reason=%d", n, why)
	}
}
