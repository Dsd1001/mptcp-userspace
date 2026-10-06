package multipath

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"
)

func TestMPX4StableCoreHandshakeVector(t *testing.T) {
	var client handshakeParams
	sid, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	copy(client.sid[:], sid)
	client.carrier = 1
	client.maxCarriers = 96
	nonce, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	copy(client.clientNonce[:], nonce)
	body, err := encodeClientParams(client)
	if err != nil {
		t.Fatal(err)
	}
	clientInit, err := encodeHandshakeMessage(mpx4HSClientInit, body)
	if err != nil {
		t.Fatal(err)
	}
	const wantClient = "01405a01001000112233445566778899aabbccddeeff020001000300010104000100050020000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f070004800080000800048001000009000248000a01024060"
	if got := hex.EncodeToString(clientInit); got != wantClient {
		t.Fatalf("Stable CLIENT_INIT mismatch\n got %s\nwant %s", got, wantClient)
	}

	var server handshakeParams
	server.maxCarriers = 128
	serverNonce, _ := hex.DecodeString("202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f")
	copy(server.serverNonce[:], serverNonce)
	serverBody, err := encodeServerParams(server)
	if err != nil {
		t.Fatal(err)
	}
	serverInit, err := encodeHandshakeMessage(mpx4HSServerInit, serverBody)
	if err != nil {
		t.Fatal(err)
	}
	const wantServer = "023b060020202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f070004800080000800048001000009000248000a01024080"
	if got := hex.EncodeToString(serverInit); got != wantServer {
		t.Fatalf("Stable SERVER_INIT mismatch\n got %s\nwant %s", got, wantServer)
	}
	h0 := sha256.Sum256(bytesJoin([]byte{'M', 'P', 'X', 0, 4}, clientInit, serverInit))
	if got := hex.EncodeToString(h0[:]); got != "53731db4646e0c60563a772a9d1a1d59ad35a3071d13f0112fbe5b63a444e2d4" {
		t.Fatalf("Stable H0 mismatch: %s", got)
	}
}

func TestMPX4StableDormantBlocksNewCommitment(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := s.Open(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DORMANT OPEN was not blocked: %v", err)
	}
	if s.nextPacket != 0 || len(s.streams) != 0 {
		t.Fatalf("DORMANT OPEN allocated protocol state: next=%d streams=%d", s.nextPacket, len(s.streams))
	}

	st := s.newStreamLocked(1)
	st.open = true
	st.peerLimit = StreamWindow
	st.SetWriteDeadline(time.Now().Add(25 * time.Millisecond))
	if n, err := st.Write([]byte("x")); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("DORMANT DATA was not blocked: n=%d err=%v", n, err)
	}
	if st.txNext != 0 || s.nextPacket != 0 {
		t.Fatalf("DORMANT DATA allocated protocol state: txNext=%d next=%d", st.txNext, s.nextPacket)
	}
}

func TestMPX4StableCreditReordering(t *testing.T) {
	s := schedulerFixture()
	s.credit.txCommitted = 1024
	s.credit.peerConsumed = 512
	s.credit.peerLimit = 1536
	if err := s.receiveSessionCreditLocked(frame{kind: kindSessionWindow, offset: 0, id: 1024}); err != nil {
		t.Fatalf("stale Session credit rejected: %v", err)
	}
	if err := s.receiveSessionCreditLocked(frame{kind: kindSessionWindow, offset: 768, id: 1400}); err == nil {
		t.Fatal("crossed Session credit accepted")
	}

	st := s.newStreamLocked(1)
	st.txNext = 1024
	st.peerConsumed = 512
	st.peerCreditConsumed = 512
	st.peerLimit = 1536
	if err := st.receiveCreditLocked(frame{kind: kindWindow, stream: 1, offset: 0, id: 1024}); err != nil {
		t.Fatalf("stale Stream credit rejected: %v", err)
	}
	if err := st.receiveCreditLocked(frame{kind: kindWindow, stream: 1, offset: 768, id: 1400}); err == nil {
		t.Fatal("crossed Stream credit accepted")
	}
}

func TestMPX4StableTransmissionRetireContiguousPrefix(t *testing.T) {
	s := schedulerFixture()
	c := schedulerPath(1)
	s.paths[1] = c
	p1 := s.queueLocked(frame{kind: kindData, stream: 1, data: []byte("a")})
	p2 := s.queueLocked(frame{kind: kindData, stream: 1, data: []byte("b")})
	if p1 == nil || p2 == nil {
		t.Fatal("unable to allocate reliable Transmissions")
	}
	if err := s.ackLocked(c, frame{kind: kindACK, stream: 1, id: p2.f.id}); err != nil {
		t.Fatal(err)
	}
	if s.settledThrough != 0 {
		t.Fatalf("out-of-order settlement advanced prefix to %d", s.settledThrough)
	}
	select {
	case f := <-c.control:
		t.Fatalf("premature retirement advertisement: %+v", f)
	default:
	}
	if err := s.ackLocked(c, frame{kind: kindACK, stream: 1, id: p1.f.id}); err != nil {
		t.Fatal(err)
	}
	if s.settledThrough != 2 {
		t.Fatalf("settled prefix=%d want 2", s.settledThrough)
	}
	select {
	case f := <-c.control:
		if f.kind != kindTransmissionRetire || f.offset != 2 {
			t.Fatalf("wrong retirement advertisement: %+v", f)
		}
	default:
		t.Fatal("missing TRANSMISSION_RETIRE")
	}
}

func TestMPX4StablePeerConfirmationReplayUntilRetire(t *testing.T) {
	s := schedulerFixture()
	c := schedulerPath(1)
	s.paths[1] = c
	s.seen[1] = true
	s.terminal[1] = terminalStream{rxFinal: 1}
	data := frame{kind: kindData, stream: 1, id: 1, data: []byte("x")}
	if err := s.handleFrame(c, data); err != nil {
		t.Fatal(err)
	}
	select {
	case ack := <-c.control:
		if ack.kind != kindACK || ack.id != 1 {
			t.Fatalf("wrong first confirmation: %+v", ack)
		}
	default:
		t.Fatal("missing first confirmation")
	}
	if err := s.handleFrame(c, data); err != nil {
		t.Fatal(err)
	}
	select {
	case ack := <-c.control:
		if ack.kind != kindACK || ack.id != 1 {
			t.Fatalf("wrong replay confirmation: %+v", ack)
		}
	default:
		t.Fatal("duplicate was not reconfirmed before retirement")
	}
	if err := s.handleFrame(c, frame{kind: kindTransmissionRetire, offset: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleFrame(c, data); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-c.control:
		t.Fatalf("retired duplicate was confirmed again: %+v", f)
	default:
	}
	if err := s.handleFrame(c, frame{kind: kindTransmissionRetire, offset: 2}); err == nil {
		t.Fatal("retirement beyond processed prefix accepted")
	}
}

func TestMPX4StableConfirmationClass(t *testing.T) {
	s := schedulerFixture()
	p := s.queueLocked(frame{kind: kindOpen, stream: 1})
	if p == nil {
		t.Fatal("unable to allocate STREAM_OPEN")
	}
	err := s.ackLocked(nil, frame{kind: kindACK, stream: 1, id: p.f.id})
	var failure *mpx4Failure
	if !errors.As(err, &failure) || failure.code != mpx4ErrTransmissionID {
		t.Fatalf("TRANSMISSION_ACK incorrectly settled STREAM_OPEN: %v", err)
	}
}

func TestMPX4StableTransmissionRetireWire(t *testing.T) {
	wire, err := encodeV4Frame(frame{kind: kindTransmissionRetire, offset: 7})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(wire); got != "1a0107" {
		t.Fatalf("TRANSMISSION_RETIRE wire=%s", got)
	}
}
