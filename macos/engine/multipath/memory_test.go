package multipath

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestReceivePagesSmallStreamsAndWrap(t *testing.T) {
	var id sessionID
	s := newSession(context.Background(), id, false, nil)
	defer s.Close()
	s.mu.Lock()
	st := s.newStreamLocked(1)
	st.open = true
	st.advertiseCreditLocked(time.Now())
	s.mu.Unlock()
	offset := uint64(0)
	for i := 0; i < 300; i++ {
		payload := bytes.Repeat([]byte{byte(i), 0, 255, byte(i >> 8)}, 2049)
		s.mu.Lock()
		err := st.receiveLocked(offset, payload)
		allocated := s.receiveAllocated
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if allocated > 2*receivePageCost {
			t.Fatalf("small stream reserved a whole window: %d", allocated)
		}
		got := make([]byte, len(payload))
		if _, err = io.ReadFull(st, got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatal("page boundary or window wrap corruption")
		}
		offset += uint64(len(payload))
		if stats := s.Snapshot(); stats.ReceiveAllocated != 0 || stats.BufferedBytes != 0 {
			t.Fatalf("consumed pages retained: %+v", stats)
		}
	}
}

// Valid flow-control commitments can still exhaust sparse physical pages.
func TestReceivePhysicalAllocationBudget(t *testing.T) {
	s, _ := rev2Fixture()
	exhausted := false
	for i := 0; i < MaxStreams; i++ {
		st := rev2Stream(s, uint64(i*2+1))
		err := st.receiveLocked(0, []byte{1})
		if err == nil {
			continue
		}
		if !errors.Is(err, ErrResourceLimit) || ResourceReason(err) != LimitReceiveAllocated {
			t.Fatal(err)
		}
		exhausted = true
		break
	}
	// 0.9.2 has enough page budget for all one-byte streams and one full
	// extra stream. Populate additional EXISTING identities, keeping each
	// logical range within its original stream and shared-credit bounds.
	for id := uint64(1); !exhausted && id <= uint64(2*MaxStreams-1); id += 2 {
		st := s.streams[id]
		for offset := MaxPayload; offset < MaxStreamWindow; offset += MaxPayload {
			err := st.receiveLocked(uint64(offset), []byte{1})
			if err == nil {
				continue
			}
			if !errors.Is(err, ErrResourceLimit) || ResourceReason(err) != LimitReceiveAllocated {
				t.Fatal(err)
			}
			exhausted = true
			break
		}
	}
	if s.receiveAllocated != (MaxBuffered/receivePageCost)*receivePageCost {
		t.Fatalf("did not reach exact physical page boundary: %d", s.receiveAllocated)
	}
	if !exhausted || s.receiveAllocated > MaxBuffered || s.receiveCredit > SessionCreditLimit {
		t.Fatal("physical budget not independently enforced")
	}
	for _, st := range s.streams {
		s.resetLocked(st, 1, true)
	}
	if s.receiveAllocated != 0 || s.bufferedBytes != 0 || s.receiveCredit != 0 {
		t.Fatal("physical/discard cleanup failed")
	}
}
