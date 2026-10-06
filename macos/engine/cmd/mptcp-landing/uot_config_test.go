package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestUOTLandingConfigCompatibility(t *testing.T) {
	c, err := defaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.UOTEnabled {
		t.Fatal("default config unexpectedly enables UoT")
	}
	old, err := decodeConfig(configBytes(c))
	if err != nil || old.UOTEnabled {
		t.Fatalf("legacy config migration: %+v %v", old, err)
	}
	c.UOTEnabled, c.UDPEnabled, c.ListenUDP = true, false, ""
	if err := c.validate(); err != nil {
		t.Fatalf("UoT should not need public UDP listener: %v", err)
	}
	decoded, err := decodeConfig(configBytes(c))
	if err != nil || !decoded.UOTEnabled || decoded.UDPEnabled {
		t.Fatalf("UoT round trip: %+v %v", decoded, err)
	}
	c.BackendUDP = ""
	if err := c.validate(); err == nil {
		t.Fatal("UoT accepted missing UDP backend")
	}
	c.BackendUDP = "0.0.0.0:8388"
	if err := c.validate(); err == nil {
		t.Fatal("UoT accepted unspecified backend")
	}
	c.BackendUDP, c.ListenUDP, c.UDPEnabled = "127.0.0.1:8388", "0.0.0.0:24001", true
	if err := c.validate(); err != nil {
		t.Fatalf("Landing may offer both native UDP and UoT: %v", err)
	}
}

func TestConfigureUOTWithoutPublicUDP(t *testing.T) {
	c, err := defaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	// Keep TCP endpoints; turn native UDP off; allow UoT; set its UDP backend.
	input := "\n\nn\ny\n127.0.0.1:8389\n\n\n\n"
	var out bytes.Buffer
	terminal := terminal{reader: bufio.NewReader(strings.NewReader(input)), out: &out}
	got, err := terminal.configure(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.UDPEnabled || !got.UOTEnabled || got.BackendUDP != "127.0.0.1:8389" {
		t.Fatalf("UoT configuration=%+v", got.redacted())
	}
	if strings.Contains(out.String(), "UDP 聚合监听") {
		t.Fatal("UoT-only configuration prompted for public UDP listener")
	}
}
