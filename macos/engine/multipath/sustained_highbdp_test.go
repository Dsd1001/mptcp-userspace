package multipath

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Separate sustained single-stream measurement, not multiple business streams.
// The original 64 MiB matrix remains unchanged and must independently pass.
func TestSustainedHighBDPSingleStream(t *testing.T) {
	if os.Getenv("MPX_SUSTAINED_BDP") == "" {
		t.Skip("opt-in 256 MiB single-stream capacity test")
	}
	var results []highBDPResult
	for _, item := range []struct {
		name                 string
		capacity, paths, rtt int
	}{{"sustained-300Mbps-6paths-30ms", 300, 6, 30}, {"sustained-500Mbps-3paths-50ms", 500, 3, 50}} {
		t.Run(item.name, func(t *testing.T) {
			rates := make([]int64, item.paths)
			rtts := make([]time.Duration, item.paths)
			for i := range rates {
				rates[i] = int64(item.capacity) * 1000000 / 8 / int64(item.paths)
				rtts[i] = time.Duration(item.rtt) * time.Millisecond
			}
			r := highBDPCase(t, item.name, rates, rtts, 256<<20)
			results = append(results, r)
			if item.capacity == 500 && r.Mbps < 300 {
				t.Errorf("sustained single stream below 300 Mbps: %.3f", r.Mbps)
			}
			if item.capacity == 300 && r.Mbps < 240 {
				t.Errorf("sustained below 80 percent of shaped rate: %.3f", r.Mbps)
			}
		})
	}
	if path := os.Getenv("MPX_SUSTAINED_REPORT"); path != "" {
		b, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(b, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
