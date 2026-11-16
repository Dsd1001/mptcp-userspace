package multipath

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStreamWriteWaitCountersMeasureActualDeadline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason int
	}{
		{"stream_credit", waitStreamWindow},
		{"session_credit", waitSessionWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := rev2Fixture()
			st := rev2Stream(s, 77)
			if tc.reason == waitStreamWindow {
				st.peerLimit = 0
			} else {
				s.credit.peerLimit = 0
			}
			if err := st.SetWriteDeadline(time.Now().Add(25 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			n, err := st.Write(make([]byte, MaxPayload))
			if n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("blocked write = %d, %v", n, err)
			}
			st.s.mu.Lock()
			streamWait := st.diagWriteWaits[tc.reason]
			sessionWait := s.credit.waits[tc.reason]
			active := st.writeWaiting
			st.s.mu.Unlock()
			if active || streamWait.Count != 1 || sessionWait.Count != 1 || streamWait.NS == 0 || streamWait.NS != sessionWait.NS {
				t.Fatalf("wait stats: stream=%+v session=%+v waiting=%v", streamWait, sessionWait, active)
			}
		})
	}
}

func TestStreamFlowSnapshotBoundedAndReasonSpecific(t *testing.T) {
	s, _ := rev2Fixture()
	now := time.Now()
	for i := 0; i < hotStreamSnapshotLimit+4; i++ {
		st := rev2Stream(s, uint64(i*2+1))
		st.txNext = uint64((i + 1) << 20)
		st.peerConsumed = st.txNext - 4096
		st.peerLimit = st.txNext + 12345
		st.lastActivity = now
	}
	hot := s.streams[1]
	hot.writeWaiting = true
	hot.writeWaitReason = waitStreamWindow
	hot.diagWriteWaitStarted = now.Add(-20 * time.Millisecond)
	hot.diagWriteWaits[waitStreamWindow] = creditWait{Count: 3, NS: 7500}
	hot.diagWriteWaits[waitQueueAdmission] = creditWait{Count: 2, NS: 8500}
	got := s.hotStreamSnapshotLocked(now)
	if len(got) != hotStreamSnapshotLimit {
		t.Fatalf("unbounded hot Stream snapshot: %d", len(got))
	}
	if got[0].ID != hot.id || !got[0].WriteWaiting || got[0].WaitReason != "stream_window" || got[0].CurrentWaitMS < 19 {
		t.Fatalf("missing blocked hot Stream: %+v", got[0])
	}
	if got[0].StreamRoom != 12345 || got[0].Unconsumed != 4096 {
		t.Fatalf("wrong credit snapshot: %+v", got[0])
	}
	if got[0].WriteWaits["stream_window"].Count != 3 || got[0].WriteWaits["queue_admission"].NS != 8500 {
		t.Fatalf("wrong wait reasons: %+v", got[0].WriteWaits)
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatal(err)
	}
	hot.writeWaiting = false
	hot.lastActivity = now.Add(-time.Minute)
	for _, stat := range s.hotStreamSnapshotLocked(now) {
		if stat.ID == hot.id {
			t.Fatal("idle old Stream obscured the active bulk sender")
		}
	}
}

func TestRouteAdmissionWakeRespectsRouteShare(t *testing.T) {
	s := routeSchedulingFixture(t, 1, 1)
	s.queueAdmissionLimit = 1 << 20
	st := routeTestQueue(t, s, 1, 1, 20, MaxPayload)
	routeTestQueue(t, s, 3, 2, 1, MaxPayload)
	st.open = true
	st.txNext = StreamWindow
	st.peerLimit = st.txNext + MaxStreamWindow/2
	cleanup := attachPendingWriter(s, st)
	defer cleanup()
	ch := s.beginWriterWaitLocked(st, waitQueueAdmission)
	defer s.endWriterWaitLocked(st)
	if room := s.routeAdmissionRoomLocked(st); room >= MaxPayload {
		t.Fatalf("fixture route is not blocked: room=%d", room)
	}
	if s.queueAdmissionCeilingLocked(st)-s.readyDataBytes-64 < MaxPayload {
		t.Fatal("fixture has no global room")
	}
	s.signalQueueAdmissionLocked(MaxPayload + 64)
	select {
	case <-ch:
		t.Fatal("signaled writer that was still blocked on route admission")
	default:
	}
	for i := 0; i < 20 && s.routeAdmissionRoomLocked(st) < MaxPayload; i++ {
		takeRouteFrame(t, s)
	}
	if room := s.routeAdmissionRoomLocked(st); room < MaxPayload {
		t.Fatalf("route did not free admission: %d", room)
	}
	s.signalQueueAdmissionLocked(MaxPayload + 64)
	select {
	case <-ch:
	default:
		t.Fatal("writable route was not woken after dispatch")
	}
}

func TestRoutedBackendReadBufferOptIn(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{
		{"", 0}, {"0", 0}, {"bad", 0}, {"64", 0},
		{"128", 128 << 10}, {"512", 512 << 10},
		{"4096", 4096 << 10}, {"4097", 0},
	} {
		t.Run("value_"+strings.ReplaceAll(tc.input, "/", "_"), func(t *testing.T) {
			t.Setenv("MPX_ROUTED_BACKEND_RCVBUF_KIB", tc.input)
			if got := routedBackendReceiveBufferBytes(); got != tc.want {
				t.Fatalf("configured buffer: got=%d want=%d", got, tc.want)
			}
		})
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv("MPX_ROUTED_BACKEND_RCVBUF_KIB", "1024")
	if !tuneRoutedBackendReadBuffer(server) {
		t.Fatal("TCP backend buffer opt-in failed")
	}
	t.Setenv("MPX_ROUTED_BACKEND_RCVBUF_KIB", "0")
	if tuneRoutedBackendReadBuffer(server) {
		t.Fatal("tuning should be disabled by default")
	}
}
