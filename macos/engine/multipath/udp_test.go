package multipath

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDatagramReorderReplayTamper(t *testing.T) {
	key, _ := ParseKey(testToken)
	var id sessionID
	rand.Read(id[:])
	client, _ := newUDPPeer(key, id, true)
	server, _ := newUDPPeer(key, id, false)
	var packets [][]byte
	client.setPath(1, "test", func(p []byte) error { packets = append(packets, append([]byte(nil), p...)); return nil })
	client.mu.Lock()
	client.paths[1].lastSeen = time.Now()
	client.mu.Unlock()
	payload := make([]byte, udpMax)
	rand.Read(payload)
	if err := client.send(7, payload); err != nil {
		t.Fatal(err)
	}
	if len(packets) < 2 {
		t.Fatal("fragmentation missing")
	}
	forged := append([]byte(nil), packets[0]...)
	forged[len(forged)-1] ^= 1
	if _, _, err := server.receive(forged, 0, "", nil); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("tamper accepted: %v", err)
	}
	completed := 0
	for i := len(packets) - 1; i >= 0; i-- {
		if len(packets[i]) > udpWireMax {
			t.Fatal("MTU exceeded")
		}
		f, ok, err := server.receive(packets[i], 0, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			completed++
			if f.flow != 7 || !bytes.Equal(payload, f.data) {
				t.Fatal("datagram changed")
			}
		}
		if _, _, err := server.receive(packets[i], 0, "", nil); err == nil {
			t.Fatal("replay accepted")
		}
	}
	if completed != 1 || server.snapshot().AssemblyBytes != 0 {
		t.Fatalf("completion/cache: %d %+v", completed, server.snapshot())
	}
	// An empty datagram must not disappear into a nil/empty sentinel.
	packets = nil
	if err := client.send(9, nil); err != nil {
		t.Fatal(err)
	}
	f, ok, err := server.receive(packets[0], 0, "", nil)
	if err != nil || !ok || len(f.data) != 0 || f.flow != 9 {
		t.Fatalf("empty: %v %v %+v", ok, err, f)
	}
}

func TestDatagramLossExpiryAndBounds(t *testing.T) {
	key, _ := ParseKey(testToken)
	var id sessionID
	rand.Read(id[:])
	client, _ := newUDPPeer(key, id, true)
	server, _ := newUDPPeer(key, id, false)
	client.mu.Lock()
	for i := 1; i <= udpAssemblyMax+10; i++ {
		packet, err := client.sealLocked(udpFrame{kind: udpData, path: 1, flow: 1, id: uint64(i), count: uint16((udpMax + udpFragment - 1) / udpFragment), total: udpMax, data: make([]byte, udpFragment)})
		if err != nil {
			t.Fatal(err)
		}
		_, complete, err := server.receive(packet, 0, "", nil)
		if complete {
			t.Fatal("incomplete datagram delivered")
		}
		if err != nil && !errors.Is(err, ErrResourceLimit) {
			t.Fatal(err)
		}
	}
	client.mu.Unlock()
	before := server.snapshot()
	if before.Assemblies == 0 || before.Assemblies > udpAssemblyMax || before.AssemblyBytes > udpAssemblyBytesMax {
		t.Fatalf("unbounded reassembly %+v", before)
	}
	server.tick(time.Now().Add(udpAssemblyTTL + time.Second))
	after := server.snapshot()
	if after.Assemblies != 0 || after.AssemblyBytes != 0 {
		t.Fatalf("expiry leak: %+v", after)
	}
	var replay replayWindow
	replay.commit(10000)
	if replay.check(1) {
		t.Fatal("ancient replay accepted")
	}
	if !replay.check(9999) {
		t.Fatal("reordering rejected")
	}
	replay.commit(9999)
	if replay.check(9999) {
		t.Fatal("duplicate accepted")
	}
}

type testUDPRelay struct {
	local, remote *net.UDPConn
	client        atomic.Pointer[net.UDPAddr]
	disabled      atomic.Bool
	dropEvery     atomic.Int64
	delayNS       atomic.Int64
	packets       atomic.Int64
}

func newTestUDPRelay(t *testing.T, target string) *testUDPRelay {
	t.Helper()
	local, err := listenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := net.DialUDP("udp", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	if err = tuneUDP(remote); err != nil {
		t.Fatal(err)
	}
	r := &testUDPRelay{local: local, remote: remote}
	stop := make(chan struct{})
	var workers sync.WaitGroup
	slots := make(chan struct{}, 128)
	// Fixed propagation delay, not a per-fragment serialization rate cap.
	// Every delayed packet owns its bytes; work and cleanup are bounded.
	deliver := func(data []byte, write func([]byte)) {
		delay := time.Duration(r.delayNS.Load())
		if delay <= 0 {
			write(data)
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			return
		}
		payload := append([]byte(nil), data...)
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-stop:
			case <-timer.C:
				write(payload)
			}
		}()
	}
	workers.Add(2)
	t.Cleanup(func() { close(stop); local.Close(); remote.Close(); workers.Wait() })
	go func() {
		defer workers.Done()
		buf := make([]byte, udpWireMax+1)
		for {
			n, a, e := local.ReadFromUDP(buf)
			if e != nil {
				return
			}
			r.client.Store(a)
			seq := r.packets.Add(1)
			drop := r.dropEvery.Load()
			if r.disabled.Load() || (drop > 0 && seq%drop == 0) {
				continue
			}
			deliver(buf[:n], func(data []byte) { remote.Write(data) })
		}
	}()
	go func() {
		defer workers.Done()
		buf := make([]byte, udpWireMax+1)
		for {
			n, e := remote.Read(buf)
			if e != nil {
				return
			}
			a := r.client.Load()
			if a != nil && !r.disabled.Load() {
				deliver(buf[:n], func(data []byte) { local.WriteToUDP(data, a) })
			}
		}
	}()
	return r
}

func udpEchoBackend(t *testing.T) string {
	t.Helper()
	c, err := listenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 65536)
		for {
			n, a, e := c.ReadFromUDP(buf)
			if e != nil {
				return
			}
			c.WriteToUDP(buf[:n], a)
		}
	}()
	return c.LocalAddr().String()
}
func setupUDP(t *testing.T) (*UDPClient, *UDPServer, []*testUDPRelay) {
	t.Helper()
	s, srv, _, _ := testTCP(t, 2, 0, 0)
	server, err := StartUDPServer(srv, "127.0.0.1:0", udpEchoBackend(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	relays := []*testUDPRelay{newTestUDPRelay(t, server.Addr().String()), newTestUDPRelay(t, server.Addr().String())}
	addresses := []string{relays[0].local.LocalAddr().String(), relays[1].local.LocalAddr().String()}
	client, err := StartClientUDP(s, testToken, "127.0.0.1:0", addresses)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = client.WaitPaths(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = StartClientUDP(s, testToken, "127.0.0.1:0", addresses); err == nil {
		t.Fatal("UDP nonce state could be restarted")
	}
	return client, server, relays
}
func udpApplication(t *testing.T, client *UDPClient) *net.UDPConn {
	t.Helper()
	c, err := net.DialUDP("udp", nil, client.Addr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err = tuneUDP(c); err != nil {
		t.Fatal(err)
	}
	return c
}
func udpRoundtrip(c *net.UDPConn, data []byte) error {
	if err := c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if _, err := c.Write(data); err != nil {
		return err
	}
	buf := make([]byte, 65536)
	n, err := c.Read(buf)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, buf[:n]) {
		return errors.New("UDP application bytes changed")
	}
	return nil
}
func TestUDPEndToEndBoundariesIsolation(t *testing.T) {
	client, server, _ := setupUDP(t)
	a := udpApplication(t, client)
	b := udpApplication(t, client)
	for _, size := range []int{0, 1, 1000, udpFragment, udpFragment + 1, 48000, udpMax} {
		payload := make([]byte, size)
		rand.Read(payload)
		if err := udpRoundtrip(a, payload); err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if err := udpRoundtrip(b, []byte("separate local sender")); err != nil {
			t.Fatal(err)
		}
	}
	if client.Snapshot().Connections != 2 {
		t.Fatalf("client maps: %+v", client.Snapshot())
	}
	snapshots := server.Snapshots()
	if len(snapshots) != 1 || snapshots[0].Connections != 2 {
		t.Fatalf("Landing per-flow mapping (not per-path): %+v", snapshots)
	}
	// Concurrent outstanding datagrams permit dynamic distribution across paths.
	const total = 80
	for i := 0; i < total; i++ {
		payload := make([]byte, 900)
		binary.BigEndian.PutUint32(payload, uint32(i))
		if _, err := a.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[uint32]bool)
	buf := make([]byte, 2048)
	a.SetReadDeadline(time.Now().Add(3 * time.Second))
	for len(seen) < total {
		n, err := a.Read(buf)
		if err != nil {
			t.Fatalf("received %d/%d: %v", len(seen), total, err)
		}
		if n != 900 {
			t.Fatal("boundary lost")
		}
		id := binary.BigEndian.Uint32(buf)
		if id >= total || seen[id] {
			t.Fatal("wrong or duplicate datagram")
		}
		seen[id] = true
	}
	for _, p := range client.Snapshot().PathStats {
		if p.Sent == 0 {
			t.Fatalf("UDP path unused: %+v", p)
		}
	}
	client.expire(time.Now().Add(udpIdle + time.Second))
	server.expire(time.Now().Add(udpIdle + time.Second))
	if client.Snapshot().Connections != 0 {
		t.Fatal("client mapping expiry failed")
	}
	for _, p := range server.Snapshots() {
		if p.Connections != 0 {
			t.Fatal("server mapping expiry failed")
		}
	}
}
func TestUDPFailureRecoveryAndLoss(t *testing.T) {
	client, _, relays := setupUDP(t)
	app := udpApplication(t, client)
	relays[0].disabled.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for client.Snapshot().Paths != 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if client.Snapshot().Paths != 1 {
		t.Fatal("failed UDP path still healthy")
	}
	for i := 0; i < 8; i++ {
		if err := udpRoundtrip(app, []byte("surviving carrier")); err != nil {
			t.Fatal(err)
		}
	}
	relays[0].disabled.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.WaitPaths(ctx, 2); err != nil {
		t.Fatal(err)
	}
	relays[0].dropEvery.Store(3)
	relays[1].dropEvery.Store(3)
	// Loss must never synthesize a partial application datagram. Either the
	// exact packet returns, or the application sees ordinary UDP loss.
	payload := make([]byte, 48000)
	rand.Read(payload)
	app.SetDeadline(time.Now().Add(400 * time.Millisecond))
	app.Write(payload)
	buf := make([]byte, 65536)
	n, err := app.Read(buf)
	if err == nil && !bytes.Equal(payload, buf[:n]) {
		t.Fatal("partial/corrupt packet delivered during loss")
	}
	relays[0].dropEvery.Store(0)
	relays[1].dropEvery.Store(0)
	if err = udpRoundtrip(app, []byte("recovered after loss")); err != nil {
		t.Fatal(err)
	}
}

func TestUDPDifferentPathRTT(t *testing.T) {
	client, _, relays := setupUDP(t)
	relays[0].delayNS.Store(int64(time.Millisecond))
	relays[1].delayNS.Store(int64(35 * time.Millisecond))
	app := udpApplication(t, client)
	deadline := time.Now().Add(5 * time.Second)
	var stats UDPStats
	for time.Now().Before(deadline) {
		stats = client.Snapshot()
		if stats.Paths == 2 && stats.PathStats[1].RTTMS > stats.PathStats[0].RTTMS+10 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if stats.Paths != 2 || stats.PathStats[1].RTTMS <= stats.PathStats[0].RTTMS+10 {
		t.Fatalf("different network delays not measured: %+v", stats)
	}
	for _, size := range []int{0, 1, udpFragment + 1, 8192, 48000} {
		payload := make([]byte, size)
		rand.Read(payload)
		if err := udpRoundtrip(app, payload); err != nil {
			t.Fatalf("different RTT size %d: %v", size, err)
		}
	}
	t.Logf("UDP asymmetric path telemetry: %+v", client.Snapshot())
}

func FuzzUDPHeader(f *testing.F) {
	f.Add([]byte("MPU1"))
	f.Add(make([]byte, udpHeader+16))
	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := parseUDPHeader(b)
		if err == nil && (h.path < 1 || h.path > 8 || len(b) > udpWireMax) {
			t.Fatal("invalid parsed header")
		}
	})
}
