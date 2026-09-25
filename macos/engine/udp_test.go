package main

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func udpSocket(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.SetReadBuffer(256 * 1024); err != nil {
		t.Fatal(err)
	}
	if err := c.SetWriteBuffer(65535); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUDPRoundRobinAndReplyIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entry := udpSocket(t)
	relayA, relayB := udpSocket(t), udpSocket(t)
	clientA, clientB := udpSocket(t), udpSocket(t)
	relays := []Relay{{Host: "127.0.0.1", Port: relayA.LocalAddr().(*net.UDPAddr).Port}, {Host: "127.0.0.1", Port: relayB.LocalAddr().(*net.UDPAddr).Port}}
	done := make(chan error, 1)
	go func() { done <- serveUDP(ctx, entry, relays, time.Minute, 32) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("UDP shutdown blocked")
		}
	}()
	clients := []*net.UDPConn{clientA, clientA, clientB, clientB, clientA, clientA}
	remotes := []*net.UDPConn{relayA, relayB}
	payloads := [][]byte{{0, 1, 0, 255}, {9, 0, 7}, {2, 3}, {}, bytes.Repeat([]byte{0xff, 0, 1}, 16000), {42}}
	var sourceA *net.UDPAddr
	buffer := make([]byte, 65535)
	for i, client := range clients {
		if _, err := client.WriteToUDP(payloads[i], entry.LocalAddr().(*net.UDPAddr)); err != nil {
			t.Fatal(err)
		}
		relay := remotes[i%2]
		relay.SetReadDeadline(time.Now().Add(time.Second))
		n, source, err := relay.ReadFromUDP(buffer)
		if err != nil || !bytes.Equal(buffer[:n], payloads[i]) {
			t.Fatalf("packet %d wrong Relay or payload: %v", i, err)
		}
		if i == 0 {
			sourceA = source
		}
		if i == 2 && source.String() == sourceA.String() {
			t.Fatal("two local clients shared a reply socket")
		}
		if i == 4 && source.String() != sourceA.String() {
			t.Fatal("existing source/Relay mapping was not reused")
		}
		if _, err := relay.WriteToUDP(buffer[:n], source); err != nil {
			t.Fatal(err)
		}
		client.SetReadDeadline(time.Now().Add(time.Second))
		n, from, err := client.ReadFromUDP(buffer)
		if err != nil || !bytes.Equal(buffer[:n], payloads[i]) || from.String() != entry.LocalAddr().String() {
			t.Fatalf("packet %d reply was changed/misrouted: %v", i, err)
		}
	}
}

func TestUDPMappingLimitAndIdleExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entry, relay, clientA, clientB := udpSocket(t), udpSocket(t), udpSocket(t), udpSocket(t)
	done := make(chan error, 1)
	go func() {
		done <- serveUDP(ctx, entry, []Relay{{Host: "127.0.0.1", Port: relay.LocalAddr().(*net.UDPAddr).Port}}, 100*time.Millisecond, 1)
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("UDP shutdown blocked")
		}
	}()
	clientA.WriteToUDP([]byte("first"), entry.LocalAddr().(*net.UDPAddr))
	buffer := make([]byte, 64)
	relay.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := relay.ReadFromUDP(buffer); err != nil {
		t.Fatal(err)
	}
	clientB.WriteToUDP([]byte("blocked"), entry.LocalAddr().(*net.UDPAddr))
	relay.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if _, _, err := relay.ReadFromUDP(buffer); err == nil {
		t.Fatal("mapping limit exceeded")
	}
	time.Sleep(220 * time.Millisecond)
	clientB.WriteToUDP([]byte("after expiry"), entry.LocalAddr().(*net.UDPAddr))
	relay.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := relay.ReadFromUDP(buffer)
	if err != nil || string(buffer[:n]) != "after expiry" {
		t.Fatalf("idle mapping not reclaimed: %v", err)
	}
}

func TestUDPConfigBackwardCompatibility(t *testing.T) {
	c, err := readConfig(bytes.NewBufferString(`{"schema_version":2,"mode":"tcp_forward","listen_port":1081,"relays":[{"host":"192.0.2.1","port":20000},{"host":"192.0.2.2","port":20000}]}`))
	if err != nil || c.UDPEnabled {
		t.Fatalf("old profile: UDP=%v err=%v", c.UDPEnabled, err)
	}
	c, err = readConfig(bytes.NewBufferString(`{"schema_version":2,"mode":"tcp_forward","listen_port":1081,"udp_enabled":true,"relays":[{"host":"192.0.2.1","port":20000},{"host":"192.0.2.2","port":20000}]}`))
	if err != nil || !c.UDPEnabled {
		t.Fatalf("UDP profile: UDP=%v err=%v", c.UDPEnabled, err)
	}
}
