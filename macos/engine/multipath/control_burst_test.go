package multipath

import (
	"context"
	"net"
	"testing"
	"time"
)

// Exercise the real encrypted writer with many paired OPEN/WINDOW records,
// regenerated controls and DATA already queued. No network is faked by stats.
func TestControlBurstPairBatchBoundAndDataProgress(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key, _ := ParseKey(testToken)
	tx, e := newSecure(a, key, []byte("paired-control-burst"), true)
	if e != nil {
		t.Fatal(e)
	}
	rx, e := newSecure(b, key, []byte("paired-control-burst"), false)
	if e != nil {
		t.Fatal(e)
	}
	s := schedulerFixture()
	s.ctx = ctx
	c := schedulerPath(1)
	c.conn = tx
	c.done = make(chan struct{})
	s.paths[1] = c
	const opens = 48
	for i := 0; i < opens; i++ {
		st := s.newStreamLocked(uint64(2*i + 1))
		p := s.queueLocked(frame{kind: kindOpen, stream: st.id})
		st.openID = p.f.id
	}
	for i := 0; i < 128; i++ {
		c.control <- frame{kind: kindPing, offset: uint64(i)}
	}
	s.dispatchControlsLocked()
	data := s.queueLocked(frame{kind: kindData, stream: 1, data: []byte("payload-cannot-starve")})
	s.dispatchLocked(time.Now())
	done := make(chan struct{})
	go func() { s.writeCarrier(c); close(done) }()
	defer func() { cancel(); a.Close(); b.Close(); <-done }()
	b.SetReadDeadline(time.Now().Add(2 * time.Second))
	seenOpen, seenWindow, seenData, seenPing := 0, 0, 0, 0
	for i := 0; i < opens*2+128+1; i++ {
		f, e := rx.readFrame()
		if e != nil {
			t.Fatalf("writer exceeded batch bounds or stalled after %d records: %v", i, e)
		}
		switch f.kind {
		case kindOpen:
			seenOpen++
		case kindWindow:
			seenWindow++
			if f.id != StreamWindow || f.offset != 0 {
				t.Fatal("invalid explicit bootstrap")
			}
		case kindData:
			seenData++
			if f.id != data.f.id || string(f.data) != "payload-cannot-starve" {
				t.Fatal("wrong DATA")
			}
			if i > 32 {
				t.Fatalf("ready DATA starved by control burst: position=%d", i)
			}
		case kindPing:
			seenPing++
		default:
			t.Fatal("unexpected frame")
		}
	}
	if seenOpen != opens || seenWindow != opens || seenData != 1 || seenPing != 128 {
		t.Fatal("control/data frame loss")
	}
	s.mu.Lock()
	if s.receiveCredit != 0 || s.receiveGrowth != 0 {
		s.mu.Unlock()
		t.Fatal("WINDOW entitlement incorrectly reserved actual DATA")
	}
	s.mu.Unlock()
}
