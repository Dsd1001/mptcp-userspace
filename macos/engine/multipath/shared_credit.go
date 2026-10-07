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

// pendingBootstrapReserveLocked reserves pending capacity only for Streams that
// are actively trying to write and have not yet completed their first 32 KiB of
// peer-consumed DATA. It deliberately does not reserve for every admitted
// Stream: idle identities cost no pending capacity. exclude lets the current
// writer consume the same frame that carries its remaining bootstrap bytes.
func (s *Session) pendingBootstrapReserveLocked(exclude *Stream) (int, int) {
	frames := 0
	for e := s.writerReady.Front(); e != nil; e = e.Next() {
		st, _ := e.Value.(*Stream)
		if st == nil || st == exclude || st.closed || st.writeFIN || st.sendReset || st.writeRemaining <= 0 {
			continue
		}
		if !st.open || st.txNext-st.peerConsumed < StreamWindow {
			frames++
		}
	}
	frames = min(frames, MaxDataPending)
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

// A high offset proves commitment of the preceding range, even when its DATA
// is reordered. A repeated/overlapping DATA or final-size declaration adds zero.
func sharedGrowthRoom(used, growth int) int {
	bootstrapUsed := used - growth
	unusedBootstrap := max(0, BootstrapCreditLimit-bootstrapUsed)
	return max(0, GrowthCreditLimit+unusedBootstrap-growth)
}

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
	// Consumption releases aggregate Session credit for any writer. Wake only
	// enough shared-credit waiters to consume the newly freed frame budget;
	// FIFO writer-turn handoff is handled separately.
	s.signalSharedCreditLocked(n)
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
	n := min(st.writeRemaining, MaxPayload, int(min(uint64(MaxStreamWindow), st.peerLimit-st.txNext)), int(min(uint64(MaxPayload), fc.peerLimit-fc.txCommitted)))
	u := int(st.txNext - st.peerConsumed)
	baseRoom := max(0, StreamWindow-u)
	baseRoom = min(baseRoom, max(0, BootstrapCreditLimit-(fc.txUsed-fc.txGrowth)))
	// RC7 lets growth borrow bootstrap share that is not occupied by actual
	// DATA. SessionCreditLimit remains the aggregate hard ceiling.
	growthRoom := sharedGrowthRoom(fc.txUsed, fc.txGrowth)
	if baseRoom+growthRoom == 0 {
		if u < StreamWindow {
			return 0, waitBootstrap
		}
		return 0, waitGrowth
	}
	n = min(n, baseRoom+growthRoom, max(0, SessionCreditLimit-fc.txUsed))
	if s.dataPendingFrames >= MaxDataPending {
		return 0, waitPendingFrames
	}
	room := MaxDataPendingBytes - s.dataPendingBytes - 64
	if room <= 0 {
		return 0, waitPendingBytes
	}
	n = min(n, room)
	if n > baseRoom {
		growthFrames, growthBytes := s.growthPendingRoomLocked(st)
		if growthFrames <= 0 {
			n = min(n, baseRoom)
			if n == 0 {
				return 0, waitPendingFrames
			}
		} else if growthBytes < n+64 {
			n = min(n, max(baseRoom, max(0, growthBytes-64)))
			if n == 0 {
				return 0, waitPendingBytes
			}
		}
	}
	return n, waitNone
}

func (s *Session) writerTurnLocked(st *Stream) bool {
	count := s.writerReady.Len()
	if count <= 1 {
		return true
	}
	// Preserve the sliding bootstrap share even while growth is contended.
	if st.txNext-st.peerConsumed < StreamWindow {
		return true
	}
	// Do not serialize writers when every waiter can receive a full DATA turn.
	// DATA dispatch remains per-stream round-robin; scarce credit uses FIFO.
	growthFrames, growthBytes := s.growthPendingRoomLocked(nil)
	if sharedGrowthRoom(s.credit.txUsed, s.credit.txGrowth) >= count*MaxPayload &&
		growthFrames >= count && growthBytes >= count*(MaxPayload+64) {
		return true
	}
	// Legacy 0.10.x policy: once there is not enough room for every active
	// writer to receive one full DATA turn, serialize scarce growth credit in
	// FIFO order. The room calculation itself stays on the 1.0.x dynamic shared
	// pending/credit model rather than restoring the old static 64 MiB reserve.
	for e := s.writerReady.Front(); e != nil; e = e.Next() {
		other := e.Value.(*Stream)
		if n, _ := other.writeAllowanceLocked(); n > 0 {
			return other == st
		}
	}
	return false
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
