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
	feedback := min(4*rtt, max(rtt, c.rtt))
	desired := int(rate*(1.25*feedback.Seconds()+.015)) + 2*MaxPayload
	return min(maxPathBudget, max(initialPathBudget, desired))
}
