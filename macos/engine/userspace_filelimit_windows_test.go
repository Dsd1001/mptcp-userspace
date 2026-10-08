//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestUserspaceFileLimitWindowsNoop(t *testing.T) {
	if err := ensureUserspaceFileLimit(); err != nil {
		t.Fatal(err)
	}
	message := userspaceEnvironmentMessage()
	if !strings.Contains(message, "Windows Userspace") || strings.Contains(message, "RLIMIT_NOFILE") {
		t.Fatalf("unexpected Windows userspace environment message: %q", message)
	}
}
