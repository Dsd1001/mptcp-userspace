package multipath

import (
	"container/list"
	"context"
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
	Paths            int            `json:"paths"`
	Connections      int            `json:"connections"`
	Sent             uint64         `json:"sent"`
	Received         uint64         `json:"received"`
	Retransmits      uint64         `json:"retransmits"`
	WindowWaits      uint64         `json:"window_waits"`
	ReorderBytes     int            `json:"reorder_bytes"`
	ReorderPeak      int            `json:"reorder_peak"`
	PendingBytes     int            `json:"pending_bytes"`
	BufferedBytes    int            `json:"buffered_bytes"`
	ReceiveAllocated int            `json:"receive_allocated_bytes"`
	PathStats        []PathStats    `json:"path_stats"`
	ReceiveCredit    int            `json:"receive_credit_bytes"`
	WindowTarget     int            `json:"max_stream_window_target"`
	ReadyFrames      int            `json:"ready_frames"`
	Resources        ResourceStats  `json:"resources"`
	Lifecycle        LifecycleStats `json:"lifecycle"`
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
	scheduler                         schedulerPathState
	id                                byte
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
	lastACK                           time.Time
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
	paths                                           map[byte]*carrier
	pathCapacities                                  [9]PathCapacity
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
	s := &Session{clockStart: time.Now(), ctx: ctx, cancel: cancel, id: id, server: server, changed: make(chan struct{}), kick: make(chan struct{}, 1), done: make(chan struct{}), streams: make(map[uint64]*Stream), pending: make(map[uint64]*outbound), paths: make(map[byte]*carrier), seen: make(map[uint64]bool), nextStream: 1, noPathsSince: time.Now(), onOpen: onOpen}
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

func (s *Session) addCarrier(id byte, address string, conn *secureConn) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		conn.Close()
		return net.ErrClosed
	}
	c := &carrier{id: id, address: address, conn: conn, active: true, done: make(chan struct{}), queue: make(chan sendTask, carrierQueue), control: make(chan frame, 512), reliableControl: make(chan sendTask, controlCarrierQueue), rtt: 50 * time.Millisecond, goodput: 4 << 20, configuredRateBPS: conn.configuredRateBPS, sampleAt: time.Now()}
	c.scheduler.role = RoleLearning
	c.scheduler.lastRoleReason = "awaiting_3_delivery_samples"
	// Fill the bounded capacity history with the explicit startup prior, not
	// zeros. One short low-utilization epoch must not poison route selection;
	// eight genuinely loaded lower-rate epochs still replace the entire prior.
	for i := range c.capacitySamples {
		c.capacitySamples[i] = c.goodput
	}
	if old := s.paths[id]; old != nil {
		s.detachLocked(old, fmt.Errorf("carrier replaced"))
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
	}
	s.wakeLocked()
}

func (s *Session) carrierFailure(c *carrier, err error) {
	s.mu.Lock()
	s.detachLocked(c, err)
	s.mu.Unlock()
}

func (s *Session) recordDialError(id byte, address string, err error) {
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
			s.carrierFailure(c, err)
			return
		}
		if err = s.handleFrame(c, f); err != nil {
			s.carrierFailure(c, err)
			return
		}
	}
}

func (s *Session) controlLocked(c *carrier, f frame) {
	// A directed ACK/PING/PONG keeps its original carrier. DATA delivery
	// measurements rely on that identity; never reroute a usable directed send.
	if c != nil && c.active {
		select {
		case c.control <- f:
			return
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
			return
		default:
		}
	}
	// Exhausted control queues remain bounded; existing retries and periodic
	// WINDOW regeneration recover loss without introducing another payload copy.
	s.controlDrops++
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
	if s.nextPacket == ^uint64(0) {
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
	if p == nil || p.f.stream != f.stream {
		return nil
	}
	if p.f.kind == kindOpen && f.kind != kindOpenOK {
		return nil
	}
	if p.f.kind != kindOpen && f.kind == kindOpenOK {
		return nil
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
		if p.attempts == 1 {
			c.observeDelivery(now, p, f.offset)
			s.schedulerReceiptLocked(c, p, now)
		}
	}
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
		return s.handleOpenLocked(c, f)
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
					return ErrProtocol
				}
			} else if !s.seen[f.stream] && !(s.maxSeen > 8192 && f.stream <= s.maxSeen-8192) {
				return ErrProtocol
			}
			s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id})
			return nil
		}
		if !st.open && s.server && !st.closed {
			return ErrProtocol
		}
		if err := st.receiveLocked(f.offset, f.data); err != nil {
			if !errors.Is(err, ErrResourceLimit) {
				return err
			}
			s.resetLocked(st, 4, true)
			st.err = err
			return nil
		}
		s.controlLocked(c, frame{kind: kindACK, stream: f.stream, id: f.id, offset: uint64(time.Since(s.clockStart))})
		s.wakeLocked()
	case kindFIN:
		return s.handleFinalLocked(c, f)
	case kindResetStream:
		return s.handleResetStreamLocked(c, f)
	case kindStopReceiving:
		return s.handleStopReceivingLocked(c, f)
	case kindCreditProbe:
		return s.handleCreditProbeLocked(c, f)
	case kindFinalConsumed:
		return s.handleFinalConsumedLocked(c, f)
	case kindOpenReject:
		if f.id == 0 || f.offset < 1 || f.offset > 4 {
			return ErrProtocol
		}
		if st := s.streams[f.stream]; st != nil {
			if f.id != st.openID {
				return ErrProtocol
			}
			// A duplicate OPEN already captured by another writer can reach the peer
			// after that peer retired it. Its rejection cannot revoke an accepted OPEN.
			if st.open {
				return nil
			}
			if st.txNext != 0 || st.rxHigh != 0 {
				return ErrProtocol
			}
			if f.offset == 4 {
				s.resourceLocked(LimitRemote, false)
			}
			s.resetLocked(st, f.offset, false)
		}
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
				s.resetLocked(st, 1, true)
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
				if p.f.kind == kindData && !now.Before(c.penaltyUntil) {
					c.goodput = max(65536, c.goodput*.5)
					c.capacitySamples = [8]float64{}
					c.capacityIndex = 0
					c.budget = max(initialPathBudget, c.flightBudget()/2)
					c.startupDone = true
					c.errors++
					c.lastError = "delivery timeout; temporarily deprioritized"
				}
				if p.f.kind == kindData {
					c.penaltyUntil = now.Add(2 * time.Second)
					s.schedulerFailureLocked(c, "delivery_timeout", now)
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
	out.Resources = s.resourceSnapshotLocked()
	out.Lifecycle = s.lifecycleSnapshotLocked()
	out.ReadyFrames = s.controlReady.Len()
	for _, q := range s.ready {
		out.ReadyFrames += q.Len()
	}
	for _, st := range s.streams {
		out.WindowTarget = max(out.WindowTarget, st.windowTarget)
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
