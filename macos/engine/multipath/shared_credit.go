package multipath

import "time"

// Rev2 charges offset commitment, not WINDOW entitlement. These counters are
// directional and protected by Session.mu. Retransmission never calls commit.
type connectionCredit struct {
	txCommitted, peerConsumed, peerLimit uint64
	rxCommitted, rxConsumed, rxLimit     uint64
	txUsed, txGrowth                     int
	windowAt                             time.Time
	windowConsumed                       uint64
	peakTX, peakRX, peakGrowth           int
	waits                                [8]creditWait
}

type creditWait struct {
	Count uint64 `json:"count"`
	NS    uint64 `json:"total_ns"`
}

const (
	waitNone = iota
	waitStreamWindow
	waitSessionWindow
	waitBootstrap
	waitGrowth
	waitPendingFrames
	waitPendingBytes
	waitWriterTurn
)

var creditWaitNames = [...]string{"none", "stream_window_or_open", "session_window", "bootstrap", "growth", "pending_frames", "pending_bytes", "writer_turn"}

const (
	sharedWindowBatch          = 128 << 10
	sessionRefillStartPressure = 70
	sessionRefillStopPressure  = 95
)

// bootstrapReserveEligibleLocked defines the exact state that consumes one
// pending bootstrap reserve slot. The state is maintained incrementally so the
// write/ACK hot path never rescans writerReady.
func (s *Session) bootstrapReserveEligibleLocked(st *Stream) bool {
	return st != nil && st.writeEntry != nil && !st.closed && !st.writeFIN && !st.sendReset &&
		st.writeRemaining > 0 && (!st.open || st.txNext-st.peerConsumed < StreamWindow)
}

func (s *Session) syncBootstrapReserveLocked(st *Stream) {
	active := s.bootstrapReserveEligibleLocked(st)
	if active == st.bootstrapReserved {
		return
	}
	if active {
		s.bootstrapWriters++
	} else if s.bootstrapWriters > 0 {
		s.bootstrapWriters--
	}
	st.bootstrapReserved = active
}

func (s *Session) setWriterRemainingLocked(st *Stream, n int) {
	st.writeRemaining = n
	s.syncBootstrapReserveLocked(st)
}

// pendingBootstrapReserveLocked is O(1). exclude lets the current bootstrap
// writer consume the frame that carries its own remaining bootstrap bytes.
func (s *Session) pendingBootstrapReserveLocked(exclude *Stream) (int, int) {
	frames := s.bootstrapWriters
	if exclude != nil && exclude.bootstrapReserved {
		frames--
	}
	frames = min(max(0, frames), MaxDataPending)
	return frames, min(MaxDataPendingBytes, frames*(MaxPayload+64))
}

func (s *Session) growthPendingRoomLocked(exclude *Stream) (int, int) {
	reserveFrames, reserveBytes := s.pendingBootstrapReserveLocked(exclude)
	return max(0, MaxDataPending-reserveFrames-s.dataPendingFrames),
		max(0, MaxDataPendingBytes-reserveBytes-s.dataPendingBytes)
}

func (s *Session) initCreditLocked() {
	s.credit.rxLimit = SessionCreditLimit
	if s.seen == nil {
		s.seen = make(map[uint64]bool)
	}
	s.closing = make(map[uint64]*Stream)
	s.terminal = make(map[uint64]terminalStream)
}

func (s *Session) sessionRefillTargetLocked() int {
	pressure := s.receivePressurePercentLocked()
	switch {
	case pressure <= sessionRefillStartPressure:
		return SessionCreditLimit
	case pressure >= sessionRefillStopPressure:
		return 0
	default:
		// Linear governor: 128 MiB target headroom at 70% pressure, tapering
		// continuously to zero additional refill at 95%. Already advertised
		// absolute credit is never revoked.
		return SessionCreditLimit * (sessionRefillStopPressure - pressure) /
			(sessionRefillStopPressure - sessionRefillStartPressure)
	}
}

func (s *Session) advertiseSessionCreditLocked(now time.Time, force bool) {
	fc := &s.credit
	if s.closed {
		return
	}
	target := s.sessionRefillTargetLocked()
	oldLimit := fc.rxLimit
	if target > 0 && fc.rxConsumed <= ^uint64(0)-uint64(target) {
		fc.rxLimit = max(fc.rxLimit, fc.rxConsumed+uint64(target))
	}
	limitChanged := fc.rxLimit != oldLimit
	consumedChanged := fc.rxConsumed-fc.windowConsumed >= sharedWindowBatch
	if !force && !limitChanged && !consumedChanged && now.Sub(fc.windowAt) < time.Second {
		return
	}
	// Even when pressure prevents a larger limit, transmit the newer consumed
	// offset periodically so the peer can retire historic Session commitment.
	s.controlLocked(nil, frame{kind: kindSessionWindow, offset: fc.rxConsumed, id: fc.rxLimit})
	fc.windowAt, fc.windowConsumed = now, fc.rxConsumed
}

func (s *Session) receiveSessionCreditLocked(f frame) error {
	fc := &s.credit
	if f.stream != 0 || len(f.data) != 0 || f.id < f.offset || f.id-f.offset > SessionCreditLimit || f.offset > fc.txCommitted {
		return flowControlFailure("invalid session credit")
	}
	oldConsumed, oldMaximum := fc.peerConsumed, fc.peerLimit
	switch {
	case f.offset >= oldConsumed && f.id >= oldMaximum:
		oldRoom := uint64(0)
		if oldMaximum > fc.txCommitted {
			oldRoom = oldMaximum - fc.txCommitted
		}
		fc.peerConsumed, fc.peerLimit = f.offset, f.id
		newRoom := uint64(0)
		if fc.peerLimit > fc.txCommitted {
			newRoom = fc.peerLimit - fc.txCommitted
		}
		if newRoom > oldRoom {
			s.signalSessionCreditLocked(int(newRoom - oldRoom))
		}
		return nil
	case f.offset <= oldConsumed && f.id <= oldMaximum:
		// Fully stale/duplicate credit is safe under cross-Carrier reordering.
		return nil
	default:
		return flowControlFailure("crossed Session credit advertisement")
	}
}

// txUsed/txGrowth below are diagnostic mirrors of per-Stream consumed
// progress. They are deliberately NOT sender admission limits: MPX/4 peer
// STREAM_WINDOW and SESSION_WINDOW are the authoritative send-credit gates.
// A peer can advance aggregate Session consumption ahead of one Stream's
// WINDOW replay, so these diagnostics may temporarily exceed 128 MiB without
// implying a protocol flow-control violation.

func (st *Stream) receiveCommitLocked(end uint64) error {
	s, fc := st.s, &st.s.credit
	if st.hasFIN && end > st.rxFIN {
		return finalSizeFailure("Stream DATA exceeds established final size")
	}
	if end > st.rxLimit {
		return flowControlFailure("Stream DATA exceeds advertised credit")
	}
	if end <= st.rxHigh {
		return nil
	}
	delta := end - st.rxHigh
	if fc.rxCommitted > fc.rxLimit || delta > fc.rxLimit-fc.rxCommitted {
		return flowControlFailure("Session DATA exceeds advertised credit")
	}
	old := int(st.rxHigh - st.rxRead)
	newGrowth := s.receiveGrowth + growthOf(old+int(delta)) - growthOf(old)
	newUsed := s.receiveCredit + int(delta)
	bootstrapUsed := newUsed - newGrowth
	// RC7 keeps the historic 64/64 MiB labels for accounting, but unused
	// bootstrap share is borrowable by active growth. The only effective DATA
	// ceiling is the 128 MiB Session hard limit; bootstrap itself can never
	// exceed 64 MiB because MaxStreams*StreamWindow is bounded.
	borrowedGrowthLimit := GrowthCreditLimit + max(0, BootstrapCreditLimit-bootstrapUsed)
	if bootstrapUsed > BootstrapCreditLimit || newGrowth > borrowedGrowthLimit || newUsed > SessionCreditLimit {
		return flowControlFailure("Session committed-byte accounting exceeds advertised limit")
	}
	fc.rxCommitted += delta
	st.rxHigh = end
	s.receiveCredit, s.receiveGrowth = newUsed, newGrowth
	fc.peakRX, fc.peakGrowth = max(fc.peakRX, newUsed), max(fc.peakGrowth, newGrowth)
	return nil
}

// rxRead has already advanced. Holes may be discarded only after local receive
// cancellation or a peer-authenticated final size; never as normal consumption.
func (st *Stream) releaseReadCreditLocked(oldRead uint64) {
	s := st.s
	if st.rxRead <= oldRead {
		return
	}
	n := int(st.rxRead - oldRead)
	old := int(st.rxHigh - oldRead)
	s.receiveCredit -= n
	s.receiveGrowth -= growthOf(old) - growthOf(old-n)
	s.credit.rxConsumed += uint64(n)
	s.advertiseSessionCreditLocked(time.Now(), false)
}

func (st *Stream) releaseSendCreditLocked(consumed uint64) error {
	if consumed > st.txNext {
		return flowControlFailure("peer consumption exceeds committed DATA")
	}
	if consumed <= st.peerConsumed {
		return nil
	}
	s := st.s
	n := int(consumed - st.peerConsumed)
	old := int(st.txNext - st.peerConsumed)
	growth := growthOf(old) - growthOf(old-n)
	if n > s.credit.txUsed || growth > s.credit.txGrowth {
		return flowControlFailure("peer consumption contradicts Session credit ledger")
	}
	s.credit.txUsed -= n
	s.credit.txGrowth -= growth
	st.peerConsumed = consumed
	s.syncBootstrapReserveLocked(st)
	return nil
}

func (st *Stream) commitSendCreditLocked(n int) {
	s := st.s
	old := int(st.txNext - st.peerConsumed)
	st.txNext += uint64(n)
	s.credit.txCommitted += uint64(n)
	s.credit.txUsed += n
	s.credit.txGrowth += growthOf(old+n) - growthOf(old)
	s.credit.peakTX = max(s.credit.peakTX, s.credit.txUsed)
	s.syncBootstrapReserveLocked(st)
}

func (st *Stream) writeAllowanceLocked() (int, int) {
	s := st.s
	if st.closed || st.writeFIN || st.sendReset || !st.open || st.peerLimit <= st.txNext {
		return 0, waitStreamWindow
	}
	fc := &s.credit
	if fc.peerLimit <= fc.txCommitted {
		return 0, waitSessionWindow
	}
	// MPX/4 peer-advertised Stream and Session WINDOWs are the only
	// flow-control authority on the send side. Do not add a second local
	// txUsed/growth ceiling: per-Stream consumed reports can lag the aggregate
	// SESSION_WINDOW and otherwise create a false 128 MiB head-of-line stall.
	n := min(st.writeRemaining, MaxPayload,
		int(min(uint64(MaxStreamWindow), st.peerLimit-st.txNext)),
		int(min(uint64(MaxPayload), fc.peerLimit-fc.txCommitted)))
	if s.dataPendingFrames >= MaxDataPending {
		return 0, waitPendingFrames
	}
	room := MaxDataPendingBytes - s.dataPendingBytes - 64
	if room <= 0 {
		return 0, waitPendingBytes
	}
	n = min(n, room)

	// Preserve only the local pending-capacity bootstrap reserve. This is not
	// flow control: it prevents established bulk Streams from occupying every
	// pending slot while a newly active Stream still needs its first frame.
	if st.txNext-st.peerConsumed >= StreamWindow {
		postBootstrapFrames, postBootstrapBytes := s.growthPendingRoomLocked(st)
		if postBootstrapFrames <= 0 {
			return 0, waitPendingFrames
		}
		if postBootstrapBytes < n+64 {
			n = min(n, max(0, postBootstrapBytes-64))
			if n == 0 {
				return 0, waitPendingBytes
			}
		}
	}
	return n, waitNone
}

func (s *Session) streamForCreditLocked(id uint64) *Stream {
	if st := s.streams[id]; st != nil {
		return st
	}
	return s.closing[id]
}

// Emit consumption even after application Close, without increasing an old
// entitlement. This is separate from the normal adaptive WINDOW grant.
func (st *Stream) advertiseConsumedLocked(c *carrier) {
	st.s.controlLocked(c, frame{kind: kindWindow, stream: st.id, offset: st.rxRead, id: st.rxLimit})
	st.windowAt = time.Now()
	st.windowSent = st.rxRead
}
