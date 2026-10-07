package multipath

import "time"

const (
	MaxStreamWindow      = 16 << 20
	SessionCreditLimit   = 128 << 20
	BootstrapCreditLimit = MaxStreams * StreamWindow
	GrowthCreditLimit    = SessionCreditLimit - BootstrapCreditLimit
	SmallStreamWindow    = 128 << 10
	StandbyStreamWindow  = 192 << 10
	// OpenBootstrapWindow is the explicit receive entitlement sent alongside a
	// new client STREAM_OPEN. StreamWindow remains the 32 KiB Session bootstrap
	// accounting baseline; bytes above it are charged to shared growth credit.
	OpenBootstrapWindow      = 192 << 10
	InitialWindowShareBudget = 16 << 20
	BulkWindowFloor          = 512 << 10
	BulkWindowMaxFloor       = 4 << 20
	SmallGrowthReserve       = 4 << 20
	initialPathBudget        = 2 * MaxPayload
	maxPathBudget            = 8 << 20
	creditIdle               = 5 * time.Second
	warmHistoryHalfLife      = 30 * time.Second
	warmHistoryTTL           = 2 * time.Minute
	streamCreditRefreshBatch = 128 << 10
	dataDispatchBatch        = 128 // bounded Session critical section; unfinished ready work self-kicks immediately
)

// All methods in this file run with Session.mu held. An absolute grant is
// irrevocable; windowTarget may shrink but rxLimit can only increase.
func (s *Session) creditRTTsLocked() (time.Duration, time.Duration) {
	now := time.Now()
	weightedBaseNS, weightedLoadNS, totalWeight := 0.0, 0.0, 0.0
	for _, c := range s.paths {
		if !c.active || now.Before(c.penaltyUntil) {
			continue
		}
		base := c.minRTT
		if base <= 0 {
			base = c.rtt
		}
		if base <= 0 {
			continue
		}
		base = min(500*time.Millisecond, max(time.Millisecond, base))
		load := c.rtt
		if load <= 0 {
			load = base
		}
		// DATA receipt feedback sees queueing that the empty-link minimum does
		// not. Use the carrier's already-smoothed RTT under load, but bound the
		// queue contribution so a transient 500-800 ms spike cannot make Stream
		// WINDOW autotune run away. The effective RTT can rise to four base RTTs
		// and normally no higher than 150 ms; a genuinely higher base RTT is
		// never forced below its propagation floor.
		loadCap := max(base, min(150*time.Millisecond, 4*base))
		load = min(loadCap, max(base, load))
		weight := max(c.goodput, float64(65536))
		if s.scheduler.configured == SchedulerWeighted && c.configuredRateBPS > 0 {
			weight = c.configuredRateBPS
		}
		weightedBaseNS += float64(base) * weight
		weightedLoadNS += float64(load) * weight
		totalWeight += weight
	}
	if totalWeight == 0 {
		return 10 * time.Millisecond, 10 * time.Millisecond
	}
	return time.Duration(weightedBaseNS / totalWeight), time.Duration(weightedLoadNS / totalWeight)
}

func (s *Session) creditRTTLocked() time.Duration {
	_, load := s.creditRTTsLocked()
	return load
}

func (s *Session) baseCreditRTTLocked() time.Duration {
	base, _ := s.creditRTTsLocked()
	return base
}

func (s *Session) legacyCreditRTTLocked() time.Duration {
	rtt := 10 * time.Millisecond
	for _, c := range s.paths {
		if c.active && time.Now().After(c.penaltyUntil) {
			rtt = max(rtt, min(c.rtt, 500*time.Millisecond))
		}
	}
	return rtt
}

func (s *Session) liveReceiveStreamsLocked() int {
	count := 0
	for _, st := range s.streams {
		if st == nil || st.closed || st.receiveStopped {
			continue
		}
		count++
	}
	return max(1, count)
}

func (s *Session) standbyWindowLocked() int {
	share := BootstrapCreditLimit / s.liveReceiveStreamsLocked()
	return min(StandbyStreamWindow, max(StreamWindow, share))
}

func (s *Session) initialWindowLocked() int {
	live := s.liveReceiveStreamsLocked()
	if s.scheduler.configured != SchedulerWeighted && live <= 1 {
		return StreamWindow
	}
	share := InitialWindowShareBudget / live
	return min(StandbyStreamWindow, max(StreamWindow, share))
}

func (s *Session) activeDemandStreamsLocked(now time.Time) int {
	count := 0
	for _, st := range s.streams {
		if st == nil || st.closed || st.receiveStopped || now.Sub(st.lastRead) > creditIdle {
			continue
		}
		if st.demandBytes >= uint64(StreamWindow/2) {
			count++
		}
	}
	return count
}

func (s *Session) activeBulkStreamsLocked(now time.Time) int {
	count := 0
	for _, st := range s.streams {
		if st == nil || st.closed || st.receiveStopped || !st.bulkActive || now.Sub(st.lastRead) > creditIdle {
			continue
		}
		count++
	}
	return count
}

func (s *Session) receivePressurePercentLocked() int {
	credit := 0
	if SessionCreditLimit > 0 {
		credit = 100 * s.receiveCredit / SessionCreditLimit
	}
	allocated := 0
	if MaxBuffered > 0 {
		allocated = 100 * s.receiveAllocated / MaxBuffered
	}
	return min(100, max(credit, allocated))
}

func (s *Session) bulkWindowFloorLocked(now time.Time) int {
	active := max(1, s.activeDemandStreamsLocked(now))
	minFloor := BulkWindowFloor
	// Below 512 live Streams, real demand is the useful fair-share signal and
	// keeps the production 50-150 Stream case aggressive. At 512+ live Streams
	// we are in connection-storm territory: use every admitted receiver in the
	// denominator and let the floor fall with the existing standby policy.
	live := s.liveReceiveStreamsLocked()
	if live >= InitialWindowShareBudget/StreamWindow {
		active = max(active, live)
		minFloor = s.standbyWindowLocked()
	}
	floor := min(BulkWindowMaxFloor, max(minFloor, GrowthCreditLimit/active))
	switch pressure := s.receivePressurePercentLocked(); {
	case pressure >= 90:
		return 0
	case pressure >= 75:
		floor /= 2
	case pressure >= 50:
		floor = 3 * floor / 4
	}
	return max(StreamWindow, floor)
}

func (st *Stream) maybeActivateBulkLocked() bool {
	if st.bulkActive {
		return false
	}
	initial := st.initialWindow
	if initial <= 0 {
		initial = st.s.initialWindowLocked()
	}
	threshold := max(StreamWindow, initial/2)
	if st.demandBytes < uint64(threshold) {
		return false
	}
	st.bulkActive = true
	return true
}

func (s *Session) streamFairCeilingLocked(now time.Time) int {
	active := max(1, s.activeDemandStreamsLocked(now))
	return min(MaxStreamWindow, s.standbyWindowLocked()+2*GrowthCreditLimit/active)
}

func (s *Session) streamWindowCeilingLocked(st *Stream, now time.Time) int {
	// Entitlement itself does not reserve memory; committed DATA remains bounded
	// by the Session ledger. Still keep an opportunistic 2x fair share so a
	// high-fanout workload cannot let every Stream race straight to 16 MiB.
	fair := s.streamFairCeilingLocked(now)
	demand := s.standbyWindowLocked()
	if st.bulkActive || st.demandBytes >= uint64(MaxStreamWindow/8) {
		demand = MaxStreamWindow
	} else {
		demand = max(demand, int(8*st.demandBytes))
	}
	return min(MaxStreamWindow, max(StreamWindow, min(fair, demand)))
}

func (st *Stream) warmHistoryTargetLocked(now time.Time) int {
	if st.warmTarget <= 0 || st.warmAt.IsZero() {
		return 0
	}
	age := now.Sub(st.warmAt)
	if age < 0 {
		age = 0
	}
	if age >= warmHistoryTTL {
		return 0
	}
	target := st.warmTarget
	for steps := int(age / warmHistoryHalfLife); steps > 0 && target > StreamWindow; steps-- {
		target = max(StreamWindow, target/2)
	}
	return max(st.s.standbyWindowLocked(), target)
}

func (st *Stream) rememberWarmHistoryLocked(now time.Time) {
	standby := st.s.standbyWindowLocked()
	if st.windowTarget <= standby {
		return
	}
	if st.warmAt.IsZero() || now.Sub(st.warmAt) >= warmHistoryTTL || st.windowTarget >= st.warmTarget {
		st.warmTarget = st.windowTarget
		st.warmAt = now
	}
}

func (st *Stream) restoreWarmHistoryLocked(now time.Time) {
	if st.warmHistoryUsed || st.demandBytes < uint64(StreamWindow) {
		return
	}
	warm := st.warmHistoryTargetLocked(now)
	if warm == 0 {
		st.warmHistoryUsed = true
		return
	}
	st.windowTarget = max(st.windowTarget, min(warm, st.s.streamFairCeilingLocked(now)))
	st.warmHistoryUsed = true
}

// Called only once after the new receiver has consumed a full bootstrap.
// Idle/small control streams must not disable a measured warm seed. Preserve
// the existing protection for another recently active or unread bulk stream.
func (s *Session) warmSeedUncontendedLocked(st *Stream, now time.Time) bool {
	standby := s.standbyWindowLocked()
	for _, other := range s.streams {
		if other == st || other.closed || other.receiveStopped {
			continue
		}
		if other.rxHigh > other.rxRead && other.rxHigh-other.rxRead >= StreamWindow {
			return false
		}
		if now.Sub(other.lastRead) <= creditIdle && (other.windowTarget > standby || other.demandBytes >= StreamWindow) {
			return false
		}
	}
	return true
}

func (st *Stream) consumeCreditLocked(n int, now time.Time) {
	// RC7 treats Stream WINDOW as optimistic receive entitlement, not as a
	// per-Stream rate controller. Actual committed DATA remains bounded by the
	// Session ledger (128 MiB), bootstrap/growth accounting and physical pages.
	st.demandBytes += uint64(n)
	st.readSampleBytes += n
	st.lastRead = now
	st.windowTarget = MaxStreamWindow
	if st.demandBytes >= StreamWindow {
		st.bulkActive = true // telemetry only; no longer gates credit growth.
	}
	// WINDOW.offset is also how the sender learns application consumption and
	// releases its Session txUsed/txGrowth accounting. Keep this update fairly
	// frequent even though the per-Stream entitlement itself is a full 16 MiB.
	if st.rxRead-st.windowSent >= streamCreditRefreshBatch ||
		st.rxLimit-st.rxRead <= MaxStreamWindow/2 {
		st.advertiseCreditLocked(now)
	}
}

func (st *Stream) consumeLegacySingleCreditLocked(n int, now time.Time) {
	s := st.s
	if now.Sub(st.lastRead) > creditIdle {
		st.rememberWarmHistoryLocked(st.lastRead)
		st.demandBytes = 0
		st.windowTarget = s.standbyWindowLocked()
		st.warmSeedUsed = false
		st.warmHistoryUsed = false
		st.readSampleBytes = 0
		st.readRateBPS = 0
		st.readSampleAt = now
	}
	st.demandBytes += uint64(n)
	st.readSampleBytes += n
	st.lastRead = now
	st.restoreWarmHistoryLocked(now)
	interval := max(50*time.Millisecond, s.legacyCreditRTTLocked()/2)
	if elapsed := now.Sub(st.readSampleAt); elapsed >= interval {
		currentTarget := st.windowTarget
		rate := float64(st.readSampleBytes) / elapsed.Seconds()
		desired := min(MaxStreamWindow, max(StreamWindow, int(2*rate*(s.legacyCreditRTTLocked().Seconds()+.010))+min(4*MaxPayload, st.readSampleBytes)))
		if st.demandBytes < SmallStreamWindow {
			desired = min(desired, SmallStreamWindow)
		}
		pressureWindow := max(2*s.legacyCreditRTTLocked(), 100*time.Millisecond) + 20*time.Millisecond
		if currentTarget >= SmallStreamWindow && st.demandBytes >= SmallStreamWindow && elapsed <= pressureWindow && st.readSampleBytes >= currentTarget/2 {
			desired = max(desired, min(MaxStreamWindow, 2*currentTarget))
		}
		if desired > st.windowTarget {
			st.windowTarget = min(desired, 2*st.windowTarget)
		} else {
			st.windowTarget = max(desired, st.windowTarget*3/4)
		}
		st.readSampleBytes = 0
		st.readSampleAt = now
		if st.windowTarget > SmallStreamWindow {
			s.windowSeed = st.windowTarget
			s.windowSeedAt = now
		}
	}
	if st.windowTarget < SmallStreamWindow && st.demandBytes >= uint64(st.windowTarget) {
		st.windowTarget = min(SmallStreamWindow, 2*st.windowTarget)
	}
	if !st.warmSeedUsed && st.demandBytes >= StreamWindow {
		if s.windowSeedAt.IsZero() || now.Sub(s.windowSeedAt) >= creditIdle {
			st.warmSeedUsed = true
		} else if s.warmSeedUncontendedLocked(st, now) {
			st.windowTarget = max(st.windowTarget, min(MaxStreamWindow, s.windowSeed))
			st.warmSeedUsed = true
		}
	}
	st.rememberWarmHistoryLocked(now)
	threshold := uint64(min(128<<10, max(MaxPayload, st.windowTarget/4)))
	if st.rxRead-st.windowSent >= threshold || st.rxLimit-st.rxRead <= uint64(st.windowTarget/2) {
		st.advertiseCreditLocked(now)
	}
}

func (st *Stream) consumeAdaptiveCreditLocked(n int, now time.Time) {
	s := st.s
	if now.Sub(st.lastRead) > creditIdle {
		st.rememberWarmHistoryLocked(st.lastRead)
		st.demandBytes = 0
		st.windowTarget = s.standbyWindowLocked()
		st.bulkActive = false
		st.warmSeedUsed = false
		st.warmHistoryUsed = false
		st.readSampleBytes = 0
		st.readRateBPS = 0
		st.readSampleAt = now
	}
	st.demandBytes += uint64(n)
	st.readSampleBytes += n
	st.lastRead = now
	st.restoreWarmHistoryLocked(now)
	bulkActivated := st.maybeActivateBulkLocked()
	priorSeed, priorSeedAt := s.windowSeed, s.windowSeedAt

	feedbackRTT := s.creditRTTLocked()
	elapsed := now.Sub(st.readSampleAt)
	remaining := int(st.rxLimit - st.rxRead)
	pressure := remaining <= max(MaxPayload, st.windowTarget/2)

	// Re-estimate before the sender can run out of credit. Half-window
	// consumption is the normal trigger; a predicted exhaustion before the next
	// feedback opportunity can trigger even earlier. This removes the old fixed
	// 32->64->128->512 KiB staircase while retaining real-consumption evidence.
	evaluate := bulkActivated || elapsed >= max(10*time.Millisecond, feedbackRTT/4) ||
		st.readSampleBytes >= max(MaxPayload/2, st.windowTarget/2) || pressure
	if elapsed > 0 && st.readSampleBytes > 0 {
		sampleRate := float64(st.readSampleBytes) / max(elapsed.Seconds(), .001)
		if st.readRateBPS == 0 {
			st.readRateBPS = sampleRate
		} else if evaluate {
			st.readRateBPS = .75*st.readRateBPS + .25*sampleRate
		}
	}
	predicted := false
	if st.readRateBPS > 0 && remaining > 0 {
		exhaustion := time.Duration(float64(remaining) / st.readRateBPS * float64(time.Second))
		predicted = exhaustion <= feedbackRTT+20*time.Millisecond
		if predicted {
			evaluate = true
		}
	}
	if evaluate {
		desired := StreamWindow
		if st.readRateBPS > 0 {
			// Two bounded load-aware feedback RTTs plus 20 ms absorbs WINDOW
			// transmission, scheduling and sustained queueing without reacting to
			// extreme transient RTT spikes. The target is continuous, not a tier.
			horizon := 2*feedbackRTT + 20*time.Millisecond
			desired = int(st.readRateBPS*horizon.Seconds()) + 2*MaxPayload
			if pressure || predicted {
				// A receive window measures a rate that the receive window itself
				// may already be limiting. Under real pressure, add one more
				// feedback interval of observed demand so autotune can escape that
				// self-limited equilibrium without a fixed 2x step.
				pressureFloor := st.windowTarget + max(MaxPayload, int(st.readRateBPS*(feedbackRTT+10*time.Millisecond).Seconds()))
				desired = max(desired, pressureFloor)
			}
		}
		if st.bulkActive {
			// Once real consumption proves a sustained bulk Stream, flow control
			// should provide headroom rather than become a second congestion
			// controller. Use a pressure-scaled Session fair-share floor; Carrier
			// TCP congestion control, pacing and the MPX scheduler still determine
			// the actual sending rate.
			desired = max(desired, s.bulkWindowFloorLocked(now))
		}
		ceiling := s.streamWindowCeilingLocked(st, now)
		desired = min(ceiling, max(StreamWindow, desired))
		if s.receivePressurePercentLocked() >= 90 && desired > st.windowTarget {
			// At hard memory pressure, stop issuing additional per-Stream
			// headroom. Already advertised absolute credit is never revoked.
			desired = st.windowTarget
		}
		if desired > st.windowTarget {
			st.windowTarget = desired
		} else {
			// Growth follows demand immediately; shrink slowly so one quiet read
			// or reordered burst cannot collapse a warmed receive window.
			st.windowTarget = max(desired, st.windowTarget*7/8)
		}
		st.readSampleBytes = 0
		st.readSampleAt = now
	}

	// A recent uncontended bulk Stream may immediately reuse the full measured
	// seed after consuming one real bootstrap. This preserves the fast single-
	// stream path. Competing Streams are excluded here and use the continuous
	// BDP/fair-share controller above instead.
	if !st.warmSeedUsed && st.demandBytes >= StreamWindow {
		switch {
		case priorSeedAt.IsZero() || now.Sub(priorSeedAt) >= creditIdle:
			st.warmSeedUsed = true
		case s.warmSeedUncontendedLocked(st, now):
			st.windowTarget = max(st.windowTarget, min(MaxStreamWindow, priorSeed))
			st.warmSeedUsed = true
			// Temporary contention is not a permanent decision. Keep retry
			// eligibility so a just-closed warmup Stream cannot suppress the seed
			// for the lifetime of the new Stream. Real concurrent bulk Streams stay
			// on the BDP/fair-share controller while they remain active.
		}
	}
	st.rememberWarmHistoryLocked(now)
	if st.windowTarget > SmallStreamWindow {
		// Keep a recent high-water seed; a transient contended/small sample must
		// not erase a proven bulk entitlement before it can be reused. Lower
		// demand can replace it only after the old seed naturally expires.
		if s.windowSeedAt.IsZero() || now.Sub(s.windowSeedAt) >= creditIdle || st.windowTarget >= s.windowSeed {
			s.windowSeed = st.windowTarget
			s.windowSeedAt = now
		}
	}

	remaining = int(st.rxLimit - st.rxRead)
	predictedExhaustion := false
	if st.readRateBPS > 0 && remaining > 0 {
		exhaustion := time.Duration(float64(remaining) / st.readRateBPS * float64(time.Second))
		predictedExhaustion = exhaustion <= feedbackRTT+20*time.Millisecond
	}
	// Keep the advertised limit rolling ahead of DATA. Fifty-percent remaining
	// credit is the normal refill point; prediction can refresh sooner.
	if st.rxRead-st.windowSent >= uint64(min(128<<10, max(MaxPayload, st.windowTarget/4))) ||
		remaining <= st.windowTarget/2 || predictedExhaustion {
		st.advertiseCreditLocked(now)
	}
}

func (st *Stream) advertiseCreditLocked(now time.Time) {
	if !st.open || st.closed {
		return
	}
	if st.receiveStopped {
		st.advertiseConsumedLocked(nil)
		return
	}
	// RC7 gives every open Stream the full protocol-local allowance. This is an
	// entitlement only: grantCreditLocked allocates no receive pages and the
	// shared Session ledger still caps actual unconsumed DATA at 128 MiB.
	st.initialWindow = MaxStreamWindow
	st.windowTarget = MaxStreamWindow
	if st.rxRead <= ^uint64(0)-MaxStreamWindow {
		st.grantCreditLocked(st.rxRead + MaxStreamWindow)
	}
	st.s.controlLocked(nil, frame{kind: kindWindow, stream: st.id, offset: st.rxRead, id: st.rxLimit})
	st.windowAt = now
	st.windowSent = st.rxRead
}

func (st *Stream) receiveCreditLocked(f frame) error {
	if f.id < f.offset || f.id-f.offset > MaxStreamWindow || f.offset > st.txNext {
		return flowControlFailure("invalid Stream credit advertisement")
	}
	oldConsumed, oldMaximum := st.peerCreditConsumed, st.peerLimit
	switch {
	case f.offset >= oldConsumed && f.id >= oldMaximum:
		if err := st.releaseSendCreditLocked(f.offset); err != nil {
			return err
		}
		st.peerCreditConsumed, st.peerLimit = f.offset, f.id
		// This WINDOW can unblock the addressed Stream directly. Shared
		// Session credit released by its consumed offset was already handed
		// out proportionally by releaseSendCreditLocked.
		if st.writeWaiting && st.writeWaitReason == waitStreamWindow {
			st.s.signalStreamWriterLocked(st)
		}
		return nil
	case f.offset <= oldConsumed && f.id <= oldMaximum:
		// Fully stale/duplicate credit can arrive later on another Carrier.
		return nil
	default:
		return flowControlFailure("crossed Stream credit advertisement")
	}
}

func (c *carrier) observeRTT(sample time.Duration) {
	if sample <= 0 {
		return
	}
	if c.minRTT == 0 {
		c.minRTT = sample
		c.rtt = sample
	} else {
		c.minRTT = min(c.minRTT, sample)
		c.rtt = time.Duration(.875*float64(c.rtt) + .125*float64(sample))
	}
}

func (c *carrier) observeDelivery(now time.Time, p *outbound, receiverStamp uint64) {
	if !p.sentAt.IsZero() {
		c.observeRTT(now.Sub(p.sentAt))
	}
	c.lastACK = now
	// Capacity discovery must not be limited by the small flight whose ACK
	// rate is being measured. Grow only after a whole budget of real DATA
	// receipts, and only when the dispatcher actually hit that budget.
	if !c.startupDone {
		c.growthACK += len(p.f.data)
		if c.deliverySamples >= 2 && c.minRTT > 0 && c.rtt > 2*c.minRTT {
			c.startupDone = true
		}
		if c.budgetLimited && c.growthACK >= c.flightBudget() {
			c.budget = min(maxPathBudget, 2*c.flightBudget())
			c.growthACK = 0
			c.budgetLimited = false
		}
		if c.budget >= maxPathBudget || c.deliverySamples >= 6 {
			c.startupDone = true
		}
	}
	// MPX/4 DATA receipts contain the receiver's monotonic arrival clock.
	// A busy reverse-direction carrier can compress/delay ACKs; measuring
	// their local arrival spacing would misclassify equal forward links.
	// Only time differences on that same remote clock are used (no sync).
	if receiverStamp == 0 {
		return
	}
	if receiverStamp <= c.remoteSampleAt {
		return
	}
	// TRANSMISSION_ACK carries receiver_timestamp_us. Keep every comparison
	// and conversion in that wire unit; casting the raw value to time.Duration
	// would interpret microseconds as nanoseconds and suppress all real delivery
	// samples by 1000x.
	const receiverClockResetUS = uint64(10 * time.Second / time.Microsecond)
	if c.remoteSampleAt == 0 || receiverStamp-c.remoteSampleAt > receiverClockResetUS {
		c.remoteSampleAt = receiverStamp
		c.ackBytes = 0
		return
	}
	c.ackBytes += uint64(len(p.f.data))
	elapsed := time.Duration(receiverStamp-c.remoteSampleAt) * time.Microsecond
	if elapsed < max(100*time.Millisecond, c.minRTT) {
		return
	}
	observed := float64(c.ackBytes) / elapsed.Seconds()
	// A path that was never offered a full flight only measured utilization,
	// not capacity. Treating a tiny startup/sparse sample as a slow-link rate
	// can make delivery-cost selection permanently stop using that path.
	// A lower sample may age the capacity filter only when actual offered
	// work filled its bounded budget during this epoch. Higher real receipt
	// rates remain useful even without a full-flight observation.
	limited := c.sampleBudgetLimited
	c.sampleBudgetLimited = false
	if observed < c.goodput && !limited {
		c.ackBytes = 0
		c.sampleAt = now
		c.remoteSampleAt = receiverStamp
		return
	}
	// Recent delivery capacity, not application-limited average utilization.
	// Keep only eight measured epochs; lower capacity replaces old samples.
	c.capacitySamples[c.capacityIndex%len(c.capacitySamples)] = min(1<<30, max(65536, observed))
	c.capacityIndex = (c.capacityIndex + 1) % len(c.capacitySamples)
	c.goodput = 65536
	for _, rate := range c.capacitySamples {
		c.goodput = max(c.goodput, rate)
	}
	c.deliverySamples++
	c.ackBytes = 0
	c.sampleAt = now
	c.remoteSampleAt = receiverStamp
	c.updateBudget()
}

func (c *carrier) flightBudget() int {
	return min(maxPathBudget, max(initialPathBudget, c.budget))
}

func (c *carrier) updateBudget() {
	rtt := c.minRTT
	if rtt <= 0 {
		rtt = c.rtt
	}
	rtt = min(time.Second, max(time.Millisecond, rtt))
	// Receipt feedback shares the reverse carrier with real DATA. Budget
	// for bounded observed feedback delay, not just empty-link propagation.
	// The four-base-RTT cap prevents queue-inflation feedback from running away.
	feedback := min(4*rtt, max(rtt, c.rtt))
	desired := min(maxPathBudget, max(initialPathBudget, int(max(65536, c.goodput)*(1.25*feedback.Seconds()+.015))+2*MaxPayload))
	current := c.flightBudget()
	if desired > current {
		c.budget = min(desired, current*2)
	} else if desired < current*3/4 && (c.startupDone || c.deliverySamples == 0) {
		c.budget = max(desired, current*3/4)
	} else {
		c.budget = current
	}
}

func (s *Session) readyLocked(p *outbound, front bool) {
	if p.ready != nil {
		return
	}
	if p.f.kind != kindData {
		if front {
			p.ready = s.controlReady.PushFront(p)
		} else {
			p.ready = s.controlReady.PushBack(p)
		}
		return
	}
	if s.ready == nil {
		s.ready = make(map[uint64]*dataReadyQueue)
	}
	rq := s.ready[p.f.stream]
	if rq == nil {
		rq = &dataReadyQueue{stream: p.f.stream}
		s.ready[p.f.stream] = rq
	}
	wasEmpty := rq.frames.Len() == 0
	if front {
		p.ready = rq.frames.PushFront(p)
	} else {
		p.ready = rq.frames.PushBack(p)
	}
	s.readyFrames++
	if wasEmpty {
		if front {
			rq.entry = s.readyStreams.PushFront(rq)
		} else {
			rq.entry = s.readyStreams.PushBack(rq)
		}
	}
	if rq.frames.Len() >= 4 {
		s.bulkReady = rq
	}
}

func (s *Session) unreadyLocked(p *outbound) {
	if p.ready == nil {
		return
	}
	if p.f.kind != kindData {
		s.controlReady.Remove(p.ready)
		p.ready = nil
		return
	}
	rq := s.ready[p.f.stream]
	if rq == nil {
		p.ready = nil
		return
	}
	rq.frames.Remove(p.ready)
	p.ready = nil
	if s.readyFrames > 0 {
		s.readyFrames--
	}
	if rq.frames.Len() == 0 {
		if rq.entry != nil {
			s.readyStreams.Remove(rq.entry)
			rq.entry = nil
		}
		delete(s.ready, p.f.stream)
		if s.bulkReady == rq {
			s.bulkReady = nil
		}
	}
}

// Fair DATA dispatch uses an intrusive per-Stream ready ring. The hot path is
// allocation-free and O(frames dispatched): no map scan, ID slice, or sort is
// performed under Session.mu. A bounded batch yields the Session lock; if ready
// work remains, the Session self-kicks so dispatch resumes immediately.
func (s *Session) dispatchLocked(now time.Time) {
	started := time.Now()
	s.dispatchRuns++
	dispatched := 0
	defer func() {
		elapsed := uint64(time.Since(started))
		s.dispatchNS += elapsed
		if elapsed > s.dispatchMaxNS {
			s.dispatchMaxNS = elapsed
		}
		s.dispatchFrames += uint64(dispatched)
		if dispatched >= dataDispatchBatch && s.readyStreams.Len() != 0 {
			s.kickLocked()
		}
	}()

	dispatchOne := func(rq *dataReadyQueue, advanceFair bool) bool {
		if rq == nil || rq.frames.Len() == 0 {
			return true
		}
		c := s.pathLocked(now)
		if c == nil {
			return false
		}
		p := rq.frames.Front().Value.(*outbound)
		s.unreadyLocked(p)
		p.path = c
		p.generation++
		p.sentAt = time.Time{}
		if c.outstanding == 0 && (c.lastACK.IsZero() || now.Sub(c.lastACK) > max(200*time.Millisecond, 3*c.rtt)) {
			c.sampleAt = now
			c.remoteSampleAt = 0
			c.ackBytes = 0
			c.sampleBudgetLimited = false
			if !c.lastACK.IsZero() && now.Sub(c.lastACK) > creditIdle {
				c.budget = initialPathBudget
				c.capacitySamples = [8]float64{}
				c.capacityIndex = 0
				c.startupDone = false
				c.growthACK = 0
			}
		}
		c.outstanding += p.cost
		c.queued += p.cost
		select {
		case c.queue <- sendTask{p: p, generation: p.generation}:
			s.schedulerAllocatedLocked(c, p, now)
			if advanceFair {
				s.dispatchSequence++
			}
			if rq.entry != nil && rq.frames.Len() != 0 {
				s.readyStreams.MoveToBack(rq.entry)
			}
			if rq.frames.Len() >= 4 {
				s.bulkReady = rq
			}
			dispatched++
			return true
		default:
			c.outstanding -= p.cost
			c.queued -= p.cost
			p.path = nil
			s.readyLocked(p, true)
			return false
		}
	}

	for dispatched < dataDispatchBatch && s.readyStreams.Len() != 0 {
		e := s.readyStreams.Front()
		rq, _ := e.Value.(*dataReadyQueue)
		if rq == nil || rq.frames.Len() == 0 {
			if rq != nil {
				delete(s.ready, rq.stream)
				rq.entry = nil
			}
			s.readyStreams.Remove(e)
			continue
		}
		if !dispatchOne(rq, true) {
			return
		}
		// Preserve the old bounded bulk bonus without scanning every ready
		// Stream. A sustained backlog becomes the current O(1) bulk hint and
		// receives one extra turn per eight fair dispatches.
		if s.dispatchSequence%8 == 0 && s.bulkReady != nil && s.bulkReady.frames.Len() >= 4 {
			bonus := s.bulkReady
			if !dispatchOne(bonus, false) {
				return
			}
		}
	}
}
