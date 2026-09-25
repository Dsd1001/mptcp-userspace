package multipath

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func rev2Fixture() (*Session, *carrier) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.initCreditLocked()
	// Explicitly model receipt of the initial authenticated SESSION_WINDOW.
	s.credit.peerLimit = SessionCreditLimit
	c := schedulerPath(1)
	s.paths[1] = c
	return s, c
}

func rev2Stream(s *Session, id uint64) *Stream {
	st := s.newStreamLocked(id)
	st.open = true
	st.peerLimit = MaxStreamWindow
	st.windowTarget = MaxStreamWindow
	st.grantCreditLocked(MaxStreamWindow)
	return st
}

func growthSaturationStreamCount() int {
	perStream := MaxStreamWindow - StreamWindow
	return (GrowthCreditLimit + perStream - 1) / perStream
}

func saturateGrowthSend(t *testing.T, s *Session) []uint64 {
	t.Helper()
	n := growthSaturationStreamCount()
	ids := make([]uint64, n)
	base := GrowthCreditLimit / n
	extra := GrowthCreditLimit % n
	for i := range ids {
		ids[i] = uint64(2*i + 1)
		growth := base
		if i < extra {
			growth++
		}
		if growth+StreamWindow > MaxStreamWindow {
			t.Fatal("growth fixture exceeds per-stream window")
		}
		rev2Stream(s, ids[i]).commitSendCreditLocked(growth + StreamWindow)
	}
	if s.credit.txGrowth != GrowthCreditLimit {
		t.Fatalf("growth not saturated: %d/%d", s.credit.txGrowth, GrowthCreditLimit)
	}
	return ids
}

func saturateGrowthReceive(t *testing.T, s *Session) []*Stream {
	t.Helper()
	n := growthSaturationStreamCount()
	out := make([]*Stream, n)
	base := GrowthCreditLimit / n
	extra := GrowthCreditLimit % n
	for i := range out {
		growth := base
		if i < extra {
			growth++
		}
		st := rev2Stream(s, uint64(2*i+1))
		if err := st.receiveCommitLocked(uint64(growth + StreamWindow)); err != nil {
			t.Fatal(err)
		}
		out[i] = st
	}
	if s.receiveGrowth != GrowthCreditLimit {
		t.Fatalf("receive growth not saturated: %d/%d", s.receiveGrowth, GrowthCreditLimit)
	}
	return out
}

func TestRev2IdleEntitlementsDoNotReserveDATA(t *testing.T) {
	s, _ := rev2Fixture()
	for id := uint64(1); id < 33; id += 2 {
		st := rev2Stream(s, id)
		if err := st.receiveLocked(0, bytes.Repeat([]byte{9}, MaxPayload)); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, MaxPayload)
		if _, err := io.ReadFull(st, got); err != nil {
			t.Fatal(err)
		}
		if st.rxLimit <= StreamWindow {
			t.Fatal("large entitlement unexpectedly removed")
		}
	}
	if s.receiveCredit != 0 || s.receiveGrowth != 0 || s.receiveAllocated != 0 || s.credit.rxCommitted != s.credit.rxConsumed {
		t.Fatalf("idle DATA retained: used=%d growth=%d pages=%d", s.receiveCredit, s.receiveGrowth, s.receiveAllocated)
	}
}

func TestRev4GrowthFullStillPermitsAllRemainingBootstrap(t *testing.T) {
	s, _ := rev2Fixture()
	// Bulk directions occupy the full growth pool. Every remaining
	// admitted identity must still obtain its independent 16 KiB bootstrap.
	ids := saturateGrowthSend(t, s)
	nextID := uint64(2*len(ids) + 1)
	for id := nextID; len(s.streams) < MaxStreams; id += 2 {
		st := rev2Stream(s, id)
		st.SetWriteDeadline(time.Now().Add(time.Second))
		if n, err := st.Write(make([]byte, StreamWindow)); err != nil || n != StreamWindow {
			t.Fatalf("id %d: bootstrap n=%d err=%v", id, n, err)
		}
	}
	st := s.streams[nextID]
	st.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	// Its per-stream window is still 16 MiB: the extra byte is blocked ONLY by
	// actual shared credit, unlike the first-stage experiment's test.
	if n, err := st.Write([]byte{1}); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(n, err)
	}
	if s.credit.txUsed != SessionCreditLimit || s.credit.txGrowth != GrowthCreditLimit {
		t.Fatal("pool bounds changed")
	}
	if err := st.releaseSendCreditLocked(StreamWindow); err != nil {
		t.Fatal(err)
	}
	if err := s.receiveSessionCreditLocked(frame{kind: kindSessionWindow, offset: StreamWindow, id: SessionCreditLimit + StreamWindow}); err != nil {
		t.Fatal(err)
	}
	st.SetWriteDeadline(time.Now().Add(time.Second))
	if n, err := st.Write(make([]byte, StreamWindow)); n != StreamWindow || err != nil {
		t.Fatalf("resumed small flow lost sliding bootstrap: %d %v", n, err)
	}
	if s.credit.txGrowth != GrowthCreditLimit || s.credit.txUsed != SessionCreditLimit {
		t.Fatal("resumed bootstrap borrowed growth")
	}
}

func TestRev4GrowthAndPendingWaitsAreIndependent(t *testing.T) {
	s, _ := rev2Fixture()
	ids := saturateGrowthSend(t, s)
	st := rev2Stream(s, uint64(2*len(ids)+1))
	st.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := st.Write(make([]byte, StreamWindow)); err != nil {
		t.Fatal(err)
	}
	st.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	if _, err := st.Write([]byte{1}); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
	if s.credit.waits[waitGrowth].Count == 0 || s.credit.waits[waitStreamWindow].Count != 0 {
		t.Fatal("did not independently exercise growth blocking")
	}
	// A different fixture isolates DATA metadata reservation from flow credit.
	s2, _ := rev2Fixture()
	s2.dataPendingFrames = growthPendingFrames
	st2 := rev2Stream(s2, 1)
	st2.SetWriteDeadline(time.Now().Add(time.Second))
	if n, e := st2.Write(make([]byte, StreamWindow)); n != StreamWindow || e != nil {
		t.Fatal("pending reserve lost", n, e)
	}
	st2.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	if _, e := st2.Write([]byte{1}); !errors.Is(e, os.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	if s2.credit.waits[waitPendingFrames].Count == 0 {
		t.Fatal("metadata bound not exercised")
	}
}

func TestRev2ReceivedOffsetsAreChargedOnce(t *testing.T) {
	s, _ := rev2Fixture()
	st := rev2Stream(s, 1)
	part := bytes.Repeat([]byte{3}, MaxPayload)
	if err := st.receiveLocked(MaxPayload, part); err != nil {
		t.Fatal(err)
	}
	if s.receiveCredit != 2*MaxPayload || s.credit.rxCommitted != 2*MaxPayload {
		t.Fatal("hole not included in commitment")
	}
	if err := st.receiveLocked(MaxPayload, part); err != nil {
		t.Fatal(err)
	}
	if err := st.receiveLocked(0, part); err != nil {
		t.Fatal(err)
	}
	if s.credit.rxCommitted != 2*MaxPayload {
		t.Fatal("duplicate or old offset double charged")
	}
	got := make([]byte, 2*MaxPayload)
	if _, err := io.ReadFull(st, got); err != nil {
		t.Fatal(err)
	}
	if s.receiveCredit != 0 || s.receiveGrowth != 0 || s.credit.rxConsumed != 2*MaxPayload {
		t.Fatal("consumption not settled")
	}
	if err := st.receiveLocked(0, part); err != nil {
		t.Fatal(err)
	}
	if s.credit.rxCommitted != s.credit.rxConsumed {
		t.Fatal("late duplicate resurrected data")
	}
}

func TestRev4ReceivePoolValidationAndNoPartialDebit(t *testing.T) {
	s, _ := rev2Fixture()
	bulk := saturateGrowthReceive(t, s)
	c := rev2Stream(s, uint64(2*len(bulk)+1))
	if e := c.receiveCommitLocked(StreamWindow); e != nil {
		t.Fatal("new receiver bootstrap lost", e)
	}
	before := s.credit.rxCommitted
	if e := c.receiveCommitLocked(StreamWindow + 1); !errors.Is(e, ErrProtocol) {
		t.Fatal("over-budget DATA accepted", e)
	}
	if before != s.credit.rxCommitted || c.rxHigh != StreamWindow {
		t.Fatal("invalid DATA partly debited")
	}
}

func TestRev2SessionWindowOrderBoundsAndOverflow(t *testing.T) {
	s, _ := rev2Fixture()
	st := rev2Stream(s, 1)
	st.commitSendCreditLocked(100)
	if e := s.receiveSessionCreditLocked(frame{offset: 100, id: SessionCreditLimit + 100}); e != nil {
		t.Fatal(e)
	}
	if e := s.receiveSessionCreditLocked(frame{id: SessionCreditLimit}); e != nil {
		t.Fatal(e)
	}
	if s.credit.peerLimit != SessionCreditLimit+100 || s.credit.peerConsumed != 100 {
		t.Fatal("old session WINDOW revoked credit")
	}
	for _, f := range []frame{{offset: 101, id: SessionCreditLimit + 101}, {offset: 10, id: 9}, {id: SessionCreditLimit + 1}, {offset: ^uint64(0) - 1, id: 10}} {
		if e := s.receiveSessionCreditLocked(f); !errors.Is(e, ErrProtocol) {
			t.Fatal("invalid session window accepted", f, e)
		}
	}
	old := st.rxLimit
	st.rxRead = ^uint64(0) - 10
	st.grantCreditLocked(^uint64(0))
	if st.rxLimit != old {
		t.Fatal("grant overflow")
	}
}

func TestRev2ACKIsNotConsumptionAndReinjectDoesNotDebit(t *testing.T) {
	s, c := rev2Fixture()
	st := rev2Stream(s, 1)
	st.SetWriteDeadline(time.Now().Add(time.Second))
	if _, e := st.Write(make([]byte, MaxPayload)); e != nil {
		t.Fatal(e)
	}
	var p *outbound
	for _, v := range s.pending {
		if v.f.kind == kindData {
			p = v
		}
	}
	if p == nil {
		t.Fatal("no pending DATA")
	}
	s.unreadyLocked(p)
	p.path = c
	p.attempts = 2
	p.sentAt = time.Now()
	c.outstanding = p.cost
	committed := s.credit.txCommitted
	if e := s.ackLocked(c, frame{kind: kindACK, stream: st.id, id: p.f.id}); e != nil {
		t.Fatal(e)
	}
	if s.credit.txCommitted != committed || s.credit.txUsed != MaxPayload {
		t.Fatal("delivery ACK or reinject changed flow commitment")
	}
	if e := st.receiveCreditLocked(frame{id: MaxStreamWindow, offset: MaxPayload}); e != nil {
		t.Fatal(e)
	}
	if s.credit.txUsed != 0 || s.credit.txGrowth != 0 {
		t.Fatal("WINDOW did not release consumption")
	}
}

func TestRev2STOPAndResetAreDirectionalAndIdempotent(t *testing.T) {
	s, c := rev2Fixture()
	st := rev2Stream(s, 1)
	st.commitSendCreditLocked(128 << 10)
	if e := st.receiveLocked(0, make([]byte, MaxPayload)); e != nil {
		t.Fatal(e)
	}
	if e := st.CloseRead(); e != nil {
		t.Fatal(e)
	}
	if st.sendReset || s.credit.txUsed != 128<<10 {
		t.Fatal("CloseRead reset local sending direction")
	}
	f := frame{kind: kindResetStream, stream: 1, id: 901, offset: 96 << 10, data: resetPayload(1)}
	if e := s.handleFrame(c, f); e != nil {
		t.Fatal(e)
	}
	beforeCommitted, beforeConsumed := s.credit.rxCommitted, s.credit.rxConsumed
	if beforeCommitted != 96<<10 || beforeConsumed != 96<<10 || s.receiveCredit != 0 {
		t.Fatal("RESET final-size holes were not settled")
	}
	if s.credit.txUsed != 128<<10 {
		t.Fatal("incoming RESET released wrong direction")
	}
	f.id++
	if e := s.handleFrame(c, f); e != nil {
		t.Fatal(e)
	}
	if s.credit.rxCommitted != beforeCommitted || s.credit.rxConsumed != beforeConsumed {
		t.Fatal("duplicate RESET double released")
	}
	if e := s.handleFrame(c, frame{kind: kindData, stream: 1, id: 990, offset: 32 << 10, data: make([]byte, MaxPayload)}); e != nil {
		t.Fatal(e)
	}
	f.offset++
	if e := s.handleFrame(c, f); !errors.Is(e, ErrProtocol) {
		t.Fatal("changed final size accepted", e)
	}
	if e := s.handleFrame(c, frame{kind: kindData, stream: 1, id: 991, offset: 96 << 10, data: []byte{1}}); !errors.Is(e, ErrProtocol) {
		t.Fatal("DATA beyond final accepted", e)
	}
}

func TestRev2StopFreezesOwnFinalUntilResetACK(t *testing.T) {
	s, c := rev2Fixture()
	st := rev2Stream(s, 1)
	st.commitSendCreditLocked(123456)
	f := frame{kind: kindStopReceiving, stream: 1, id: 700, offset: 1}
	if e := s.handleFrame(c, f); e != nil {
		t.Fatal(e)
	}
	if e := s.handleFrame(c, f); e != nil {
		t.Fatal(e)
	}
	var reset *outbound
	count := 0
	for _, p := range s.pending {
		if p.f.kind == kindResetStream {
			reset = p
			count++
		}
	}
	if count != 1 || reset.f.offset != 123456 || len(reset.f.data) != 8 {
		t.Fatal("missing/duplicate/wrong directional final")
	}
	if st.receiveStopped || s.credit.txUsed != 123456 {
		t.Fatal("STOP corrupted reverse half or refunded before ACK")
	}
	if e := s.ackLocked(c, frame{kind: kindACK, stream: 1, id: reset.f.id}); e != nil {
		t.Fatal(e)
	}
	if e := s.ackLocked(c, frame{kind: kindACK, stream: 1, id: reset.f.id}); e != nil {
		t.Fatal(e)
	}
	if s.credit.txUsed != 0 || !st.sendResetACK {
		t.Fatal("RESET ACK did not settle exactly once")
	}
}

func TestRev2TerminalStateBounded(t *testing.T) {
	s, _ := rev2Fixture()
	for i := 0; i < maxTerminalStreams+1000; i++ {
		s.rememberTerminalLocked(uint64(i*2+1), terminalStream{txFinal: 10, rxFinal: 20, rxLimit: 30})
	}
	if len(s.terminal) != maxTerminalStreams || len(s.terminalOrder) != maxTerminalStreams || len(s.seen) > 8193 {
		t.Fatal("unbounded terminal state")
	}
}

func TestRev2PhysicalLimitOnlyAbortsAffectedStream(t *testing.T) {
	s, c := rev2Fixture()
	var healthy []*Stream
	// Leave less than one full 16 MiB stream worth of page capacity so the
	// large sparse stream deterministically hits the independent 128 MiB guard.
	healthyBytes := MaxPayload + 1 // two real pages, fully consumable
	healthyCount := (MaxBuffered/receivePageCost - (MaxStreamWindow/MaxPayload)/2) / 2
	if healthyCount <= 0 || healthyCount >= MaxStreams {
		t.Fatal("invalid physical isolation fixture")
	}
	for i := 0; i < healthyCount; i++ {
		st := rev2Stream(s, uint64(2*i+1))
		if e := st.receiveLocked(0, bytes.Repeat([]byte{1}, healthyBytes)); e != nil {
			t.Fatal(e)
		}
		healthy = append(healthy, st)
	}
	large := rev2Stream(s, uint64(2*healthyCount+1))
	failed := false
	for off := 0; off < MaxStreamWindow; off += MaxPayload {
		e := large.receiveLocked(uint64(off), []byte{1})
		if e != nil {
			if ResourceReason(e) != LimitReceiveAllocated {
				t.Fatal("not the independent physical guard", e)
			}
			s.resetLocked(large, 4, true)
			failed = true
			break
		}
	}
	if !failed || s.receiveAllocated > MaxBuffered || s.receiveCredit > SessionCreditLimit || s.closed || !c.active {
		t.Fatal("physical isolation failed")
	}
	for _, st := range healthy {
		buf := make([]byte, healthyBytes)
		if _, e := io.ReadFull(st, buf); e != nil || !bytes.Equal(buf, bytes.Repeat([]byte{1}, healthyBytes)) {
			t.Fatal("healthy stream disrupted", e)
		}
	}
	if s.receiveAllocated != 0 || s.receiveCredit != 0 {
		t.Fatal("discard/read pages or actual credit retained")
	}
}

func TestRev2RepeatedRealResetsReturnCredit(t *testing.T) {
	s, srv, _, _ := testTCP(t, 2, 32<<20, 2*time.Millisecond)
	for i := 0; i < 24; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		st, e := s.Open(ctx)
		cancel()
		if e != nil {
			t.Fatal(e)
		}
		st.SetDeadline(time.Now().Add(2 * time.Second))
		if _, e = st.Write(make([]byte, MaxPayload)); e != nil {
			t.Fatal(e)
		}
		st.Close()
		st.Close()
	}
	peer := srv.session(s.id)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		okA := len(s.closing) == 0 && s.credit.txUsed == 0 && s.receiveCredit == 0 && s.pendingBytes == 0
		s.mu.Unlock()
		peer.mu.Lock()
		okB := len(peer.closing) == 0 && peer.credit.txUsed == 0 && peer.receiveCredit == 0 && peer.pendingBytes == 0
		peer.mu.Unlock()
		if okA && okB {
			if _, e := transfer(s, 256<<10); e != nil {
				t.Fatal(e)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("reset credit failed to settle: client=%+v peer=%+v", s.Snapshot(), peer.Snapshot())
}
