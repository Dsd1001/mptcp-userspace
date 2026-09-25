package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"mptcp-desktop/engine/multipath"
)

func weightedTestMbps(v float64) *float64 { return &v }

func weightedConfig(t *testing.T) Config {
	t.Helper()
	key, err := multipath.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		SchemaVersion: 3,
		Mode:          "userspace_multipath",
		ListenPort:    1081,
		SchedulerMode: func() *string { v := "weighted"; return &v }(),
		TransportKey:  key,
		Relays: []Relay{
			{Host: "192.0.2.10", Port: 24001, DownloadMbps: weightedTestMbps(50), UploadMbps: weightedTestMbps(20)},
			{Host: "198.51.100.20", Port: 24001, DownloadMbps: weightedTestMbps(80)},
		},
	}
}

func TestWeightedConfigRequiresDownloadAndAllowsUploadBlank(t *testing.T) {
	c := weightedConfig(t)
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	caps, err := c.weightedCapacities()
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 2 || caps[0].DownloadMbps != 50 || caps[0].UploadMbps != 20 || caps[1].DownloadMbps != 80 || caps[1].UploadMbps != 0 {
		t.Fatalf("directional capacities decoded incorrectly: %+v", caps)
	}
	c.Relays[1].DownloadMbps = nil
	if err := c.validate(); err == nil {
		t.Fatal("weighted relay without required download Mbps accepted")
	}
}

func TestWeightedConfigRejectsInvalidCapacityButLegacyModesIgnoreAbsentCapacity(t *testing.T) {
	c := weightedConfig(t)
	c.Relays[0].DownloadMbps = weightedTestMbps(50.01)
	if err := c.validate(); err == nil {
		t.Fatal("excess capacity precision accepted")
	}
	c = weightedConfig(t)
	c.Relays[0].UploadMbps = weightedTestMbps(0)
	if err := c.validate(); err == nil {
		t.Fatal("explicit zero upload accepted instead of blank/omitted")
	}
	c = weightedConfig(t)
	mode := "aggregate"
	c.SchedulerMode = &mode
	for i := range c.Relays {
		c.Relays[i].DownloadMbps = nil
		c.Relays[i].UploadMbps = nil
	}
	if err := c.validate(); err != nil {
		t.Fatalf("legacy scheduler unexpectedly requires weighted capacity: %v", err)
	}
}

func TestWeightedConfigJSONRoundTripPreservesOptionalUpload(t *testing.T) {
	c := weightedConfig(t)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := readConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Relays[0].DownloadMbps == nil || decoded.Relays[0].UploadMbps == nil || decoded.Relays[1].DownloadMbps == nil || decoded.Relays[1].UploadMbps != nil {
		t.Fatalf("weighted optional fields changed after JSON round trip: %+v", decoded.Relays)
	}
}
