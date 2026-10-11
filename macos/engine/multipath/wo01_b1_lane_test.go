package multipath

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestWO01B1PriorityCarrierQueueBypassesBoundedBulk(t *testing.T) {
	s, c := wo01B1Session()
	s.streams[1] = &Stream{s: s, id: 1}
	// The normal writer has four queued Bulk tasks. First DATA has a
	// different two-slot queue, subject to one shared Session burst cap.
	for range carrierQueue {
		c.queue <- sendTask{p: &outbound{f: frame{kind: kindData, stream: 9}}}
	}
	p := s.queueLocked(frame{kind: kindData, stream: 1, data: make([]byte, MaxPayload)})
	if p == nil {
		t.Fatal("cannot enqueue first DATA")
	}
	s.dispatchLocked(time.Now())
	if p.ready != nil || !p.firstDataTokenCharged || len(c.priorityDATA) != 1 {
		t.Fatalf("first DATA did not bypass queued Bulk: ready=%v charged=%v fast=%d", p.ready, p.firstDataTokenCharged, len(c.priorityDATA))
	}
	if len(c.queue) != carrierQueue || s.firstDataPromoted != 1 {
		t.Fatal("fast DATA modified ordinary DATA queue or attribution")
	}
}

func TestWO01B1PriorityCarrierFairnessAndControlFirst(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	key, err := ParseKey(testToken)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := newSecure(a, key, []byte("wo01-b1-priority-ordered"), true)
	if err != nil {
		t.Fatal(err)
	}
	rx, err := newSecure(b, key, []byte("wo01-b1-priority-ordered"), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, c := wo01B1Session()
	s.ctx = ctx
	c.conn = tx
	c.done = make(chan struct{})
	// Test-only enlarged fast queue, to prove that continuously backlogged
	// urgent DATA cannot permanently starve a normal DATA frame.
	c.priorityDATA = make(chan sendTask, 12)
	s.streams[1] = &Stream{s: s, id: 1}
	enqueue := func(n byte, priority bool) {
		p := s.queueLocked(frame{kind: kindData, stream: 1, offset: uint64(n) * MaxPayload, data: []byte{n}})
		if p == nil {
			t.Fatalf("cannot enqueue DATA %d", n)
		}
		s.unreadyLocked(p)
		p.path = c
		p.generation++
		c.outstanding += p.cost
		c.queued += p.cost
		task := sendTask{p: p, generation: p.generation}
		if priority {
			c.priorityDATA <- task
		} else {
			c.queue <- task
		}
	}
	enqueue(255, false)
	for i := byte(0); i < 12; i++ {
		enqueue(i, true)
	}
	c.control <- frame{kind: kindPing}
	done := make(chan struct{})
	go func() { s.writeCarrier(c); close(done) }()
	defer func() {
		cancel()
		a.Close()
		b.Close()
		<-done
	}()
	if e := b.SetReadDeadline(time.Now().Add(3 * time.Second)); e != nil {
		t.Fatal(e)
	}
	bulkAt, controlAt := -1, -1
	for i := 0; i < 14; i++ {
		f, e := rx.readFrame()
		if e != nil {
			t.Fatalf("read encrypted writer frame %d: %v", i, e)
		}
		if f.kind == kindPing {
			controlAt = i
			continue
		}
		if f.kind != kindData || len(f.data) != 1 {
			t.Fatalf("wrong frame %d: %d", i, f.kind)
		}
		if f.data[0] == 255 {
			bulkAt = i
		}
	}
	if controlAt != 0 {
		t.Fatal(fmt.Sprintf("control lost highest priority: control frame at %d", controlAt))
	}
	if bulkAt < 0 || bulkAt > 6 {
		t.Fatal(fmt.Sprintf("normal DATA starved by fast queue: bulk at %d", bulkAt))
	}
}
