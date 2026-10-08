package main

import (
	"bytes"
	"encoding/json"
	"net"
	"testing"
)

func TestUOTConfigTransportSelection(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		tcp, udp, uot, valid bool
	}{
		{"tcp", true, false, false, true},
		{"native-udp", false, true, false, true},
		{"uot", false, false, true, true},
		{"tcp-uot", true, false, true, true},
		{"tcp-native-udp", true, true, false, true},
		{"empty", false, false, false, false},
		{"udp-uot", false, true, true, false},
		{"all", true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := bundleProfile("uot", "UoT", 1081)
			p.TCPEnabled, p.UDPEnabled, p.UOTEnabled = tc.tcp, tc.udp, tc.uot
			c := p.config()
			for _, goos := range []string{"darwin", "linux", "windows"} {
				if err := c.validateForOS(goos); (err == nil) != tc.valid {
					t.Fatalf("%s validate=%v; want valid=%t", goos, err, tc.valid)
				}
			}
			if tc.valid {
				raw, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := readConfig(bytes.NewReader(raw))
				if err != nil || decoded.UOTEnabled != tc.uot || decoded.UDPEnabled != tc.udp || decoded.tcpEnabled() != tc.tcp {
					t.Fatalf("transport selection lost in JSON round trip: %+v, %v", decoded, err)
				}
			}
		})
	}
	c := bundleProfile("native", "Native", 1081).config()
	c.Mode, c.UOTEnabled = "native_mptcp", true
	if err := c.validateForOS("darwin"); err == nil {
		t.Fatal("Native mode accepted UoT")
	}
	c.SchemaVersion, c.Mode = 2, "tcp_forward"
	if err := c.validateForOS("darwin"); err == nil {
		t.Fatal("legacy Native mode accepted UoT")
	}
}

func TestNativeMPTCPIsMacOSOnly(t *testing.T) {
	p := bundleProfile("native", "Native", 1081)
	p.Mode = "native_mptcp"
	c := p.config()
	if err := c.validateForOS("darwin"); err != nil {
		t.Fatalf("darwin rejected native_mptcp: %v", err)
	}
	for _, goos := range []string{"linux", "windows"} {
		if err := c.validateForOS(goos); err == nil {
			t.Fatalf("%s accepted native_mptcp", goos)
		}
	}
}

func TestUOTBundlePreflightChecksUDPPort(t *testing.T) {
	occupied, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	p := bundleProfile("uot", "UoT only", occupied.LocalAddr().(*net.UDPAddr).Port)
	p.TCPEnabled, p.UOTEnabled = false, true
	if err := preflightBundlePorts([]BundleProfile{p}); err == nil {
		t.Fatal("UoT preflight accepted occupied local UDP port")
	}
	occupied.Close()
	if err := preflightBundlePorts([]BundleProfile{p}); err != nil {
		t.Fatal(err)
	}
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p.ListenPort})
	if err != nil {
		t.Fatalf("preflight leaked local UDP listener: %v", err)
	}
	probe.Close()
}
