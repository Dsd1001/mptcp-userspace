package multipath

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

const maxTerminalStreams = 8192

type terminalStream struct {
	txFinal, rxFinal, rxLimit uint64
}

func resetPayload(code uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, code)
	return b
}

func resetError(code uint64) error {
	if code == 4 {
		return &ResourceLimitError{Reason: LimitRemote}
	}
	return fmt.Errorf("MPX stream reset (code %d)", code)
}

func (s *Session) rememberTerminalLocked(id uint64, t terminalStream) {
	if s.terminal == nil {
		s.terminal = make(map[uint64]terminalStream)
	}
	if _, exists := s.terminal[id]; !exists {
		if len(s.terminalOrder) < maxTerminalStreams {
			s.terminalOrder = append(s.terminalOrder, id)
		} else {
			delete(s.terminal, s.terminalOrder[s.terminalCursor])
			s.terminalOrder[s.terminalCursor] = id
			s.terminalCursor = (s.terminalCursor + 1) % maxTerminalStreams
		}
	}
	s.terminal[id] = t
	s.rememberStreamLocked(id)
}

func (s *Session) tryRetireStreamLocked(st *Stream) {
	if st == nil || !st.closed || !st.hasFIN || st.rxRead != st.rxFIN || st.peerConsumed != st.txNext {
		return
	}
	if !st.receiveStopped && !st.finalConsumedQueued {
		return
	}
	if st.sendReset {
		if !st.sendResetACK {
			return
		}
	} else if !st.writeFIN || !st.finACK {
		return
	}
	for _, p := range s.pending {
		if p.f.stream == st.id {
			return
		}
	}
	delete(s.closing, st.id)
	s.rememberTerminalLocked(st.id, terminalStream{st.txNext, st.rxFIN, st.rxLimit})
	s.wakeLocked()
}

func (st *Stream) discardReceiveLocked() {
	old := st.rxRead
	st.releaseReceiveLocked() // physical pages only; never invent a peer final size
	st.rxRead = st.rxHigh
	st.rxContiguous = st.rxHigh
	st.releaseReadCreditLocked(old)
	st.advertiseConsumedLocked(nil)
}

func (s *Session) ensureTerminationControlsLocked(st *Stream) {
	if s.closed {
		return
	}
	if st.sendReset && !st.sendResetQueued && !st.sendResetACK {
		if p := s.queueLocked(frame{kind: kindResetStream, stream: st.id, offset: st.txNext, data: resetPayload(st.sendResetCode)}); p != nil {
			st.sendResetQueued = true
		}
	}
	if st.receiveStopped && !st.hasFIN && !st.stopQueued {
		if p := s.queueLocked(frame{kind: kindStopReceiving, stream: st.id, offset: st.stopCode}); p != nil {
			st.stopQueued = true
		}
	}
}

func (s *Session) resetSendLocked(st *Stream, code uint64) {
	if !st.sendReset {
		st.sendReset, st.sendResetCode = true, code
		// txNext was debited at commitment. Canceling payload transmission does
		// NOT refund it: only authenticated peer consumption/RESET ACK can.
		for _, p := range s.pending {
			if p.f.stream == st.id && (p.f.kind == kindData || p.f.kind == kindFIN || p.f.kind == kindOpen) {
				s.removePendingLocked(p)
			}
		}
	}
	s.ensureTerminationControlsLocked(st)
	s.wakeLocked()
}

func (s *Session) stopReceiveLocked(st *Stream, code uint64) {
	if !st.receiveStopped {
		st.receiveStopped, st.stopCode = true, code
		st.readErr = resetError(code)
	}
	st.discardReceiveLocked()
	s.ensureTerminationControlsLocked(st)
	s.wakeLocked()
}

// resetLocked preserves the existing application-level full-abort entry point,
// but the wire transition is two independent directional operations in rev2.
func (s *Session) resetLocked(st *Stream, code uint64, send bool) {
	if st.closed {
		return
	}
	st.closed = true
	st.err = resetError(code)
	s.closedStreams++
	s.eventLocked("stream_closed", "reset", 0, st.id)
	delete(s.streams, st.id)
	if s.closing == nil {
		s.closing = make(map[uint64]*Stream)
	}
	s.closing[st.id] = st
	if !send && st.txNext == 0 && st.rxHigh == 0 {
		// OPEN could not enter the reliable queue. No peer DATA permission or
		// commitment exists; no final size is being inferred for a live sender.
		st.hasFIN, st.sendReset, st.sendResetACK = true, true, true
		st.receiveStopped = true
		st.rxFIN = 0
		for _, p := range s.pending {
			if p.f.stream == st.id {
				s.removePendingLocked(p)
			}
		}
		st.releaseReceiveLocked()
		s.tryRetireStreamLocked(st)
	} else {
		s.resetSendLocked(st, code)
		s.stopReceiveLocked(st, code)
		s.tryRetireStreamLocked(st)
	}
	s.wakeLocked()
}

func (s *Session) handleResetStreamLocked(c *carrier, f frame) error {
	if f.id == 0 || len(f.data) != 8 {
		return ErrProtocol
	}
	code := binary.BigEndian.Uint64(f.data)
	if code < 1 || code > 4 {
		return ErrProtocol
	}
	st := s.streamForCreditLocked(f.stream)
	if st == nil {
		if term, ok := s.terminal[f.stream]; ok {
			if f.offset != term.rxFinal {
				return fmt.Errorf("%w: changed retired final size", ErrProtocol)
			}
			s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id})
			return nil
		}
		unknown := !s.seen[f.stream] && (s.maxSeen < 8192 || f.stream > s.maxSeen-8192)
		if unknown && !(s.server && f.offset == 0) {
			return ErrProtocol
		}
		if s.server && f.offset == 0 && unknown {
			// Cancellation can overtake OPEN on another carrier. Neither side
			// has sent DATA on an unaccepted stream; remember the zero final sizes
			// so a late OPEN cannot allocate a backend after cancellation.
			s.rememberTerminalLocked(f.stream, terminalStream{})
			s.queueTerminalResetLocked(f.stream, 0, code)
		}
		// Old retired identities have no credit to reclaim, even if their
		// bounded final-size record has aged out. Never recreate them here.
		s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id})
		return nil
	}
	if f.offset < st.rxHigh || f.offset < st.rxRead || f.offset > st.rxLimit || (st.hasFIN && f.offset != st.rxFIN) {
		return fmt.Errorf("%w: reset final size", ErrProtocol)
	}
	if err := st.receiveCommitLocked(f.offset); err != nil {
		return err
	}
	if !st.receivedReset && code == 4 {
		s.resourceLocked(LimitRemote, false)
	}
	st.receivedReset = true
	st.hasFIN, st.rxFIN = true, f.offset
	st.receiveStopped = true
	st.readErr = resetError(code)
	st.discardReceiveLocked()
	// All accounting is complete before this ACK can settle sender credit.
	s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id})
	if !st.open && !st.closed {
		s.resetLocked(st, code, true)
	}
	s.tryRetireStreamLocked(st)
	s.wakeLocked()
	return nil
}

func (s *Session) queueTerminalResetLocked(id, final, code uint64) {
	for _, p := range s.pending {
		if p.f.stream == id && p.f.kind == kindResetStream {
			return
		}
	}
	s.queueLocked(frame{kind: kindResetStream, stream: id, offset: final, data: resetPayload(code)})
}

func (s *Session) handleStopReceivingLocked(c *carrier, f frame) error {
	if f.id == 0 || f.offset < 1 || f.offset > 4 || len(f.data) != 0 {
		return ErrProtocol
	}
	if st := s.streamForCreditLocked(f.stream); st != nil {
		s.resetSendLocked(st, f.offset)
	} else if term, ok := s.terminal[f.stream]; ok {
		s.queueTerminalResetLocked(f.stream, term.txFinal, f.offset)
	} else if s.server && !s.seen[f.stream] && (s.maxSeen < 8192 || f.stream > s.maxSeen-8192) {
		// STOP overtook a canceled, not-yet-accepted OPEN. Its sender is still
		// required to independently send its own RESET final size.
		if len(s.streams)+len(s.closing) < MaxStreams {
			st := s.newStreamLocked(f.stream)
			s.resetLocked(st, f.offset, true)
		}
	}
	s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id})
	return nil
}

func (s *Session) handleFinalLocked(c *carrier, f frame) error {
	if f.id == 0 {
		return ErrProtocol
	}
	st := s.streamForCreditLocked(f.stream)
	if st == nil {
		if term, ok := s.terminal[f.stream]; ok {
			if f.offset != term.rxFinal {
				return fmt.Errorf("%w: changed retired FIN size", ErrProtocol)
			}
			s.controlLocked(c, frame{kind: kindWindow, stream: f.stream, offset: term.rxFinal, id: term.rxLimit})
		}
		s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id})
		return nil
	}
	if f.offset < st.rxHigh || f.offset < st.rxRead || f.offset > st.rxLimit || (st.hasFIN && f.offset != st.rxFIN) {
		return fmt.Errorf("%w: FIN final size", ErrProtocol)
	}
	if err := st.receiveCommitLocked(f.offset); err != nil {
		return err
	}
	st.hasFIN, st.rxFIN = true, f.offset
	if st.receiveStopped || st.closed {
		st.discardReceiveLocked()
	} else if st.rxRead == st.rxFIN {
		st.advertiseConsumedLocked(c)
		s.ensureFinalConsumedLocked(st)
	}
	s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id, offset: uint64(time.Since(s.clockStart))})
	s.tryRetireStreamLocked(st)
	s.wakeLocked()
	return nil
}

func (s *Session) handleCreditProbeLocked(c *carrier, f frame) error {
	if f.id != 0 || f.offset != 0 || len(f.data) != 0 {
		return ErrProtocol
	}
	if st := s.streamForCreditLocked(f.stream); st != nil {
		st.advertiseConsumedLocked(c)
	} else if term, ok := s.terminal[f.stream]; ok {
		s.controlLocked(c, frame{kind: kindWindow, stream: f.stream, offset: term.rxFinal, id: term.rxLimit})
	}
	s.advertiseSessionCreditLocked(time.Now(), true)
	return nil
}

func (s *Session) sweepClosingLocked(now time.Time) {
	for _, st := range s.closing {
		s.ensureTerminationControlsLocked(st)
		s.ensureFinalConsumedLocked(st)
		if now.Sub(st.windowAt) >= time.Second {
			st.advertiseConsumedLocked(nil)
			s.controlLocked(nil, frame{kind: kindCreditProbe, stream: st.id})
		}
		s.tryRetireStreamLocked(st)
	}
	for _, st := range s.streams {
		s.ensureFinalConsumedLocked(st)
		if st.sendReset || st.receiveStopped {
			s.ensureTerminationControlsLocked(st)
		}
	}
}

// CloseRead is an explicit directional cancellation, unlike a full Close.
func (st *Stream) CloseRead() error {
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.closed {
		return net.ErrClosed
	}
	s.stopReceiveLocked(st, 1)
	return nil
}

// A terminal record may be evicted only after the peer has reliably learned
// final consumption. WINDOW remains a best-effort fast path; this one-shot
// control prevents a lost last WINDOW from stranding credit beyond tombstone age.
func (s *Session) ensureFinalConsumedLocked(st *Stream) {
	if s.closed || st.receiveStopped || !st.hasFIN || st.rxRead != st.rxFIN || st.finalConsumedQueued {
		return
	}
	if p := s.queueLocked(frame{kind: kindFinalConsumed, stream: st.id, offset: st.rxFIN}); p != nil {
		st.finalConsumedQueued = true
	}
}
func (s *Session) handleFinalConsumedLocked(c *carrier, f frame) error {
	if f.id == 0 || len(f.data) != 0 {
		return ErrProtocol
	}
	if st := s.streamForCreditLocked(f.stream); st != nil {
		if (!st.writeFIN && !st.sendReset) || f.offset != st.txNext {
			return ErrProtocol
		}
		if err := st.releaseSendCreditLocked(f.offset); err != nil {
			return err
		}
		s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id})
		s.tryRetireStreamLocked(st)
	} else {
		if term, ok := s.terminal[f.stream]; ok && f.offset != term.txFinal {
			return ErrProtocol
		}
		s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id})
	}
	return nil
}
