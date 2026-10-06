package multipath

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"
)

func TestMPX4VarIntCanonical(t *testing.T) {
	values := []uint64{0, 1, 63, 64, 16383, 16384, 1073741823, 1073741824, mpx4VarIntMax}
	widths := []int{1, 1, 1, 2, 2, 4, 4, 8, 8}
	for i, v := range values {
		b, err := appendV4VarInt(nil, v)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != widths[i] {
			t.Fatalf("%d encoded in %d bytes", v, len(b))
		}
		got, n, err := readV4VarInt(b)
		if err != nil || n != len(b) || got != v {
			t.Fatalf("round trip %d got %d/%d: %v", v, got, n, err)
		}
	}
	bad := [][]byte{{0x40, 0x01}, {0x80, 0, 0, 1}, {0xc0, 0, 0, 0, 0, 0, 0, 1}}
	for _, b := range bad {
		if _, _, err := readV4VarInt(b); !errors.Is(err, ErrProtocol) {
			t.Fatalf("non-canonical accepted: %x", b)
		}
	}
}

func TestMPX4CoreFrameRoundTrip(t *testing.T) {
	frames := []frame{
		{kind: kindOpen, stream: 1, id: 1},
		{kind: kindOpenOK, stream: 1, id: 1},
		{kind: kindData, stream: 1, offset: 32768, id: 2, data: []byte("mpx4")},
		{kind: kindACK, stream: 1, id: 2, offset: 1234567},
		{kind: kindWindow, stream: 1, offset: 1024, id: 65536},
		{kind: kindFIN, stream: 1, id: 3, offset: 32773},
		{kind: kindSessionWindow, offset: 4096, id: 1 << 20},
		{kind: kindPing, offset: 9},
		{kind: kindPong, offset: 9},
		{kind: kindCreditProbe, stream: 1},
		{kind: kindFinalConsumed, stream: 1, id: 4, offset: 32773},
		{kind: kindTransmissionRetire, offset: 7},
	}
	for _, want := range frames {
		wire, err := encodeV4Frame(want)
		if err != nil {
			t.Fatalf("encode %+v: %v", want, err)
		}
		typ, n1, err := readV4VarInt(wire)
		if err != nil {
			t.Fatal(err)
		}
		ln, n2, err := readV4VarInt(wire[n1:])
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeV4Frame(typ, wire[n1+n2:n1+n2+int(ln)])
		if err != nil {
			t.Fatalf("decode %+v: %v", want, err)
		}
		if got.kind != want.kind || got.stream != want.stream || got.offset != want.offset || got.id != want.id || !bytes.Equal(got.data, want.data) {
			t.Fatalf("frame changed: want=%+v got=%+v", want, got)
		}
	}
}

func TestMPX4StableParametersAndCapacityHint(t *testing.T) {
	var p handshakeParams
	p.sid[0] = 1
	p.action = 0
	p.carrier = 1<<40 + 7
	p.generation = 7
	p.clientNonce[0] = 1
	p.maxCarriers = MaxCarriers
	p.receiveCapacityHint = 1005
	p.hasReceiveCapacityHint = true
	body, err := encodeClientParams(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseParams(body, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.carrier != p.carrier || got.generation != 7 || got.maxCarriers != MaxCarriers || !got.hasReceiveCapacityHint || got.receiveCapacityHint != 1005 {
		t.Fatalf("params changed: %+v", got)
	}
	if bytes.Contains(body, []byte{0x10, 0x00}) || bytes.Contains(body, []byte{0x11, 0x00}) {
		t.Fatal("retired Core scheduler parameters leaked into Stable handshake")
	}
}

func TestMPX4HandshakeSecureRecordAndGeneration(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	var sid sessionID
	sid[0] = 7
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	serverDone := make(chan error, 1)
	go func() {
		h, err := readHandshake(b, key)
		if err != nil {
			serverDone <- err
			return
		}
		if !h.create || h.carrier != 1 || h.generation != 0 || h.client.maxCarriers != MaxCarriers {
			serverDone <- ErrProtocol
			return
		}
		sc, err := h.finish(key, 0)
		if err != nil {
			serverDone <- err
			return
		}
		f, err := sc.readFrame()
		if err != nil {
			serverDone <- err
			return
		}
		if f.kind != kindData || f.stream != 1 || f.id != 9 || f.offset != 0 || string(f.data) != "hello" {
			serverDone <- ErrProtocol
			return
		}
		serverDone <- sc.writeFrame(frame{kind: kindACK, stream: 1, id: 9, offset: 777})
	}()
	c, err := clientHandshakePolicyGeneration(a, key, sid, 1, 0, true, SchedulerAggregate, PathCapacity{})
	if err != nil {
		t.Fatal(err)
	}
	if c.generation != 0 {
		t.Fatalf("generation=%d", c.generation)
	}
	if err := c.writeFrame(frame{kind: kindData, stream: 1, id: 9, data: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	ack, err := c.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if ack.kind != kindACK || ack.offset != 777 {
		t.Fatalf("bad ack: %+v", ack)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server handshake timeout")
	}
}

func TestMPX4RejectsMPX3Preface(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	go func() { _, err := readHandshake(b, key); done <- err }()
	_, _ = a.Write([]byte("MPX3\x03"))
	select {
	case err := <-done:
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("old preface error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("old preface not rejected")
	}
}
