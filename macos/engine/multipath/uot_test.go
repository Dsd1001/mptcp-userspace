package multipath

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"testing/iotest"
	"time"
)

type uotShortWriter struct{ bytes.Buffer }

func (w *uotShortWriter) Write(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return w.Buffer.Write(p)
}

func TestUOTDatagramBoundaries(t *testing.T) {
	packets := [][]byte{nil, {0, 255, 1}, bytes.Repeat([]byte{0xa5}, udpMax), {}, []byte("after maximum")}
	var wire uotShortWriter
	for _, p := range packets {
		if err := writeUOTDatagram(&wire, p); err != nil {
			t.Fatal(err)
		}
	}
	r := iotest.OneByteReader(bytes.NewReader(wire.Bytes()))
	buffer := make([]byte, udpMax)
	for i, p := range packets {
		got, err := readUOTDatagram(r, buffer)
		if err != nil || !bytes.Equal(got, p) {
			t.Fatalf("datagram %d: len=%d err=%v", i, len(got), err)
		}
	}
	if _, err := readUOTDatagram(r, buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing bytes: %v", err)
	}
}

func TestUOTRejectsMalformedAndTruncatedDatagrams(t *testing.T) {
	for _, raw := range [][]byte{{0}, {0, 3, 1}, {255, 255}, {255, 228}} {
		if _, err := readUOTDatagram(bytes.NewReader(raw), make([]byte, 65535)); err == nil {
			t.Fatalf("accepted %x", raw)
		}
	}
	var wire bytes.Buffer
	if err := writeUOTDatagram(&wire, make([]byte, udpMax+1)); err == nil || wire.Len() != 0 {
		t.Fatal("oversized datagram wrote a partial frame")
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], 4)
	if _, err := readUOTDatagram(bytes.NewReader(append(hdr[:], 1, 2, 3, 4)), make([]byte, 3)); err == nil {
		t.Fatal("undersized buffer accepted")
	}
}

func TestUOTQueueLimitsAndIdleCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	address := netip.MustParseAddrPort("127.0.0.1:20000")
	flowCtx, stopFlow := context.WithCancel(ctx)
	defer stopFlow()
	flow := &clientUOTFlow{address: address, ctx: flowCtx, cancel: stopFlow, packets: make(chan []byte, uotQueuePackets)}
	u := &UOTClient{ctx: ctx, flows: map[netip.AddrPort]*clientUOTFlow{address: flow}}
	for i := 0; i < uotQueuePackets+1; i++ {
		u.enqueue(address, nil)
	}
	if len(flow.packets) != uotQueuePackets || u.queuedBytes != 2*uotQueuePackets || u.dropped != 1 {
		t.Fatalf("empty packet bound: packets=%d bytes=%d drops=%d", len(flow.packets), u.queuedBytes, u.dropped)
	}
	// A global byte cap applies even when a particular association has room.
	<-flow.packets
	u.queuedBytes = uotQueueBytes - 1
	u.enqueue(address, nil)
	if u.queuedBytes != uotQueueBytes-1 || u.dropped != 2 {
		t.Fatal("global cap exceeded")
	}
	u.enqueue(address, make([]byte, udpMax+1))
	if u.dropped != 3 {
		t.Fatal("oversized local packet accepted")
	}
	u.expire(time.Now().Add(udpIdle + time.Second))
	if flow.ctx.Err() == nil {
		t.Fatal("idle association retained")
	}
	u.queuedBytes = 0
	u.enqueue(address, []byte("must not revive expired flow"))
	if u.queuedBytes != 0 || u.dropped != 4 {
		t.Fatal("expired association accepted more data")
	}
	for i := 1; i < udpFlowsMax; i++ {
		a := netip.AddrPortFrom(address.Addr(), uint16(20000+i))
		u.flows[a] = flow
	}
	u.enqueue(netip.MustParseAddrPort("127.0.0.1:40000"), nil)
	if len(u.flows) != udpFlowsMax || u.dropped != 5 {
		t.Fatal("association cap exceeded")
	}
}

func TestUOTShutdownAndIdleRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		buffer := make([]byte, 65536)
		for {
			n, addr, e := backend.ReadFromUDP(buffer)
			if e != nil {
				return
			}
			backend.WriteToUDP(buffer[:n], addr)
		}
	}()
	srv, err := NewServer(ctx, testToken, "127.0.0.1:9", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if err = srv.EnableUOT(backend.LocalAddr().String()); err != nil {
		t.Fatal(err)
	}
	listener, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(listener)
	session, err := DialClientWithUOTPolicy(ctx, []string{listener.Addr().String()}, testToken, SchedulerAuto, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	u, err := StartClientUOT(session, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	app, err := net.DialUDP("udp", nil, u.Addr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = app.Write([]byte("expire")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	if _, err = app.Read(buffer); err != nil {
		t.Fatal(err)
	}
	u.expire(time.Now().Add(udpIdle + time.Second))
	deadline := time.Now().Add(3 * time.Second)
	for u.Snapshot().Connections != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if u.Snapshot().Connections != 0 {
		t.Fatal("expired flow worker retained")
	}
	// New input may establish a fresh association; old queued data is not reused.
	if _, err = app.Write([]byte("fresh")); err != nil {
		t.Fatal(err)
	}
	if n, e := app.Read(buffer); e != nil || string(buffer[:n]) != "fresh" {
		t.Fatalf("new association: %q %v", buffer[:n], e)
	}
	session.Close()
	select {
	case <-u.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Session close leaked UoT workers")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.queuedBytes != 0 || len(u.flows) != 0 {
		t.Fatalf("shutdown retained bytes=%d flows=%d", u.queuedBytes, len(u.flows))
	}
}
