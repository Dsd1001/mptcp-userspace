package multipath

import (
	"errors"
	"fmt"
	"net"
	"time"
	"unicode/utf8"
)

const (
	mpx4ErrNoError              uint64 = 0x00
	mpx4ErrInternal             uint64 = 0x01
	mpx4ErrProtocolViolation    uint64 = 0x02
	mpx4ErrAuthenticationFailed uint64 = 0x03
	mpx4ErrVersionUnsupported   uint64 = 0x04
	mpx4ErrResourceLimit        uint64 = 0x05
	mpx4ErrSessionNotFound      uint64 = 0x06
	mpx4ErrSessionConflict      uint64 = 0x07
	mpx4ErrStreamLimit          uint64 = 0x08
	mpx4ErrFlowControl          uint64 = 0x09
	mpx4ErrFrameEncoding        uint64 = 0x0a
	mpx4ErrSchedulerMismatch    uint64 = 0x0b
	mpx4ErrCarrierConflict      uint64 = 0x0c
	mpx4ErrUnsupportedParameter uint64 = 0x0d
	mpx4ErrStreamState          uint64 = 0x0e
	mpx4ErrFinalSize            uint64 = 0x0f
	mpx4ErrTransmissionID       uint64 = 0x10
)

type mpx4FailureScope uint8

const (
	mpx4ScopeStreamOpening mpx4FailureScope = iota + 1
	mpx4ScopeCarrier
	mpx4ScopeSession
	mpx4ScopePreEstablishmentCarrier
)

type mpx4Failure struct {
	code    uint64
	trigger uint64
	scope   mpx4FailureScope
	reason  string
	cause   error
}

func (e *mpx4Failure) Error() string {
	if e == nil {
		return "MPX/4 failure"
	}
	if e.reason != "" {
		return fmt.Sprintf("MPX/4 error 0x%x: %s", e.code, e.reason)
	}
	return fmt.Sprintf("MPX/4 error 0x%x", e.code)
}

func (e *mpx4Failure) Unwrap() error {
	if e == nil || e.cause == nil {
		return ErrProtocol
	}
	return e.cause
}

func newSessionFailure(code uint64, reason string) error {
	return &mpx4Failure{code: code, scope: mpx4ScopeSession, reason: reason, cause: ErrProtocol}
}

func flowControlFailure(reason string) error { return newSessionFailure(mpx4ErrFlowControl, reason) }
func finalSizeFailure(reason string) error   { return newSessionFailure(mpx4ErrFinalSize, reason) }
func transmissionIDFailure(reason string) error {
	return newSessionFailure(mpx4ErrTransmissionID, reason)
}
func streamStateFailure(reason string) error { return newSessionFailure(mpx4ErrStreamState, reason) }
func protocolViolation(reason string) error {
	return newSessionFailure(mpx4ErrProtocolViolation, reason)
}

var ErrCarrierConflict = errors.New("MPX/4 carrier generation conflict")

func frameTypeForKind(kind byte) uint64 {
	switch kind {
	case kindPing:
		return mpx4FramePing
	case kindPong:
		return mpx4FramePong
	case kindCarrierClose:
		return mpx4FrameCarrierClose
	case kindSessionClose:
		return mpx4FrameSessionClose
	case kindOpen:
		return mpx4FrameStreamOpen
	case kindOpenOK:
		return mpx4FrameStreamOpenOK
	case kindOpenReject:
		return mpx4FrameStreamOpenReject
	case kindData:
		return mpx4FrameStreamData
	case kindACK:
		return mpx4FrameTransmissionACK
	case kindWindow:
		return mpx4FrameStreamCredit
	case kindFIN:
		return mpx4FrameStreamFIN
	case kindResetStream, kindRST:
		return mpx4FrameResetStream
	case kindStopReceiving:
		return mpx4FrameStopSending
	case kindFinalConsumed:
		return mpx4FrameStreamConsumed
	case kindTransmissionRetire:
		return mpx4FrameTransmissionRetire
	case kindSessionWindow:
		return mpx4FrameSessionCredit
	case kindCreditProbe:
		return mpx4FrameCreditProbe
	default:
		return 0
	}
}

func normalizeEstablishedFailure(err error, f frame) *mpx4Failure {
	var failure *mpx4Failure
	if errors.As(err, &failure) {
		out := *failure
		if out.trigger == 0 {
			out.trigger = frameTypeForKind(f.kind)
		}
		return &out
	}
	if errors.Is(err, ErrResourceLimit) {
		return &mpx4Failure{code: mpx4ErrResourceLimit, scope: mpx4ScopeSession, trigger: frameTypeForKind(f.kind), reason: err.Error(), cause: err}
	}
	return &mpx4Failure{code: mpx4ErrProtocolViolation, scope: mpx4ScopeSession, trigger: frameTypeForKind(f.kind), reason: err.Error(), cause: err}
}

func closeReason(reason string) []byte {
	b := []byte(reason)
	if len(b) > 256 {
		b = b[:256]
	}
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return b
}

func validOpenRejectCode(code uint64) bool {
	return code == mpx4ErrStreamLimit || code == mpx4ErrResourceLimit || code == mpx4ErrStreamState
}

func (s *Session) ensureCarrierStateMapsLocked() {
	if s.carrierUsed == nil {
		s.carrierUsed = make(map[uint64]bool)
	}
	if s.highestGeneration == nil {
		s.highestGeneration = make(map[uint64]uint64)
	}
	if s.nextCandidateGeneration == nil {
		s.nextCandidateGeneration = make(map[uint64]uint64)
	}
	if s.generationExhausted == nil {
		s.generationExhausted = make(map[uint64]bool)
	}
}

func (s *Session) validateCarrierGenerationLocked(id uint64, generation uint64) error {
	s.ensureCarrierStateMapsLocked()
	if id == 0 || id > mpx4VarIntMax || generation > mpx4VarIntMax {
		return ErrCarrierConflict
	}
	if !s.carrierUsed[id] {
		if generation != 0 {
			return ErrCarrierConflict
		}
		return nil
	}
	if generation <= s.highestGeneration[id] {
		return ErrCarrierConflict
	}
	return nil
}

func (s *Session) validateCarrierGeneration(id uint64, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSessionExpired
	}
	return s.validateCarrierGenerationLocked(id, generation)
}

func (s *Session) commitCarrierGenerationLocked(id uint64, generation uint64) error {
	if err := s.validateCarrierGenerationLocked(id, generation); err != nil {
		return err
	}
	s.carrierUsed[id] = true
	s.highestGeneration[id] = generation
	if generation == mpx4VarIntMax {
		s.generationExhausted[id] = true
		s.nextCandidateGeneration[id] = 0
	} else {
		s.generationExhausted[id] = false
		if s.nextCandidateGeneration[id] <= generation {
			s.nextCandidateGeneration[id] = generation + 1
		}
	}
	return nil
}

// nextCarrierCandidateGenerationLocked allocates a local candidate Generation
// without changing Highest Accepted Generation. Failed candidates may therefore
// consume/skips numbers, but never mutate accepted Session state.
func (s *Session) nextCarrierCandidateGenerationLocked(id uint64) (uint64, bool) {
	s.ensureCarrierStateMapsLocked()
	if id == 0 || id > mpx4VarIntMax {
		return 0, false
	}
	if !s.carrierUsed[id] {
		// The first accepted incarnation of an unused Carrier ID MUST be 0.
		return 0, true
	}
	if s.highestGeneration[id] == mpx4VarIntMax || s.generationExhausted[id] {
		return 0, false
	}
	next := s.nextCandidateGeneration[id]
	if next <= s.highestGeneration[id] {
		next = s.highestGeneration[id] + 1
	}
	if next > mpx4VarIntMax {
		s.generationExhausted[id] = true
		return 0, false
	}
	if next == mpx4VarIntMax {
		s.generationExhausted[id] = true
	} else {
		s.nextCandidateGeneration[id] = next + 1
	}
	return next, true
}

func (s *Session) protocolCarrierFailure(c *carrier, failure *mpx4Failure) {
	if failure == nil {
		failure = &mpx4Failure{code: mpx4ErrInternal, scope: mpx4ScopeCarrier, reason: "carrier failure", cause: ErrProtocol}
	}
	s.mu.Lock()
	writable := c != nil && c.active && !s.closed
	s.mu.Unlock()
	if writable {
		_ = c.conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
		_ = c.conn.writeFrame(frame{kind: kindCarrierClose, offset: failure.code, id: failure.trigger, data: closeReason(failure.reason)})
	}
	if c != nil {
		s.carrierFailure(c, failure)
	}
}

func (s *Session) protocolSessionFailure(preferred *carrier, failure *mpx4Failure) {
	if failure == nil {
		failure = &mpx4Failure{code: mpx4ErrInternal, scope: mpx4ScopeSession, reason: "session failure", cause: ErrProtocol}
	}
	var target *carrier
	s.mu.Lock()
	if !s.closed {
		if preferred != nil && preferred.active {
			target = preferred
		} else {
			for _, c := range s.paths {
				if c.active {
					target = c
					break
				}
			}
		}
	}
	s.mu.Unlock()
	if target != nil {
		_ = target.conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
		_ = target.conn.writeFrame(frame{kind: kindSessionClose, offset: failure.code, id: failure.trigger, data: closeReason(failure.reason)})
	}
	s.stop(failure)
}

func remoteCloseError(scope string, f frame) error {
	reason := string(f.data)
	if reason == "" {
		reason = "peer closed"
	}
	return fmt.Errorf("MPX/4 %s close code=0x%x trigger=0x%x: %s", scope, f.offset, f.id, reason)
}

func isLocalClosed(err error) bool {
	return errors.Is(err, net.ErrClosed)
}
