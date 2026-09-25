package multipath

import (
	"container/list"
	"context"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type streamAddr string

func (a streamAddr) Network() string { return "mpx3" }
func (a streamAddr) String() string  { return string(a) }

// Receive storage uses bounded, lazily allocated 32 KiB pages. A larger flow
// window must not reserve a megabyte for every idle or one-byte logical stream.
const receivePageCost = 40 << 10 // includes data, bitmap and allocator rounding

type receivePage struct {
	data    [MaxPayload]byte
	present [MaxPayload / 64]uint64
	live    int
}

// Stream satisfies net.Conn and TCP-style CloseWrite. Both byte credit and
// actual allocated receive pages have independent hard bounds.
type Stream struct {
	receivedReset                            bool
	finalConsumedQueued                      bool
	writeEntry                               *list.Element
	writeRemaining                           int
	sendReset, sendResetQueued, sendResetACK bool
	sendResetCode                            uint64
	receiveStopped, stopQueued               bool
	stopCode                                 uint64
	readErr                                  error
	s                                        *Session
	id                                       uint64
	writeMu                                  sync.Mutex
	open, closed                             bool
	err                                      error
	openID                                   uint64
	txNext, peerConsumed                     uint64
	peerLimit, rxLimit                       uint64
	windowTarget, readSampleBytes            int
	createdAt, lastActivity                  time.Time
	readSampleAt, lastRead                   time.Time
	writeFIN, finACK                         bool
	rxRead, rxContiguous, rxHigh             uint64
	hasFIN                                   bool
	rxFIN                                    uint64
	pages                                    map[uint64]*receivePage
	buffered                                 int
	readDeadline, writeDeadline              time.Time
	windowAt                                 time.Time
	windowSent                               uint64
	demandBytes                              uint64
	warmSeedUsed                             bool
}

var _ net.Conn = (*Stream)(nil)

func (s *Session) newStreamLocked(id uint64) *Stream {
	now := time.Now()
	st := &Stream{s: s, id: id, windowTarget: StreamWindow, createdAt: now, lastActivity: now, readSampleAt: now, lastRead: now}
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

func (s *Session) Open(ctx context.Context) (*Stream, error) {
	return s.openWithAdmission(ctx)
}

func (st *Stream) receiveLocked(offset uint64, data []byte) error {
	end := offset + uint64(len(data))
	if end < offset || (st.hasFIN && end > st.rxFIN) {
		return ErrProtocol
	}
	if end <= st.rxRead {
		return nil
	}
	if offset < st.rxRead {
		data = data[st.rxRead-offset:]
		offset = st.rxRead
	}
	oldHigh := st.rxHigh
	if err := st.receiveCommitLocked(end); err != nil {
		return err
	}
	if end > oldHigh {
		st.lastActivity = time.Now()
	}
	if st.receiveStopped {
		st.discardReceiveLocked()
		return nil
	}
	if st.pages == nil {
		st.pages = make(map[uint64]*receivePage)
	}
	for len(data) > 0 {
		index := offset / MaxPayload
		start := int(offset % MaxPayload)
		page := st.pages[index]
		if page == nil {
			if st.s.receiveAllocated+receivePageCost > MaxBuffered {
				return st.s.resourceLocked(LimitReceiveAllocated, false)
			}
			page = &receivePage{}
			st.pages[index] = page
			st.s.receiveAllocated += receivePageCost
		}
		n := min(len(data), MaxPayload-start)
		added, err := page.store(start, data[:n])
		if err != nil {
			return err
		}
		st.buffered += added
		st.s.bufferedBytes += added
		data = data[n:]
		offset += uint64(n)
	}
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
	return nil
}

func (st *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
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
		if !st.readDeadline.IsZero() && !time.Now().Before(st.readDeadline) {
			s.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		available := int(st.rxContiguous - st.rxRead)
		if available > 0 {
			oldRead := st.rxRead
			n := min(len(p), available)
			for copied := 0; copied < n; {
				index := st.rxRead / MaxPayload
				pos := int(st.rxRead % MaxPayload)
				page := st.pages[index]
				part := min(n-copied, MaxPayload-pos)
				copy(p[copied:copied+part], page.data[pos:pos+part])
				page.clear(pos, pos+part)
				page.live -= part
				copied += part
				st.rxRead += uint64(part)
				if page.live == 0 {
					delete(st.pages, index)
					s.receiveAllocated -= receivePageCost
				}
			}
			st.buffered -= n
			s.bufferedBytes -= n
			s.received += uint64(n)
			st.lastActivity = time.Now()
			st.releaseReadCreditLocked(oldRead)
			s.ensureFinalConsumedLocked(st)
			st.consumeCreditLocked(n, time.Now())
			s.wakeLocked()
			s.mu.Unlock()
			return n, nil
		}
		if st.hasFIN && st.rxRead == st.rxFIN {
			st.advertiseConsumedLocked(nil)
			s.ensureFinalConsumedLocked(st)
			s.mu.Unlock()
			return 0, io.EOF
		}
		ch, deadline := s.changed, st.readDeadline
		s.mu.Unlock()
		if err := waitChange(s.ctx, ch, deadline); err != nil {
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
	defer func() {
		s.writerReady.Remove(st.writeEntry)
		st.writeEntry = nil
		st.writeRemaining = 0
		s.wakeLocked()
		s.mu.Unlock()
	}()
	total := 0
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
		st.writeRemaining = len(p)
		n, reason := st.writeAllowanceLocked()
		if n > 0 && !s.writerTurnLocked(st) {
			n = 0
			reason = waitWriterTurn
		}
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
			st.writeRemaining = len(p)
			s.writerReady.MoveToBack(st.writeEntry)
			continue
		}
		s.windowWaits++
		blocked := reason >= waitStreamWindow && reason <= waitGrowth
		if blocked {
			s.windowBlockedWriters++
		}
		s.credit.waits[reason].Count++
		started := time.Now()
		ch, deadline := s.changed, st.writeDeadline
		s.mu.Unlock()
		err := waitChange(s.ctx, ch, deadline)
		s.mu.Lock()
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
	s.resetLocked(st, 1, true)
	return nil
}

// This is physical storage cleanup only. Credit settlement uses authenticated
// offsets, never the size of an old WINDOW entitlement.
func (st *Stream) releaseReceiveLocked() {
	st.s.bufferedBytes -= st.buffered
	st.buffered = 0
	st.s.receiveAllocated -= len(st.pages) * receivePageCost
	st.pages = nil
}

func (st *Stream) LocalAddr() net.Addr  { return streamAddr("mpx3/local") }
func (st *Stream) RemoteAddr() net.Addr { return streamAddr("mpx3/peer") }
func (st *Stream) SetDeadline(t time.Time) error {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	st.readDeadline = t
	st.writeDeadline = t
	st.s.wakeLocked()
	return nil
}
func (st *Stream) SetReadDeadline(t time.Time) error {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	st.readDeadline = t
	st.s.wakeLocked()
	return nil
}
func (st *Stream) SetWriteDeadline(t time.Time) error {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	st.writeDeadline = t
	st.s.wakeLocked()
	return nil
}
