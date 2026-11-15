//go:build !windows

package multipath

import (
	"runtime"
	"syscall"
)

type testProcessUsage struct {
	Available    bool
	CPUSeconds   float64
	RSSPeakBytes int64
}

func readTestProcessUsage() testProcessUsage {
	var r syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		return testProcessUsage{}
	}
	cpu := float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	rss := r.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	return testProcessUsage{Available: true, CPUSeconds: cpu, RSSPeakBytes: rss}
}
