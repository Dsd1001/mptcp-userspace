package multipath

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

const (
	LimitStreams          = "streams"
	LimitPendingFrames    = "pending_frames"
	LimitPendingBytes     = "pending_bytes"
	LimitReceiveCredit    = "receive_credit"
	LimitReceiveAllocated = "receive_allocated"
	LimitOpenQueue        = "open_wait_queue"
	LimitPacketIDs        = "packet_ids"
	LimitRemote           = "remote_stream_limit"
	LimitLocalAccept      = "local_accept_resources"
	LimitLocalConnections = "local_connections"
	NewStreamReserve      = BootstrapCreditLimit // compatibility name for diagnostics
	MaxDataPending        = MaxPending
	MaxControlPending     = 4 * MaxStreams
	MaxControlBytes       = MaxControlPending * 64
	LimitControlFrames    = "control_pending_frames"
	LimitControlBytes     = "control_pending_bytes"
)

// ResourceLimitError is scoped to one request/stream, not the carrier/session.
// errors.Is(err, ErrResourceLimit) remains compatible with existing callers.
type ResourceLimitError struct{ Reason string }

func (e *ResourceLimitError) Error() string { return "MPX/3 resource limit: " + e.Reason }
func (e *ResourceLimitError) Unwrap() error { return ErrResourceLimit }
func ResourceReason(err error) string {
	var e *ResourceLimitError
	if errors.As(err, &e) {
		return e.Reason
	}
	return ""
}

type ResourceStats struct {
	SharedCreditStats
	BootstrapCredit         int               `json:"bootstrap_credit_bytes"`
	BootstrapLimit          int               `json:"bootstrap_credit_limit_bytes"`
	GrowthCredit            int               `json:"growth_credit_bytes"`
	GrowthLimit             int               `json:"growth_credit_limit_bytes"`
	DataPendingFrames       int               `json:"data_pending_frames"`
	DataPendingLimit        int               `json:"data_pending_frame_limit"`
	DataPendingBytes        int               `json:"data_pending_bytes"`
	DataPendingByteLimit    int               `json:"data_pending_byte_limit"`
	ControlPendingFrames    int               `json:"control_pending_frames"`
	ControlPendingLimit     int               `json:"control_pending_frame_limit"`
	ControlPendingBytes     int               `json:"control_pending_bytes"`
	ControlPendingByteLimit int               `json:"control_pending_byte_limit"`
	WindowBlockedWriters    int               `json:"window_blocked_writers"`
	ControlQueuedFrames     int               `json:"control_queued_frames"`
	ControlDrops            uint64            `json:"control_regeneration_drops"`
	OpenReceiveCreditWaits  uint64            `json:"open_receive_credit_waits"`
	OpenedStreams           uint64            `json:"opened_streams"`
	ClosedStreams           uint64            `json:"closed_streams"`
	LocalConnections        int               `json:"local_connections"`
	LifecycleOpening        int               `json:"lifecycle_opening"`
	LifecycleOpen           int               `json:"lifecycle_open_bidirectional"`
	LifecycleHalfClosed     int               `json:"lifecycle_half_closed"`
	LifecycleWaitFinalACK   int               `json:"lifecycle_wait_local_final_ack"`
	LifecycleWaitPeerFinal  int               `json:"lifecycle_wait_peer_final"`
	LifecycleBothFinal      int               `json:"lifecycle_both_final_wait_close"`
	LifecycleWaitConsumed   int               `json:"lifecycle_wait_final_consumed"`
	LifecycleClosingOther   int               `json:"lifecycle_closing_other"`
	DataIdleOver30s         int               `json:"data_idle_over_30s"`
	DataIdleOver1m          int               `json:"data_idle_over_1m"`
	DataIdleOver5m          int               `json:"data_idle_over_5m"`
	DataIdleOver10m         int               `json:"data_idle_over_10m"`
	OldestStreamAgeSeconds  int64             `json:"oldest_stream_age_seconds"`
	OldestDataIdleSeconds   int64             `json:"oldest_data_idle_seconds"`
	IdleStreams             int               `json:"idle_streams"`
	SmallStreams            int               `json:"small_streams"`
	BulkStreams             int               `json:"bulk_streams"`
	IdleGrowthHeld          int               `json:"idle_irrevocable_growth_bytes"`
	ActiveStreams           int               `json:"active_streams"`
	StreamLimit             int               `json:"stream_limit"`
	PendingFrames           int               `json:"pending_frames"`
	PendingFrameLimit       int               `json:"pending_frame_limit"`
	PendingBytes            int               `json:"pending_bytes"`
	PendingByteLimit        int               `json:"pending_byte_limit"`
	ReceiveCredit           int               `json:"receive_credit_bytes"`
	ReceiveCreditLimit      int               `json:"receive_credit_limit_bytes"`
	ReceiveAllocated        int               `json:"receive_allocated_bytes"`
	ReceiveAllocatedLimit   int               `json:"receive_allocated_limit_bytes"`
	AdmissionReserve        int               `json:"admission_reserve_bytes"`
	WaitingOpens            int               `json:"waiting_opens"`
	Waits                   map[string]uint64 `json:"waits"`
	Rejections              map[string]uint64 `json:"rejections"`
	FirstLimitAt            string            `json:"first_limit_at,omitempty"`
	LastLimitAt             string            `json:"last_limit_at,omitempty"`
	LastReason              string            `json:"last_reason,omitempty"`
}

type TransportEvent struct {
	Sequence uint64 `json:"sequence"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Reason   string `json:"reason,omitempty"`
	Path     int    `json:"path,omitempty"`
	Stream   uint64 `json:"stream,omitempty"`
}
type LifecycleStats struct {
	SessionTag    string           `json:"session_tag"`
	CreatedAt     string           `json:"created_at"`
	Closed        bool             `json:"closed"`
	ClosedAt      string           `json:"closed_at,omitempty"`
	CloseReason   string           `json:"close_reason,omitempty"`
	EventSequence uint64           `json:"event_sequence"`
	Events        []TransportEvent `json:"events,omitempty"`
}

func (s *Session) eventLocked(kind, reason string, path byte, stream uint64) {
	s.eventSequence++
	e := TransportEvent{s.eventSequence, time.Now().UTC().Format(time.RFC3339Nano), kind, reason, int(path), stream}
	if len(s.transportEvents) >= 64 {
		copy(s.transportEvents, s.transportEvents[1:])
		s.transportEvents = s.transportEvents[:63]
	}
	s.transportEvents = append(s.transportEvents, e)
}
func (s *Session) resourceLocked(reason string, wait bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if s.resources.FirstLimitAt == "" {
		s.resources.FirstLimitAt = now
	}
	s.resources.LastLimitAt = now
	s.resources.LastReason = reason
	kind := "stream_resource_rejected"
	if wait {
		if s.resources.Waits == nil {
			s.resources.Waits = make(map[string]uint64)
		}
		s.resources.Waits[reason]++
		kind = "stream_admission_wait"
	} else {
		if s.resources.Rejections == nil {
			s.resources.Rejections = make(map[string]uint64)
		}
		s.resources.Rejections[reason]++
	}
	s.eventLocked(kind, reason, 0, 0)
	return &ResourceLimitError{reason}
}

// Record only fixed local reasons; never let arbitrary error text or secrets
// become map keys in the bounded telemetry.
func (s *Session) NoteLocalResource(reason string, wait bool) {
	if reason != LimitLocalAccept && reason != LimitLocalConnections {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resourceLocked(reason, wait)
}

// NoteLocalConnection tracks accepted TCP sockets at the Mac userspace entry.
// It is intentionally separate from MPX stream identity/lifecycle accounting.
func (s *Session) NoteLocalConnection(delta int) {
	s.mu.Lock()
	s.localConnections = max(0, s.localConnections+delta)
	s.wakeLocked()
	s.mu.Unlock()
}
func copyCounts(m map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
func (s *Session) resourceSnapshotLocked() ResourceStats {
	r := s.resources
	r.SharedCreditStats = s.sharedCreditSnapshotLocked()
	r.ActiveStreams = len(s.streams)
	r.StreamLimit = MaxStreams
	r.PendingFrames = len(s.pending)
	r.PendingFrameLimit = MaxDataPending + MaxControlPending
	r.PendingBytes = s.pendingBytes
	r.PendingByteLimit = MaxDataPendingBytes + MaxControlBytes
	r.ReceiveCredit = s.receiveCredit
	r.ReceiveCreditLimit = SessionCreditLimit
	r.ReceiveAllocated = s.receiveAllocated
	r.ReceiveAllocatedLimit = MaxBuffered
	r.AdmissionReserve = s.admissionReserveLocked()
	r.BootstrapCredit = s.receiveCredit - s.receiveGrowth
	r.BootstrapLimit = BootstrapCreditLimit
	r.GrowthCredit = s.receiveGrowth
	r.GrowthLimit = GrowthCreditLimit
	r.DataPendingFrames = s.dataPendingFrames
	r.DataPendingLimit = MaxDataPending
	r.DataPendingBytes = s.dataPendingBytes
	r.DataPendingByteLimit = MaxDataPendingBytes
	r.ControlPendingFrames = s.controlPendingFrames
	r.ControlPendingLimit = MaxControlPending
	r.ControlPendingBytes = s.controlPendingBytes
	r.ControlPendingByteLimit = MaxControlBytes
	r.WindowBlockedWriters = s.windowBlockedWriters
	r.ControlDrops = s.controlDrops
	r.OpenedStreams = s.openedStreams
	r.ClosedStreams = s.closedStreams
	r.LocalConnections = s.localConnections
	now := time.Now()
	finalConsumedPending := make(map[uint64]bool)
	for _, p := range s.pending {
		if p.f.kind == kindFinalConsumed {
			finalConsumedPending[p.f.stream] = true
		}
	}
	recordLifecycle := func(st *Stream, closing bool) {
		if st == nil {
			return
		}
		if !st.createdAt.IsZero() {
			r.OldestStreamAgeSeconds = max(r.OldestStreamAgeSeconds, int64(max(time.Duration(0), now.Sub(st.createdAt))/time.Second))
		}
		if !st.lastActivity.IsZero() {
			idle := max(time.Duration(0), now.Sub(st.lastActivity))
			r.OldestDataIdleSeconds = max(r.OldestDataIdleSeconds, int64(idle/time.Second))
			if idle >= 30*time.Second {
				r.DataIdleOver30s++
			}
			if idle >= time.Minute {
				r.DataIdleOver1m++
			}
			if idle >= 5*time.Minute {
				r.DataIdleOver5m++
			}
			if idle >= 10*time.Minute {
				r.DataIdleOver10m++
			}
		}
		localFinalStarted := st.writeFIN || st.sendReset
		localFinalACKed := (st.writeFIN && st.finACK) || (st.sendReset && st.sendResetACK)
		switch {
		case !st.open && !st.closed:
			r.LifecycleOpening++
		case localFinalStarted && !localFinalACKed:
			r.LifecycleWaitFinalACK++
		case (st.receiveStopped || localFinalACKed) && !st.hasFIN:
			r.LifecycleWaitPeerFinal++
		case st.hasFIN && !localFinalStarted:
			r.LifecycleHalfClosed++
		case st.hasFIN && localFinalACKed && !closing:
			r.LifecycleBothFinal++
		case closing && finalConsumedPending[st.id]:
			r.LifecycleWaitConsumed++
		case closing:
			r.LifecycleClosingOther++
		default:
			r.LifecycleOpen++
		}
	}
	for _, st := range s.streams {
		recordLifecycle(st, false)
		if !st.open {
			r.WaitingOpens++
		}
		if st.windowTarget <= StreamWindow || now.Sub(st.lastRead) > creditIdle {
			r.IdleStreams++
			r.IdleGrowthHeld += growthOf(int(st.rxLimit - st.rxRead))
		} else if st.windowTarget <= SmallStreamWindow {
			r.SmallStreams++
		} else {
			r.BulkStreams++
		}
	}
	for _, st := range s.closing {
		recordLifecycle(st, true)
	}
	for _, c := range s.paths {
		r.ControlQueuedFrames += len(c.control) + len(c.reliableControl)
	}
	r.Waits = copyCounts(r.Waits)
	r.Rejections = copyCounts(r.Rejections)
	return r
}
func (s *Session) lifecycleSnapshotLocked() LifecycleStats {
	out := LifecycleStats{SessionTag: fmt.Sprintf("%x", s.id[:6]), CreatedAt: s.clockStart.UTC().Format(time.RFC3339Nano), Closed: s.closed, EventSequence: s.eventSequence, Events: append([]TransportEvent(nil), s.transportEvents...)}
	if s.closed {
		out.ClosedAt = s.closedAt.UTC().Format(time.RFC3339Nano)
		if s.err != nil {
			out.CloseReason = s.err.Error()
		}
	}
	return out
}
func (s *Session) recordDialAttempt(id byte, address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.paths[id]
	if c == nil {
		c = &carrier{id: id, address: address}
		s.paths[id] = c
	}
	c.dialAttempts++
	s.eventLocked("carrier_dial", "", id, 0)
}

// Temporary accept exhaustion is recoverable. It must never tear down every
// established stream/carrier through a deferred Server.Close/serve cleanup.
func TemporaryAcceptError(err error) bool {
	if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.ENOMEM) {
		return true
	}
	var e net.Error
	return errors.As(err, &e) && e.Temporary()
}
