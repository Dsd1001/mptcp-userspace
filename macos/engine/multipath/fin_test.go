package multipath

import (
	"context"
	"testing"
	"time"
)

// A FIN can arrive on a fast carrier before queued DATA on another carrier.
// Its receipt ACK alone must not let Bridge close and discard pending bytes.
func TestCloseWriteWaitsForEarlierDataAfterFINACK(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.mu.Lock()
	st := s.newStreamLocked(1)
	st.open = true
	st.commitSendCreditLocked(3)
	data := s.queueLocked(frame{kind: kindData, stream: 1, data: []byte("abc")})
	s.mu.Unlock()
	defer st.Close()
	st.SetWriteDeadline(time.Now().Add(2 * time.Second))
	done := make(chan error, 1)
	go func() { done <- st.CloseWrite() }()
	var fin *outbound
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, p := range s.pending {
			if p.f.kind == kindFIN {
				fin = p
			}
		}
		s.mu.Unlock()
		if fin != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if fin == nil {
		t.Fatal("FIN was not queued")
	}
	path := schedulerPath(1)
	s.mu.Lock()
	s.ackLocked(path, frame{kind: kindACK, stream: 1, id: fin.f.id})
	s.mu.Unlock()
	select {
	case err := <-done:
		t.Fatalf("CloseWrite returned before earlier DATA receipt: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	s.mu.Lock()
	s.ackLocked(path, frame{kind: kindACK, stream: 1, id: data.f.id, offset: 3})
	s.mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseWrite did not finish after DATA and FIN receipts")
	}
}

func TestCloseWithPendingDataIsExplicitResetNotGracefulLoss(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.mu.Lock()
	st := s.newStreamLocked(1)
	st.open, st.writeFIN, st.finACK, st.hasFIN = true, true, true, true
	st.commitSendCreditLocked(3)
	s.queueLocked(frame{kind: kindData, stream: 1, data: []byte("abc")})
	s.mu.Unlock()
	st.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pending {
		if p.f.kind == kindResetStream && p.f.stream == 1 && p.f.offset == 3 && len(p.f.data) == 8 {
			return
		}
	}
	t.Fatal("pending DATA was silently dropped without informing the peer")
}
