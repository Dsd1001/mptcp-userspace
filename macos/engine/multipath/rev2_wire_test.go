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
	if e := s.handleFrame(c, frame{kind: kindOpenReject, stream: 1, id: 77, offset: 1}); e != nil {
		t.Fatal(e)
	}
	if !st.open || st.closed || s.closed {
		t.Fatal("late OPEN rejection harmed active stream")
	}
	if e := s.handleFrame(c, frame{kind: kindOpenReject, stream: 1, id: 78, offset: 1}); !errors.Is(e, ErrProtocol) {
		t.Fatal("unrelated OPEN id accepted")
	}
}
func TestRev4FairWriterTurnsSkipBlockedStreams(t *testing.T) {
	s, _ := rev2Fixture()
	n := growthSaturationStreamCount()
	streams := make([]*Stream, n)
	base := GrowthCreditLimit / n
	extra := GrowthCreditLimit % n
	for i := range streams {
		growth := base
		if i < extra {
			growth++
		}
		streams[i] = rev2Stream(s, uint64(2*i+1))
		commit := growth + StreamWindow
		if i == 0 {
			commit -= MaxPayload
		}
		streams[i].commitSendCreditLocked(commit)
	}
	a, b := streams[0], streams[1]
	a.writeRemaining, b.writeRemaining = MaxPayload, MaxPayload
	ea := s.writerReady.PushBack(a)
	s.writerReady.PushBack(b)
	if !s.writerTurnLocked(a) || s.writerTurnLocked(b) {
		t.Fatal("scarce growth did not preserve FIFO")
	}
	a.commitSendCreditLocked(MaxPayload)
	if err := a.releaseSendCreditLocked(MaxPayload); err != nil {
		t.Fatal(err)
	}
	s.writerReady.MoveToBack(ea)
	if !s.writerTurnLocked(b) || s.writerTurnLocked(a) {
		t.Fatal("bulk retained the next scarce-credit turn")
	}
	b.peerLimit = b.txNext
	if !s.writerTurnLocked(a) {
		t.Fatal("blocked writer stopped an eligible writer")
	}
	// A newcomer retains bootstrap even when it is behind growth waiters.
	small := rev2Stream(s, uint64(2*len(streams)+1))
	small.writeRemaining = StreamWindow
	s.writerReady.PushBack(small)
	if !s.writerTurnLocked(small) {
		t.Fatal("small flow lost bootstrap to growth fairness")
	}
}

func TestRev2UncontendedFairnessDoesNotSerialize512Writers(t *testing.T) {
	s, _ := rev2Fixture()
	for i := 0; i < MaxStreams; i++ {
		st := rev2Stream(s, uint64(2*i+1))
		st.commitSendCreditLocked(StreamWindow)
		st.writeRemaining = MaxPayload
		s.writerReady.PushBack(st)
	}
	for e := s.writerReady.Front(); e != nil; e = e.Next() {
		st := e.Value.(*Stream)
		if n, _ := st.writeAllowanceLocked(); n != MaxPayload {
			t.Fatal("fixture did not leave a full DATA turn")
		}
		if !s.writerTurnLocked(st) {
			t.Fatal("ample credit created artificial cross-writer waiting")
		}
	}
}
