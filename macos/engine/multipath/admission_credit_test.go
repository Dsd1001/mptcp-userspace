package multipath

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewStreamHasNoImplicitCredit(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	st := s.newStreamLocked(1)
	defer st.Close()
	if st.rxLimit != 0 || st.peerLimit != 0 || s.receiveCredit != 0 || s.receiveAllocated != 0 {
		t.Fatal("OPEN implied credit or pages")
	}
	st.open = true
	st.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	if n, e := st.Write([]byte{1}); n != 0 || e == nil {
		t.Fatal("zero WINDOW did not backpressure DATA")
	}
	c := schedulerPath(1)
	s.paths[1] = c
	if e := s.handleFrame(c, frame{kind: kindWindow, stream: 1, id: 0, offset: 0}); e != nil {
		t.Fatal(e)
	}
	if st.peerLimit != 0 {
		t.Fatal("zero WINDOW invented permission")
	}
	if e := s.handleFrame(c, frame{kind: kindWindow, stream: 1, id: StreamWindow}); e != nil {
		t.Fatal(e)
	}
	st.SetWriteDeadline(time.Now().Add(time.Second))
	if n, e := st.Write([]byte{1}); n != 1 || e != nil {
		t.Fatal(n, e)
	}
	if s.Snapshot().Resources.WindowBlockedWriters != 0 {
		t.Fatal("blocked writer count leaked")
	}
}

func TestAdmissionIgnoresReceiveCreditAndDataLedgers(t *testing.T) {
	for _, reason := range []string{LimitReceiveCredit, LimitPendingFrames, LimitPendingBytes} {
		t.Run(reason, func(t *testing.T) {
			s := schedulerFixture()
			s.ctx = context.Background()
			s.nextStream = 1
			// Isolate an adversarial ledger state from the actual network dispatcher.
			if reason == LimitReceiveCredit {
				s.receiveCredit = SessionCreditLimit
				s.receiveGrowth = GrowthCreditLimit
			}
			if reason == LimitPendingFrames {
				s.dataPendingFrames = MaxDataPending
			}
			if reason == LimitPendingBytes {
				s.dataPendingBytes = MaxDataPendingBytes
			}
			if r := s.openBlockReasonLocked(); r != "" {
				t.Fatalf("OPEN depended on %s: %s", reason, r)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				st, e := s.Open(ctx)
				if st != nil {
					st.Close()
				}
				done <- e
			}()
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				s.mu.Lock()
				var opening *outbound
				for _, p := range s.pending {
					if p.f.kind == kindOpen {
						opening = p
						break
					}
				}
				if opening != nil {
					s.ackLocked(schedulerPath(1), frame{kind: kindOpenOK, stream: opening.f.stream, id: opening.f.id})
					s.mu.Unlock()
					break
				}
				s.mu.Unlock()
				time.Sleep(time.Millisecond)
			}
			if e := <-done; e != nil {
				t.Fatal(e)
			}
			if s.Snapshot().Resources.OpenReceiveCreditWaits != 0 || s.resources.Waits[LimitReceiveCredit] != 0 {
				t.Fatal("credit leaked back into admission")
			}
		})
	}
}

func TestOpenWhileCreditFullPreservesSixCarriers(t *testing.T) {
	s, srv, _, _ := testTCP(t, 6, 32<<20, 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Real existing transport, deliberately corrupt/full receive ledger only;
	// even this state cannot prevent identity OPEN/OPEN_OK completing.
	s.mu.Lock()
	original, originalGrowth := s.receiveCredit, s.receiveGrowth
	s.receiveCredit = SessionCreditLimit
	s.receiveGrowth = GrowthCreditLimit
	s.mu.Unlock()
	before := srv.AdmissionSnapshot()
	st, e := s.Open(ctx)
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	rx := st.rxLimit
	s.receiveCredit = original
	s.receiveGrowth = originalGrowth
	st.advertiseCreditLocked(time.Now())
	s.mu.Unlock()
	if rx != StreamWindow {
		t.Fatal("explicit stream entitlement was tied to actual DATA usage")
	}
	defer st.Close()
	if _, e = st.Write([]byte("ok")); e != nil {
		t.Fatal(e)
	}
	got := make([]byte, 2)
	st.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = io.ReadFull(st, got); e != nil || string(got) != "ok" {
		t.Fatal(e, string(got))
	}
	after := srv.AdmissionSnapshot()
	if before.Accepted != after.Accepted || before.Rejected != after.Rejected || s.Snapshot().Paths != 6 || s.Snapshot().Lifecycle.Closed {
		t.Fatal("credit pressure restarted transport")
	}
}

func TestIncomingOpenDoesNotWaitForCreditAndCancels(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.server = true
	s.seen = make(map[uint64]bool)
	s.receiveCredit = SessionCreditLimit
	s.receiveGrowth = GrowthCreditLimit
	c := schedulerPath(1)
	s.paths[1] = c
	var backends atomic.Int64
	s.onOpen = func(st *Stream) { backends.Add(1) }
	f := frame{kind: kindOpen, stream: 1, id: 17}
	for i := 0; i < 2; i++ {
		if e := s.handleFrame(c, f); e != nil {
			t.Fatal(e)
		}
	}
	deadline := time.Now().Add(time.Second)
	for backends.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if backends.Load() != 1 || len(s.streams) != 1 || s.streams[1].rxLimit != 0 {
		t.Fatal("full credit delayed or duplicated identity OPEN")
	}
	if e := s.handleFrame(c, frame{kind: kindResetStream, stream: 1, offset: 0, id: 18, data: resetPayload(1)}); e != nil {
		t.Fatal(e)
	}
	if len(s.streams) != 0 {
		t.Fatal("cancelled stream retained")
	}
	if e := s.handleFrame(c, f); e != nil {
		t.Fatal(e)
	}
	if len(s.streams) != 0 || backends.Load() != 1 {
		t.Fatal("cancelled OPEN resurrected backend")
	}
	// RST can overtake OPEN on another carrier.
	if e := s.handleFrame(c, frame{kind: kindResetStream, stream: 3, offset: 0, id: 19, data: resetPayload(1)}); e != nil {
		t.Fatal(e)
	}
	if e := s.handleFrame(c, frame{kind: kindOpen, stream: 3, id: 20}); e != nil {
		t.Fatal(e)
	}
	if len(s.streams) != 0 {
		t.Fatal("RST-before-OPEN resurrected stream")
	}
}

func TestTrueStreamAndLedgerBoundsRemainTyped(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.nextStream = 1
	for id := uint64(1); len(s.streams) < MaxStreams; id += 2 {
		s.newStreamLocked(id)
	}
	if _, e := s.Open(context.Background()); ResourceReason(e) != LimitStreams || !errors.Is(e, ErrResourceLimit) {
		t.Fatal(e)
	}
	if s.receiveCredit != 0 {
		t.Fatal("idle identities allocated implicit credit")
	}
	for _, st := range s.streams {
		s.resetLocked(st, 1, false)
	}
	for i := 0; i < MaxDataPending; i++ {
		if s.queueLocked(frame{kind: kindData, stream: 1, data: []byte{1}}) == nil {
			t.Fatal("early DATA refusal")
		}
	}
	if s.queueLocked(frame{kind: kindData, stream: 1, data: []byte{1}}) != nil || s.resources.LastReason != LimitPendingFrames {
		t.Fatal("DATA frame bound missing")
	}
	// A completely full DATA ledger cannot consume reliable control capacity.
	for i := 0; i < MaxControlPending; i++ {
		if s.queueLocked(frame{kind: kindRST, stream: 1, offset: 1}) == nil {
			t.Fatal("DATA blocked control")
		}
	}
	if s.queueLocked(frame{kind: kindOpen, stream: 3}) != nil || s.resources.LastReason != LimitControlFrames {
		t.Fatal("control bound missing")
	}
	for _, p := range s.pending {
		s.removePendingLocked(p)
	}
	if s.dataPendingFrames != 0 || s.controlPendingFrames != 0 || s.pendingBytes != 0 || s.controlReady.Len() != 0 || len(s.ready) != 0 {
		t.Fatal("ledger release failed")
	}
	s.dataPendingBytes = MaxDataPendingBytes - 64
	if s.queueLocked(frame{kind: kindData, stream: 1, data: []byte{1}}) != nil || s.resources.LastReason != LimitPendingBytes {
		t.Fatal("DATA byte bound missing")
	}
	if s.queueLocked(frame{kind: kindOpen, stream: 3}) == nil {
		t.Fatal("DATA bytes blocked OPEN")
	}
}

func TestBootstrapGuaranteeAtMaxStreamsWithBulkGrowth(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.nextStream = 1
	a := s.newStreamLocked(1)
	a.open = true
	a.windowTarget = MaxStreamWindow
	a.advertiseCreditLocked(time.Now())
	b := s.newStreamLocked(3)
	b.open = true
	b.windowTarget = MaxStreamWindow
	b.advertiseCreditLocked(time.Now())
	if a.rxLimit < 15<<20 {
		t.Fatal("bulk window globally reduced")
	}
	for id := uint64(5); len(s.streams) < MaxStreams; id += 2 {
		if reason := s.openBlockReasonLocked(); reason != "" {
			t.Fatal(reason)
		}
		st := s.newStreamLocked(id)
		st.open = true
		st.advertiseCreditLocked(time.Now())
		if st.rxLimit != StreamWindow || st.peerLimit != 0 {
			t.Fatal("bootstrap missing or implicit send grant")
		}
	}
	r := s.Snapshot().Resources
	if r.BootstrapCredit != 0 || r.GrowthCredit != 0 || r.ReceiveCredit != 0 || r.AdmissionReserve != BootstrapCreditLimit || r.ReceiveAllocated != 0 {
		t.Fatalf("bootstrap accounting: %+v", r)
	}
	for _, st := range s.streams {
		s.resetLocked(st, 1, false)
	}
	if s.receiveCredit != 0 || s.receiveGrowth != 0 {
		t.Fatal("credit leaked")
	}
}

func TestControlDispatchBypassesFullDataFlight(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	c := schedulerPath(1)
	s.paths[1] = c
	c.outstanding = c.flightBudget()
	for i := 0; i < carrierQueue; i++ {
		c.queue <- sendTask{}
	}
	p := s.queueLocked(frame{kind: kindOpen, stream: 1})
	s.dispatchControlsLocked()
	select {
	case task := <-c.reliableControl:
		if task.p != p {
			t.Fatal("wrong control")
		}
	default:
		t.Fatal("full DATA flight starved OPEN")
	}
	if c.controlOutstanding != 64 || s.controlReady.Len() != 0 {
		t.Fatal("control flight accounting")
	}
	s.removePendingLocked(p)
	if c.controlOutstanding != 0 || c.controlQueued != 0 {
		t.Fatal("control flight leaked")
	}
}
