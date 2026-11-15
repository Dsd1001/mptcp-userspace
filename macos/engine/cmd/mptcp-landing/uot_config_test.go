package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestEgressTunnelOnlyConfigAndRoutedUOT(t *testing.T) {
	c, err := defaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.SchemaVersion != 2 || c.Kind != "mpx4_tunnel_egress" || c.UDPEnabled || len(c.TCPRoutes) != 1 {
		t.Fatal("default is not tunnel-only")
	}
	decoded, err := decodeConfig(configBytes(c))
	if err != nil || decoded.TCPRoutes[0].UDPBackend == "" {
		t.Fatalf("routed UoT roundtrip: %v", err)
	}
	old := bytes.Replace(configBytes(c), []byte(`"schema_version": 2`), []byte(`"schema_version": 1`), 1)
	if _, err = decodeConfig(old); err == nil {
		t.Fatal("legacy schema accepted")
	}
	for _, field := range []string{`"backend_tcp":"127.0.0.1:8388"`, `"udp_enabled":true`, `"listen_udp":"0.0.0.0:24001"`, `"uot_enabled":true`} {
		bad := append([]byte("{"+field+","), configBytes(c)[1:]...)
		if _, err = decodeConfig(bad); err == nil {
			t.Fatalf("legacy field accepted: %s", field)
		}
	}
	c.TCPRoutes[0].Backend = ""
	if err = c.validate(); err != nil {
		t.Fatalf("UDP-only route rejected: %v", err)
	}
	c.TCPRoutes[0].UDPBackend = "0.0.0.0:8388"
	if err = c.validate(); err == nil {
		t.Fatal("unspecified UDP target accepted")
	}
	c.TCPRoutes[0].UDPBackend = ""
	if err = c.validate(); err == nil {
		t.Fatal("route without service accepted")
	}
}

func TestConfigureUOTWithoutPublicUDP(t *testing.T) {
	c, err := defaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	// listener, count, id, name, TCP target disabled, UDP target, weight,
	// maximum connections, scheduler, key unchanged, maximum sessions.
	input := "\n\n\n\n-\n127.0.0.1:8389\n\n\n\n\n\n"
	var out bytes.Buffer
	term := terminal{reader: bufio.NewReader(strings.NewReader(input)), out: &out}
	got, err := term.configure(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.TCPRoutes[0].Backend != "" || got.TCPRoutes[0].UDPBackend != "127.0.0.1:8389" || got.ListenUDP != "" {
		t.Fatal("wrong routed UoT config")
	}
	if strings.Contains(out.String(), "UDP 聚合监听") || strings.Contains(out.String(), c.TransportKey) {
		t.Fatal("unexpected public UDP listener prompt or leaked key")
	}
}
