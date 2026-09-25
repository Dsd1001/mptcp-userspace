package multipath

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestOPENIsFollowedBySeparateExplicitWINDOW(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key, _ := ParseKey(testToken)
	send, e := newSecure(a, key, []byte("explicit-bootstrap-pair"), true)
	if e != nil {
		t.Fatal(e)
	}
	recv, e := newSecure(b, key, []byte("explicit-bootstrap-pair"), false)
	if e != nil {
		t.Fatal(e)
	}
	s := schedulerFixture()
	s.ctx = ctx
	c := schedulerPath(1)
	c.conn = send
	c.done = make(chan struct{})
	s.paths[1] = c
	st := s.newStreamLocked(1)
	p := s.queueLocked(frame{kind: kindOpen, stream: 1})
	st.openID = p.f.id
	if st.peerLimit != 0 || st.rxLimit != 0 {
		t.Fatal("identity allocation implied credit")
	}
	s.dispatchControlsLocked()
	done := make(chan struct{})
	go func() { s.writeCarrier(c); close(done) }()
	defer func() { cancel(); a.Close(); b.Close(); <-done }()
	b.SetReadDeadline(time.Now().Add(time.Second))
	first, e := recv.readFrame()
	if e != nil || first.kind != kindOpen || first.offset != 0 {
		t.Fatal("bad identity OPEN", e)
	}
	second, e := recv.readFrame()
	if e != nil || second.kind != kindWindow || second.stream != st.id || second.offset != 0 || second.id != StreamWindow {
		t.Fatalf("missing separate bootstrap WINDOW: frame=%+v error=%v", second, e)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.open || st.peerLimit != 0 || st.rxLimit != StreamWindow || s.receiveCredit != 0 {
		t.Fatal("explicit local grant completed OPEN or peer permission")
	}
}
