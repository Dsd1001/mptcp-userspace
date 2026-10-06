package multipath

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"
)

const carrierQueue = 4

type PathStats struct {
	MeasuredDeliveryBPS    float64  `json:"measured_delivery_bps"`
	ValidDeliverySamples   int      `json:"valid_delivery_samples"`
	Role                   PathRole `json:"role,omitempty"`
	RoleReason             string   `json:"role_reason,omitempty"`
	DeliverySamples        int      `json:"delivery_samples"`
	SchedulerProbeCount    uint64   `json:"scheduler_probe_count"`
	SchedulerProbeBytes    uint64   `json:"scheduler_probe_payload_bytes"`
	SchedulerProbeDebtPeak int      `json:"scheduler_probe_debt_peak"`
	SchedulerDataACKs      int      `json:"scheduler_data_acks"`
	ID                     int      `json:"id"`
	Address                string   `json:"address"`
	Connected              bool     `json:"connected"`
	Sent                   uint64   `json:"sent"`
	Received               uint64   `json:"received"`
	RTTMS                  float64  `json:"rtt_ms"`
	GoodputBPS             float64  `json:"goodput_bps"`
	ConfiguredRateBPS      float64  `json:"configured_rate_bps,omitempty"`
	Outstanding            int      `json:"outstanding_bytes"`
	Queue                  int      `json:"queue_bytes"`
	Errors                 uint64   `json:"errors"`
	LastError              string   `json:"last_error,omitempty"`
	Budget                 int      `json:"budget_bytes,omitempty"`
	DialAttempts           uint64   `json:"dial_attempts"`
	Connections            uint64   `json:"carrier_connections"`
	ConnectedAt            string   `json:"connected_at,omitempty"`
	DisconnectedAt         string   `json:"disconnected_at,omitempty"`
	ControlOutstanding     int      `json:"control_outstanding_bytes"`
	ControlQueue           int      `json:"control_queue_bytes"`
}

type Stats struct {
	SchedulerStats
	Paths               int            `json:"paths"`
	Connections         int            `json:"connections"`
	Sent                uint64         `json:"sent"`
	Received            uint64         `json:"received"`
	Retransmits         uint64         `json:"retransmits"`
	WindowWaits         uint64         `json:"window_waits"`
	ReorderBytes        int            `json:"reorder_bytes"`
	ReorderPeak         int            `json:"reorder_peak"`
	PendingBytes        int            `json:"pending_bytes"`
	BufferedBytes       int            `json:"buffered_bytes"`
	ReceiveAllocated    int            `json:"receive_allocated_bytes"`
	PathStats           []PathStats    `json:"path_stats"`
	ReceiveCredit       int            `json:"receive_credit_bytes"`
	WindowTarget        int            `json:"max_stream_window_target"`
	WarmTarget          int            `json:"max_stream_warm_target_bytes"`
	StandbyWindow       int            `json:"standby_window_bytes"`
	WindowSeed          int            `json:"window_seed_bytes"`
	WindowSeedAgeMS     float64        `json:"window_seed_age_ms"`
	CreditRTTMS         float64        `json:"credit_rtt_ms"`
	CreditBaseRTTMS     float64        `json:"credit_base_rtt_ms"`
	DemandStreams       int            `json:"active_demand_streams"`
	BulkStreams         int            `json:"active_bulk_streams"`
	BulkWindowFloor     int            `json:"bulk_window_floor_bytes"`
	ReceivePressure     int            `json:"receive_pressure_percent"`
	StreamAllowance     int            `json:"stream_allowance_bytes"`
	SessionRefillTarget int            `json:"session_refill_target_bytes"`
	ReadyFrames         int            `json:"ready_frames"`
	Resources           ResourceStats  `json:"resources"`
	Lifecycle           LifecycleStats `json:"lifecycle"`
}

type outbound struct {
	f               frame
	ready           *list.Element
	path            *carrier
	generation      uint64
	sentAt, created time.Time
	cost            int
	attempts        int
}

type sendTask struct {
	p          *outbound
	generation uint64
}

type carrier struct {
	generation                        uint64
	scheduler                         schedulerPathState
	id                                uint64
	address                           string
	conn                              *secureConn
	active                            bool
	done                              chan struct{}
	queue                             chan sendTask
	control                           chan frame
	reliableControl                   chan sendTask
	controlOutstanding, controlQueued int
	outstanding, queued               int
	budget                            int
	growthACK                         int
	budgetLimited, startupDone        bool
	sampleBudgetLimited               bool
	minRTT                            time.Duration
	lastACK, lastTimeoutAt            time.Time
	timeoutStreak                     int
	rtt                               time.Duration
	goodput                           float64
	configuredRateBPS                 float64
	capacitySamples                   [8]float64
	capacityIndex                     int
	ackBytes                          uint64
	remoteSampleAt                    uint64
	deliverySamples                   int
	dialAttempts, connects            uint64
	connectedAt, disconnectedAt       string
	sampleAt                          time.Time
	pingAt                            time.Time
	pingID                            uint64
	penaltyUntil                      time.Time
	sent, received, errors            uint64
	lastError                         string
}

// All stream, ledger and carrier accounting lives under one mutex. Network I/O
// and backend I/O never hold it. Readers cannot block unrelated logical flows.
type Session struct {
	credit                                          connectionCredit
	closing                                         map[uint64]*Stream
	terminal                                        map[uint64]terminalStream
	terminalOrder                                   []uint64
	terminalCursor                                  int
	writerReady                                     list.List
	scheduler                                       schedulerState
	udpStarted                                      bool
	productMux                                      bool // immutable product service binding; absent from MPX/4 wire state
	productPool                                     []*Stream
	productPoolOpening                              int
	productPoolKick                                 chan struct{}
	uotActiveFlows                                  int
	ctx                                             context.Context
	cancel                                          context.CancelFunc
	id                                              sessionID
	server                                          bool
	mu                                              sync.Mutex
	changed                                         chan struct{}
	kick                                            chan struct{}
	done                                            chan struct{}
	closed                                          bool
	err                                             error
	streams                                         map[uint64]*Stream
	ready                                           map[uint64]*list.List
	receiveCredit, windowSeed                       int
	windowSeedAt                                    time.Time
	clockStart                                      time.Time
	pending                                         map[uint64]*outbound
	paths                                           map[uint64]*carrier
	carrierUsed                                     map[uint64]bool
	highestGeneration                               map[uint64]uint64
	nextCandidateGeneration                         map[uint64]uint64
	generationExhausted                             map[uint64]bool
	pathCapacities                                  map[uint64]PathCapacity
	localMaxCarriers, peerMaxCarriers               uint64
	peerMaxFrame, peerMaxRecord, peerMaxStreams     uint64
	effectiveCarrierLimit                           uint64
	settled                                         map[uint64]bool
	peerProcessed                                   map[uint64]bool
	peerTransmissionFingerprint                     map[uint64][32]byte
	confirmationReplay                              map[uint64]frame
	settledThrough, lastRetireAdvertised            uint64
	peerProcessedThrough, peerRetiredThrough        uint64
	seen                                            map[uint64]bool
	maxSeen, nextStream, nextPacket, dispatchCursor uint64
	bulkDispatchCursor, dispatchSequence            uint64
	pendingBytes, bufferedBytes                     int
	receiveAllocated                                int
	sent, received, retransmits, windowWaits        uint64
	reorderPeak                                     int
	noPathsSince                                    time.Time
	lastSweep                                       time.Time
	onOpen                                          func(*Stream)
	resources                                       ResourceStats
	transportEvents                                 []TransportEvent
	eventSequence                                   uint64
	closedAt                                        time.Time
	controlReady                                    list.List
	receiveGrowth                                   int
	dataPendingFrames, controlPendingFrames         int
	dataPendingBytes, controlPendingBytes           int
	windowBlockedWriters                            int
	controlDrops, openedStreams, closedStreams      uint64
	localConnections                                int
}

func newSession(parent context.Context, id sessionID, server bool, onOpen func(*Stream), modes ...SchedulerMode) *Session {
	ctx, cancel := context.WithCancel(parent)
	s := &Session{clockStart: time.Now(), ctx: ctx, cancel: cancel, id: id, server: server, changed: make(chan struct{}), kick: make(chan struct{}, 1), done: make(chan struct{}), streams: make(map[uint64]*Stream), pending: make(map[uint64]*outbound), paths: make(map[uint64]*carrier), carrierUsed: make(map[uint64]bool), highestGeneration: make(map[uint64]uint64), nextCandidateGeneration: make(map[uint64]uint64), generationExhausted: make(map[uint64]bool), pathCapacities: make(map[uint64]PathCapacity), settled: make(map[uint64]bool), peerProcessed: make(map[uint64]bool), peerTransmissionFingerprint: make(map[uint64][32]byte), confirmationReplay: make(map[uint64]frame), seen: make(map[uint64]bool), nextStream: 1, noPathsSince: time.Now(), localMaxCarriers: MaxCarriers, onOpen: onOpen}
	mode := SchedulerAuto
	if len(modes) > 0 {
		mode = modes[0]
	}
	s.initScheduler(mode)
	s.initCreditLocked()
	go s.run()
	return s
}

func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Err() error            { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *Session) Close() error          { s.stop(net.ErrClosed); return nil }

func (s *Session) wakeLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Session) stop(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.err = err
	s.closedAt = time.Now()
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	s.eventLocked("session_closed", reason, 0, 0)
	s.cancel()
	for _, c := range s.paths {
		s.detachLocked(c, err)
	}
	for _, st := range s.streams {
		st.closed = true
		s.closedStreams++
		st.err = err
		st.releaseReceiveLocked()
	}
	for _, st := range s.closing {
		st.closed = true
		st.err = err
		st.releaseReceiveLocked()
	}
	s.closing = make(map[uint64]*Stream)
	s.terminal = make(map[uint64]terminalStream)
	s.terminalOrder = nil
	s.credit = connectionCredit{}
	s.receiveCredit, s.receiveGrowth = 0, 0
	s.streams = make(map[uint64]*Stream)
	s.pending = make(map[uint64]*outbound)
	s.ready = nil
	s.controlReady.Init()
	s.dataPendingFrames = 0
	s.controlPendingFrames = 0
	s.dataPendingBytes = 0
	s.controlPendingBytes = 0
	s.pendingBytes = 0
	s.bufferedBytes = 0
	s.wakeLocked()
}

func (s *Session) activeCarrierCountLocked() uint64 {
	var n uint64
	for _, c := range s.paths {
		if c != nil && c.active {
			n++
		}
	}
	return n
}

func (s *Session) peerLimitsLocked() (handshakeLimits, bool) {
	if s.peerMaxCarriers == 0 {
		return handshakeLimits{}, false
	}
	return handshakeLimits{maxFrame: s.peerMaxFrame, maxRecord: s.peerMaxRecord, maxStreams: s.peerMaxStreams, maxCarriers: s.peerMaxCarriers}, true
}

func (s *Session) validateCarrierAdmissionLocked(id, generation uint64, peer handshakeLimits) error {
	if peer.maxFrame == 0 || peer.maxRecord == 0 || peer.maxStreams == 0 || peer.maxCarriers == 0 || peer.maxCarriers > mpx4VarIntMax {
		return &mpx4Failure{code: mpx4ErrProtocolViolation, scope: mpx4ScopePreEstablishmentCarrier, reason: "invalid peer Session limits", cause: ErrProtocol}
	}
	if established, ok := s.peerLimitsLocked(); ok && peer != established {
		return &mpx4Failure{code: mpx4ErrSessionConflict, scope: mpx4ScopePreEstablishmentCarrier, reason: "Session receive limits changed on JOIN", cause: ErrProtocol}
	}
	if err := s.validateCarrierGenerationLocked(id, generation); err != nil {
		return &mpx4Failure{code: mpx4ErrCarrierConflict, scope: mpx4ScopePreEstablishmentCarrier, reason: "carrier generation conflict", cause: err}
	}
	localMax := s.localMaxCarriers
	if localMax == 0 {
		localMax = MaxCarriers
	}
	effective := min(localMax, peer.maxCarriers)
	old := s.paths[id]
	needsSlot := old == nil || !old.active
	if needsSlot && s.activeCarrierCountLocked() >= effective {
		return &mpx4Failure{code: mpx4ErrResourceLimit, scope: mpx4ScopePreEstablishmentCarrier, reason: "effective MAX_CARRIERS reached", cause: ErrResourceLimit}
	}
	return nil
}

func (s *Session) validateCarrierAdmission(id, generation uint64, peer handshakeLimits) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSessionExpired
	}
	return s.validateCarrierAdmissionLocked(id, generation, peer)
}

func (s *Session) expectedPeerLimits() (handshakeLimits, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peerLimitsLocked()
}

func (s *Session) addCarrier(id uint64, address string, conn *secureConn) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		conn.Close()
		return net.ErrClosed
	}
	peer := handshakeLimits{maxFrame: conn.peerMaxFrame, maxRecord: conn.peerMaxRecord, maxStreams: conn.peerMaxStreams, maxCarriers: conn.peerMaxCarriers}
	if err := s.validateCarrierAdmissionLocked(id, conn.generation, peer); err != nil {
		failure := normalizeEstablishedFailure(err, frame{})
		var candidate *mpx4Failure
		if errors.As(err, &candidate) {
			failure = candidate
		}
		s.mu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
		_ = conn.writeFrame(frame{kind: kindCarrierClose, offset: failure.code, data: closeReason(failure.reason)})
		conn.Close()
		return err
	}
	if err := s.commitCarrierGenerationLocked(id, conn.generation); err != nil {
		s.mu.Unlock()
		conn.Close()
		return err
	}
	if s.peerMaxCarriers == 0 {
		s.peerMaxFrame = conn.peerMaxFrame
		s.peerMaxRecord = conn.peerMaxRecord
		s.peerMaxStreams = conn.peerMaxStreams
		s.peerMaxCarriers = conn.peerMaxCarriers
		s.localMaxCarriers = conn.localMaxCarriers
		if s.localMaxCarriers == 0 {
			s.localMaxCarriers = MaxCarriers
		}
		s.effectiveCarrierLimit = min(s.localMaxCarriers, s.peerMaxCarriers)
	}
	c := &carrier{id: id, generation: conn.generation, address: address, conn: conn, active: true, done: make(chan struct{}), queue: make(chan sendTask, carrierQueue), control: make(chan frame, 512), reliableControl: make(chan sendTask, controlCarrierQueue), rtt: 50 * time.Millisecond, goodput: 4 << 20, configuredRateBPS: conn.configuredRateBPS, sampleAt: time.Now()}
	c.scheduler.role = RoleLearning
	c.scheduler.lastRoleReason = "awaiting_3_delivery_samples"
	// Fill the bounded capacity history with the explicit startup prior, not
	// zeros. One short low-utilization epoch must not poison route selection;
	// eight genuinely loaded lower-rate epochs still replace the entire prior.
	for i := range c.capacitySamples {
		c.capacitySamples[i] = c.goodput
	}
	if old := s.paths[id]; old != nil {
		// The Generation commit above makes every lower incarnation superseded
		// atomically. detachLocked removes it from scheduling and requeues any
		// outstanding Attempts while retaining Session-owned Transmission IDs.
		s.detachLocked(old, fmt.Errorf("carrier superseded by generation %d", conn.generation))
		c.sent = old.sent
		c.received = old.received
		c.errors = old.errors
		c.lastError = old.lastError
		c.dialAttempts = old.dialAttempts
		c.connects = old.connects
		c.disconnectedAt = old.disconnectedAt
	}
	c.connects++
	c.connectedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.eventLocked("carrier_connected", "", id, 0)
	s.paths[id] = c
	s.advertiseSessionCreditLocked(time.Now(), true)
	s.advertiseTransmissionRetireLocked()
	s.refreshSchedulerLocked(time.Now(), false)
	s.noPathsSince = time.Time{}
	s.wakeLocked()
	s.mu.Unlock()
	go s.readCarrier(c)
	go s.writeCarrier(c)
	return nil
}

func (s *Session) detachLocked(c *carrier, err error) {
	if !c.active {
		return
	}
	c.active = false
	c.errors++
	c.disconnectedAt = time.Now().UTC().Format(time.RFC3339Nano)
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	s.eventLocked("carrier_disconnected", reason, c.id, 0)
	if err != nil {
		c.lastError = err.Error()
	}
	close(c.done)
	c.conn.Close()
	for _, p := range s.pending {
		if p.path == c {
			if !p.sentAt.IsZero() && !s.closed {
				s.retransmits++
			}
			p.path = nil
			p.sentAt = time.Time{}
			p.generation++
			if !s.closed {
				s.readyLocked(p, true)
			}
		}
	}
	c.outstanding = 0
	c.queued = 0
	c.controlOutstanding = 0
	c.controlQueued = 0
	s.refreshSchedulerLocked(time.Now(), false)
	any := false
	for _, p := range s.paths {
		if p.active {
			any = true
			break
		}
	}
	if !any && s.noPathsSince.IsZero() {
		s.noPathsSince = time.Now()
		// Refresh the current cumulative retirement watermark after DORMANT recovery.
		s.lastRetireAdvertised = 0
	}
	s.wakeLocked()
}

func (s *Session) carrierFailure(c *carrier, err error) {
	s.mu.Lock()
	s.detachLocked(c, err)
	s.mu.Unlock()
}

func (s *Session) recordDialError(id uint64, address string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.paths[id]
	if c == nil {
		c = &carrier{id: id, address: address}
		s.paths[id] = c
	}
	if !c.active {
		c.errors++
		c.lastError = err.Error()
		s.eventLocked("carrier_dial_failed", err.Error(), id, 0)
		s.wakeLocked()
	}
}

func (s *Session) readCarrier(c *carrier) {
	for {
		if err := c.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			s.carrierFailure(c, err)
			return
		}
		f, err := c.conn.readFrame()
		if err != nil {
			if errors.Is(err, ErrAuthentication) {
				// Integrity failures are Carrier-scoped and need not be reported on
				// the wire because the failed input is not authenticated.
				s.carrierFailure(c, err)
			} else if errors.Is(err, ErrProtocol) {
				s.protocolCarrierFailure(c, &mpx4Failure{code: mpx4ErrFrameEncoding, scope: mpx4ScopeCarrier, reason: err.Error(), cause: err})
			} else {
				s.carrierFailure(c, err)
			}
			return
		}
		if f.kind == kindCarrierClose {
			s.carrierFailure(c, remoteCloseError("carrier", f))
			return
		}
		if f.kind == kindSessionClose {
			s.stop(remoteCloseError("session", f))
			return
		}
		if err = s.handleFrame(c, f); err != nil {
			if isLocalClosed(err) {
				return
			}
			failure := normalizeEstablishedFailure(err, f)
			if failure.scope == mpx4ScopeCarrier {
				s.protocolCarrierFailure(c, failure)
			} else {
				s.protocolSessionFailure(c, failure)
			}
			return
		}
	}
}

func reliableTransmissionKind(kind byte) bool {
	switch kind {
	case kindOpen, kindData, kindFIN, kindResetStream, kindStopReceiving, kindFinalConsumed:
		return true
	default:
		return false
	}
}

func confirmationKind(kind byte) bool {
	return kind == kindACK || kind == kindOpenOK || kind == kindOpenReject
}

func cloneFrame(f frame) frame {
	f.data = append([]byte(nil), f.data...)
	return f
}

func (s *Session) hasActiveCarrierLocked() bool {
	for _, c := range s.paths {
		if c != nil && c.active {
			return true
		}
	}
	return false
}

func (s *Session) peerAttemptLocked(c *carrier, f frame) (bool, error) {
	if !reliableTransmissionKind(f.kind) {
		return false, nil
	}
	if s.peerTransmissionFingerprint == nil {
		s.peerTransmissionFingerprint = make(map[uint64][32]byte)
	}
	if s.confirmationReplay == nil {
		s.confirmationReplay = make(map[uint64]frame)
	}
	if s.peerProcessed == nil {
		s.peerProcessed = make(map[uint64]bool)
	}
	if f.id == 0 || f.id > mpx4VarIntMax {
		return false, transmissionIDFailure("invalid peer Transmission ID")
	}
	if f.id <= s.peerRetiredThrough {
		return true, nil
	}
	wire, err := encodeV4Frame(f)
	if err != nil {
		return false, err
	}
	fingerprint := sha256.Sum256(wire)
	if previous, ok := s.peerTransmissionFingerprint[f.id]; ok {
		if previous != fingerprint {
			return false, transmissionIDFailure("Transmission ID reused with different Frame semantics")
		}
		if confirmation, ok := s.confirmationReplay[f.id]; ok {
			s.controlLocked(c, confirmation)
			return true, nil
		}
		return false, nil
	}
	s.peerTransmissionFingerprint[f.id] = fingerprint
	return false, nil
}

func (s *Session) markPeerProcessedLocked(id uint64) {
	if id == 0 || id <= s.peerRetiredThrough {
		return
	}
	if s.peerProcessed == nil {
		s.peerProcessed = make(map[uint64]bool)
	}
	s.peerProcessed[id] = true
	for s.peerProcessedThrough < mpx4VarIntMax && s.peerProcessed[s.peerProcessedThrough+1] {
		s.peerProcessedThrough++
	}
}

func (s *Session) handleTransmissionRetireLocked(f frame) error {
	if f.stream != 0 || f.id != 0 || len(f.data) != 0 || f.offset > mpx4VarIntMax {
		return protocolViolation("invalid TRANSMISSION_RETIRE")
	}
	if f.offset > s.peerProcessedThrough {
		return transmissionIDFailure("TRANSMISSION_RETIRE exceeds contiguous processed peer prefix")
	}
	if f.offset <= s.peerRetiredThrough {
		return nil
	}
	s.peerRetiredThrough = f.offset
	for id := range s.peerProcessed {
		if id <= f.offset {
			delete(s.peerProcessed, id)
		}
	}
	for id := range s.peerTransmissionFingerprint {
		if id <= f.offset {
			delete(s.peerTransmissionFingerprint, id)
			delete(s.confirmationReplay, id)
		}
	}
	return nil
}

func (s *Session) advertiseTransmissionRetireLocked() {
	if s.settledThrough == 0 || s.settledThrough <= s.lastRetireAdvertised || !s.hasActiveCarrierLocked() {
		return
	}
	if s.controlLocked(nil, frame{kind: kindTransmissionRetire, offset: s.settledThrough}) {
		s.lastRetireAdvertised = s.settledThrough
	}
}

func (s *Session) markSettledLocked(id uint64) {
	if id == 0 {
		return
	}
	if s.settled == nil {
		s.settled = make(map[uint64]bool)
	}
	s.settled[id] = true
	for s.settledThrough < mpx4VarIntMax && s.settled[s.settledThrough+1] {
		delete(s.settled, s.settledThrough+1)
		s.settledThrough++
	}
	s.advertiseTransmissionRetireLocked()
}

func (s *Session) controlLocked(c *carrier, f frame) bool {
	if confirmationKind(f.kind) && f.id != 0 && f.id > s.peerRetiredThrough {
		if s.confirmationReplay == nil {
			s.confirmationReplay = make(map[uint64]frame)
		}
		s.confirmationReplay[f.id] = cloneFrame(f)
	}
	// A directed ACK/PING/PONG keeps its original carrier. DATA delivery
	// measurements rely on that identity; never reroute a usable directed send.
	if c != nil && c.active {
		select {
		case c.control <- f:
			return true
		default:
		}
	}
	// Generic WINDOW/OPEN_OK must not be randomly put behind a slow carrier's
	// DATA backlog. Control has its own bounded queue, NOT the DATA flight gate.
	now := time.Now()
	var best *carrier
	bestTier, bestScore := 4, 1e30
	for _, p := range s.paths {
		if p == c || !p.active || len(p.control) >= cap(p.control) {
			continue
		}
		tier := 2
		if !now.Before(p.penaltyUntil) {
			tier = 1
			if s.scheduler.effective != SchedulerProtect || p.scheduler.role == RoleActive {
				tier = 0
			}
		}
		rate := max(p.goodput, 65536)
		debt := p.outstanding + p.controlOutstanding + 64*len(p.control)
		score := schedulerBaseRTT(p).Seconds()/2 + float64(debt+64)/rate
		if best == nil || tier < bestTier || (tier == bestTier && (score < bestScore || (score == bestScore && p.id < best.id))) {
			best, bestTier, bestScore = p, tier, score
		}
	}
	if best != nil {
		select {
		case best.control <- f:
			return true
		default:
		}
	}
	// Exhausted control queues remain bounded; existing retries and periodic
	// WINDOW regeneration recover loss without introducing another payload copy.
	s.controlDrops++
	return false
}

func (s *Session) queueLocked(f frame) *outbound {
	cost := len(f.data) + 64
	if f.kind == kindData {
		if s.dataPendingFrames >= MaxDataPending {
			s.resourceLocked(LimitPendingFrames, false)
			return nil
		}
		if s.dataPendingBytes+cost > MaxDataPendingBytes {
			s.resourceLocked(LimitPendingBytes, false)
			return nil
		}
	} else {
		if len(f.data) != 0 && !(f.kind == kindResetStream && len(f.data) == 8) {
			return nil
		}
		if s.controlPendingFrames >= MaxControlPending {
			s.resourceLocked(LimitControlFrames, false)
			return nil
		}
		if s.controlPendingBytes+cost > MaxControlBytes {
			s.resourceLocked(LimitControlBytes, false)
			return nil
		}
	}
	if s.nextPacket >= mpx4VarIntMax {
		s.resourceLocked(LimitPacketIDs, false)
		return nil
	}
	s.nextPacket++
	f.id = s.nextPacket
	p := &outbound{f: f, created: time.Now(), cost: cost}
	s.pending[f.id] = p
	if f.kind == kindData {
		s.dataPendingFrames++
		s.dataPendingBytes += cost
	} else {
		s.controlPendingFrames++
		s.controlPendingBytes += cost
	}
	s.readyLocked(p, false)
	s.pendingBytes += cost
	s.wakeLocked()
	return p
}

func (s *Session) removePendingLocked(p *outbound) {
	if s.pending[p.f.id] != p {
		return
	}
	delete(s.pending, p.f.id)
	s.unreadyLocked(p)
	s.pendingBytes -= p.cost
	if p.f.kind == kindData {
		s.dataPendingFrames--
		s.dataPendingBytes -= p.cost
	} else {
		s.controlPendingFrames--
		s.controlPendingBytes -= p.cost
	}
	if p.path != nil {
		p.path.releaseFlight(p)
		if p.sentAt.IsZero() {
			p.path.releaseQueued(p)
		}
	}
	p.generation++
}

func (s *Session) ackLocked(c *carrier, f frame) error {
	p := s.pending[f.id]
	if p == nil {
		if f.id > s.nextPacket {
			return transmissionIDFailure("confirmation references never-allocated Transmission ID")
		}
		if f.id <= s.settledThrough || s.settled[f.id] {
			return nil
		}
		return transmissionIDFailure("confirmation references missing unsettled Transmission state")
	}
	if p.f.stream != f.stream {
		return transmissionIDFailure("acknowledgement Stream ID does not match Transmission ID")
	}
	if p.f.kind == kindOpen {
		if f.kind != kindOpenOK {
			return transmissionIDFailure("STREAM_OPEN requires STREAM_OPEN_OK or STREAM_OPEN_REJECT")
		}
	} else if f.kind != kindACK {
		return transmissionIDFailure("reliable Frame requires TRANSMISSION_ACK")
	}
	st := s.streamForCreditLocked(f.stream)
	if st != nil {
		if p.f.kind == kindOpen && !st.closed {
			st.open = true
			st.advertiseCreditLocked(time.Now())
		}
		if p.f.kind == kindFIN {
			st.finACK = true
		}
		if p.f.kind == kindResetStream {
			if p.f.offset != st.txNext {
				return ErrProtocol
			}
			if err := st.releaseSendCreditLocked(p.f.offset); err != nil {
				return err
			}
			st.sendResetACK = true
		}
	}
	if p.f.kind == kindData && len(p.f.data) > 0 && p.path == c {
		now := time.Now()
		c.scheduler.lastProgressAt = now
		// Any real DATA progress clears transient timeout suspicion. A healthy
		// Carrier must not carry a startup-burst penalty into the next epoch.
		c.timeoutStreak = 0
		c.lastTimeoutAt = time.Time{}
		if p.attempts == 1 {
			c.observeDelivery(now, p, f.offset)
			s.schedulerReceiptLocked(c, p, now)
		}
	}
	s.markSettledLocked(p.f.id)
	s.removePendingLocked(p)
	s.tryRetireStreamLocked(st)
	s.wakeLocked()
	return nil
}

func (s *Session) handleFrame(c *carrier, f frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !c.active || s.closed {
		return net.ErrClosed
	}
	if f.kind == kindData {
		c.received += uint64(len(f.data))
	}
	if f.kind == kindSessionWindow {
		return s.receiveSessionCreditLocked(f)
	}
	if f.kind == kindTransmissionRetire {
		return s.handleTransmissionRetireLocked(f)
	}
	if handled, err := s.peerAttemptLocked(c, f); err != nil {
		return err
	} else if handled {
		return nil
	}
	if f.kind == kindPing || f.kind == kindPong {
		if f.stream != 0 || f.id != 0 {
			return ErrProtocol
		}
		if f.kind == kindPing {
			s.controlLocked(c, frame{kind: kindPong, offset: f.offset})
		} else if f.offset == c.pingID && !c.pingAt.IsZero() {
			c.observeRTT(time.Since(c.pingAt))
		}
		return nil
	}
	if f.stream == 0 || f.stream%2 == 0 {
		return ErrProtocol
	}
	switch f.kind {
	case kindOpen:
		err := s.handleOpenLocked(c, f)
		if err == nil {
			s.markPeerProcessedLocked(f.id)
		}
		return err
	case kindOpenOK, kindACK:
		if f.id == 0 {
			return ErrProtocol
		}
		return s.ackLocked(c, f)
	case kindWindow:
		if st := s.streamForCreditLocked(f.stream); st != nil {
			if err := st.receiveCreditLocked(f); err != nil {
				return err
			}
			s.tryRetireStreamLocked(st)
			s.wakeLocked()
		} else if term, ok := s.terminal[f.stream]; ok && (f.offset > term.txFinal || f.id < f.offset || f.id-f.offset > MaxStreamWindow) {
			return ErrProtocol
		}
	case kindData:
		if f.id == 0 || len(f.data) == 0 || f.offset > ^uint64(0)-uint64(len(f.data)) {
			return ErrProtocol
		}
		st := s.streamForCreditLocked(f.stream)
		if st == nil {
			if term, ok := s.terminal[f.stream]; ok {
				if f.offset+uint64(len(f.data)) > term.rxFinal {
					return finalSizeFailure("late STREAM_DATA exceeds retired final size")
				}
			} else if !s.seen[f.stream] && !(s.maxSeen > 8192 && f.stream <= s.maxSeen-8192) {
				return streamStateFailure("STREAM_DATA for unknown Stream")
			}
			s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id})
			s.markPeerProcessedLocked(f.id)
			return nil
		}
		if !st.open && s.server && !st.closed {
			return streamStateFailure("STREAM_DATA before Stream acceptance")
		}
		if err := st.receiveLocked(f.offset, f.data); err != nil {
			if !errors.Is(err, ErrResourceLimit) {
				return err
			}
			s.resetLocked(st, mpx4ErrResourceLimit, true)
			st.err = err
			s.markPeerProcessedLocked(f.id)
			return nil
		}
		s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id, offset: uint64(time.Since(s.clockStart) / time.Microsecond)})
		s.markPeerProcessedLocked(f.id)
		s.wakeLocked()
	case kindFIN:
		err := s.handleFinalLocked(c, f)
		if err == nil {
			s.markPeerProcessedLocked(f.id)
		}
		return err
	case kindResetStream:
		err := s.handleResetStreamLocked(c, f)
		if err == nil {
			s.markPeerProcessedLocked(f.id)
		}
		return err
	case kindStopReceiving:
		err := s.handleStopReceivingLocked(c, f)
		if err == nil {
			s.markPeerProcessedLocked(f.id)
		}
		return err
	case kindCreditProbe:
		return s.handleCreditProbeLocked(c, f)
	case kindFinalConsumed:
		err := s.handleFinalConsumedLocked(c, f)
		if err == nil {
			s.markPeerProcessedLocked(f.id)
		}
		return err
	case kindOpenReject:
		if f.id == 0 || !validOpenRejectCode(f.offset) {
			return protocolViolation("invalid STREAM_OPEN_REJECT Error Code")
		}
		p := s.pending[f.id]
		if p == nil {
			if f.id > s.nextPacket {
				return transmissionIDFailure("STREAM_OPEN_REJECT references never-allocated Transmission ID")
			}
			if f.id <= s.settledThrough || s.settled[f.id] {
				return nil
			}
			return transmissionIDFailure("STREAM_OPEN_REJECT references missing unsettled Transmission")
		}
		if p.f.kind != kindOpen || p.f.stream != f.stream {
			return transmissionIDFailure("STREAM_OPEN_REJECT confirmation class mismatch")
		}
		if st := s.streams[f.stream]; st != nil {
			if f.id != st.openID {
				return transmissionIDFailure("STREAM_OPEN_REJECT Transmission ID mismatch")
			}
			// A duplicate OPEN already captured by another writer can reach the peer
			// after that peer retired it. Its rejection cannot revoke an accepted OPEN.
			if st.open {
				return nil
			}
			if st.txNext != 0 || st.rxHigh != 0 {
				return streamStateFailure("STREAM_OPEN_REJECT after Stream acceptance evidence")
			}
			if f.offset == mpx4ErrResourceLimit || f.offset == mpx4ErrStreamLimit {
				s.resourceLocked(LimitRemote, false)
			}
			s.resetLocked(st, f.offset, false)
		}
		s.markSettledLocked(p.f.id)
		s.removePendingLocked(p)
	default:
		return ErrProtocol
	}
	return nil
}

func (s *Session) accept(st *Stream) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || st.closed {
		return false
	}
	st.open = true
	st.advertiseCreditLocked(time.Now())
	s.controlLocked(nil, frame{kind: kindOpenOK, stream: st.id, id: st.openID})
	s.wakeLocked()
	return true
}

func (s *Session) aggregatePathLocked(now time.Time, restricted bool) *carrier {
	var best *carrier
	bestScore, earliest := 1e30, 1e30
	healthy := false
	for _, c := range s.paths {
		if restricted && !s.schedulerPathEligibleLocked(c) {
			continue
		}
		if c.active && !now.Before(c.penaltyUntil) {
			healthy = true
			break
		}
	}
	for _, c := range s.paths {
		if restricted && !s.schedulerPathEligibleLocked(c) {
			continue
		}
		if !c.active || (healthy && now.Before(c.penaltyUntil)) {
			continue
		}
		rate := max(c.goodput, 65536)
		if s.scheduler.configured == SchedulerWeighted && c.configuredRateBPS > 0 {
			rate = c.configuredRateBPS
		}
		// Flight bytes already include serialization queueing. Do not charge
		// measured queue-inflated RTT a second time when choosing a path.
		baseRTT := c.minRTT
		if baseRTT <= 0 {
			baseRTT = c.rtt
		}
		score := baseRTT.Seconds()/2 + float64(c.outstanding+MaxPayload)/rate
		earliest = min(earliest, score)
		// Cap application-layer in-flight work to an estimated bandwidth-delay
		// budget. Filling a slow carrier's OS send buffer creates avoidable HOL.
		budget := c.flightBudget()
		if s.scheduler.configured == SchedulerWeighted && c.configuredRateBPS > 0 {
			budget = weightedFlightBudget(c, c.configuredRateBPS)
		}
		if c.outstanding >= budget {
			c.budgetLimited = true
			c.sampleBudgetLimited = true
		}
		if len(c.queue) >= carrierQueue || c.outstanding >= budget {
			continue
		}
		if best == nil || score < bestScore || (score == bestScore && c.id < best.id) {
			best = c
			bestScore = score
		}
	}
	// Waiting briefly for the earliest route can beat sending immediately over
	// a high-delay path merely because another writer's tiny queue is full.
	if best != nil && bestScore > earliest+.025 {
		return nil
	}
	return best
}

func (s *Session) sweepLocked(now time.Time) {
	if now.Sub(s.lastSweep) < 50*time.Millisecond {
		return
	}
	s.lastSweep = now
	s.advertiseSessionCreditLocked(now, false)
	s.sweepClosingLocked(now)
	s.sweepSchedulerLocked(now)
	for _, p := range s.pending {
		if (p.f.kind == kindResetStream || p.f.kind == kindStopReceiving || p.f.kind == kindFinalConsumed) && now.Sub(p.created) > 30*time.Second {
			p.created = now
		}
		if now.Sub(p.created) > 30*time.Second {
			if st := s.streams[p.f.stream]; st != nil {
				s.resetLocked(st, mpx4ErrInternal, true)
			} else {
				s.removePendingLocked(p)
			}
			continue
		}
		if p.path != nil && !p.sentAt.IsZero() {
			c := p.path
			rto := max(500*time.Millisecond, 4*c.rtt)
			if now.Sub(p.sentAt) > rto {
				c.releaseFlight(p)
				if p.f.kind == kindData {
					// One delayed receipt can be reverse-path compression or a
					// multi-Stream startup burst. Count at most one timeout epoch per
					// RTO/2 and require two epochs without intervening DATA progress
					// before removing a Carrier from scheduling.
					epochGap := max(100*time.Millisecond, rto/2)
					newEpoch := c.lastTimeoutAt.IsZero() || now.Sub(c.lastTimeoutAt) >= epochGap
					if newEpoch {
						c.timeoutStreak++
						c.lastTimeoutAt = now
						c.errors++
						c.lastError = "delivery timeout; retransmitted without path penalty"
					}
					if newEpoch && c.timeoutStreak >= 2 && !now.Before(c.penaltyUntil) {
						c.goodput = max(65536, c.goodput*.5)
						c.capacitySamples = [8]float64{}
						c.capacityIndex = 0
						c.budget = max(initialPathBudget, c.flightBudget()/2)
						c.startupDone = true
						c.lastError = "repeated delivery timeout; temporarily deprioritized"
						c.penaltyUntil = now.Add(2 * time.Second)
						c.timeoutStreak = 1
						s.schedulerFailureLocked(c, "delivery_timeout", now)
					}
				}
				p.path = nil
				p.generation++
				p.sentAt = time.Time{}
				s.readyLocked(p, true)
				s.retransmits++
			}
		}
	}
	for _, c := range s.paths {
		if c.active && now.Sub(c.pingAt) >= time.Second {
			c.pingAt = now
			c.pingID = uint64(now.UnixNano())
			s.controlLocked(c, frame{kind: kindPing, offset: c.pingID})
		}
	}
	for _, st := range s.streams {
		if st.open && (now.Sub(st.windowAt) >= time.Second || (st.rxRead != st.windowSent && now.Sub(st.windowAt) >= 50*time.Millisecond)) {
			st.advertiseCreditLocked(now)
		}
	}
}

func (s *Session) run() {
	defer close(s.done)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			s.stop(s.ctx.Err())
			return
		case <-s.kick:
		case <-ticker.C:
		}
		now := time.Now()
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		expired := !s.noPathsSince.IsZero() && now.Sub(s.noPathsSince) > 30*time.Second
		s.sweepLocked(now)
		s.dispatchControlsLocked()
		s.dispatchLocked(now)
		s.mu.Unlock()
		if expired {
			s.stop(ErrNoPaths)
			return
		}
	}
}

func (s *Session) Snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Stats{Connections: len(s.streams), Sent: s.sent, Received: s.received, Retransmits: s.retransmits, WindowWaits: s.windowWaits, ReorderPeak: s.reorderPeak, PendingBytes: s.pendingBytes, BufferedBytes: s.bufferedBytes, ReceiveAllocated: s.receiveAllocated}
	out.SchedulerStats = s.schedulerSnapshotLocked()
	out.ReceiveCredit = s.receiveCredit
	now := time.Now()
	out.WindowSeed = s.windowSeed
	if !s.windowSeedAt.IsZero() {
		out.WindowSeedAgeMS = float64(max(time.Duration(0), now.Sub(s.windowSeedAt))) / float64(time.Millisecond)
	}
	baseCreditRTT, loadCreditRTT := s.creditRTTsLocked()
	out.CreditBaseRTTMS = float64(baseCreditRTT) / float64(time.Millisecond)
	out.CreditRTTMS = float64(loadCreditRTT) / float64(time.Millisecond)
	out.DemandStreams = s.activeDemandStreamsLocked(now)
	out.BulkStreams = s.activeBulkStreamsLocked(now)
	out.BulkWindowFloor = s.bulkWindowFloorLocked(now)
	out.ReceivePressure = s.receivePressurePercentLocked()
	out.StreamAllowance = MaxStreamWindow
	out.SessionRefillTarget = s.sessionRefillTargetLocked()
	out.StandbyWindow = s.standbyWindowLocked()
	out.Resources = s.resourceSnapshotLocked()
	out.Lifecycle = s.lifecycleSnapshotLocked()
	out.ReadyFrames = s.controlReady.Len()
	for _, q := range s.ready {
		out.ReadyFrames += q.Len()
	}
	for _, st := range s.streams {
		out.WindowTarget = max(out.WindowTarget, st.windowTarget)
		out.WarmTarget = max(out.WarmTarget, st.warmHistoryTargetLocked(now))
		out.ReorderBytes += st.buffered - int(st.rxContiguous-st.rxRead)
	}
	for _, c := range s.paths {
		if c.active {
			out.Paths++
		}
		measured, validSamples := schedulerMeasuredRate(c)
		budget := c.flightBudget()
		if s.scheduler.configured == SchedulerWeighted && c.configuredRateBPS > 0 {
			budget = weightedFlightBudget(c, c.configuredRateBPS)
		}
		out.PathStats = append(out.PathStats, PathStats{MeasuredDeliveryBPS: measured, ValidDeliverySamples: validSamples, Role: c.scheduler.role, RoleReason: c.scheduler.lastRoleReason, DeliverySamples: c.deliverySamples, SchedulerProbeCount: c.scheduler.probeCount, SchedulerProbeBytes: c.scheduler.probeBytes, SchedulerProbeDebtPeak: c.scheduler.probeDebtPeak, SchedulerDataACKs: c.scheduler.dataACKs, ID: int(c.id), Address: c.address, Connected: c.active, Sent: c.sent, Received: c.received, RTTMS: float64(c.rtt) / float64(time.Millisecond), GoodputBPS: c.goodput, ConfiguredRateBPS: c.configuredRateBPS, Outstanding: c.outstanding, Queue: c.queued, Errors: c.errors, LastError: c.lastError, Budget: budget, DialAttempts: c.dialAttempts, Connections: c.connects, ConnectedAt: c.connectedAt, DisconnectedAt: c.disconnectedAt, ControlOutstanding: c.controlOutstanding, ControlQueue: c.controlQueued + 64*len(c.control)})
	}
	sort.Slice(out.PathStats, func(i, j int) bool { return out.PathStats[i].ID < out.PathStats[j].ID })
	return out
}

func (s *Session) WaitPaths(ctx context.Context, n int) error {
	for {
		s.mu.Lock()
		count := 0
		for _, c := range s.paths {
			if c.active {
				count++
			}
		}
		ch := s.changed
		closed, err := s.closed, s.err
		s.mu.Unlock()
		if closed {
			return err
		}
		if count >= n {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}
