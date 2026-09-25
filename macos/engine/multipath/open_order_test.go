package multipath

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// An authenticated backend may greet first. OPEN_OK and DATA can use different
// carriers, so the DATA/FIN may legitimately arrive before OPEN_OK. Admission
// succeeds only after an explicit prior WINDOW, never merely because of OPEN.
func TestServerGreetingBeforeOpenOK(t *testing.T) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.clockStart = time.Now()
	c := schedulerPath(1)
	s.paths[1] = c
	s.mu.Lock()
	st := s.newStreamLocked(1)
	// Explicitly model a WINDOW already advertised on a locally created opening stream.
	st.windowTarget = 64 << 10
	st.grantCreditLocked(64 << 10)
	opening := s.queueLocked(frame{kind: kindOpen, stream: 1})
	s.mu.Unlock()
	defer st.Close()
	greeting := bytes.Repeat([]byte{0, 255, 19, 77}, 9000)
	for offset := 0; offset < len(greeting); {
		n := min(MaxPayload, len(greeting)-offset)
		if err := s.handleFrame(c, frame{kind: kindData, stream: 1, id: uint64(900 + offset), offset: uint64(offset), data: greeting[offset : offset+n]}); err != nil {
			t.Fatal(err)
		}
		offset += n
	}
	s.mu.Lock()
	admitted := !st.closed && !st.open && st.buffered == len(greeting) && s.pending[opening.f.id] == opening
	s.mu.Unlock()
	if !admitted {
		t.Fatal("server-first DATA arriving before OPEN_OK was rejected or implicitly completed OPEN")
	}
	if err := s.handleFrame(c, frame{kind: kindFIN, stream: 1, id: 100000, offset: uint64(len(greeting))}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleFrame(c, frame{kind: kindOpenOK, stream: 1, id: opening.f.id}); err != nil {
		t.Fatal(err)
	}
	st.SetReadDeadline(time.Now().Add(time.Second))
	got, err := io.ReadAll(st)
	if err != nil || !bytes.Equal(got, greeting) {
		t.Fatalf("greeting or pre-OPEN_OK FIN lost: bytes=%d err=%v", len(got), err)
	}
}

func TestEarlyDataDoesNotBypassServerAcceptanceOrCreateStream(t *testing.T) {
	for _, mode := range []string{"unknown-client-stream", "backend-not-accepted"} {
		t.Run(mode, func(t *testing.T) {
			s := schedulerFixture()
			s.ctx = context.Background()
			c := schedulerPath(1)
			s.paths[1] = c
			if mode == "backend-not-accepted" {
				s.server = true
				s.mu.Lock()
				st := s.newStreamLocked(1)
				s.mu.Unlock()
				defer st.Close()
			}
			if err := s.handleFrame(c, frame{kind: kindData, stream: 1, id: 900, data: []byte("unaccepted")}); !errors.Is(err, ErrProtocol) {
				t.Fatal("unauthorized DATA did not fail closed", err)
			}
			if s.Snapshot().BufferedBytes != 0 || (mode == "unknown-client-stream" && len(s.streams) != 0) {
				t.Fatal("early DATA allocated unauthorized stream storage")
			}
		})
	}
}
