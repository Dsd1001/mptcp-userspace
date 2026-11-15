//go:build windows

package multipath

// Windows-native functional tests do not require Unix getrusage.
// The optional process CPU/RSS benchmark fields are unavailable here;
// the actual throughput, integrity and resource accounting checks still run.
type testProcessUsage struct {
	CPUSeconds   float64
	RSSPeakBytes int64
}

func readTestProcessUsage() testProcessUsage {
	return testProcessUsage{}
}
