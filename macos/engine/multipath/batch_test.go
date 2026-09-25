package multipath

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"
)

type fragmentedConn struct{ net.Conn }

func (c fragmentedConn) Write(p []byte) (int, error) { return c.Conn.Write(p[:min(len(p), 71)]) }

func TestBatchedEncryptedRecordsAcrossPartialIO(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	a.SetDeadline(time.Now().Add(3 * time.Second))
	b.SetDeadline(time.Now().Add(3 * time.Second))
	key, _ := ParseKey(testToken)
	sender, err := newSecure(fragmentedConn{a}, key, []byte("batch-test-transcript"), true)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := newSecure(b, key, []byte("batch-test-transcript"), false)
	if err != nil {
		t.Fatal(err)
	}
	frames := []frame{{kind: kindOpen, stream: 1, id: 1}, {kind: kindData, stream: 1, id: 2, data: bytes.Repeat([]byte{0, 127, 255, 1}, MaxPayload/4)}, {kind: kindWindow, stream: 1, offset: 123, id: StreamWindow}, {kind: kindData, stream: 3, id: 4, offset: 9, data: []byte("independent\x00\xff")}, {kind: kindFIN, stream: 1, id: 5, offset: MaxPayload}}
	done := make(chan error, 1)
	go func() { done <- sender.writeFrames(frames) }()
	for _, want := range frames {
		got, err := receiver.readFrame()
		if err != nil {
			t.Fatal(err)
		}
		if got.kind != want.kind || got.stream != want.stream || got.offset != want.offset || got.id != want.id || !bytes.Equal(got.data, want.data) {
			t.Fatalf("batch record changed: %+v", got)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if sender.txCounter != uint64(len(frames)) || receiver.rxCounter != uint64(len(frames)) {
		t.Fatal("batch nonce reuse or record omission")
	}
}

func TestBatchLimitsBeforeIO(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	key, _ := ParseKey(testToken)
	sender, _ := newSecure(a, key, nil, true)
	if !errors.Is(sender.writeFrames(make([]frame, carrierBatchFrames+1)), ErrResourceLimit) {
		t.Fatal("unbounded frame batch")
	}
	frames := make([]frame, 8)
	for i := range frames {
		frames[i] = frame{kind: kindData, data: make([]byte, MaxPayload)}
	}
	if !errors.Is(sender.writeFrames(frames), ErrResourceLimit) {
		t.Fatal("unbounded byte batch")
	}
	if sender.txCounter != 0 {
		t.Fatal("invalid batch consumed encryption state")
	}
}
