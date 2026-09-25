package multipath

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestMPX3RejectsOldHelloAndRecord(t *testing.T) {
	key, _ := ParseKey(testToken)
	for _, version := range []byte{1, 2} {
		a, b := net.Pipe()
		done := make(chan error, 1)
		go func() { _, e := readHandshake(b, key); b.Close(); done <- e }()
		h := make([]byte, helloSize)
		copy(h, "MPX0")
		h[3] = '0' + version
		h[4] = version
		h[5] = 1
		h[6] = 1
		binary.BigEndian.PutUint32(h[40:44], 128<<10)
		binary.BigEndian.PutUint32(h[44:], MaxPayload)
		a.Write(h)
		a.Close()
		if e := <-done; !errors.Is(e, ErrProtocol) {
			t.Fatal("old hello accepted", version, e)
		}
		a, b = net.Pipe()
		sc, e := newSecure(b, key, []byte("old-record-test"), false)
		if e != nil {
			t.Fatal(e)
		}
		go func() {
			defer a.Close()
			h := make([]byte, frameHeader)
			copy(h, "MPT0")
			h[3] = '0' + version
			h[4] = version
			h[5] = kindData
			a.Write(h)
		}()
		_, e = sc.readFrame()
		b.Close()
		if !errors.Is(e, ErrProtocol) {
			t.Fatal("old record accepted", version, e)
		}
	}
}

func TestMPX3HelloHasZeroImplicitSendPermission(t *testing.T) {
	a, b := net.Pipe()
	key, _ := ParseKey(testToken)
	done := make(chan error, 1)
	go func() { _, e := clientHandshake(a, key, sessionID{1}, 1, true); a.Close(); done <- e }()
	h := make([]byte, helloSize)
	if _, e := io.ReadFull(b, h); e != nil {
		t.Fatal(e)
	}
	b.Close()
	<-done
	if string(h[:4]) != "MPX3" || h[4] != 3 || binary.BigEndian.Uint32(h[40:44]) != 0 || binary.BigEndian.Uint32(h[44:]) != MaxPayload {
		t.Fatal("incorrect MPX3 hello")
	}
}

func TestWindowBeforeOpenOKDoesNotCompleteOpen(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	c := schedulerPath(1)
	s.paths[1] = c
	st := s.newStreamLocked(1)
	defer st.Close()
	p := s.queueLocked(frame{kind: kindOpen, stream: 1})
	st.openID = p.f.id
	if e := s.handleFrame(c, frame{kind: kindWindow, stream: 1, id: StreamWindow}); e != nil {
		t.Fatal(e)
	}
	if st.open {
		t.Fatal("WINDOW implicitly completed OPEN")
	}
	st.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	if n, e := st.Write([]byte{1}); n != 0 || e == nil {
		t.Fatal("DATA before OPEN_OK")
	}
	if e := s.handleFrame(c, frame{kind: kindOpenOK, stream: 1, id: p.f.id}); e != nil {
		t.Fatal(e)
	}
	st.SetWriteDeadline(time.Now().Add(time.Second))
	if n, e := st.Write([]byte{1}); n != 1 || e != nil {
		t.Fatal(n, e)
	}
}

func TestKeepaliveNeverInheritsBulkGrowth(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.windowSeed = MaxStreamWindow
	s.windowSeedAt = time.Now()
	st := s.newStreamLocked(1)
	st.open = true
	defer st.Close()
	now := time.Now()
	st.advertiseCreditLocked(now)
	for i := 0; i < 100; i++ {
		now = now.Add(100 * time.Millisecond)
		oldRead := st.rxRead
		if err := st.receiveCommitLocked(st.rxRead + 1); err != nil {
			t.Fatal(err)
		}
		st.rxRead++
		st.releaseReadCreditLocked(oldRead)
		st.consumeCreditLocked(1, now)
		st.advertiseCreditLocked(now)
		if st.windowTarget != StreamWindow || s.receiveGrowth != 0 || int(st.rxLimit-st.rxRead) > StreamWindow {
			t.Fatal("keepalive took bulk growth")
		}
	}
}

// Rev2 rebalances actual in-flight DATA, not irrevocable WINDOW entitlements.
func TestBulkCreditNaturallyRebalancesAndIdleNeverRevokes(t *testing.T) {
	s, _ := rev2Fixture()
	streams := saturateGrowthReceive(t, s)
	first := make([]uint64, len(streams))
	now := time.Now()
	for i, st := range streams {
		first[i] = st.rxLimit
	}
	a, b := streams[0], streams[1]
	oldRead := a.rxRead
	a.rxRead += 1 << 20
	a.releaseReadCreditLocked(oldRead)
	if err := b.receiveCommitLocked(b.rxHigh + (1 << 20)); err != nil {
		t.Fatal("consumed capacity could not be shared", err)
	}
	if s.receiveGrowth != GrowthCreditLimit || s.receiveCredit > SessionCreditLimit {
		t.Fatal("shared pool accounting")
	}
	a.advertiseCreditLocked(now.Add(creditIdle + time.Second))
	for i, st := range streams {
		if st.rxLimit != first[i] {
			t.Fatal("idle revoked WINDOW")
		}
	}
	if a.windowTarget != StreamWindow {
		t.Fatal("idle target did not shrink")
	}
	for _, st := range streams {
		st.discardReceiveLocked()
	}
	if s.receiveGrowth != 0 || s.receiveCredit != 0 {
		t.Fatal("historical grants pinned actual credit")
	}
}

func TestMPX3OpenCancellationReleasesIdentity(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.nextStream = 1
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e := s.Open(ctx); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if len(s.streams) != 0 || s.receiveCredit != 0 || s.receiveAllocated != 0 {
		t.Fatal("cancelled OPEN leaked")
	}
	for _, p := range s.pending {
		if p.f.kind != kindResetStream && p.f.kind != kindStopReceiving {
			t.Fatal("cancel retained OPEN")
		}
		s.removePendingLocked(p)
	}
	if s.pendingBytes != 0 || s.controlPendingFrames != 0 {
		t.Fatal("cancelled control metadata leaked")
	}
}

func TestZeroCreditFINAndPartialCloseRemainIndependent(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	c := schedulerPath(1)
	s.paths[1] = c
	st := s.newStreamLocked(1)
	st.open = true
	defer st.Close()
	st.SetDeadline(time.Now().Add(time.Second))
	result := make(chan error, 1)
	go func() { result <- st.CloseWrite() }()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		var fin *outbound
		for _, p := range s.pending {
			if p.f.kind == kindFIN {
				fin = p
				break
			}
		}
		if fin != nil {
			if fin.f.offset != 0 {
				t.Error("nonzero FIN")
			}
			s.ackLocked(c, frame{kind: kindACK, stream: 1, id: fin.f.id})
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if e := <-result; e != nil {
		t.Fatal(e)
	}
	if e := s.handleFrame(c, frame{kind: kindFIN, stream: 1, id: 900, offset: 0}); e != nil {
		t.Fatal(e)
	}
	if data, e := io.ReadAll(st); e != nil || len(data) != 0 {
		t.Fatal(e)
	}
	if st.peerLimit != 0 || st.rxLimit != 0 || s.receiveCredit != 0 {
		t.Fatal("FIN invented WINDOW")
	}
}
