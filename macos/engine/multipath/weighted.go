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

func weightedBaseFlightBudget(c *carrier, rate float64) int {
	if rate <= 0 {
		return c.flightBudget()
	}
	rtt := c.minRTT
	if rtt <= 0 {
		rtt = c.rtt
	}
	rtt = min(time.Second, max(time.Millisecond, rtt))
	// Configured capacity and propagation RTT define the safe initial flight.
	// Queue-inflated load RTT must not directly enlarge permission to send.
	desired := int(rate*(1.25*rtt.Seconds()+.015)) + 2*MaxPayload
	return min(maxPathBudget, max(initialPathBudget, desired))
}

func weightedFlightBudget(c *carrier, rate float64) int {
	if rate <= 0 {
		return c.flightBudget()
	}
	base := weightedBaseFlightBudget(c, rate)
	return min(maxPathBudget, max(base, c.weightedBudget))
}

func (c *carrier) resetWeightedFlight(rate float64) {
	c.weightedBudget = weightedBaseFlightBudget(c, rate)
	c.weightedGrowthACK = 0
	c.weightedBudgetLimited = false
	c.weightedGrowthEpoch = c.recentDeliveryEpoch
}

func (c *carrier) backoffWeightedFlight(rate float64) {
	if rate <= 0 {
		return
	}
	base := weightedBaseFlightBudget(c, rate)
	current := weightedFlightBudget(c, rate)
	c.weightedBudget = max(base, current/2)
	c.weightedGrowthACK = 0
	c.weightedBudgetLimited = false
	c.weightedGrowthEpoch = c.recentDeliveryEpoch
}

// observeWeightedFlightLocked expands a configured Weighted path only when the
// application has real queued DATA, the current flight cap was reached, ACKs
// are progressing, and the latest receiver-clock delivery epoch is still below
// the configured capacity. Load RTT is deliberately not an input to growth.
//
// This turns the configured base BDP into a startup floor rather than a hard
// ceiling: delayed receipt feedback can be covered without reintroducing the
// old RTT -> budget -> queue -> RTT positive-feedback loop.
func (s *Session) observeWeightedFlightLocked(c *carrier, acked int) {
	if s.scheduler.configured != SchedulerWeighted || c == nil || c.configuredRateBPS <= 0 || acked <= 0 {
		return
	}
	base := weightedBaseFlightBudget(c, c.configuredRateBPS)
	if c.weightedBudget < base {
		c.weightedBudget = base
	}
	if !c.weightedBudgetLimited || len(s.ready) == 0 || c.recentDeliveryBPS <= 0 || c.recentDeliveryEpoch == c.weightedGrowthEpoch || c.recentDeliveryBPS >= .90*c.configuredRateBPS {
		if c.recentDeliveryBPS >= .90*c.configuredRateBPS || len(s.ready) == 0 {
			c.weightedGrowthACK = 0
			c.weightedBudgetLimited = false
		}
		return
	}
	current := weightedFlightBudget(c, c.configuredRateBPS)
	c.weightedGrowthACK += acked
	// Require substantial real progress at the current cap before probing a
	// larger flight. Half a flight reacts quickly to delayed ACK feedback while
	// still preventing one or two receipts from opening a large queue.
	if c.weightedGrowthACK < max(4*MaxPayload, current/2) {
		return
	}
	ratio := c.configuredRateBPS / max(c.recentDeliveryBPS, 1)
	ratio = min(2.0, max(1.25, ratio))
	next := int(float64(current) * ratio)
	next = ((next + MaxPayload - 1) / MaxPayload) * MaxPayload
	c.weightedBudget = min(maxPathBudget, max(current+MaxPayload, next))
	c.weightedGrowthACK = 0
	c.weightedBudgetLimited = false
	c.weightedGrowthEpoch = c.recentDeliveryEpoch
	c.weightedBudgetGrowths++
}
