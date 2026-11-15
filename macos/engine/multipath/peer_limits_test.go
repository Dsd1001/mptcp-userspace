package multipath

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestPeerLimitsBoundLocalDataChunks(t *testing.T) {
	s, _ := rev2Fixture()
	st := rev2Stream(s, 1)
	st.writeRemaining = MaxPayload
	s.peerMaxFrame = 1024
	s.peerMaxRecord = 4096
	if n, reason := st.writeAllowanceLocked(); n != 1024 || reason != waitNone {
		t.Fatalf("MAX_FRAME_PAYLOAD ignored: n=%d reason=%d", n, reason)
	}
	s.peerMaxFrame = MaxPayload
	s.peerMaxRecord = 2048
	if n, reason := st.writeAllowanceLocked(); n != 2048-frameHeader || reason != waitNone {
		t.Fatalf("MAX_RECORD_SIZE ignored: n=%d reason=%d", n, reason)
	}
}

func TestPeerMaxStreamsBlocksAdditionalLocalOpen(t *testing.T) {
	s, _ := rev2Fixture()
	s.peerMaxStreams = 1
	s.newStreamLocked(1)
	if reason := s.openBlockReasonLocked(); reason != LimitStreams {
		t.Fatalf("peer MAX_STREAMS ignored: reason=%q", reason)
	}
}

func TestSecureWriterRejectsDataAbovePeerFrameLimit(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	key, err := ParseKey(testToken)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newSecure(a, key, []byte("peer-frame-limit"), true)
	if err != nil {
		t.Fatal(err)
	}
	c.peerMaxFrame = 1024
	if err := c.writeFrames([]frame{{kind: kindData, stream: 1, offset: 0, id: 1, data: make([]byte, 1025)}}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversized DATA was sent: %v", err)
	}
}

func TestReadFrameSkipsPaddingOnlyRecord(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	a.SetDeadline(time.Now().Add(3 * time.Second))
	b.SetDeadline(time.Now().Add(3 * time.Second))
	key, err := ParseKey(testToken)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := newSecure(a, key, []byte("padding-record"), true)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := newSecure(b, key, []byte("padding-record"), false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		if err := sender.writeRecord([]byte{0x00, 0x01, 0x00}); err != nil {
			done <- err
			return
		}
		wire, err := encodeV4Frame(frame{kind: kindPing, offset: 42})
		if err == nil {
			err = sender.writeRecord(wire)
		}
		done <- err
	}()
	f, err := receiver.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if f.kind != kindPing || f.offset != 42 || receiver.rxCounter != 2 {
		t.Fatalf("padding record was not skipped: frame=%+v rxCounter=%d", f, receiver.rxCounter)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReadFrameSkipsUnknownExtensionOnlyRecord(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	a.SetDeadline(time.Now().Add(3 * time.Second))
	b.SetDeadline(time.Now().Add(3 * time.Second))
	key, err := ParseKey(testToken)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := newSecure(a, key, []byte("unknown-record"), true)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := newSecure(b, key, []byte("unknown-record"), false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		unknown, err := appendV4VarInt(nil, 0x40)
		if err == nil {
			unknown, err = appendV4VarInt(unknown, 1)
		}
		unknown = append(unknown, 0xff)
		if err == nil {
			err = sender.writeRecord(unknown)
		}
		if err != nil {
			done <- err
			return
		}
		wire, err := encodeV4Frame(frame{kind: kindPing, offset: 7})
		if err == nil {
			err = sender.writeRecord(wire)
		}
		done <- err
	}()
	f, err := receiver.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if f.kind != kindPing || f.offset != 7 || receiver.rxCounter != 2 {
		t.Fatalf("unknown extension record was not skipped: frame=%+v rxCounter=%d", f, receiver.rxCounter)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
