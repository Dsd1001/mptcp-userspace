package multipath

import (
	"container/list"
	"context"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"time"
)

type streamAddr string

func (a streamAddr) Network() string { return "mpx4" }
func (a streamAddr) String() string  { return string(a) }

// Receive storage uses bounded, lazily allocated 32 KiB pages. A larger flow
// window must not reserve a megabyte for every idle or one-byte logical stream.
const receivePageCost = 40 << 10 // includes data, bitmap and allocator rounding
const writeLockQuantumFrames = 8 // at most 256 KiB of DATA commit work per Session lock hold

type receivePage struct {
	data    [MaxPayload]byte
	present [MaxPayload / 64]uint64
	live    int
}

// Stream satisfies net.Conn and TCP-style CloseWrite. Both byte credit and
// actual allocated receive pages have independent hard bounds.
type Stream struct {
	receivedReset                                bool
	finalConsumedQueued                          bool
	writeEntry                                   *list.Element
	writeRemaining                               int
	bootstrapReserved                            bool
	openWake, readWake, writeWake                chan struct{}
	writeWaiting                                 bool
	writeWaitReason                              int
	sendReset, sendResetQueued, sendResetACK     bool
	sendResetCode                                uint64
	receiveStopped, stopQueued                   bool
	stopCode                                     uint64
	readErr                                      error
	s                                            *Session
	id                                           uint64
	readMu, writeMu, rxMu, rxOpMu                sync.Mutex
	open, closed                                 bool
	err                                          error
	openID                                       uint64
	txNext, peerConsumed, peerCreditConsumed     uint64
	peerLimit, rxLimit                           uint64
	windowTarget, initialWindow, readSampleBytes int
	warmTarget                                   int
	readRateBPS                                  float64
	createdAt, lastActivity                      time.Time
	readSampleAt, lastRead, warmAt               time.Time
	writeFIN, finACK                             bool
	rxRead, rxContiguous, rxHigh                 uint64
	hasFIN                                       bool
	rxFIN                                        uint64
	pages                                        map[uint64]*receivePage
	buffered                                     int
	readDeadline, writeDeadline                  time.Time
	windowAt                                     time.Time
	windowSent                                   uint64
	demandBytes                                  uint64
	productReplyKind                             byte
	productReplyPending                          bool
	productReplyDeadline                         time.Time
	productReplyErr                              error
	bulkActive                                   bool
	warmSeedUsed, warmHistoryUsed                bool
}

var _ net.Conn = (*Stream)(nil)

func (s *Session) newStreamLocked(id uint64) *Stream {
	now := time.Now()
	st := &Stream{s: s, id: id, windowTarget: StreamWindow, createdAt: now, lastActivity: now, readSampleAt: now, lastRead: now, openWake: make(chan struct{}, 1), readWake: make(chan struct{}, 1), writeWake: make(chan struct{}, 1)}
	s.streams[id] = st
	s.openedStreams++
	s.eventLocked("stream_created", "", 0, id)
	return st
}

func waitChange(ctx context.Context, ch <-chan struct{}, deadline time.Time) error {
	if deadline.IsZero() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
			return nil
		}
	}
	left := time.Until(deadline)
	if left <= 0 {
		return os.ErrDeadlineExceeded
	}
	timer := time.NewTimer(left)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch:
		return nil
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}

func waitTargetedChange(ctx context.Context, targeted, global <-chan struct{}, deadline time.Time) error {
	if deadline.IsZero() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-targeted:
			return nil
		case <-global:
			return nil
		}
	}
	left := time.Until(deadline)
	if left <= 0 {
		return os.ErrDeadlineExceeded
	}
	timer := time.NewTimer(left)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-targeted:
		return nil
	case <-global:
		return nil
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}

func (s *Session) Open(ctx context.Context) (*Stream, error) {
	return s.openWithAdmission(ctx)
}

type receiveWork struct {
	offset uint64
	data   []byte
	active bool
}

// prepareReceiveLocked reserves Session credit and physical page capacity.
// Session.mu must be held. On active work it returns with rxMu held; callers may
// then release Session.mu while the page copy/reassembly itself runs.
func (st *Stream) prepareReceiveLocked(offset uint64, data []byte) (receiveWork, error) {
	end := offset + uint64(len(data))
	if end < offset {
		return receiveWork{}, protocolViolation("Stream DATA offset overflow")
	}
	if st.hasFIN && end > st.rxFIN {
		return receiveWork{}, finalSizeFailure("Stream DATA exceeds established final size")
	}
	if end <= st.rxRead {
		return receiveWork{}, nil
	}
	if offset < st.rxRead {
		data = data[st.rxRead-offset:]
		offset = st.rxRead
	}
	oldHigh := st.rxHigh
	if err := st.receiveCommitLocked(end); err != nil {
		return receiveWork{}, err
	}
	if end > oldHigh {
		st.lastActivity = time.Now()
	}
	if st.receiveStopped {
		st.discardReceiveLocked()
		return receiveWork{}, nil
	}
	if len(data) == 0 {
		return receiveWork{}, nil
	}

	st.rxMu.Lock()
	if st.pages == nil {
		st.pages = make(map[uint64]*receivePage)
	}
	first := offset / MaxPayload
	last := (offset + uint64(len(data)) - 1) / MaxPayload
	missing := 0
	for index := first; index <= last; index++ {
		if st.pages[index] == nil {
			missing++
		}
	}
	if missing > (MaxBuffered-st.s.receiveAllocated)/receivePageCost {
		st.rxMu.Unlock()
		return receiveWork{}, st.s.resourceLocked(LimitReceiveAllocated, false)
	}
	for index := first; index <= last; index++ {
		if st.pages[index] == nil {
			st.pages[index] = &receivePage{}
			st.s.receiveAllocated += receivePageCost
		}
	}
	return receiveWork{offset: offset, data: data, active: true}, nil
}

// storeReceiveWorkLocked performs only Stream-local page work. rxMu must be
// held; Session.mu is intentionally not required.
func (st *Stream) storeReceiveWorkLocked(work receiveWork) (int, error) {
	offset, data := work.offset, work.data
	addedTotal := 0
	for len(data) > 0 {
		index := offset / MaxPayload
		start := int(offset % MaxPayload)
		page := st.pages[index]
		if page == nil {
			return addedTotal, protocolViolation("reserved receive page missing")
		}
		n := min(len(data), MaxPayload-start)
		added, err := page.store(start, data[:n])
		addedTotal += added
		if err != nil {
			return addedTotal, err
		}
		data = data[n:]
		offset += uint64(n)
	}
	return addedTotal, nil
}

// finishReceiveWorkLocked publishes Stream-local reassembly accounting after the
// page operation. Session.mu and rxMu must both be held.
func (st *Stream) finishReceiveWorkLocked(added int) {
	st.buffered += added
	st.s.bufferedBytes += added
	for st.rxContiguous < st.rxHigh {
		page := st.pages[st.rxContiguous/MaxPayload]
		if page == nil {
			break
		}
		pos := int(st.rxContiguous % MaxPayload)
		limit := min(MaxPayload, pos+int(st.rxHigh-st.rxContiguous))
		n := page.contiguous(pos, limit)
		st.rxContiguous += uint64(n)
		if pos+n < limit || n == 0 {
			break
		}
	}
	reordered := st.buffered - int(st.rxContiguous-st.rxRead)
	if reordered > st.s.reorderPeak {
		st.s.reorderPeak = reordered
	}
}

// receiveLocked preserves the synchronous helper used by tests and uncommon
// local paths. Production Carrier DATA uses the split prepare/store/finish path.
func (st *Stream) receiveLocked(offset uint64, data []byte) error {
	work, err := st.prepareReceiveLocked(offset, data)
	if err != nil || !work.active {
		return err
	}
	added, storeErr := st.storeReceiveWorkLocked(work)
	st.finishReceiveWorkLocked(added)
	st.rxMu.Unlock()
	return storeErr
}

// copyReceivePagesLocked copies an already-contiguous range without changing
// Stream or Session accounting. rxMu must be held; Session.mu deliberately need
// not be held.
func (st *Stream) copyReceivePagesLocked(dst []byte, offset uint64) int {
	copied := 0
	for copied < len(dst) {
		index := offset / MaxPayload
		pos := int(offset % MaxPayload)
		page := st.pages[index]
		if page == nil {
			break
		}
		part := min(len(dst)-copied, MaxPayload-pos)
		if page.contiguous(pos, pos+part) != part {
			break
		}
		copy(dst[copied:copied+part], page.data[pos:pos+part])
		copied += part
		offset += uint64(part)
	}
	return copied
}

// consumeReceivePagesLocked retires bytes that were copied earlier. Both
// Session.mu and rxMu must be held and rxRead must still equal offset.
func (st *Stream) consumeReceivePagesLocked(offset uint64, n int) (int, bool) {
	if st.rxRead != offset {
		return 0, false
	}
	freedPages := 0
	cleared := 0
	for cleared < n {
		index := offset / MaxPayload
		pos := int(offset % MaxPayload)
		page := st.pages[index]
		if page == nil {
			return freedPages, false
		}
		part := min(n-cleared, MaxPayload-pos)
		if page.contiguous(pos, pos+part) != part || page.live < part {
			return freedPages, false
		}
		page.clear(pos, pos+part)
		page.live -= part
		cleared += part
		offset += uint64(part)
		if page.live == 0 {
			delete(st.pages, index)
			freedPages++
		}
	}
	st.rxRead += uint64(n)
	st.buffered -= n
	return freedPages, true
}

func (st *Stream) armProductReply(kind byte, deadline time.Time) {
	st.readMu.Lock()
	st.productReplyKind = kind
	st.productReplyPending = true
	st.productReplyDeadline = deadline
	st.productReplyErr = nil
	st.readMu.Unlock()
}

func earlierDeadline(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

func (st *Stream) effectiveReadDeadlineLocked() time.Time {
	deadline := st.readDeadline
	if st.productReplyPending {
		deadline = earlierDeadline(deadline, st.productReplyDeadline)
	}
	return deadline
}

func (st *Stream) consumeProductReplyLocked() error {
	if st.productReplyErr != nil {
		return st.productReplyErr
	}
	if !st.productReplyPending {
		return nil
	}
	var response [6]byte
	for off := 0; off < len(response); {
		n, err := st.readRaw(response[off:])
		off += n
		if err != nil {
			st.productReplyPending = false
			st.productReplyErr = err
			st.Close()
			return err
		}
	}
	err := productServiceResponseError(st.productReplyKind, response[:])
	st.productReplyPending = false
	st.productReplyDeadline = time.Time{}
	st.productReplyErr = err
	if err != nil {
		st.Close()
	}
	return err
}

func (st *Stream) waitProductReply() error {
	st.readMu.Lock()
	defer st.readMu.Unlock()
	return st.consumeProductReplyLocked()
}

func (st *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	st.readMu.Lock()
	defer st.readMu.Unlock()
	if err := st.consumeProductReplyLocked(); err != nil {
		return 0, err
	}
	return st.readRaw(p)
}

func (st *Stream) readRaw(p []byte) (int, error) {
	s := st.s
	s.mu.Lock()
	for {
		if st.closed {
			err := st.err
			if err == nil {
				err = net.ErrClosed
			}
			s.mu.Unlock()
			return 0, err
		}
		if st.receiveStopped {
			err := st.readErr
			if err == nil {
				err = net.ErrClosed
			}
			s.mu.Unlock()
			return 0, err
		}
		deadline := st.effectiveReadDeadlineLocked()
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			s.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		available := int(st.rxContiguous - st.rxRead)
		if available > 0 {
			oldRead := st.rxRead
			n := min(len(p), available)

			// The expensive user-buffer copy is Stream-local. Freeze page
			// storage with rxMu, release Session.mu, copy, then briefly re-enter
			// the Session ledger to retire exactly the range we observed.
			st.rxMu.Lock()
			s.mu.Unlock()
			copied := st.copyReceivePagesLocked(p[:n], oldRead)
			st.rxMu.Unlock()
			if copied != n {
				s.mu.Lock()
				s.mu.Unlock()
				return 0, protocolViolation("contiguous receive storage missing")
			}

			s.mu.Lock()
			if st.closed || st.receiveStopped || st.rxRead != oldRead {
				err := st.err
				if st.receiveStopped && st.readErr != nil {
					err = st.readErr
				}
				if err == nil {
					err = net.ErrClosed
				}
				s.mu.Unlock()
				return 0, err
			}
			st.rxMu.Lock()
			freedPages, ok := st.consumeReceivePagesLocked(oldRead, n)
			st.rxMu.Unlock()
			if !ok {
				s.mu.Unlock()
				return 0, protocolViolation("receive storage changed before consume")
			}
			s.receiveAllocated -= freedPages * receivePageCost
			s.bufferedBytes -= n
			s.received += uint64(n)
			st.lastActivity = time.Now()
			st.releaseReadCreditLocked(oldRead)
			s.ensureFinalConsumedLocked(st)
			st.consumeCreditLocked(n, time.Now())
			s.kickLocked()
			s.mu.Unlock()
			return n, nil
		}
		if st.hasFIN && st.rxRead == st.rxFIN {
			st.advertiseConsumedLocked(nil)
			s.ensureFinalConsumedLocked(st)
			s.mu.Unlock()
			return 0, io.EOF
		}
		for len(st.readWake) > 0 {
			<-st.readWake
		}
		targeted, global := st.readWake, s.changed
		deadline = st.effectiveReadDeadlineLocked()
		s.mu.Unlock()
		if err := waitTargetedChange(s.ctx, targeted, global, deadline); err != nil {
			return 0, err
		}
		s.mu.Lock()
	}
}

func (st *Stream) Write(p []byte) (int, error) {
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	s := st.s
	s.mu.Lock()
	st.writeEntry = s.writerReady.PushBack(st)
	s.setWriterRemainingLocked(st, len(p))
	defer func() {
		s.endWriterWaitLocked(st)
		s.setWriterRemainingLocked(st, 0)
		s.writerReady.Remove(st.writeEntry)
		st.writeEntry = nil
		s.signalPendingWriterLocked()
		s.mu.Unlock()
	}()
	total := 0
	framesInHold := 0
	for len(p) > 0 {
		if st.closed {
			return total, st.err
		}
		if st.writeFIN || st.sendReset {
			return total, io.ErrClosedPipe
		}
		if !st.writeDeadline.IsZero() && !time.Now().Before(st.writeDeadline) {
			return total, os.ErrDeadlineExceeded
		}
		s.setWriterRemainingLocked(st, len(p))
		if !s.hasActiveCarrierLocked() {
			ch, deadline := s.changed, st.writeDeadline
			s.mu.Unlock()
			err := waitChange(s.ctx, ch, deadline)
			s.mu.Lock()
			if err != nil {
				return total, err
			}
			continue
		}
		n, reason := st.writeAllowanceLocked()
		if n > 0 {
			if st.txNext > ^uint64(0)-uint64(n) || s.credit.txCommitted > ^uint64(0)-uint64(n)-SessionCreditLimit {
				return total, ErrProtocol
			}
			data := append([]byte(nil), p[:n]...)
			if s.queueLocked(frame{kind: kindData, stream: st.id, offset: st.txNext, data: data}) == nil {
				return total, &ResourceLimitError{s.resources.LastReason}
			}
			st.commitSendCreditLocked(n)
			st.lastActivity = time.Now()
			s.sent += uint64(n)
			total += n
			p = p[n:]
			s.setWriterRemainingLocked(st, len(p))
			s.writerReady.MoveToBack(st.writeEntry)
			framesInHold++
			if framesInHold >= writeLockQuantumFrames && len(p) != 0 {
				// Large application writes must not monopolize Session.mu.
				// Release after a bounded DATA quantum so Carrier receipts,
				// dispatch and unrelated Streams can enter the ledger.
				framesInHold = 0
				s.mu.Unlock()
				runtime.Gosched()
				s.mu.Lock()
			}
			continue
		}
		s.windowWaits++
		blocked := reason == waitStreamWindow || reason == waitSessionWindow
		if blocked {
			s.windowBlockedWriters++
		}
		s.credit.waits[reason].Count++
		started := time.Now()
		deadline := st.writeDeadline
		targeted, global := s.beginWriterWaitLocked(st, reason), s.changed
		s.mu.Unlock()
		err := waitTargetedChange(s.ctx, targeted, global, deadline)
		s.mu.Lock()
		s.endWriterWaitLocked(st)
		s.credit.waits[reason].NS += uint64(time.Since(started))
		if blocked {
			s.windowBlockedWriters--
		}
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// The session mutex must be held. A FIN receipt does not cumulatively ACK
// DATA: another carrier may still have preceding payload queued or in flight.
func (st *Stream) hasPendingDataLocked() bool {
	for _, p := range st.s.pending {
		if p.f.stream == st.id && p.f.kind == kindData {
			return true
		}
	}
	return false
}

func (st *Stream) CloseWrite() error {
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if st.sendReset {
			return io.ErrClosedPipe
		}
		if st.closed {
			return st.err
		}
		if st.finACK && !st.hasPendingDataLocked() {
			return nil
		}
		if !st.writeDeadline.IsZero() && !time.Now().Before(st.writeDeadline) {
			return os.ErrDeadlineExceeded
		}
		if !st.writeFIN && st.open && s.controlPendingFrames < MaxControlPending && s.controlPendingBytes+64 <= MaxControlBytes {
			if s.queueLocked(frame{kind: kindFIN, stream: st.id, offset: st.txNext}) == nil {
				return &ResourceLimitError{s.resources.LastReason}
			}
			st.writeFIN = true
		}
		ch, deadline := s.changed, st.writeDeadline
		s.mu.Unlock()
		err := waitChange(s.ctx, ch, deadline)
		s.mu.Lock()
		if err != nil {
			return err
		}
	}
}

func (st *Stream) Close() error {
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.closed {
		return nil
	}
	if st.writeFIN && st.finACK && !st.hasPendingDataLocked() && st.hasFIN && st.rxRead == st.rxFIN {
		st.closed = true
		st.err = net.ErrClosed
		s.closedStreams++
		s.eventLocked("stream_closed", "graceful", 0, st.id)
		delete(s.streams, st.id)
		if s.closing == nil {
			s.closing = make(map[uint64]*Stream)
		}
		s.closing[st.id] = st
		st.releaseReceiveLocked()
		st.advertiseConsumedLocked(nil)
		s.ensureFinalConsumedLocked(st)
		s.tryRetireStreamLocked(st)
		s.wakeLocked()
		return nil
	}
	s.resetLocked(st, mpx4ErrNoError, true)
	return nil
}

// This is physical storage cleanup only. Credit settlement uses authenticated
// offsets, never the size of an old WINDOW entitlement.
func (st *Stream) releaseReceiveLocked() {
	st.rxMu.Lock()
	st.s.bufferedBytes -= st.buffered
	st.buffered = 0
	st.s.receiveAllocated -= len(st.pages) * receivePageCost
	st.pages = nil
	st.rxMu.Unlock()
}

func (st *Stream) LocalAddr() net.Addr  { return streamAddr("mpx4/local") }
func (st *Stream) RemoteAddr() net.Addr { return streamAddr("mpx4/peer") }
func (st *Stream) SetDeadline(t time.Time) error {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	st.readDeadline = t
	st.writeDeadline = t
	st.s.signalStreamReaderLocked(st)
	st.s.signalStreamWriterLocked(st)
	return nil
}
func (st *Stream) SetReadDeadline(t time.Time) error {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	st.readDeadline = t
	st.s.signalStreamReaderLocked(st)
	return nil
}
func (st *Stream) SetWriteDeadline(t time.Time) error {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	st.writeDeadline = t
	st.s.signalStreamWriterLocked(st)
	return nil
}
