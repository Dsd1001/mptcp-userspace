package multipath

import (
	"errors"
	"fmt"
	"time"
)

// SchedulerMode is immutable for a session. It is authenticated on every
// carrier hello, including reconnects. Auto chooses a policy, not a third
// DATA algorithm. Each sender evaluates its own direction independently.
type SchedulerMode string

const (
	SchedulerAuto      SchedulerMode = "auto"
	SchedulerAggregate SchedulerMode = "aggregate"
	SchedulerProtect   SchedulerMode = "protect"
	SchedulerWeighted  SchedulerMode = "weighted"
)

func ParseSchedulerMode(value string) (SchedulerMode, error) {
	if value == "" {
		return SchedulerAuto, nil
	}
	mode := SchedulerMode(value)
	if mode != SchedulerAuto && mode != SchedulerAggregate && mode != SchedulerProtect && mode != SchedulerWeighted {
		return "", errors.New("scheduler_mode must be auto, aggregate, protect or weighted")
	}
	return mode, nil
}

func schedulerWire(mode SchedulerMode) byte {
	switch mode {
	case SchedulerAuto:
		return 0x41
	case SchedulerAggregate:
		return 0x42
	case SchedulerProtect:
		return 0x43
	case SchedulerWeighted:
		return 0x44
	default:
		return 0
	}
}

func schedulerFromWire(value byte) (SchedulerMode, error) {
	switch value {
	case 0x41:
		return SchedulerAuto, nil
	case 0x42:
		return SchedulerAggregate, nil
	case 0x43:
		return SchedulerProtect, nil
	case 0x44:
		return SchedulerWeighted, nil
	default:
		return "", fmt.Errorf("%w: authenticated scheduler capability revision 5 required", ErrProtocol)
	}
}

var ErrSchedulerMismatch = errors.New("MPX/3 scheduler mode conflicts with the existing session")

type PathRole string

const (
	RoleLearning PathRole = "learning"
	RoleActive   PathRole = "active"
	RoleProbe    PathRole = "probe"
	RoleBackup   PathRole = "backup"

	schedulerLearningDebt = 4 * (MaxPayload + 64)
	schedulerProbePeriod  = time.Second
	schedulerBackupPeriod = 5 * time.Second
	schedulerHealthyTime  = 2 * time.Second
)

type SchedulerStats struct {
	ConfiguredSchedulerMode SchedulerMode `json:"configured_scheduler_mode,omitempty"`
	EffectiveSchedulerMode  SchedulerMode `json:"effective_scheduler_mode,omitempty"`
	ModeSwitches            uint64        `json:"mode_switches"`
	LastModeReason          string        `json:"last_mode_reason,omitempty"`
}

type schedulerState struct {
	configured   SchedulerMode
	effective    SchedulerMode
	reason       string
	switches     uint64
	restricted   bool
	healthySince time.Time
	lastIssue    time.Time
	recovery     int
	recoveryAt   [9]int
}

type schedulerPathState struct {
	role            PathRole
	epoch           int
	badEpochs       int
	goodProbes      int
	healthySince    time.Time
	lastProbe       time.Time
	probeID         uint64
	probeCount      uint64
	probeBytes      uint64
	probeDebtPeak   int
	dataACKs        int
	lastProgressAt  time.Time
	flightStartedAt time.Time
	lastRoleReason  string
}

func (s *Session) initScheduler(mode SchedulerMode) {
	s.scheduler = schedulerState{configured: mode, effective: SchedulerAggregate, reason: "auto_initial_aggregate"}
	if mode == SchedulerAggregate {
		s.scheduler.reason = "forced_aggregate"
	} else if mode == SchedulerWeighted {
		s.scheduler.effective = SchedulerWeighted
		s.scheduler.reason = "forced_weighted"
	} else if mode == SchedulerProtect {
		s.scheduler.effective = SchedulerProtect
		s.scheduler.reason = "forced_protect"
		s.scheduler.restricted = true
	}
}

func (s *Session) schedulerSnapshotLocked() SchedulerStats {
	return SchedulerStats{s.scheduler.configured, s.scheduler.effective, s.scheduler.switches, s.scheduler.reason}
}

func schedulerBaseRTT(c *carrier) time.Duration {
	if c.minRTT > 0 {
		return c.minRTT
	}
	return max(time.Millisecond, c.rtt)
}

// Read only slots already replaced by measured epochs. The original carrier
// goodput retains its startup prior for Aggregate selection; classification
// must not mistake the remaining synthetic slots for observed capacity.
func schedulerMeasuredRate(c *carrier) (float64, int) {
	rate := 0.0
	samples := 0
	for i := 0; i < min(c.deliverySamples, len(c.capacitySamples)); i++ {
		if c.capacitySamples[i] > 0 {
			rate = max(rate, c.capacitySamples[i])
			samples++
		}
	}
	return rate, samples
}

// Compare only paths with at least three valid receiver-clock delivery epochs.
// Sparse control traffic, PINGs, and repeated reads of one epoch never count.
func (s *Session) schedulerTargetLocked(c *carrier, now time.Time) PathRole {
	bestRate := 0.0
	bestRTT := time.Duration(1<<63 - 1)
	for _, other := range s.paths {
		otherRate, otherSamples := schedulerMeasuredRate(other)
		if !other.active || now.Before(other.penaltyUntil) || otherSamples < 3 {
			continue
		}
		bestRate = max(bestRate, otherRate)
		bestRTT = min(bestRTT, schedulerBaseRTT(other))
	}
	rate, samples := schedulerMeasuredRate(c)
	if samples < 3 || bestRate == 0 {
		return RoleLearning
	}
	rtt := schedulerBaseRTT(c)
	// Latency is independent safety evidence; insufficient bandwidth supply
	// must never disable the original RTT outlier thresholds.
	if rtt > 3*bestRTT {
		return RoleBackup
	}
	// Auto starts in Aggregate and equal high-fanout paths can receive uneven
	// offered work during their first few epochs. A healthy path may become
	// ACTIVE after three real samples, but a *rate-only* initial downgrade
	// needs five samples so startup utilization is not mistaken for capacity.
	// Forced Protect keeps the original three-sample qualification, and RTT
	// outliers (>2x) are still eligible for immediate conservative demotion.
	if s.scheduler.configured == SchedulerAuto && c.scheduler.role == RoleLearning && samples < 5 && rtt <= 2*bestRTT && rate < .50*bestRate {
		return RoleLearning
	}
	if rate < .50*bestRate && rtt <= 2*bestRTT && c.scheduler.role == RoleActive {
		// Do not downgrade an established path for a rate that our OWN flight
		// cap cannot exceed. A 50%-rate capacity test needs that much offered
		// DATA over the actual ACK-feedback interval. This is a conservative
		// evidence guard, not a larger sending budget or an inferred rate.
		// Initial qualification and PROBE/BACKUP promotion remain unchanged.
		feedback := max(rtt, c.rtt)
		required := .50 * bestRate * feedback.Seconds()
		if float64(c.flightBudget()+MaxPayload+64) < required {
			return RoleActive
		}
	}
	if rate < .15*bestRate {
		return RoleBackup
	}
	if rate < .50*bestRate || rtt > 2*bestRTT {
		return RoleProbe
	}
	return RoleActive
}

// RTT safety evidence is independent of the candidate path's own throughput
// epochs, but it still needs a stable reference. Under a high-concurrency
// startup, early ACK queueing can temporarily make equal paths differ by >3x.
// Never enter Protect from RTT-only evidence until at least one healthy path
// has three real receiver-clock delivery epochs.
func (s *Session) schedulerRTTOutlierLocked(c *carrier, now time.Time) PathRole {
	if c.scheduler.dataACKs < 3 || c.minRTT <= 0 {
		return RoleLearning
	}
	bestRTT := time.Duration(1<<63 - 1)
	for _, other := range s.paths {
		_, samples := schedulerMeasuredRate(other)
		if !other.active || now.Before(other.penaltyUntil) || samples < 3 || other.minRTT <= 0 {
			continue
		}
		bestRTT = min(bestRTT, schedulerBaseRTT(other))
	}
	if bestRTT == time.Duration(1<<63-1) {
		return RoleLearning
	}
	rtt := schedulerBaseRTT(c)
	if rtt > 3*bestRTT {
		return RoleBackup
	}
	if rtt > 2*bestRTT {
		return RoleProbe
	}
	return RoleLearning
}

func (s *Session) setSchedulerRoleLocked(c *carrier, role PathRole, reason string, now time.Time) {
	if c.scheduler.role == role {
		return
	}
	c.scheduler.role = role
	c.scheduler.lastRoleReason = reason
	c.scheduler.badEpochs = 0
	c.scheduler.goodProbes = 0
	c.scheduler.healthySince = time.Time{}
	c.scheduler.probeID = 0
	// A newly demoted BACKUP does not immediately put an old stream offset on
	// the slow path. Qualification is delayed by its full fixed period.
	c.scheduler.lastProbe = now
	s.eventLocked("scheduler_path_role", string(role)+":"+reason, c.id, 0)
}

func (s *Session) switchSchedulerLocked(mode SchedulerMode, reason string) {
	if s.scheduler.effective == mode {
		return
	}
	s.scheduler.effective = mode
	s.scheduler.reason = reason
	s.scheduler.switches++
	s.eventLocked("scheduler_mode_changed", string(mode)+":"+reason, 0, 0)
}

// Update the policy cache only on delivery epochs, role changes or the existing
// 50ms sweep. Normal dispatch does not scan/sort metrics or allocate metadata.
func (s *Session) refreshSchedulerLocked(now time.Time, epochAdvanced bool) {
	state := &s.scheduler
	if state.configured == SchedulerAggregate || state.configured == SchedulerWeighted || state.configured == "" {
		state.restricted = false
		return
	}
	learning, degraded, allActive, any := false, false, true, false
	for _, c := range s.paths {
		if !c.active {
			continue
		}
		any = true
		learning = learning || c.scheduler.role == RoleLearning
		degraded = degraded || c.scheduler.role == RoleProbe || c.scheduler.role == RoleBackup
		_, validSamples := schedulerMeasuredRate(c)
		allActive = allActive && c.scheduler.role == RoleActive && validSamples >= 3 && !now.Before(c.penaltyUntil)
	}
	allActive = allActive && any
	if state.configured == SchedulerAuto {
		// Auto starts as Aggregate. Do not switch the whole session while any
		// connected path is still in its initial learning phase: under large
		// fan-out, startup queueing can let one path finish three epochs before
		// its peers and create a self-fulfilling Protect classification.
		if degraded && !learning {
			s.switchSchedulerLocked(SchedulerProtect, "stable_outlier_or_delivery_failure")
		}
		if state.effective == SchedulerProtect {
			if !allActive {
				state.healthySince = time.Time{}
				state.recovery = 0
			} else if state.healthySince.IsZero() {
				state.healthySince = now
				state.recovery = 0
				for _, c := range s.paths {
					if c.active && c.id <= 8 {
						state.recoveryAt[c.id] = c.deliverySamples
					}
				}
			} else if epochAdvanced {
				advanced := true
				for _, c := range s.paths {
					if c.active && c.id <= 8 && c.deliverySamples <= state.recoveryAt[c.id] {
						advanced = false
					}
				}
				if advanced {
					state.recovery++
					for _, c := range s.paths {
						if c.active && c.id <= 8 {
							state.recoveryAt[c.id] = c.deliverySamples
						}
					}
				}
				if state.recovery >= 5 && now.Sub(state.healthySince) >= schedulerHealthyTime && (state.lastIssue.IsZero() || now.Sub(state.lastIssue) >= schedulerHealthyTime) {
					s.switchSchedulerLocked(SchedulerAggregate, "all_paths_active_5_epochs_2s")
				}
			}
		}
	}
	// Auto is only a policy selector: while its effective mode is Aggregate,
	// unknown LEARNING paths must see the same original Aggregate scheduler as
	// every other healthy path. Applying the Protect learning-debt cap here
	// underfeeds a homogeneous high-BDP path, then mistakes that self-imposed
	// utilization limit for low capacity. Once Auto actually enters Protect,
	// LEARNING/PROBE/BACKUP bounds apply normally.
	_ = learning
	state.restricted = state.effective == SchedulerProtect
}

func (s *Session) schedulerReceiptLocked(c *carrier, p *outbound, now time.Time) {
	state := &c.scheduler
	roleBefore := state.role
	if p != nil && p.path == c && p.attempts == 1 {
		state.dataACKs++
	}
	probeMatched := p != nil && state.probeID == p.f.id && p.path == c && p.attempts == 1
	advanced := c.deliverySamples > state.epoch
	if advanced {
		state.epoch = c.deliverySamples
		if s.scheduler.configured == SchedulerAggregate || s.scheduler.configured == SchedulerWeighted {
			if c.deliverySamples >= 3 {
				reason := "forced_aggregate_unfiltered"
				if s.scheduler.configured == SchedulerWeighted {
					reason = "forced_weighted_configured_capacity"
				}
				s.setSchedulerRoleLocked(c, RoleActive, reason, now)
			}
		} else {
			target := s.schedulerTargetLocked(c, now)
			// LEARNING already means we are collecting the first evidence set.
			// Once three valid delivery samples exist, classify that initial set
			// immediately. Requiring three *additional* bad epochs here keeps a
			// known slow path in LEARNING until its fifth sample and lets it keep
			// placing old offsets on the ordered stream. The three-new-epoch
			// hysteresis below is for an already ACTIVE path becoming worse.
			if state.role == RoleLearning && target != RoleLearning {
				if target == RoleActive {
					s.setSchedulerRoleLocked(c, RoleActive, "3_valid_delivery_samples", now)
				} else {
					s.setSchedulerRoleLocked(c, target, "initial_3_delivery_samples", now)
				}
			} else if target == RoleActive {
				state.badEpochs = 0
			} else if target != RoleLearning {
				state.badEpochs++
				state.goodProbes = 0
				state.healthySince = time.Time{}
				if state.badEpochs >= 3 && (state.role == RoleActive || target == RoleBackup) {
					s.setSchedulerRoleLocked(c, target, "3_outlier_delivery_epochs", now)
				}
			}
		}
	}
	// If Aggregate naturally gives a high-delay LEARNING path too little load
	// to form three capacity epochs, do not keep feeding it forever merely to
	// prove it is slow. Three real DATA ACK RTT samples may conservatively move
	// it to PROBE/BACKUP; bandwidth-based classification still requires the
	// original three receiver-clock delivery epochs above.
	if state.role == RoleLearning && s.scheduler.configured != SchedulerAggregate && s.scheduler.configured != SchedulerWeighted {
		if rttRole := s.schedulerRTTOutlierLocked(c, now); rttRole == RoleProbe || rttRole == RoleBackup {
			s.setSchedulerRoleLocked(c, rttRole, "initial_3_data_ack_rtt", now)
		}
	}
	if probeMatched {
		state.probeID = 0
		if s.schedulerTargetLocked(c, now) == RoleActive && !now.Before(c.penaltyUntil) {
			if state.role == RoleBackup {
				s.setSchedulerRoleLocked(c, RoleProbe, "backup_qualification_ack", now)
			}
			if state.role == RoleProbe {
				if state.healthySince.IsZero() {
					state.healthySince = now
				}
				state.goodProbes++
				if state.goodProbes >= 3 && now.Sub(state.healthySince) >= schedulerHealthyTime {
					s.setSchedulerRoleLocked(c, RoleActive, "3_qualified_probes_2s", now)
				}
			}
		} else {
			state.goodProbes = 0
			state.healthySince = time.Time{}
		}
	}
	if advanced || probeMatched || state.role != roleBefore {
		s.refreshSchedulerLocked(now, advanced)
	}
}

func (s *Session) schedulerFailureLocked(c *carrier, reason string, now time.Time) {
	if s.scheduler.configured == SchedulerAggregate || s.scheduler.configured == SchedulerWeighted || s.scheduler.configured == "" {
		return
	}
	if s.scheduler.configured == SchedulerAuto && s.scheduler.effective == SchedulerAggregate {
		qualifiedReference := false
		for _, other := range s.paths {
			_, samples := schedulerMeasuredRate(other)
			if other.active && !now.Before(other.penaltyUntil) && samples >= 3 {
				qualifiedReference = true
				break
			}
		}
		// Before any path has a real capacity baseline, a 500 ms startup delay
		// is not enough evidence to classify an equal path as BACKUP. Normal
		// DATA timeout/reinjection still protects delivery; the next sweep may
		// demote once a measured reference exists.
		if !qualifiedReference {
			return
		}
	}
	s.scheduler.lastIssue = now
	s.setSchedulerRoleLocked(c, RoleBackup, reason, now)
	s.refreshSchedulerLocked(now, false)
}

func (s *Session) sweepSchedulerLocked(now time.Time) {
	if s.scheduler.configured == SchedulerAggregate || s.scheduler.configured == SchedulerWeighted || s.scheduler.configured == "" {
		return
	}
	for _, c := range s.paths {
		progressAt := c.scheduler.lastProgressAt
		if progressAt.IsZero() {
			progressAt = c.lastACK
		}
		// Time with no sent DATA outstanding is idle, not delivery failure.
		// The first DATA of a new flight starts a fresh no-progress interval;
		// later writes in that flight do not extend it. Only ACKs can do that.
		if c.scheduler.flightStartedAt.After(progressAt) {
			progressAt = c.scheduler.flightStartedAt
		}
		if c.active && c.outstanding > c.queued && !progressAt.IsZero() && now.Sub(progressAt) > max(500*time.Millisecond, 4*schedulerBaseRTT(c)) {
			s.schedulerFailureLocked(c, "stale_data_delivery", now)
		}
	}
}

// ACTIVE Protect paths use the original Aggregate selector unchanged except
// this membership filter. Auto's stable homogeneous hot path bypasses it.
func (s *Session) schedulerPathEligibleLocked(c *carrier) bool {
	if c.scheduler.role == RoleLearning {
		if c.outstanding+MaxPayload+64 > schedulerLearningDebt {
			c.sampleBudgetLimited = true
			return false
		}
		return true
	}
	return c.scheduler.role == RoleActive
}

func (s *Session) pathLocked(now time.Time) *carrier {
	if !s.scheduler.restricted {
		return s.aggregatePathLocked(now, false)
	}
	if s.scheduler.effective == SchedulerProtect {
		var probe *carrier
		for _, c := range s.paths {
			role := c.scheduler.role
			if !c.active || now.Before(c.penaltyUntil) || (role != RoleProbe && role != RoleBackup) || c.outstanding != 0 || len(c.queue) >= carrierQueue {
				continue
			}
			period := schedulerProbePeriod
			if role == RoleBackup {
				period = schedulerBackupPeriod
			}
			if now.Sub(c.scheduler.lastProbe) < period {
				continue
			}
			if probe == nil || c.scheduler.lastProbe.Before(probe.scheduler.lastProbe) || c.scheduler.lastProbe.Equal(probe.scheduler.lastProbe) && c.id < probe.id {
				probe = c
			}
		}
		if probe != nil {
			return probe
		}
	}
	return s.aggregatePathLocked(now, true)
}

func (s *Session) schedulerAllocatedLocked(c *carrier, p *outbound, now time.Time) {
	if s.scheduler.effective != SchedulerProtect || (c.scheduler.role != RoleProbe && c.scheduler.role != RoleBackup) {
		return
	}
	c.scheduler.lastProbe = now
	c.scheduler.probeID = p.f.id
	c.scheduler.probeCount++
	c.scheduler.probeBytes += uint64(len(p.f.data))
	c.scheduler.probeDebtPeak = max(c.scheduler.probeDebtPeak, c.outstanding)
}
