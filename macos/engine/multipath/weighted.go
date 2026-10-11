package multipath

import (
	"errors"
	"math"
	"time"
)

const (
	weightedCapacityUnitsPerMbps = 10.0 // authenticated hello uses 0.1 Mbps units
	MaxConfiguredMbps            = 6553.5
)

type PathCapacity struct {
	DownloadMbps float64
	UploadMbps   float64 // zero means automatic estimation for the client upload direction
}

func capacityUnits(mbps float64, required bool) (uint16, error) {
	if mbps == 0 && !required {
		return 0, nil
	}
	if !math.IsNaN(mbps) && !math.IsInf(mbps, 0) && mbps >= 0.1 && mbps <= MaxConfiguredMbps {
		units := math.Round(mbps * weightedCapacityUnitsPerMbps)
		if math.Abs(units-mbps*weightedCapacityUnitsPerMbps) <= 1e-6 && units >= 1 && units <= math.MaxUint16 {
			return uint16(units), nil
		}
	}
	return 0, errors.New("weighted capacity must be 0.1-6553.5 Mbps with 0.1 Mbps precision")
}

func ValidatePathCapacity(p PathCapacity) error { return p.validateWeighted() }

func (p PathCapacity) validateWeighted() error {
	if _, err := capacityUnits(p.DownloadMbps, true); err != nil {
		return err
	}
	if _, err := capacityUnits(p.UploadMbps, false); err != nil {
		return err
	}
	return nil
}

func capacityFromUnits(units uint16) float64 {
	return float64(units) / weightedCapacityUnitsPerMbps
}

func configuredRateBPS(mbps float64) float64 {
	if mbps <= 0 {
		return 0
	}
	return mbps * 1_000_000 / 8
}

func (p PathCapacity) txRateBPS(server bool) float64 {
	if server {
		return configuredRateBPS(p.DownloadMbps)
	}
	return configuredRateBPS(p.UploadMbps)
}

func weightedFlightBudget(c *carrier, rate float64) int {
	if rate <= 0 {
		return c.flightBudget()
	}
	rtt := c.minRTT
	if rtt <= 0 {
		rtt = c.rtt
	}
	rtt = min(time.Second, max(time.Millisecond, rtt))

	// Legacy 0.10.x Weighted behavior: let real MPX receipt feedback enlarge
	// flight beyond the propagation-RTT BDP, but cap that influence at 4x the
	// base RTT. This avoids both extremes we saw in 1.0.x: a hard minRTT ceiling
	// that starves delayed feedback, and an unbounded load-RTT feedback loop.
	feedback := min(4*rtt, max(rtt, c.rtt))
	desired := int(rate*(1.25*feedback.Seconds()+.015)) + 2*MaxPayload
	return min(maxPathBudget, max(initialPathBudget, desired))
}

// weightedAdaptiveTargetBudget uses the unloaded propagation RTT rather than
// loaded ACK RTT. Configured Mbps is an upper prior, never guaranteed capacity.
// Three actual delivered-rate epochs are required before applying a measured
// adjustment. Application-limited sparse samples cannot collapse the window.
func weightedAdaptiveTargetBudget(c *carrier, configuredRate float64, now time.Time) int {
	if configuredRate <= 0 {
		return c.flightBudget()
	}
	base := c.minRTT
	if base <= 0 {
		base = c.rtt
	}
	base = min(time.Second, max(time.Millisecond, base))
	rate := configuredRate
	if c.deliverySamples >= 3 && c.goodput >= 65536 {
		rate = min(rate, max(.70*configuredRate, c.goodput))
	}
	factor := 1.65 // startup: enough propagation flight before valid feedback
	if c.deliverySamples >= 2 {
		factor = 1.50
		if c.rtt > 2*base {
			factor = 1.25 // queuing evidence lowers, never enlarges, flight
		}
	}
	if c.timeoutStreak > 0 || now.Before(c.penaltyUntil) {
		factor = min(factor, 1.0)
	}
	if stamp := c.writeStartedNS.Load(); stamp != 0 &&
		now.Sub(time.Unix(0, stamp)) > max(100*time.Millisecond, 2*base) {
		factor = min(factor, .85)
	}
	// If ACK progress truly stops under load, back off rather than enlarge an
	// already overfull queue. Do not punish an idle (app-limited) Carrier.
	if c.outstanding > 0 && !c.lastACK.IsZero() &&
		now.Sub(c.lastACK) > max(750*time.Millisecond, 4*base) {
		factor = min(factor, 1.0)
	}
	desired := int(rate*(factor*base.Seconds()+.015)) + 2*MaxPayload
	return min(maxPathBudget, max(initialPathBudget, desired))
}

// Only the Weighted choice path calls this while holding Session.mu. The
// bounded per-update ramp and 100ms cooldown prevent ACK-driven oscillation.
// No timers/sleeps, unbounded growth, or global shared-state goroutine.
func weightedAdaptiveFlightBudget(c *carrier, rate float64, now time.Time) int {
	if rate <= 0 {
		return c.flightBudget()
	}
	target := weightedAdaptiveTargetBudget(c, rate, now)
	if c.weightedAdaptiveBudget <= 0 {
		c.weightedAdaptiveBudget = target
		c.weightedAdaptiveAt = now
		return target
	}
	if now.Sub(c.weightedAdaptiveAt) < 100*time.Millisecond {
		return c.weightedAdaptiveBudget
	}
	old := c.weightedAdaptiveBudget
	if target > old {
		c.weightedAdaptiveBudget = min(target, old+max(MaxPayload, old/4))
	} else if target < old {
		c.weightedAdaptiveBudget = max(target, old-max(MaxPayload, old/4))
	}
	c.weightedAdaptiveBudget = min(maxPathBudget, max(initialPathBudget, c.weightedAdaptiveBudget))
	c.weightedAdaptiveAt = now
	return c.weightedAdaptiveBudget
}
