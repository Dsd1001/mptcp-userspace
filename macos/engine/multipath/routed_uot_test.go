package multipath

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"
)

func TestRoutedUOTSharesSessionAndPreservesDatagrams(t *testing.T) {
	tcp, _ := routedBannerBackend(t, "")
	udp := udpEchoBackend(t)
	srv, s, _ := routedFixture(t, []TCPRoute{{ID: 1, Backend: tcp, UDPBackend: udp}, {ID: 2, UDPBackend: udp}}, []RoutePolicy{{ID: 1}, {ID: 2}}, 2)
	var clients []*UOTClient
	for _, id := range []uint32{1, 2} {
		socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		u, err := StartRoutedClientUOT(s, socket, id, 8)
		if err != nil {
			socket.Close()
			t.Fatal(err)
		}
		clients = append(clients, u)
		defer u.Close()
		local, err := net.DialUDP("udp4", nil, u.Addr().(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		local.SetDeadline(time.Now().Add(5 * time.Second))
		sizes := []int{0, 1, 1200, 8192}
		// Darwin's local UDP socket limit can be below the protocol maximum.
		// Do not mutate host sysctls in a test. Codec maximum is tested below;
		// Linux/Windows also exercise that size through real UDP sockets.
		if runtime.GOOS != "darwin" {
			sizes = append(sizes, udpMax)
		}
		for _, size := range sizes {
			payload := bytes.Repeat([]byte{byte(id)}, size)
			if _, err := local.Write(payload); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, 65536)
			n, err := local.Read(got)
			if err != nil || !bytes.Equal(got[:n], payload) {
				t.Fatalf("route %d datagram %d: n=%d err=%v", id, size, n, err)
			}
		}
		local.Close()
	}
	if err := routedRoundtrip(s, 1, "", 256<<10); err != nil {
		t.Fatal(err)
	}
	if len(srv.Snapshots()) != 1 || s.Snapshot().Paths != 2 {
		t.Fatal("UoT created another Session or paths")
	}
	for _, u := range clients {
		u.Close()
	}
	s.mu.Lock()
	queued, flows := s.uotQueuedBytes, s.uotClientFlows
	s.mu.Unlock()
	if queued != 0 || flows != 0 {
		t.Fatalf("UoT shutdown leaked queue/flow budget: %d/%d", queued, flows)
	}
}

func TestRoutedUOTUnsupportedRouteDoesNotStopTCP(t *testing.T) {
	tcp, _ := routedBannerBackend(t, "")
	_, s, _ := routedFixture(t, []TCPRoute{{ID: 1, Backend: tcp}}, []RoutePolicy{{ID: 1}}, 2)
	st, err := s.OpenRoutedUOT(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(3 * time.Second))
	var b [1]byte
	if _, err = st.Read(b[:]); err == nil {
		t.Fatal("UDP service accepted on TCP-only route")
	}
	if err = routedRoundtrip(s, 1, "", 128<<10); err != nil {
		t.Fatalf("TCP affected by unavailable UDP: %v", err)
	}
}

func TestRoutedUOTAttachedSocketSurvivesSessionClose(t *testing.T) {
	udp := udpEchoBackend(t)
	_, s, _ := routedFixture(t, []TCPRoute{{ID: 1, UDPBackend: udp}}, []RoutePolicy{{ID: 1}}, 2)
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	u, err := AttachRoutedClientUOT(s, socket, 1, 8)
	if err != nil {
		t.Fatal(err)
	}
	u.Close()
	// Closing an association manager must not close the persistent local port.
	c, err := net.DialUDP("udp4", nil, socket.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("retained socket"))
	socket.SetReadDeadline(time.Now().Add(time.Second))
	var b [32]byte
	if n, _, err := socket.ReadFromUDPAddrPort(b[:]); err != nil || string(b[:n]) != "retained socket" {
		t.Fatalf("socket lost on detach: %v", err)
	}
	// Late input is dropped after the manager has closed, never queued/replayed.
	u.ReceiveDatagram(netip.MustParseAddrPort("127.0.0.1:12345"), []byte("late"))
	if u.Snapshot().Connections != 0 {
		t.Fatal("closed UoT client created a flow")
	}
}

func TestRoutedUOTQueueBudgetIsSessionWide(t *testing.T) {
	s, _ := receiveRouteFixture(t)
	us := make([]*UOTClient, 5)
	for i := range us {
		us[i] = &UOTClient{session: s, routeID: uint32(i + 1)}
	}
	for i := 0; i < 4; i++ {
		if !us[i].reserveSessionQueueLocked(2 << 20) {
			t.Fatal("initial bounded capacity unavailable")
		}
		us[i].queuedBytes = 2 << 20
	}
	if us[4].reserveSessionQueueLocked(1) {
		t.Fatal("new inlet bypassed global UoT queue bound")
	}
	for i := 0; i < 4; i++ {
		us[i].releaseSessionQueueLocked(2 << 20)
	}
	if s.uotQueuedBytes != 0 {
		t.Fatal("global queued bytes leaked")
	}
}

func TestRoutedUOTCodecMaximumAndEmptyDatagrams(t *testing.T) {
	for _, size := range []int{0, 1, 8192, udpMax} {
		var wire bytes.Buffer
		payload := bytes.Repeat([]byte{0x53}, size)
		if err := writeUOTDatagram(&wire, payload); err != nil {
			t.Fatal(err)
		}
		got, err := readUOTDatagram(&wire, make([]byte, udpMax))
		if err != nil || !bytes.Equal(got, payload) || wire.Len() != 0 {
			t.Fatalf("codec size %d: %v", size, err)
		}
	}
	var wire bytes.Buffer
	if writeUOTDatagram(&wire, make([]byte, udpMax+1)) == nil {
		t.Fatal("oversized datagram accepted")
	}
}
