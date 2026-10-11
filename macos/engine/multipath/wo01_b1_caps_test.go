package multipath

import "testing"

func TestWO01B1GraduatedPressureCapsAndHerdSafety(t *testing.T) {
	s, _ := wo01B1Session()
	for i := 0; i < 12; i++ {
		id := uint64(1 + 2*i)
		s.streams[id] = &Stream{s: s, id: id}
	}
	if got := s.firstDataTokenCapLocked(); got != firstDataTokenCapNormal {
		t.Fatalf("idle 12 streams cap=%d", got)
	}
	s.readyDataBytes = 16 << 20
	if got := s.firstDataTokenCapLocked(); got != firstDataTokenCapBusy {
		t.Fatalf("busy 12 streams cap=%d", got)
	}
	s.initFirstDataTokensLocked()
	if s.firstDataTokens != firstDataTokenCapBusy {
		t.Fatalf("busy cap not enforced: %d", s.firstDataTokens)
	}
	for i := 12; i < 150; i++ {
		id := uint64(1 + 2*i)
		s.streams[id] = &Stream{s: s, id: id}
	}
	if got := s.firstDataTokenCapLocked(); got != firstDataTokenCapHerd {
		t.Fatalf("150 synchronized streams burst cap=%d", got)
	}
	s.initFirstDataTokensLocked()
	if s.firstDataTokens != firstDataTokenCapHerd {
		t.Fatalf("150 cap overflow: %d", s.firstDataTokens)
	}
	s.queueLocked(frame{kind: kindData, stream: 1, offset: 0, data: []byte{1}})
	if rq, token := s.firstDataCandidateLocked(); rq != nil || token != 0 {
		t.Fatal("herd guard allowed first DATA bypass")
	}
}
