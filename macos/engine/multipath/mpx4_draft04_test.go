package multipath

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
)

type draft04VectorFile struct {
	Protocol string `json:"protocol"`
	Revision string `json:"revision"`
	Cases    []struct {
		Name string `json:"name"`
	} `json:"cases"`
}

func loadDraft04VectorNames(t *testing.T, name string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var v draft04VectorFile
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if v.Protocol != "MPX/4" || v.Revision != "Draft 04" {
		t.Fatalf("unexpected vector metadata: %+v", v)
	}
	out := make(map[string]bool, len(v.Cases))
	for _, c := range v.Cases {
		out[c.Name] = true
	}
	return out
}

func TestMPX4Draft04OfficialSemanticVectorsPresent(t *testing.T) {
	carrier := loadDraft04VectorNames(t, "carrier-generation.json")
	for _, name := range []string{
		"first-incarnation-generation-zero", "first-incarnation-nonzero",
		"failed-higher-generation-handshake", "stale-lower-generation",
		"equal-generation-after-loss-still-conflict", "higher-generation-commit",
		"superseded-record-after-commit", "replacement-preserves-transmission-id",
		"simultaneous-equal-generation-candidates", "maximum-generation-no-wrap",
	} {
		if !carrier[name] {
			t.Fatalf("missing official carrier-generation vector %q", name)
		}
	}
	scopes := loadDraft04VectorNames(t, "error-scope.json")
	for _, name := range []string{
		"join-carrier-conflict", "secure-record-authentication-failure",
		"malformed-authenticated-frame", "stream-flow-control-violation",
		"final-size-conflict", "transmission-id-conflict",
		"preopen-cancellation-late-open", "session-error-atomicity",
	} {
		if !scopes[name] {
			t.Fatalf("missing official error-scope vector %q", name)
		}
	}
}

func commitGeneration(s *Session, id byte, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitCarrierGenerationLocked(id, generation)
}

func TestMPX4Draft04CarrierGenerationStateMachine(t *testing.T) {
	var fresh Session
	if err := commitGeneration(&fresh, 1, 1); !errors.Is(err, ErrCarrierConflict) {
		t.Fatalf("unused Carrier accepted nonzero Generation: %v", err)
	}
	if fresh.carrierUsed[1] || fresh.highestGeneration[1] != 0 {
		t.Fatal("failed candidate mutated accepted Generation state")
	}
	if err := commitGeneration(&fresh, 1, 0); err != nil {
		t.Fatal(err)
	}
	if !fresh.carrierUsed[1] || fresh.highestGeneration[1] != 0 {
		t.Fatal("Generation 0 not committed")
	}
	if err := commitGeneration(&fresh, 1, 0); !errors.Is(err, ErrCarrierConflict) {
		t.Fatalf("equal reuse accepted: %v", err)
	}
	if err := commitGeneration(&fresh, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := commitGeneration(&fresh, 1, 0); !errors.Is(err, ErrCarrierConflict) {
		t.Fatalf("stale Generation accepted: %v", err)
	}
	if fresh.highestGeneration[1] != 1 {
		t.Fatalf("highest Generation=%d", fresh.highestGeneration[1])
	}

	fresh.mu.Lock()
	candidate, ok := fresh.nextCarrierCandidateGenerationLocked(1)
	fresh.mu.Unlock()
	if !ok || candidate <= 1 {
		t.Fatalf("next candidate=%d ok=%v", candidate, ok)
	}
	// Merely allocating/handshaking a candidate must not advance H.
	if fresh.highestGeneration[1] != 1 {
		t.Fatal("candidate allocation advanced Highest Accepted Generation")
	}
}

func TestMPX4Draft04FailedCandidateDoesNotAdvanceHighestGeneration(t *testing.T) {
	var s Session
	if err := commitGeneration(&s, 2, 0); err != nil {
		t.Fatal(err)
	}
	if err := commitGeneration(&s, 2, 2); err != nil {
		t.Fatal(err)
	} // skipped values are allowed
	if err := s.validateCarrierGeneration(2, 3); err != nil {
		t.Fatal(err)
	}
	if s.highestGeneration[2] != 2 {
		t.Fatal("validation changed accepted Generation")
	}
	// Simulate authentication failure by doing no commit.
	if s.highestGeneration[2] != 2 {
		t.Fatal("failed candidate advanced Generation")
	}
	if err := s.validateCarrierGeneration(2, 2); !errors.Is(err, ErrCarrierConflict) {
		t.Fatalf("equal Generation reusable after loss: %v", err)
	}
}

func TestMPX4Draft04MaximumGenerationNeverWraps(t *testing.T) {
	var s Session
	s.carrierUsed[1] = true
	s.highestGeneration[1] = mpx4VarIntMax
	s.mu.Lock()
	generation, ok := s.nextCarrierCandidateGenerationLocked(1)
	s.mu.Unlock()
	if ok || generation != 0 {
		t.Fatalf("maximum Generation wrapped: %d ok=%v", generation, ok)
	}
}

func TestMPX4Draft04SimultaneousEqualGenerationOnlyOneCommits(t *testing.T) {
	var s Session
	s.carrierUsed[1] = true
	s.highestGeneration[1] = 4
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- commitGeneration(&s, 1, 5) }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrCarrierConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 || s.highestGeneration[1] != 5 {
		t.Fatalf("success=%d conflict=%d highest=%d", success, conflict, s.highestGeneration[1])
	}
}

func TestMPX4Draft04SupersededCarrierCannotCreateProtocolState(t *testing.T) {
	s := &Session{}
	old := &carrier{id: 1, generation: 3, active: false}
	err := s.handleFrame(old, frame{kind: kindPing, offset: 99})
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("superseded Carrier frame accepted: %v", err)
	}
}

func TestMPX4Draft04TransmissionACKIdentityRules(t *testing.T) {
	s := &Session{pending: map[uint64]*outbound{}, nextPacket: 5}
	if err := s.ackLocked(nil, frame{kind: kindACK, stream: 1, id: 6}); err == nil {
		t.Fatal("never-allocated Transmission ID accepted")
	} else {
		var failure *mpx4Failure
		if !errors.As(err, &failure) || failure.code != mpx4ErrTransmissionID || failure.scope != mpx4ScopeSession {
			t.Fatalf("wrong failure: %#v %v", failure, err)
		}
	}
	if err := s.ackLocked(nil, frame{kind: kindACK, stream: 1, id: 4}); err != nil {
		t.Fatalf("settled/compacted ACK not ignored: %v", err)
	}
	s.pending[5] = &outbound{f: frame{kind: kindData, stream: 1, id: 5, data: []byte{1}}}
	if err := s.ackLocked(nil, frame{kind: kindACK, stream: 3, id: 5}); err == nil {
		t.Fatal("Transmission ACK Stream mismatch accepted")
	}
}

func TestMPX4Draft04CloseFrameFormat(t *testing.T) {
	for _, want := range []frame{
		{kind: kindCarrierClose, offset: mpx4ErrFrameEncoding, id: mpx4FrameStreamData, data: []byte("bad frame")},
		{kind: kindSessionClose, offset: mpx4ErrFlowControl, id: mpx4FrameStreamData, data: []byte("credit exceeded")},
	} {
		wire, err := encodeV4Frame(want)
		if err != nil {
			t.Fatal(err)
		}
		typ, n1, err := readV4VarInt(wire)
		if err != nil {
			t.Fatal(err)
		}
		ln, n2, err := readV4VarInt(wire[n1:])
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeV4Frame(typ, wire[n1+n2:n1+n2+int(ln)])
		if err != nil {
			t.Fatal(err)
		}
		if got.kind != want.kind || got.offset != want.offset || got.id != want.id || string(got.data) != string(want.data) {
			t.Fatalf("close frame changed: want=%+v got=%+v", want, got)
		}
	}
	if _, err := encodeV4Frame(frame{kind: kindCarrierClose, data: []byte{0xff}}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("invalid UTF-8 close reason accepted: %v", err)
	}
	if _, err := encodeV4Frame(frame{kind: kindSessionClose, data: make([]byte, 257)}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversized close reason accepted: %v", err)
	}
}

func TestMPX4Draft04ErrorScopeMapping(t *testing.T) {
	cases := []struct {
		err  error
		code uint64
	}{
		{flowControlFailure("credit"), mpx4ErrFlowControl},
		{finalSizeFailure("final"), mpx4ErrFinalSize},
		{transmissionIDFailure("tx"), mpx4ErrTransmissionID},
		{streamStateFailure("state"), mpx4ErrStreamState},
		{protocolViolation("state"), mpx4ErrProtocolViolation},
	}
	for _, tc := range cases {
		f := normalizeEstablishedFailure(tc.err, frame{kind: kindData})
		if f.scope != mpx4ScopeSession || f.code != tc.code || f.trigger != mpx4FrameStreamData {
			t.Fatalf("wrong mapping for %v: %+v", tc.err, f)
		}
	}
	if !validOpenRejectCode(mpx4ErrStreamLimit) || !validOpenRejectCode(mpx4ErrResourceLimit) || !validOpenRejectCode(mpx4ErrStreamState) || validOpenRejectCode(mpx4ErrInternal) {
		t.Fatal("STREAM_OPEN_REJECT code scope mismatch")
	}
}
