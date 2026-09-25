package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestExplicitRestartClearsOnlyOwnedStartLimiter(t *testing.T) {
	var calls []string
	m := &manager{out: io.Discard, command: func(name string, args ...string) ([]byte, error) {
		if name != "/usr/bin/systemctl" {
			t.Fatalf("unexpected command %s", name)
		}
		calls = append(calls, strings.Join(args, " "))
		return nil, nil
	}}
	if err := m.restartIf(false); err != nil || len(calls) != 0 {
		t.Fatal("inactive transaction restarted a service")
	}
	if err := m.restartIf(true); err != nil {
		t.Fatal(err)
	}
	want := "reset-failed " + serviceName + ";restart " + serviceName + ";is-active --quiet " + serviceName
	if strings.Join(calls, ";") != want {
		t.Fatalf("wrong explicit restart sequence: %v", calls)
	}
	if strings.Contains(unitText, "StartLimitIntervalSec=0") {
		t.Fatal("automatic crash-loop throttle disabled")
	}
}

func TestExplicitRestartDoesNotHideResetFailure(t *testing.T) {
	calls := 0
	m := &manager{out: io.Discard, command: func(_ string, _ ...string) ([]byte, error) {
		calls++
		return nil, errors.New("test systemctl failure")
	}}
	if err := m.restartIf(true); err == nil || calls != 1 {
		t.Fatalf("failure hidden or restart attempted: calls=%d err=%v", calls, err)
	}
}
