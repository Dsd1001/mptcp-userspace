package main

import (
	"encoding/json"
	"mptcp-desktop/engine/multipath"
	"testing"
)

func TestRoutedLandingConfiguration(t *testing.T) {
	key, _ := multipath.NewKey()
	c := Config{SchemaVersion: 1, ListenTCP: "127.0.0.1:24099", TransportKey: key, MaxSessions: 4, TCPRoutes: []multipath.TCPRoute{{ID: 1, Backend: "landing.example.test:8388"}, {ID: 2, Backend: "127.0.0.1:8389"}}}
	data, _ := json.Marshal(c)
	if _, err := decodeConfig(data); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.BackendTCP = "127.0.0.1:8388" },
		func(c *Config) { c.UDPEnabled = true },
		func(c *Config) { c.UOTEnabled = true },
		func(c *Config) { c.ListenUDP = "127.0.0.1:24099" },
		func(c *Config) { c.TCPRoutes[0].Backend = c.ListenTCP },
		func(c *Config) { c.TCPRoutes[0].ID = 0 },
		func(c *Config) { c.MaxSessions = 17 },
	} {
		var test Config
		json.Unmarshal(data, &test)
		mutate(&test)
		if test.validate() == nil {
			t.Fatal("ambiguous or invalid routed egress configuration accepted")
		}
	}
	if c.redacted().TransportKey == key {
		t.Fatal("redacted config exposes transport key")
	}
}
