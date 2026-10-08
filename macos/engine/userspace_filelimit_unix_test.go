//go:build !windows

package main

import (
	"syscall"
	"testing"

	"mptcp-desktop/engine/multipath"
)

func TestUserspaceFileLimitSupportsMaxStreams(t *testing.T) {
	if err := ensureUserspaceFileLimit(); err != nil {
		t.Fatal(err)
	}
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		t.Fatal(err)
	}
	need := uint64(multipath.MaxStreams + 512)
	if lim.Cur < need {
		t.Fatalf("RLIMIT_NOFILE=%d, want >=%d", lim.Cur, need)
	}
}
