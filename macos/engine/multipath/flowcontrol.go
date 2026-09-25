package multipath

import (
	"container/list"
	"sort"
	"time"
)

const (
	MaxStreamWindow      = 16 << 20
	SessionCreditLimit   = 128 << 20
	BootstrapCreditLimit = MaxStreams * StreamWindow
	GrowthCreditLimit    = SessionCreditLimit - BootstrapCreditLimit
	SmallStreamWindow    = 128 << 10
	SmallGrowthReserve   = 4 << 20
	initialPathBudget    = 2 * MaxPayload
	maxPathBudget        = 8 << 20
	creditIdle           = 5 * time.Second
	dataDispatchBatch    = MaxStreams / 4 // preserve ~4 scheduler cycles for a full ready-set sweep as capacity scales
)

// All methods in this file run with Session.mu held. An absolute grant is
// irrevocable; windowTarget may shrink but rxLimit can only increase.
func (s *Session) creditRTTLocked() time.Duration {
	rtt := 10 * time.Millisecond
	for _, c := range s.paths {
		if c.active && time.Now().After(c.penaltyUntil) {
			rtt = max(rtt, min(c.rtt, 500*time.Millisecond))
		}
	}
	return rtt
}

// Called only once after the new receiver has consumed a full bootstrap.
// Idle/small control streams must not disable a measured warm seed. Preserve
// the existing protection for another recently active or unread bulk stream.
func (s *Session) warmSeedUncontendedLocked(st *Stream, now time.Time) bool {
	for _, other := range s.streams {
		if other == st || other.closed || other.receiveStopped {
			continue
		}
		if other.rxHigh > other.rxRead && other.rxHigh-other.rxRead >= StreamWindow {
			return false
		}
		if now.Sub(other.lastRead) <= creditIdle && (other.windowTarget > StreamWindow || other.demandBytes >= StreamWindow) {
			return false
		}
	}
	return true
}

func (st *Stream) consumeCreditLocked(n int, now time.Time) {
	s := st.s
	if now.Sub(st.lastRead) > creditIdle {
		st.demandBytes = 0
		st.windowTarget = StreamWindow
		st.warmSeedUsed = false
		st.readSampleBytes = 0
		st.readSampleAt = now
	}
	st.demandBytes += uint64(n)
	st.readSampleBytes += n
	st.lastRead = now
	interval := max(50*time.Millisecond, s.creditRTTLocked()/2)
	if elapsed := now.Sub(st.readSampleAt); elapsed >= interval {
		rate := float64(st.readSampleBytes) / elapsed.Seconds()
		desired := min(MaxStreamWindow, max(StreamWindow, int(2*rate*(s.creditRTTLocked().Seconds()+.010))+min(4*MaxPayload, st.readSampleBytes)))
		if st.demandBytes < SmallStreamWindow {
			desired = min(desired, SmallStreamWindow)
		}
		if desired > st.windowTarget {
			st.windowTarget = min(desired, 2*st.windowTarget)
		} else {
			// Reordering creates bursty consumption. A single quiet sample
			// must not collapse a warmed window; idle still resets it promptly.
			st.windowTarget = max(desired, st.windowTarget*3/4)
		}
		st.readSampleBytes = 0
		st.readSampleAt = now
		if st.windowTarget > SmallStreamWindow {
			s.windowSeed = st.windowTarget
			s.windowSeedAt = now
		}
	}
	// Real full-bootstrap consumption, not OPEN, may accelerate interactive ramp-up.
	if st.windowTarget < SmallStreamWindow && st.demandBytes >= uint64(st.windowTarget) {
		st.windowTarget = min(SmallStreamWindow, 2*st.windowTarget)
	}
	// A fresh stream cannot inherit a bulk window while idle. After consuming
	// a complete bootstrap it has demonstrated demand and may reuse a recent
	// measured seed. A single keepalive byte or OPEN alone can never do so.
	if !st.warmSeedUsed && st.demandBytes >= StreamWindow {
		st.warmSeedUsed = true
		if s.warmSeedUncontendedLocked(st, now) && now.Sub(s.windowSeedAt) < creditIdle {
			st.windowTarget = max(st.windowTarget, min(MaxStreamWindow, s.windowSeed))
		}
	}
	// Batched credit is still sent well before the available grant runs out.
	threshold := uint64(min(128<<10, max(MaxPayload, st.windowTarget/4)))
	if st.rxRead-st.windowSent >= threshold || st.rxLimit-st.rxRead <= uint64(st.windowTarget/2) {
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
	if now.Sub(st.lastRead) > creditIdle {
		st.windowTarget = StreamWindow
	}
	if st.rxRead <= ^uint64(0)-uint64(st.windowTarget) {
		desired := st.rxRead + uint64(st.windowTarget)
		st.grantCreditLocked(desired)
	}
	st.s.controlLocked(nil, frame{kind: kindWindow, stream: st.id, offset: st.rxRead, id: st.rxLimit})
	st.windowAt = now
	st.windowSent = st.rxRead
}

func (st *Stream) receiveCreditLocked(f frame) error {
	if f.id < f.offset || f.id-f.offset > MaxStreamWindow || f.offset > st.txNext {
		return ErrProtocol
	}
	if err := st.releaseSendCreditLocked(f.offset); err != nil {
		return err
	}
	if f.id > st.peerLimit {
		st.peerLimit = f.id
	}
	return nil
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
	// MPX/3 DATA receipts contain the receiver's monotonic arrival clock.
	// A busy reverse-direction carrier can compress/delay ACKs; measuring
	// their local arrival spacing would misclassify equal forward links.
	// Only time differences on that same remote clock are used (no sync).
	if receiverStamp == 0 {
		return
	}
	if receiverStamp <= c.remoteSampleAt {
		return
	}
	if c.remoteSampleAt == 0 || receiverStamp-c.remoteSampleAt > uint64(10*time.Second) {
		c.remoteSampleAt = receiverStamp
		c.ackBytes = 0
		return
	}
	c.ackBytes += uint64(len(p.f.data))
	elapsed := time.Duration(receiverStamp - c.remoteSampleAt)
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
		s.ready = make(map[uint64]*list.List)
	}
	q := s.ready[p.f.stream]
	if q == nil {
		q = list.New()
		s.ready[p.f.stream] = q
	}
	if front {
		p.ready = q.PushFront(p)
	} else {
		p.ready = q.PushBack(p)
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
	q := s.ready[p.f.stream]
	q.Remove(p.ready)
	p.ready = nil
	if q.Len() == 0 {
		delete(s.ready, p.f.stream)
	}
}

// Fair dispatch without repeatedly scanning/sorting all in-flight payloads.
// Each live queue element is owned by a pending ledger entry, never a second
// payload copy, and ACK/reset/close remove it immediately.
func (s *Session) dispatchLocked(now time.Time) {
	ids := make([]uint64, 0, len(s.ready))
	for id := range s.ready {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) == 0 {
		return
	}

	dispatchOne := func(id uint64, advanceFair bool) bool {
		q := s.ready[id]
		if q == nil || q.Len() == 0 {
			return true
		}
		c := s.pathLocked(now)
		if c == nil {
			return false
		}
		p := q.Front().Value.(*outbound)
		s.unreadyLocked(p)
		p.path = c
		p.generation++
		p.sentAt = time.Time{}
		// An ACK-clocked flight can momentarily empty without the application being
		// idle. Reset samples only on a genuine idle gap, not every drained flight.
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
				s.dispatchCursor = id
				s.dispatchSequence++
			} else {
				s.bulkDispatchCursor = id
			}
			return true
		default:
			c.outstanding -= p.cost
			c.queued -= p.cost
			p.path = nil
			s.readyLocked(p, true)
			return false
		}
	}

	bulkCandidate := func() uint64 {
		if len(ids) == 0 {
			return 0
		}
		start := sort.Search(len(ids), func(i int) bool { return ids[i] > s.bulkDispatchCursor })
		if start == len(ids) {
			start = 0
		}
		bestLen := 3 // at least four queued DATA frames proves sustained backlog
		var best uint64
		for step := 0; step < len(ids); step++ {
			id := ids[(start+step)%len(ids)]
			if q := s.ready[id]; q != nil && q.Len() > bestLen {
				best, bestLen = id, q.Len()
			}
		}
		return best
	}

	start := sort.Search(len(ids), func(i int) bool { return ids[i] > s.dispatchCursor })
	if start == len(ids) {
		start = 0
	}
	for count, empty := 0, 0; count < dataDispatchBatch && empty < len(ids); count++ {
		id := ids[(start+count)%len(ids)]
		q := s.ready[id]
		if q == nil || q.Len() == 0 {
			empty++
			continue
		}
		empty = 0
		if !dispatchOne(id, true) {
			return
		}
		// Normal per-stream round-robin remains intact. Every eight successful
		// fair DATA frames, offer one additional turn to the largest sustained
		// backlog. This caps the bonus near 1/9 of DATA dispatch opportunities
		// while preventing a bulk stream from collapsing to one frame per full
		// 2048-stream rotation.
		if s.dispatchSequence%8 == 0 {
			if bonus := bulkCandidate(); bonus != 0 && !dispatchOne(bonus, false) {
				return
			}
		}
	}
}
