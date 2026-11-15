package main

import "testing"

func TestV114NewLandingDefaultsToEightSessions(t *testing.T) {
	c, err := defaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxSessions != 8 {
		t.Fatalf("default max_sessions=%d, want 8", c.MaxSessions)
	}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	c.MaxSessions = 16
	if err := c.validate(); err != nil {
		t.Fatalf("max 16 should remain supported: %v", err)
	}
	c.MaxSessions = 17
	if err := c.validate(); err == nil {
		t.Fatal("max 17 must be rejected")
	}
}
