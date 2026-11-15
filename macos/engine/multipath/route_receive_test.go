package multipath

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func receiveRouteFixture(t *testing.T) (*Session, *carrier) {
	t.Helper()
	s, c := rev2Fixture()
	s.productMux = true
	if err := s.ConfigureRoutePolicies([]RoutePolicy{{ID: 1}, {ID: 2}, {ID: 3}}); err != nil {
		t.Fatal(err)
	}
	return s, c
}
func receiveRouteStream(t *testing.T, s *Session, id uint64, route uint32) *Stream {
	t.Helper()
	st := rev2Stream(s, id)
	if route != 0 {
		if err := s.bindRouteLocked(st, route); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestRouteReceiveTracksHolesDuplicatesConsumptionAndReset(t *testing.T) {
	s, _ := receiveRouteFixture(t)
	st := receiveRouteStream(t, s, 1, 1)
	r := s.routeGroups[1]
	payload := bytes.Repeat([]byte{7}, MaxPayload)
	// A sparse last frame charges the whole offset extent, not just payload.
	if err := st.receiveLocked(2*MaxPayload, payload); err != nil {
		t.Fatal(err)
	}
	if r.receiveUsed != 3*MaxPayload || r.receiveAllocated != receivePageCost {
		t.Fatalf("sparse accounting: %+v", r)
	}
	if err := st.receiveLocked(2*MaxPayload, payload); err != nil {
		t.Fatal(err)
	}
	if r.receiveUsed != 3*MaxPayload || r.receiveAllocated != receivePageCost {
		t.Fatal("duplicate charged twice")
	}
	for i := 0; i < 2; i++ {
		if err := st.receiveLocked(uint64(i*MaxPayload), payload); err != nil {
			t.Fatal(err)
		}
	}
	got := make([]byte, 3*MaxPayload)
	if _, err := io.ReadFull(st, got); err != nil {
		t.Fatal(err)
	}
	if r.receiveUsed != 0 || r.receiveAllocated != 0 || s.receiveCredit != 0 || s.receiveAllocated != 0 {
		t.Fatalf("consumption leaked route/session accounting: %+v", r)
	}
	if err := st.receiveLocked(uint64(3*MaxPayload), payload); err != nil {
		t.Fatal(err)
	}
	limit := st.rxLimit
	s.resetLocked(st, mpx4ErrResourceLimit, true)
	if r.receiveUsed != 0 || r.receiveAllocated != 0 || st.rxLimit != limit {
		t.Fatal("reset leaked resources or revoked WINDOW")
	}
}

func TestRouteReceivePressureOnlyResetsAffectedStream(t *testing.T) {
	s, c := receiveRouteFixture(t)
	bad := receiveRouteStream(t, s, 1, 1)
	good := receiveRouteStream(t, s, 3, 2)
	r := s.routeGroups[1]
	r.receiveLimit = receivePageCost // room for exactly one page
	initialLimit := bad.rxLimit
	if err := s.handleDataFrame(c, frame{kind: kindData, stream: bad.id, id: 1, offset: 0, data: make([]byte, MaxPayload)}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDataFrame(c, frame{kind: kindData, stream: bad.id, id: 2, offset: MaxPayload, data: make([]byte, MaxPayload)}); err != nil {
		t.Fatalf("local pressure escaped as Session failure: %v", err)
	}
	if !bad.closed || s.closed || !c.active || r.receivePressureResets != 1 {
		t.Fatalf("wrong error scope: stream=%v session=%v resets=%d", bad.closed, s.closed, r.receivePressureResets)
	}
	if bad.rxLimit != initialLimit || r.receiveUsed != 0 || r.receiveAllocated != 0 {
		t.Fatal("pressure reset revoked permission or leaked bytes")
	}
	payload := []byte("healthy route still works")
	if err := s.handleDataFrame(c, frame{kind: kindData, stream: good.id, id: 3, data: payload}); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(good, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("healthy route failed: %q %v", got, err)
	}
}

func TestRouteReceiveProtectsIdleRouteCapacityAndMovesSetupBytes(t *testing.T) {
	s, _ := receiveRouteFixture(t)
	a := receiveRouteStream(t, s, 1, 1)
	r := s.routeGroups[1]
	// Occupied bytes, not WINDOW entitlements, consume the reserve.
	for i := 0; i < 512; i++ {
		st := receiveRouteStream(t, s, uint64(101+i*2), 1)
		st.grantCreditLocked(MaxStreamWindow)
	}
	if r.receiveUsed != 0 || r.receiveAllocated != 0 {
		t.Fatal("idle WINDOW grants pinned memory")
	}
	if room := s.routeLogicalRoomLocked(r); room != routeReceiveLimit {
		t.Fatalf("idle grants restricted active route: %d", room)
	}
	s.receiveCredit = SessionCreditLimit - (2 << 20)
	r.receiveUsed = routeReceiveLimit
	// Other routes' protected reservation still has to be honored at the
	// allocator even if a peer sends against old, irrevocable permissions.
	if s.routeLogicalRoomLocked(r) > 0 {
		t.Fatal("saturated route has additional room")
	}
	s.receiveCredit = 0
	r.receiveUsed = 0
	setup := receiveRouteStream(t, s, 5, 0)
	if err := setup.receiveLocked(0, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	old := s.routeGroups[0]
	if old.receiveUsed != 1024 || old.receiveAllocated != receivePageCost {
		t.Fatal("unclassified data not accounted")
	}
	if err := s.bindRouteLocked(setup, 2); err != nil {
		t.Fatal(err)
	}
	if old.receiveUsed != 0 || old.receiveAllocated != 0 || s.routeGroups[2].receiveUsed != 1024 || s.routeGroups[2].receiveAllocated != receivePageCost {
		t.Fatal("route classification leaked or double charged")
	}
	setup.discardReceiveLocked()
	if s.routeGroups[2].receiveUsed != 0 || s.routeGroups[2].receiveAllocated != 0 {
		t.Fatal("discard did not refund route")
	}
	_ = a
}

func TestRouteReceiveWindowNeverRetractsAndPressureClears(t *testing.T) {
	s, _ := receiveRouteFixture(t)
	st := receiveRouteStream(t, s, 1, 1)
	r := s.routeGroups[1]
	st.advertiseCreditLocked(time.Now())
	old := st.rxLimit
	s.adjustRouteReceiveLocked(r, routeReceiveLimit, 0)
	s.receiveCredit += routeReceiveLimit
	st.rxRead = 1024
	st.advertiseCreditLocked(time.Now())
	if st.rxLimit != old || r.receiveWindowDeferrals == 0 {
		t.Fatal("pressure retracted grant or did not defer")
	}
	s.adjustRouteReceiveLocked(r, -routeReceiveLimit, 0)
	s.receiveCredit -= routeReceiveLimit
	st.advertiseCreditLocked(time.Now())
	if st.rxLimit <= old {
		t.Fatal("window did not resume after consumption")
	}
}

func TestRouteReceiveSlowReaderDoesNotBlockOtherRoute(t *testing.T) {
	backend, _ := routedBannerBackend(t, "")
	srv, s, _ := routedFixture(t, []TCPRoute{{ID: 1, Backend: backend}, {ID: 2, Backend: backend}}, []RoutePolicy{{ID: 1}, {ID: 2}}, 2)
	slow, err := s.OpenRoutedTCP(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	slow.SetDeadline(time.Now().Add(10 * time.Second))
	// Force a small local receive budget in this test. The peer already has
	// a legitimate larger WINDOW. Over-budget echo must reset this Stream,
	// not reinterpret existing permissions as a protocol violation.
	s.mu.Lock()
	s.routeGroups[1].receiveLimit = 2 * receivePageCost
	s.mu.Unlock()
	written := make(chan error, 1)
	go func() { _, e := slow.Write(make([]byte, 1<<20)); written <- e }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap := s.Snapshot()
		var resets uint64
		for _, r := range snap.Routes {
			if r.ID == 1 {
				resets = r.ReceivePressureResets
			}
		}
		if resets > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow reader did not reach test pressure limit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := 0; i < 5; i++ {
		if err := routedRoundtrip(s, 2, "", 128<<10); err != nil {
			t.Fatalf("healthy route %d: %v", i, err)
		}
	}
	if s.Err() != nil || len(srv.Snapshots()) != 1 {
		t.Fatal("route failure terminated Session")
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("slow writer did not wake after reset")
	}
	var one [1]byte
	_, err = slow.Read(one[:])
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow stream did not report its own failure: %v", err)
	}
}

func TestRouteReceiveFINCannotPinUnboundedHoleCredit(t *testing.T) {
	s, c := receiveRouteFixture(t)
	bad := receiveRouteStream(t, s, 1, 1)
	good := receiveRouteStream(t, s, 3, 2)
	s.routeGroups[1].receiveLimit = MaxPayload
	oldLimit := bad.rxLimit
	s.mu.Lock()
	err := s.handleFinalLocked(c, frame{kind: kindFIN, stream: bad.id, id: 1, offset: 2 * MaxPayload})
	s.mu.Unlock()
	if err != nil || !bad.closed || s.closed || !c.active {
		t.Fatalf("FIN resource scope: %v stream=%v session=%v", err, bad.closed, s.closed)
	}
	r := s.routeGroups[1]
	if r.receiveUsed != 0 || r.receiveAllocated != 0 || r.receivePressureResets != 1 || bad.rxLimit != oldLimit {
		t.Fatal("FIN retained holes, revoked grant, or failed to record route pressure")
	}
	if err := s.handleDataFrame(c, frame{kind: kindData, stream: good.id, id: 2, data: []byte("ok")}); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 2)
	if _, err := io.ReadFull(good, got); err != nil || string(got) != "ok" {
		t.Fatalf("healthy route after FIN pressure: %v", err)
	}
}
