package multipath

import (
	"fmt"
	"time"
)

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
	sharedWindowBatch = 128 << 10
	// Do not allow ordinary growth frames to consume the pending storage
	// required for one full bootstrap frame per admitted stream.
	growthPendingFrames = MaxDataPending - MaxStreams
	growthPendingBytes  = MaxDataPendingBytes - BootstrapCreditLimit - 64*MaxStreams
)

func (s *Session) initCreditLocked() {
	s.credit.rxLimit = SessionCreditLimit
	if s.seen == nil {
		s.seen = make(map[uint64]bool)
	}
	s.closing = make(map[uint64]*Stream)
	s.terminal = make(map[uint64]terminalStream)
}

func (s *Session) advertiseSessionCreditLocked(now time.Time, force bool) {
	fc := &s.credit
	if s.closed {
		return
	}
	if !force && now.Sub(fc.windowAt) < time.Second && fc.rxConsumed-fc.windowConsumed < sharedWindowBatch && fc.rxLimit-fc.rxCommitted > SessionCreditLimit/2 {
		return
	}
	if fc.rxConsumed > ^uint64(0)-SessionCreditLimit {
		return // no wrap; exhaustion cannot grant more credit
	}
	fc.rxLimit = max(fc.rxLimit, fc.rxConsumed+SessionCreditLimit)
	s.controlLocked(nil, frame{kind: kindSessionWindow, offset: fc.rxConsumed, id: fc.rxLimit})
	fc.windowAt, fc.windowConsumed = now, fc.rxConsumed
}

func (s *Session) receiveSessionCreditLocked(f frame) error {
	fc := &s.credit
	if f.stream != 0 || len(f.data) != 0 || f.id < f.offset || f.id-f.offset > SessionCreditLimit || f.offset > fc.txCommitted {
		return fmt.Errorf("%w: invalid session credit", ErrProtocol)
	}
	fc.peerConsumed = max(fc.peerConsumed, f.offset)
	fc.peerLimit = max(fc.peerLimit, f.id)
	s.wakeLocked()
	return nil
}

// A high offset proves commitment of the preceding range, even when its DATA
// is reordered. A repeated/overlapping DATA or final-size declaration adds zero.
func (st *Stream) receiveCommitLocked(end uint64) error {
	s, fc := st.s, &st.s.credit
	if end > st.rxLimit || (st.hasFIN && end > st.rxFIN) {
		return fmt.Errorf("%w: stream credit/final size", ErrProtocol)
	}
	if end <= st.rxHigh {
		return nil
	}
	delta := end - st.rxHigh
	if fc.rxCommitted > fc.rxLimit || delta > fc.rxLimit-fc.rxCommitted {
		return fmt.Errorf("%w: session MAX_DATA exceeded", ErrProtocol)
	}
	old := int(st.rxHigh - st.rxRead)
	newGrowth := s.receiveGrowth + growthOf(old+int(delta)) - growthOf(old)
	newUsed := s.receiveCredit + int(delta)
	if newGrowth > GrowthCreditLimit || newUsed-newGrowth > BootstrapCreditLimit || newUsed > SessionCreditLimit {
		return fmt.Errorf("%w: actual DATA pool exceeded", ErrProtocol)
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
		return fmt.Errorf("%w: consumption beyond committed DATA", ErrProtocol)
	}
	if consumed <= st.peerConsumed {
		return nil
	}
	s := st.s
	n := int(consumed - st.peerConsumed)
	old := int(st.txNext - st.peerConsumed)
	growth := growthOf(old) - growthOf(old-n)
	if n > s.credit.txUsed || growth > s.credit.txGrowth {
		return fmt.Errorf("%w: inconsistent consumption ledger", ErrProtocol)
	}
	s.credit.txUsed -= n
	s.credit.txGrowth -= growth
	st.peerConsumed = consumed
	s.wakeLocked()
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
	growthRoom := max(0, GrowthCreditLimit-fc.txGrowth)
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
		if s.dataPendingFrames >= growthPendingFrames {
			n = min(n, baseRoom)
			if n == 0 {
				return 0, waitPendingFrames
			}
		} else if s.dataPendingBytes+n+64 > growthPendingBytes {
			n = min(n, max(baseRoom, max(0, growthPendingBytes-s.dataPendingBytes-64)))
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
	if GrowthCreditLimit-s.credit.txGrowth >= count*MaxPayload &&
		growthPendingBytes-s.dataPendingBytes >= count*(MaxPayload+64) &&
		growthPendingFrames-s.dataPendingFrames >= count {
		return true
	}
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
