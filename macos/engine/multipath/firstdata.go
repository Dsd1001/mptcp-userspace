package multipath

// Weighted-only, DATA-only fast lane. It never redirects or reprioritizes
// reliable controls, ACK, OPEN, WINDOW, FIN or RESET. Session.mu protects all
// state: no timers, goroutines, configuration UI or MPX/4 wire changes.
const (
	firstDataTokenCapNormal = 256 << 10
	firstDataTokenCapBusy   = 64 << 10
	firstDataFastTurnStride = 4 // at least three fair turns for each priority turn
	firstDataTokenRefillDiv = 8 // gated on real DATA delivery confirmation
)

func (s *Session) firstDataTokenCapLocked() int {
	if len(s.streams) >= 64 || s.readyDataBytes >= 8<<20 {
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
	if s.scheduler.configured != SchedulerWeighted ||
		s.fastReady.Len() == 0 || s.dispatchSequence%firstDataFastTurnStride != 0 {
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
