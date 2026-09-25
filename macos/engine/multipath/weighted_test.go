package multipath

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestWeightedCapacityValidationAndWireUnits(t *testing.T) {
	for _, tc := range []struct {
		p    PathCapacity
		good bool
	}{
		{PathCapacity{DownloadMbps: 50}, true},
		{PathCapacity{DownloadMbps: 50, UploadMbps: 12.5}, true},
		{PathCapacity{DownloadMbps: .1, UploadMbps: 6553.5}, true},
		{PathCapacity{}, false},
		{PathCapacity{DownloadMbps: .01}, false},
		{PathCapacity{DownloadMbps: 50.01}, false},
		{PathCapacity{DownloadMbps: 6553.6}, false},
	} {
		err := ValidatePathCapacity(tc.p)
		if (err == nil) != tc.good {
			t.Fatalf("capacity %+v validation err=%v good=%v", tc.p, err, tc.good)
		}
	}
	units, err := capacityUnits(50.5, true)
	if err != nil || units != 505 || capacityFromUnits(units) != 50.5 {
		t.Fatalf("capacity wire quantization units=%d err=%v decoded=%.1f", units, err, capacityFromUnits(units))
	}
}

func TestWeightedSchedulerWireAndLegacyModesRemainStable(t *testing.T) {
	if schedulerWire(SchedulerAuto) != 0x41 || schedulerWire(SchedulerAggregate) != 0x42 || schedulerWire(SchedulerProtect) != 0x43 || schedulerWire(SchedulerWeighted) != 0x44 {
		t.Fatal("scheduler wire assignments changed")
	}
	for _, value := range []byte{0x41, 0x42, 0x43, 0x44} {
		if _, err := schedulerFromWire(value); err != nil {
			t.Fatalf("wire %x rejected: %v", value, err)
		}
	}
}

func TestWeightedLegacyHelloLeavesCapacityBytesZero(t *testing.T) {
	key, _ := ParseKey(testToken)
	for _, mode := range []SchedulerMode{SchedulerAuto, SchedulerAggregate, SchedulerProtect} {
		a, b := net.Pipe()
		done := make(chan error, 1)
		go func() {
			var sid sessionID
			sid[0] = 1
			_, err := clientHandshakeMode(a, key, sid, 1, true, mode)
			done <- err
		}()
		h := make([]byte, helloSize)
		if _, err := io.ReadFull(b, h); err != nil {
			t.Fatal(err)
		}
		if binary.BigEndian.Uint32(h[40:44]) != 0 {
			t.Fatalf("%s changed legacy hello capacity bytes", mode)
		}
		b.Close()
		a.Close()
		<-done
	}
}

func TestWeightedCapacityIsAuthenticated(t *testing.T) {
	key, _ := ParseKey(testToken)
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := readHandshake(b, key)
		b.Close()
		done <- err
	}()

	h := make([]byte, helloSize)
	copy(h, "MPX3")
	h[4], h[5], h[6], h[7], h[8] = 3, 1, 1, schedulerWire(SchedulerWeighted), 1
	binary.BigEndian.PutUint16(h[40:42], 500)
	binary.BigEndian.PutUint16(h[42:44], 250)
	binary.BigEndian.PutUint32(h[44:48], MaxPayload)
	original := append([]byte(nil), h...)
	// Tamper authenticated download capacity after the proof transcript was chosen.
	binary.BigEndian.PutUint16(h[40:42], 600)
	if err := writeAll(a, h); err != nil {
		t.Fatal(err)
	}
	challenge := make([]byte, 32)
	if _, err := io.ReadFull(a, challenge); err != nil {
		t.Fatal(err)
	}
	if err := writeAll(a, mac(key, "mpx3/client-proof", append(original, challenge...))); err != nil {
		t.Fatal(err)
	}
	err := <-done
	a.Close()
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("tampered weighted capacity was not authenticated: %v", err)
	}
}

func TestWeightedDirectionConfigEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capacity   PathCapacity
		clientRate float64
		serverRate float64
	}{
		{"both", PathCapacity{DownloadMbps: 80, UploadMbps: 30}, 30_000_000 / 8, 80_000_000 / 8},
		{"upload-auto", PathCapacity{DownloadMbps: 80}, 0, 80_000_000 / 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			backend, _ := echoBackend(t)
			srv, err := NewServer(ctx, testToken, backend, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer srv.Close()
			l, err := PlainListen(ctx, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			go srv.Serve(l)
			client, err := DialClientWithPolicy(ctx, []string{l.Addr().String()}, testToken, SchedulerWeighted, []PathCapacity{tc.capacity})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err := transfer(client, 1<<20); err != nil {
				t.Fatal(err)
			}
			peer := srv.session(client.id)
			if peer == nil {
				t.Fatal("server session missing")
			}
			clientStats, serverStats := client.Snapshot(), peer.Snapshot()
			if clientStats.ConfiguredSchedulerMode != SchedulerWeighted || clientStats.EffectiveSchedulerMode != SchedulerWeighted ||
				serverStats.ConfiguredSchedulerMode != SchedulerWeighted || serverStats.EffectiveSchedulerMode != SchedulerWeighted {
				t.Fatal("weighted mode not fixed on both directions")
			}
			if len(clientStats.PathStats) != 1 || len(serverStats.PathStats) != 1 {
				t.Fatal("weighted path missing")
			}
			if clientStats.PathStats[0].ConfiguredRateBPS != tc.clientRate || serverStats.PathStats[0].ConfiguredRateBPS != tc.serverRate {
				t.Fatalf("directional capacity client=%.0f/%0.f server=%.0f/%0.f",
					clientStats.PathStats[0].ConfiguredRateBPS, tc.clientRate,
					serverStats.PathStats[0].ConfiguredRateBPS, tc.serverRate)
			}
		})
	}
}

func TestWeightedSelectionUsesConfiguredRateButHonorsPenalty(t *testing.T) {
	s := schedulerFixture()
	s.initScheduler(SchedulerWeighted)
	now := time.Now()
	a, b := schedulerPath(1), schedulerPath(2)
	for _, c := range []*carrier{a, b} {
		c.active = true
		c.minRTT = 50 * time.Millisecond
		c.rtt = 50 * time.Millisecond
		c.configuredRateBPS = configuredRateBPS(50)
		c.budget = 2 << 20
		c.lastACK = now
	}
	// Stale learned goodput is intentionally opposite the current queue debt.
	a.goodput = 20 << 20
	b.goodput = 512 << 10
	a.outstanding = 4 * (MaxPayload + 64)
	s.paths = map[byte]*carrier{1: a, 2: b}
	if got := s.pathLocked(now); got != b {
		t.Fatalf("configured capacity did not remove stale-goodput bias: got=%v", got)
	}
	b.penaltyUntil = now.Add(time.Second)
	if got := s.pathLocked(now); got != a {
		t.Fatalf("weighted capacity overrode live penalty: got=%v", got)
	}
}

func TestWeightedUploadAutoUsesLearnedFlight(t *testing.T) {
	s := schedulerFixture()
	s.initScheduler(SchedulerWeighted)
	c := schedulerPath(1)
	c.active = true
	c.minRTT = 50 * time.Millisecond
	c.rtt = 50 * time.Millisecond
	c.configuredRateBPS = 0
	c.goodput = 2 << 20
	c.budget = 384 << 10
	c.lastACK = time.Now()
	s.paths = map[byte]*carrier{1: c}
	if got := s.pathLocked(time.Now()); got != c {
		t.Fatal("upload-auto weighted path became unusable")
	}
	if weightedFlightBudget(c, 0) != c.flightBudget() {
		t.Fatal("upload-auto did not preserve learned flight")
	}
}
