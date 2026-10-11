package multipath

import "time"

// Weighted-only, DATA-only fast lane. It never redirects or reprioritizes
// reliable controls, ACK, OPEN, WINDOW, FIN or RESET. Session.mu protects all
// state: no timers, goroutines, configuration UI or MPX/4 wire changes.
const (
	firstDataTokenCapNormal = 256 << 10
	firstDataTokenCapBusy   = 128 << 10
	firstDataTokenCapHerd   = 64 << 10
	firstDataTokenRefillDiv = 8 // gated on real DATA delivery confirmation
)

func (s *Session) firstDataTokenCapLocked() int {
	if len(s.streams) >= 64 {
		return firstDataTokenCapHerd
	}
	if s.readyDataBytes >= 8<<20 {
		return firstDataTokenCapBusy
	}
	return firstDataTokenCapNormal
}

func (s *Session) initFirstDataTokensLocked() {
	if !s.firstDataTokensInitialized {
		s.firstDataTokens = firstDataTokenCapNormal
		s.firstDataTokensInitialized = true
	}
	if cap := s.firstDataTokenCapLocked(); s.firstDataTokens > cap {
		s.firstDataTokens = cap
	}
}

func (s *Session) firstDataCandidateLocked() (*dataReadyQueue, int) {
	if !s.server || s.scheduler.configured != SchedulerWeighted ||
		s.weightedHerdGuardLocked() || s.fastReady.Len() == 0 {
		return nil, 0
	}
	s.initFirstDataTokensLocked()
	rq, _ := s.fastReady.Front().Value.(*dataReadyQueue)
	if rq == nil || rq.frames.Len() == 0 {
		return nil, 0
	}
	p, _ := rq.frames.Front().Value.(*outbound)
	if p == nil || p.f.offset != 0 || p.generation != 0 {
		return nil, 0
	}
	if p.cost > s.firstDataTokens {
		s.firstDataFairFallbacks++
		return nil, 0
	}
	return rq, p.cost
}

// Replenish from reliable progress, never a periodic synchronized wakeup.
func (s *Session) firstDataOnReceiptLocked(p *outbound) {
	if s.scheduler.configured != SchedulerWeighted || p == nil || p.f.kind != kindData {
		return
	}
	s.initFirstDataTokensLocked()
	refill := max(1, p.cost/firstDataTokenRefillDiv)
	s.firstDataTokens = min(s.firstDataTokenCapLocked(), s.firstDataTokens+refill)
}

// A first response must be able to bypass already queued Bulk DATA, not
// merely move ahead in the Session's Ready ring. Choose a low-baseRTT
// Carrier with two reserved DATA slots, allowing at most 64KiB extra
// flight. Unlike legacy score selection this explicitly prefers low latency.
// Uses the existing per-Session Token and 4:1 fairness gate. Never bypasses
// control, penalized Carrier, stalled writer or 48MiB total flight ceiling.
// The fallback is ordinary fair DATA and does not spend a Token.
// The Socket writer (not the Session dispatch modulo) enforces the 4:1
// DATA-class fairness rule. A saturated normal flight must not stop an
// eligible short first response from claiming its bounded bypass slot.
func (s *Session) firstDataCarrierLocked(p *outbound, now time.Time) *carrier {
	if !s.server || s.scheduler.configured != SchedulerWeighted ||
		s.weightedHerdGuardLocked() || p == nil || p.f.kind != kindData ||
		p.f.offset != 0 || p.generation != 0 {
		return nil
	}
	flight := 0
	for _, c := range s.paths {
		if c.active {
			flight += c.outstanding
		}
	}
	if flight+p.cost >= 48<<20 {
		return nil
	}
	var best *carrier
	bestScore := 1e30
	for _, c := range s.paths {
		if !c.active || c.configuredRateBPS <= 0 || now.Before(c.penaltyUntil) ||
			s.controlWriterStalled(c, now) || c.priorityDATA == nil ||
			len(c.priorityDATA) >= cap(c.priorityDATA) {
			continue
		}
		budget := c.weightedAdaptiveBudget
		if budget <= 0 {
			budget = weightedAdaptiveTargetBudget(c, c.configuredRateBPS, now)
		}
		if c.outstanding+p.cost > min(maxPathBudget, budget+2*MaxPayload) {
			continue
		}
		rtt := c.minRTT
		if rtt <= 0 {
			rtt = c.rtt
		}
		rtt = max(time.Millisecond, rtt)
		rate := max(c.configuredRateBPS, float64(65536))
		score := rtt.Seconds() + .20*float64(c.outstanding+p.cost)/rate +
			.008*float64(len(c.priorityDATA))
		if best == nil || score < bestScore || (score == bestScore && c.id < best.id) {
			best, bestScore = c, score
		}
	}
	return best
}
