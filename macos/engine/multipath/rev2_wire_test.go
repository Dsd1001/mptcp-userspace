package multipath

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestRev4RejectsAllOlderSchedulerHellos(t *testing.T) {
	t.Skip("legacy MPX/3 wire-format test; superseded by MPX/4 Protocol Version 4 Stable conformance tests")
	key, _ := ParseKey(testToken)
	for _, old := range []byte{0x11, 0x12, 0x13, 0x21, 0x22, 0x23, 0x31, 0x32, 0x33} {
		a, b := net.Pipe()
		done := make(chan error, 1)
		go func() { _, err := readHandshake(b, key); b.Close(); done <- err }()
		h := make([]byte, helloSize)
		copy(h, "MPX3")
		h[4], h[5], h[6], h[7], h[8] = 3, 1, 1, old, 1
		binary.BigEndian.PutUint32(h[44:], MaxPayload)
		a.SetDeadline(time.Now().Add(time.Second))
		if err := writeAll(a, h); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, ErrProtocol) {
			t.Fatal("older peer accepted", old, err)
		}
		a.Close()
	}
}

func TestRev2ResetCodecAndControlPayloadBounds(t *testing.T) {
	key, _ := ParseKey(testToken)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	tx, err := newSecure(a, key, []byte("rev2-bounds"), true)
	if err != nil {
		t.Fatal(err)
	}
	rx, err := newSecure(b, key, []byte("rev2-bounds"), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []frame{{kind: kindResetStream, data: make([]byte, 7)}, {kind: kindResetStream, data: make([]byte, 9)}, {kind: kindSessionWindow, data: []byte{1}}, {kind: kindStopReceiving, data: []byte{1}}} {
		if err := tx.writeFrame(f); !errors.Is(err, ErrProtocol) {
			t.Fatal("malformed control accepted", err)
		}
	}
	done := make(chan error, 1)
	go func() {
		done <- tx.writeFrame(frame{kind: kindResetStream, stream: 1, id: 99, offset: 12345, data: resetPayload(4)})
	}()
	f, err := rx.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.kind != kindResetStream || f.id != 99 || f.offset != 12345 || binary.BigEndian.Uint64(f.data) != 4 {
		t.Fatal("reset fields lost")
	}
}

func TestRev2InitialSessionCreditIsExplicitAndRegenerated(t *testing.T) {
	s := newSession(context.Background(), sessionID{1}, false, nil)
	defer s.Close()
	s.mu.Lock()
	if s.credit.peerLimit != 0 {
		t.Fatal("implicit session permission")
	}
	st := s.newStreamLocked(1)
	st.open = true
	st.peerLimit = MaxStreamWindow
	s.mu.Unlock()
	st.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	if n, err := st.Write([]byte{1}); n != 0 || err == nil {
		t.Fatal("per-stream window bypassed absent session grant")
	}
	s.mu.Lock()
	c := schedulerPath(1)
	s.paths[1] = c
	s.advertiseSessionCreditLocked(time.Now(), true)
	f := <-c.control
	if f.kind != kindSessionWindow || f.id != SessionCreditLimit || f.offset != 0 {
		t.Fatal("initial session grant")
	}
	// Drop this frame. Periodic regeneration must not require an incoming DATA
	// or blocked notification from the peer to repair the lost credit message.
	s.credit.windowAt = time.Now().Add(-2 * time.Second)
	s.advertiseSessionCreditLocked(time.Now(), false)
	if f = <-c.control; f.kind != kindSessionWindow || f.id != SessionCreditLimit {
		t.Fatal("lost session grant stalled")
	}
	// Do not leave a fake carrier for Session.Close to dereference.
	delete(s.paths, 1)
	s.mu.Unlock()
}

func TestRev2ClosingKeepsSlotAndConsumptionProbeRecoversTerminal(t *testing.T) {
	s, c := rev2Fixture()
	st := rev2Stream(s, 1)
	st.commitSendCreditLocked(100)
	st.writeFIN, st.finACK, st.hasFIN = true, true, true
	st.Close()
	if len(s.closing) != 1 || s.credit.txUsed != 100 {
		t.Fatal("unsettled graceful close was refunded")
	}
	for i := 1; i < MaxStreams; i++ {
		s.newStreamLocked(uint64(2*i + 1))
	}
	if s.openBlockReasonLocked() != LimitStreams {
		t.Fatal("closing slot was reused before settlement")
	}
	if err := s.handleFrame(c, frame{kind: kindWindow, stream: 1, offset: 100, id: MaxStreamWindow}); err != nil {
		t.Fatal(err)
	}
	if len(s.closing) != 1 || s.credit.txUsed != 0 {
		t.Fatal("final consumption confirmation was not retained")
	}
	var confirmation *outbound
	for _, p := range s.pending {
		if p.f.kind == kindFinalConsumed {
			confirmation = p
		}
	}
	if confirmation == nil {
		t.Fatal("no reliable final consumption confirmation")
	}
	// Simulate a lost best-effort WINDOW and a delayed confirmation ACK. The
	// reliable control is still retried, rather than forgetting the ledger.
	confirmation.created = time.Now().Add(-31 * time.Second)
	s.sweepLocked(time.Now())
	if s.pending[confirmation.f.id] != confirmation {
		t.Fatal("final confirmation aged out before ACK")
	}
	if e := s.ackLocked(c, frame{kind: kindACK, stream: st.id, id: confirmation.f.id}); e != nil {
		t.Fatal(e)
	}
	if len(s.closing) != 0 || s.credit.txUsed != 0 || s.openBlockReasonLocked() != "" {
		t.Fatal("settled slot not returned")
	}
	for len(c.control) > 0 {
		<-c.control
	}
	if err := s.handleFrame(c, frame{kind: kindCreditProbe, stream: 1}); err != nil {
		t.Fatal(err)
	}
	f := <-c.control
	if f.kind != kindWindow || f.offset != 0 {
		t.Fatal("terminal consumption not re-advertised")
	}
}

func TestRev2BothDirectionsResetAtOnce(t *testing.T) {
	a, ca := rev2Fixture()
	b, cb := rev2Fixture()
	sa, sb := rev2Stream(a, 1), rev2Stream(b, 1)
	sa.commitSendCreditLocked(128 << 10)
	sb.commitSendCreditLocked(64 << 10)
	sa.Close()
	sb.Close()
	for rounds := 0; rounds < 8; rounds++ {
		for _, edge := range []struct {
			from, to *Session
			carrier  *carrier
		}{{a, b, cb}, {b, a, ca}} {
			frames := []frame{}
			for _, p := range edge.from.pending {
				frames = append(frames, p.f)
			}
			for _, f := range frames {
				if e := edge.to.handleFrame(edge.carrier, f); e != nil {
					t.Fatal(e)
				}
				if e := edge.to.handleFrame(edge.carrier, f); e != nil {
					t.Fatal("duplicate termination", e)
				}
			}
		}
		for len(ca.control) > 0 {
			if e := b.handleFrame(cb, <-ca.control); e != nil {
				t.Fatal(e)
			}
		}
		for len(cb.control) > 0 {
			if e := a.handleFrame(ca, <-cb.control); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, s := range []*Session{a, b} {
		if s.credit.txUsed != 0 || s.receiveCredit != 0 || len(s.closing) != 0 || s.pendingBytes != 0 {
			t.Fatalf("simultaneous termination did not settle: %+v", s.Snapshot())
		}
	}
}

func TestRev2LateOpenRejectCannotRevokeAcceptedStream(t *testing.T) {
	s, c := rev2Fixture()
	st := rev2Stream(s, 1)
	st.openID = 77
	s.nextPacket = 77
	s.settledThrough = 77
	if e := s.handleFrame(c, frame{kind: kindOpenReject, stream: 1, id: 77, offset: mpx4ErrStreamState}); e != nil {
		t.Fatal(e)
	}
	if !st.open || st.closed || s.closed {
		t.Fatal("late OPEN rejection harmed active stream")
	}
	if e := s.handleFrame(c, frame{kind: kindOpenReject, stream: 1, id: 78, offset: mpx4ErrStreamState}); !errors.Is(e, ErrProtocol) {
		t.Fatal("unrelated OPEN id accepted")
	}
}
func TestPeerWindowCreditAuthoritativePastLegacyTXUsedLimit(t *testing.T) {
	s, _ := rev2Fixture()
	st := rev2Stream(s, 1)
	st.txNext = StreamWindow
	st.peerConsumed = 0
	st.peerLimit = uint64(StreamWindow + MaxPayload)
	s.credit.txCommitted = StreamWindow
	s.credit.peerLimit = uint64(StreamWindow + MaxPayload)

	// Per-Stream consumed reports can lag aggregate SESSION_WINDOW progress.
	// These diagnostics are intentionally above the old 128 MiB local ceiling:
	// peer-advertised WINDOW room must still permit the DATA frame.
	s.credit.txUsed = 2 * SessionCreditLimit
	s.credit.txGrowth = s.credit.txUsed - StreamWindow
	st.writeEntry = s.writerReady.PushBack(st)
	defer s.writerReady.Remove(st.writeEntry)
	s.setWriterRemainingLocked(st, MaxPayload)
	defer s.setWriterRemainingLocked(st, 0)
	s.syncBootstrapReserveLocked(st)

	if n, reason := st.writeAllowanceLocked(); n != MaxPayload || reason != waitNone {
		t.Fatalf("diagnostic txUsed/growth overrode peer WINDOW: n=%d reason=%d", n, reason)
	}
}

func TestPeerSessionWindowRemainsHardSendGate(t *testing.T) {
	s, _ := rev2Fixture()
	st := rev2Stream(s, 1)
	st.peerLimit = MaxStreamWindow
	st.writeEntry = s.writerReady.PushBack(st)
	defer s.writerReady.Remove(st.writeEntry)
	s.setWriterRemainingLocked(st, MaxPayload)
	defer s.setWriterRemainingLocked(st, 0)

	s.credit.txCommitted = SessionCreditLimit
	s.credit.peerLimit = SessionCreditLimit
	s.credit.txUsed = 0
	s.credit.txGrowth = 0
	if n, reason := st.writeAllowanceLocked(); n != 0 || reason != waitSessionWindow {
		t.Fatalf("peer SESSION_WINDOW was not authoritative: n=%d reason=%d", n, reason)
	}
}
