package multipath

import (
	"context"
	"time"
)

// OPEN accounts only for lightweight stream and reliable control metadata.
// Neither the receive-credit ledger nor DATA's pending budget can block it.
func (s *Session) openBlockReasonLocked() string {
	if len(s.streams)+len(s.closing) >= MaxStreams {
		return LimitStreams
	}
	if s.controlPendingFrames >= MaxControlPending {
		return LimitControlFrames
	}
	if s.controlPendingBytes+64 > MaxControlBytes {
		return LimitControlBytes
	}
	return ""
}

func (s *Session) openWithAdmission(ctx context.Context) (*Stream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, s.err
	}
	if s.server {
		return nil, ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if reason := s.openBlockReasonLocked(); reason != "" {
		return nil, s.resourceLocked(reason, false)
	}
	id := s.nextStream
	if id == 0 || id > ^uint64(0)-2 {
		return nil, s.resourceLocked(LimitPacketIDs, false)
	}
	s.nextStream += 2
	st := s.newStreamLocked(id) // zero receive/send credit, no receive pages
	p := s.queueLocked(frame{kind: kindOpen, stream: id})
	if p == nil {
		s.resetLocked(st, mpx4ErrResourceLimit, false)
		return nil, &ResourceLimitError{s.resources.LastReason}
	}
	st.openID = p.f.id
	for {
		if err := ctx.Err(); err != nil {
			s.resetLocked(st, mpx4ErrNoError, true)
			return nil, err
		}
		if st.closed {
			return nil, st.err
		}
		if st.open {
			return st, nil
		}
		ch := s.changed
		s.mu.Unlock()
		err := waitChange(ctx, ch, time.Time{})
		s.mu.Lock()
		if err != nil {
			s.resetLocked(st, mpx4ErrNoError, true)
			return nil, err
		}
	}
}

func (s *Session) rememberStreamLocked(id uint64) {
	s.seen[id] = true
	if id > s.maxSeen {
		s.maxSeen = id
		if s.maxSeen > 8192 {
			for old := range s.seen {
				if old <= s.maxSeen-8192 {
					delete(s.seen, old)
				}
			}
		}
	}
}

func (s *Session) handleOpenLocked(c *carrier, f frame) error {
	if !s.server || f.id == 0 || f.offset != 0 {
		return streamStateFailure("invalid STREAM_OPEN state")
	}
	if st := s.streams[f.stream]; st != nil {
		if st.openID != f.id {
			return transmissionIDFailure("STREAM_OPEN reused Stream ID with different Transmission ID")
		}
		if st.open {
			s.controlLocked(c, frame{kind: kindOpenOK, stream: st.id, id: st.openID})
			st.advertiseCreditLocked(time.Now())
		}
		return nil
	}
	if st := s.closing[f.stream]; st != nil {
		if st.openID != 0 && st.openID != f.id {
			return transmissionIDFailure("STREAM_OPEN reused closing Stream ID with different Transmission ID")
		}
		if st.open {
			s.controlLocked(c, frame{kind: kindOpenOK, stream: f.stream, id: f.id})
		} else {
			s.controlLocked(c, frame{kind: kindOpenReject, stream: f.stream, offset: mpx4ErrStreamState, id: f.id})
		}
		return nil
	}
	if term, ok := s.terminal[f.stream]; ok {
		if term.openID != 0 && term.openID != f.id {
			return transmissionIDFailure("STREAM_OPEN reused tombstoned Stream ID with different Transmission ID")
		}
		if term.openAccepted {
			s.controlLocked(c, frame{kind: kindOpenOK, stream: f.stream, id: f.id})
		} else {
			s.controlLocked(c, frame{kind: kindOpenReject, stream: f.stream, offset: mpx4ErrStreamState, id: f.id})
		}
		return nil
	}
	if s.seen[f.stream] || (s.maxSeen > 8192 && f.stream <= s.maxSeen-8192) {
		// Detailed response state has been compacted. Draft 04 treats this as
		// retired identity: ignore without recreating application Stream state.
		return nil
	}
	if len(s.streams)+len(s.closing) >= MaxStreams {
		s.resourceLocked(LimitStreams, false)
		s.controlLocked(c, frame{kind: kindOpenReject, stream: f.stream, offset: mpx4ErrStreamLimit, id: f.id})
		return nil
	}
	s.rememberStreamLocked(f.stream)
	st := s.newStreamLocked(f.stream)
	st.openID = f.id
	if s.onOpen == nil {
		s.resetLocked(st, mpx4ErrInternal, true)
	} else {
		go s.onOpen(st)
	}
	return nil
}

// Rev2 reserves actual DATA capacity, not historical WINDOW grants.
func (s *Session) admissionReserveLocked() int {
	return max(0, BootstrapCreditLimit-(s.receiveCredit-s.receiveGrowth))
}
func growthOf(outstanding int) int { return max(0, outstanding-StreamWindow) }
func (st *Stream) grantCreditLocked(desired uint64) {
	if st.rxRead > ^uint64(0)-MaxStreamWindow {
		return
	}
	desired = min(desired, st.rxRead+MaxStreamWindow)
	if desired > st.rxLimit {
		st.rxLimit = desired
	}
}
