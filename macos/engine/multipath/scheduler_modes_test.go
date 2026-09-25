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

func modeFixture(mode SchedulerMode) (*Session, *carrier, *carrier) {
	s := schedulerFixture()
	s.ctx = context.Background()
	s.initScheduler(mode)
	a, b := schedulerPath(1), schedulerPath(2)
	for _, c := range []*carrier{a, b} {
		c.minRTT, c.rtt = 20*time.Millisecond, 20*time.Millisecond
		c.goodput = 20e6
		c.deliverySamples = 3
		for i := range c.capacitySamples {
			c.capacitySamples[i] = c.goodput
		}
		c.lastACK = time.Now()
		c.scheduler = schedulerPathState{role: RoleActive, epoch: 3}
		c.budget = 1 << 20
		s.paths[c.id] = c
	}
	s.refreshSchedulerLocked(time.Now(), false)
	return s, a, b
}

func TestSchedulerModeValuesAndWireCapability(t *testing.T) {
	for _, mode := range []SchedulerMode{SchedulerAuto, SchedulerAggregate, SchedulerProtect} {
		parsed, err := ParseSchedulerMode(string(mode))
		if err != nil || parsed != mode {
			t.Fatal(mode, err)
		}
		decoded, err := schedulerFromWire(schedulerWire(mode))
		if err != nil || decoded != mode {
			t.Fatal("mode wire mismatch")
		}
	}
	if m, err := ParseSchedulerMode(""); err != nil || m != SchedulerAuto {
		t.Fatal("absent mode must default Auto")
	}
	for _, value := range []string{"AUTO", "tcp-native", "Protect", " auto", "auto\n"} {
		if _, err := ParseSchedulerMode(value); err == nil {
			t.Fatal("invalid mode", value)
		}
	}
	for _, value := range []byte{0, 1, 3, 0x10, 0x14, 0xff} {
		if _, err := schedulerFromWire(value); !errors.Is(err, ErrProtocol) {
			t.Fatal("legacy/unknown capability accepted", value)
		}
	}
}

func TestSchedulerRTTOnlyDemotionWaitsForMeasuredReference(t *testing.T) {
	s := schedulerFixture()
	s.initScheduler(SchedulerAuto)
	now := time.Now()
	fast, queued := schedulerPath(1), schedulerPath(2)
	for _, c := range []*carrier{fast, queued} {
		c.active = true
		c.scheduler.role = RoleLearning
		c.scheduler.dataACKs = 3
		c.lastACK = now
		s.paths[c.id] = c
	}
	fast.minRTT, fast.rtt = 30*time.Millisecond, 30*time.Millisecond
	queued.minRTT, queued.rtt = 120*time.Millisecond, 120*time.Millisecond
	if got := s.schedulerRTTOutlierLocked(queued, now); got != RoleLearning {
		t.Fatalf("startup queueing caused premature RTT-only demotion: %s", got)
	}
	fast.deliverySamples = 3
	for i := 0; i < 3; i++ {
		fast.capacitySamples[i] = 10e6
	}
	if got := s.schedulerRTTOutlierLocked(queued, now); got != RoleBackup {
		t.Fatalf("qualified extreme RTT outlier not demoted: %s", got)
	}
}

func TestSchedulerAutoWaitsForAllInitialLearningBeforeProtect(t *testing.T) {
	s := schedulerFixture()
	s.initScheduler(SchedulerAuto)
	now := time.Now()
	probe, active, learning := schedulerPath(1), schedulerPath(2), schedulerPath(3)
	for _, c := range []*carrier{probe, active, learning} {
		c.active = true
		c.lastACK = now
		s.paths[c.id] = c
	}
	probe.scheduler.role = RoleProbe
	probe.deliverySamples = 3
	probe.capacitySamples[0], probe.capacitySamples[1], probe.capacitySamples[2] = 2e6, 2e6, 2e6
	active.scheduler.role = RoleActive
	active.deliverySamples = 3
	active.capacitySamples[0], active.capacitySamples[1], active.capacitySamples[2] = 20e6, 20e6, 20e6
	learning.scheduler.role = RoleLearning
	learning.deliverySamples = 2
	s.refreshSchedulerLocked(now, false)
	if s.scheduler.effective != SchedulerAggregate {
		t.Fatal("Auto entered Protect before all paths finished initial learning")
	}
	learning.scheduler.role = RoleActive
	learning.deliverySamples = 3
	learning.capacitySamples[0], learning.capacitySamples[1], learning.capacitySamples[2] = 20e6, 20e6, 20e6
	s.refreshSchedulerLocked(now.Add(time.Second), false)
	if s.scheduler.effective != SchedulerProtect {
		t.Fatal("stable degraded path was ignored after learning completed")
	}
}

func TestSchedulerAutoStartupFailureNeedsMeasuredReference(t *testing.T) {
	s := schedulerFixture()
	s.initScheduler(SchedulerAuto)
	now := time.Now()
	a, b := schedulerPath(1), schedulerPath(2)
	for _, c := range []*carrier{a, b} {
		c.active = true
		c.scheduler.role = RoleLearning
		s.paths[c.id] = c
	}
	s.schedulerFailureLocked(a, "stale_data_delivery", now)
	if a.scheduler.role != RoleLearning || s.scheduler.effective != SchedulerAggregate {
		t.Fatal("startup delay without measured reference caused Protect")
	}
	b.scheduler.role = RoleActive
	b.deliverySamples = 3
	b.capacitySamples[0], b.capacitySamples[1], b.capacitySamples[2] = 10e6, 10e6, 10e6
	s.schedulerFailureLocked(a, "stale_data_delivery", now.Add(time.Second))
	if a.scheduler.role != RoleBackup || s.scheduler.effective != SchedulerProtect {
		t.Fatal("qualified delivery failure did not enter Protect")
	}
}

func TestSchedulerAutoHomogeneousAndForcedModes(t *testing.T) {
	for _, mode := range []SchedulerMode{SchedulerAuto, SchedulerAggregate, SchedulerProtect} {
		s, a, b := modeFixture(mode)
		now := time.Now()
		for epoch := 4; epoch < 14; epoch++ {
			for _, c := range []*carrier{a, b} {
				c.deliverySamples = epoch
				s.schedulerReceiptLocked(c, nil, now.Add(time.Duration(epoch)*time.Second))
			}
		}
		want := SchedulerAggregate
		if mode == SchedulerProtect {
			want = SchedulerProtect
		}
		got := s.schedulerSnapshotLocked()
		if got.ConfiguredSchedulerMode != mode || got.EffectiveSchedulerMode != want || got.ModeSwitches != 0 {
			t.Fatal("homogeneous or forced mode changed", got)
		}
	}
}

func TestSchedulerClassificationThresholdsAndThreeEpochs(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate float64
		rtt  time.Duration
		want PathRole
	}{
		{"active-boundaries", 10e6, 40 * time.Millisecond, RoleActive},
		{"rate-probe", 9e6, 20 * time.Millisecond, RoleProbe},
		{"rate-backup", 2e6, 20 * time.Millisecond, RoleBackup},
		{"rate-15percent", 3e6, 20 * time.Millisecond, RoleProbe},
		{"rtt-probe", 20e6, 50 * time.Millisecond, RoleProbe},
		{"rtt-backup", 20e6, 61 * time.Millisecond, RoleBackup},
		{"rtt-3x", 20e6, 60 * time.Millisecond, RoleProbe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, b := modeFixture(SchedulerAuto)
			b.goodput, b.minRTT = tc.rate, tc.rtt
			for i := range b.capacitySamples {
				b.capacitySamples[i] = tc.rate
			}
			now := time.Now()
			for i := 1; i <= 3; i++ {
				b.deliverySamples++
				s.schedulerReceiptLocked(b, nil, now.Add(time.Duration(i)*time.Second))
				if i < 3 && b.scheduler.role != RoleActive {
					t.Fatal("downgrade before three epochs")
				}
				// More reads/ACKs in the same epoch cannot fake hysteresis.
				for repeat := 0; repeat < 5; repeat++ {
					s.schedulerReceiptLocked(b, nil, now.Add(time.Duration(i)*time.Second))
				}
			}
			if b.scheduler.role != tc.want {
				t.Fatalf("role=%s want %s", b.scheduler.role, tc.want)
			}
			want := SchedulerAggregate
			if tc.want != RoleActive {
				want = SchedulerProtect
			}
			if s.scheduler.effective != want || s.scheduler.configured != SchedulerAuto {
				t.Fatal("Auto policy selection not linked to stable roles")
			}
		})
	}
}

func TestSchedulerLearningUsesThreeHealthyOrRTTSamplesAndFiveRateOnlySamples(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rate    float64
		rtt     time.Duration
		wantAt3 PathRole
		wantAt5 PathRole
	}{
		{"healthy", 20e6, 20 * time.Millisecond, RoleActive, RoleActive},
		{"slow-rate", 2e6, 20 * time.Millisecond, RoleLearning, RoleBackup},
		{"moderate-rate", 9e6, 20 * time.Millisecond, RoleLearning, RoleProbe},
		{"high-rtt", 20e6, 61 * time.Millisecond, RoleBackup, RoleBackup},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, b := modeFixture(SchedulerAuto)
			b.scheduler = schedulerPathState{role: RoleLearning, epoch: 2}
			b.deliverySamples = 3
			b.goodput, b.minRTT = tc.rate, tc.rtt
			for i := range b.capacitySamples {
				b.capacitySamples[i] = tc.rate
			}
			now := time.Now()
			s.schedulerReceiptLocked(b, nil, now)
			if b.scheduler.role != tc.wantAt3 {
				t.Fatalf("role at 3 samples=%s want %s", b.scheduler.role, tc.wantAt3)
			}
			if tc.wantAt3 == RoleLearning {
				b.deliverySamples = 5
				s.schedulerReceiptLocked(b, nil, now.Add(time.Second))
				if b.scheduler.role != tc.wantAt5 {
					t.Fatalf("role at 5 samples=%s want %s", b.scheduler.role, tc.wantAt5)
				}
			}
		})
	}
}

func TestSchedulerLearningRTTOutlierUsesThreeDataACKs(t *testing.T) {
	for _, tc := range []struct {
		name string
		rtt  time.Duration
		want PathRole
	}{
		{"moderate-rtt", 50 * time.Millisecond, RoleProbe},
		{"high-rtt", 61 * time.Millisecond, RoleBackup},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, fast, slow := modeFixture(SchedulerAuto)
			fast.scheduler.dataACKs = 3
			fast.minRTT = 20 * time.Millisecond
			slow.scheduler = schedulerPathState{role: RoleLearning}
			slow.deliverySamples = 0
			slow.minRTT = tc.rtt
			now := time.Now()
			for i := 1; i <= 3; i++ {
				p := &outbound{f: frame{kind: kindData, id: uint64(100 + i), data: []byte{1}}, path: slow, attempts: 1}
				s.schedulerReceiptLocked(slow, p, now.Add(time.Duration(i)*time.Millisecond))
				if i < 3 && slow.scheduler.role != RoleLearning {
					t.Fatal("RTT-only classification used fewer than three DATA ACKs")
				}
			}
			if slow.scheduler.role != tc.want || slow.scheduler.dataACKs != 3 {
				t.Fatalf("role=%s dataACKs=%d want=%s", slow.scheduler.role, slow.scheduler.dataACKs, tc.want)
			}
			if s.scheduler.effective != SchedulerProtect {
				t.Fatal("Auto did not enter Protect for a proven RTT outlier")
			}
		})
	}

	// Forced Aggregate records evidence but never filters/demotes the path.
	s, fast, slow := modeFixture(SchedulerAggregate)
	fast.scheduler.dataACKs = 3
	fast.minRTT = 20 * time.Millisecond
	slow.scheduler = schedulerPathState{role: RoleLearning}
	slow.deliverySamples = 0
	slow.minRTT = 100 * time.Millisecond
	for i := 0; i < 3; i++ {
		p := &outbound{f: frame{kind: kindData, id: uint64(200 + i), data: []byte{1}}, path: slow, attempts: 1}
		s.schedulerReceiptLocked(slow, p, time.Now())
	}
	if slow.scheduler.role != RoleLearning || s.scheduler.effective != SchedulerAggregate {
		t.Fatal("forced Aggregate was changed by RTT safety evidence")
	}
}

func TestSchedulerLearningUsesAggregateUntilProtect(t *testing.T) {
	s, a, b := modeFixture(SchedulerAuto)
	b.active = false
	a.deliverySamples = 0
	a.scheduler = schedulerPathState{role: RoleLearning}
	a.outstanding = schedulerLearningDebt
	s.refreshSchedulerLocked(time.Now(), false)
	if s.scheduler.effective != SchedulerAggregate || s.scheduler.restricted {
		t.Fatal("Auto LEARNING changed the effective Aggregate data path")
	}
	if s.pathLocked(time.Now()) != a {
		t.Fatal("Auto Aggregate applied Protect learning debt cap")
	}
	for i := 1; i <= 2; i++ {
		a.deliverySamples = i
		s.schedulerReceiptLocked(a, nil, time.Now())
		if a.scheduler.role != RoleLearning {
			t.Fatal("classification used fewer than three samples")
		}
	}
	a.deliverySamples = 3
	s.schedulerReceiptLocked(a, nil, time.Now())
	if a.scheduler.role != RoleActive || s.pathLocked(time.Now()) != a {
		t.Fatal("known healthy path did not remain on original Aggregate")
	}

	// Protect still bounds an unknown path before it proves safe.
	s.initScheduler(SchedulerProtect)
	a.scheduler = schedulerPathState{role: RoleLearning}
	a.outstanding = schedulerLearningDebt
	s.refreshSchedulerLocked(time.Now(), false)
	if !s.scheduler.restricted || s.pathLocked(time.Now()) != nil {
		t.Fatal("Protect failed to bound an unknown learning path")
	}

	s.initScheduler(SchedulerAggregate)
	a.scheduler.role = RoleBackup
	if s.pathLocked(time.Now()) != a {
		t.Fatal("forced Aggregate applied Protect role filtering")
	}
}

func TestSchedulerProbeOneFrameAndBackupFiveSeconds(t *testing.T) {
	for _, role := range []PathRole{RoleProbe, RoleBackup} {
		s, a, b := modeFixture(SchedulerProtect)
		now := time.Now()
		s.setSchedulerRoleLocked(b, role, "test_role", now)
		s.refreshSchedulerLocked(now, false)
		period := schedulerProbePeriod
		if role == RoleBackup {
			period = schedulerBackupPeriod
		}
		if s.pathLocked(now.Add(period-time.Nanosecond)) == b {
			t.Fatal("qualification before period")
		}
		now = now.Add(period)
		if s.pathLocked(now) != b {
			t.Fatal("eligible qualification not dispatched")
		}
		p := s.queueLocked(frame{kind: kindData, stream: 1, data: make([]byte, MaxPayload)})
		s.dispatchLocked(now)
		if p.path != b || b.outstanding != MaxPayload+64 || b.scheduler.probeDebtPeak != MaxPayload+64 {
			t.Fatal("probe did not stay within one maximum frame", b.outstanding)
		}
		for i := 0; i < 40; i++ {
			s.queueLocked(frame{kind: kindData, stream: 1, data: make([]byte, MaxPayload)})
		}
		s.dispatchLocked(now.Add(20 * time.Second))
		if b.outstanding != p.cost {
			t.Fatal("timer alone bypassed unACKed probe debt")
		}
		s.removePendingLocked(p)
		if len(b.queue) > 0 {
			<-b.queue // discard the now-stale task in this writer-free fixture
		}
		a.active = false
		if s.pathLocked(now.Add(period)) != b {
			t.Fatal("ACKed probe did not become eligible again")
		}
	}
}

func TestSchedulerProbePromotionNeedsThreeValidACKsAndTwoSeconds(t *testing.T) {
	s, _, b := modeFixture(SchedulerProtect)
	now := time.Now()
	s.setSchedulerRoleLocked(b, RoleProbe, "fixture", now)
	for i := 0; i < 3; i++ {
		p := &outbound{f: frame{kind: kindData, id: uint64(i + 1), data: []byte{1}}, path: b, attempts: 1}
		b.scheduler.probeID = p.f.id
		s.schedulerReceiptLocked(b, p, now.Add(time.Duration(i)*time.Second))
		if i < 2 && b.scheduler.role != RoleProbe {
			t.Fatal("premature promotion")
		}
	}
	if b.scheduler.role != RoleActive {
		t.Fatal("three qualified probes over two seconds did not promote")
	}
	s.setSchedulerRoleLocked(b, RoleProbe, "fixture-again", now)
	for i := 0; i < 4; i++ {
		p := &outbound{f: frame{kind: kindData, id: uint64(20 + i)}, path: b, attempts: 2}
		b.scheduler.probeID = p.f.id
		s.schedulerReceiptLocked(b, p, now.Add(time.Duration(i)*time.Second))
	}
	if b.scheduler.role != RoleProbe || b.scheduler.goodProbes != 0 {
		t.Fatal("retransmitted/late ACK used as qualification")
	}
}

func TestSchedulerAutoRecoveryNeedsFiveAllPathEpochsAndTwoSeconds(t *testing.T) {
	s, a, b := modeFixture(SchedulerAuto)
	now := time.Now()
	s.setSchedulerRoleLocked(b, RoleBackup, "fixture-outlier", now)
	s.refreshSchedulerLocked(now, false)
	if s.scheduler.effective != SchedulerProtect {
		t.Fatal("Auto did not protect")
	}
	s.setSchedulerRoleLocked(b, RoleActive, "fixture-restored", now)
	s.refreshSchedulerLocked(now, false)
	for round := 1; round <= 5; round++ {
		at := now.Add(time.Duration(round) * 500 * time.Millisecond)
		a.deliverySamples++
		s.schedulerReceiptLocked(a, nil, at)
		if s.scheduler.effective != SchedulerProtect {
			t.Fatal("one path faked all-path recovery")
		}
		b.deliverySamples++
		s.schedulerReceiptLocked(b, nil, at)
		if round < 5 && s.scheduler.effective != SchedulerProtect {
			t.Fatal("returned before five complete path epochs")
		}
	}
	if s.scheduler.effective != SchedulerAggregate || s.scheduler.switches != 2 {
		t.Fatal("Auto recovery failed", s.schedulerSnapshotLocked())
	}
}

func TestSchedulerStaleAndTimeoutDoNotRevokeDebt(t *testing.T) {
	s, _, b := modeFixture(SchedulerAuto)
	now := time.Now()
	b.outstanding = 2 << 20
	b.lastACK = now.Add(-time.Second)
	s.sweepSchedulerLocked(now)
	if b.scheduler.role != RoleBackup || b.outstanding != 2<<20 || s.scheduler.effective != SchedulerProtect {
		t.Fatal("stale role/debt semantics")
	}
	for i := 0; i < 100; i++ {
		s.setSchedulerRoleLocked(b, RoleActive, "fixture", now)
		s.schedulerFailureLocked(b, "delivery_timeout", now)
	}
	if len(s.transportEvents) > 64 {
		t.Fatal("scheduler lifecycle unbounded")
	}
	s.initScheduler(SchedulerAggregate)
	b.scheduler.role = RoleActive
	s.schedulerFailureLocked(b, "delivery_timeout", now)
	if b.scheduler.role != RoleActive || s.scheduler.effective != SchedulerAggregate {
		t.Fatal("forced mode silently changed")
	}
}

func TestSchedulerRetransmitACKIsLivenessNotCapacity(t *testing.T) {
	s, _, b := modeFixture(SchedulerAuto)
	old := time.Now().Add(-time.Second)
	b.lastACK = old
	b.scheduler.lastProgressAt = old
	beforeSamples, beforeGoodput := b.deliverySamples, b.goodput
	p := s.queueLocked(frame{kind: kindData, stream: 1, data: make([]byte, MaxPayload)})
	s.unreadyLocked(p)
	p.path = b
	p.sentAt = time.Now().Add(-100 * time.Millisecond)
	p.attempts = 2
	b.outstanding = p.cost
	s.ackLocked(b, frame{kind: kindACK, stream: 1, id: p.f.id, offset: 1})
	if !b.scheduler.lastProgressAt.After(old) {
		t.Fatal("retransmitted ACK did not refresh delivery liveness")
	}
	if b.deliverySamples != beforeSamples || b.goodput != beforeGoodput || !b.lastACK.Equal(old) {
		t.Fatal("retransmitted ACK polluted capacity learning")
	}
	// A fresh in-flight frame immediately after that retransmit must not be
	// declared stale merely because the last capacity-qualified ACK is old.
	b.outstanding = MaxPayload + 64
	b.queued = 0
	b.scheduler.role = RoleActive
	s.sweepSchedulerLocked(time.Now())
	if b.scheduler.role != RoleActive || s.scheduler.effective != SchedulerAggregate {
		t.Fatal("recent retransmit progress was ignored by stale detection")
	}
}

func TestSchedulerModeAuthenticatedAndLegacyCapabilityRejected(t *testing.T) {
	key, _ := ParseKey(testToken)
	for _, legacy := range []bool{false, true} {
		a, b := net.Pipe()
		done := make(chan error, 1)
		go func() { _, err := readHandshake(b, key); b.Close(); done <- err }()
		h := make([]byte, helloSize)
		copy(h, "MPX3")
		h[4], h[5], h[6], h[7], h[8] = 3, 1, 1, schedulerWire(SchedulerAuto), 1
		binary.BigEndian.PutUint32(h[44:], MaxPayload)
		original := append([]byte(nil), h...)
		if legacy {
			h[7] = 0
		} else {
			h[7] = schedulerWire(SchedulerProtect) // unauthenticated tampering
		}
		a.SetDeadline(time.Now().Add(time.Second))
		if err := writeAll(a, h); err != nil {
			t.Fatal(err)
		}
		if !legacy {
			challenge := make([]byte, 32)
			if _, err := io.ReadFull(a, challenge); err != nil {
				t.Fatal(err)
			}
			if err := writeAll(a, mac(key, "mpx3/client-proof", append(original, challenge...))); err != nil {
				t.Fatal(err)
			}
		}
		err := <-done
		a.Close()
		want := ErrAuthentication
		if legacy {
			want = ErrProtocol
		}
		if !errors.Is(err, want) {
			t.Fatal("mode capability not authenticated/fail-closed", err)
		}
	}
}

func TestSchedulerModeBothDirectionsAndJoinConflict(t *testing.T) {
	for _, mode := range []SchedulerMode{SchedulerAuto, SchedulerAggregate, SchedulerProtect} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
			go srv.Serve(l)
			client, err := DialClientWithScheduler(ctx, []string{l.Addr().String()}, testToken, mode)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err := transfer(client, 1<<20); err != nil {
				t.Fatal(err)
			}
			peer := srv.session(client.id)
			if peer == nil {
				t.Fatal("no corresponding session")
			}
			for _, side := range []*Session{client, peer} {
				st := side.Snapshot()
				if st.ConfiguredSchedulerMode != mode || st.Sent == 0 || st.Received == 0 {
					t.Fatal("mode or actual bidirectional DATA absent", st)
				}
			}
			wrong := SchedulerAggregate
			if mode == wrong {
				wrong = SchedulerProtect
			}
			c, err := PlainDial(ctx, l.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			key, _ := ParseKey(testToken)
			_, err = clientHandshakeMode(c, key, client.id, 2, false, wrong)
			c.Close()
			if !errors.Is(err, ErrSchedulerMismatch) {
				t.Fatal("join silently changed configured mode", err)
			}
			if peer.Snapshot().ConfiguredSchedulerMode != mode || client.Snapshot().Paths != 1 {
				t.Fatal("conflicting join changed established session")
			}
			if _, err := transfer(client, 65537); err != nil {
				t.Fatal("conflict harmed existing data", err)
			}
		})
	}
}

// The delivery filter starts with eight 4 MiB/s priors. Only slots replaced by
// real epochs qualify for role classification. A newly rejoined route must
// not downgrade an established equal-rate route merely because of its prior.
func TestSchedulerClassificationExcludesStartupPriors(t *testing.T) {
	s, a, b := modeFixture(SchedulerAuto)
	a.goodput = 1 << 20
	b.goodput = 4 << 20
	a.capacitySamples = [8]float64{1 << 20, 1 << 20, 1 << 20, 1 << 20, 1 << 20, 1 << 20, 1 << 20, 1 << 20}
	a.deliverySamples = 10
	b.capacitySamples = [8]float64{1 << 20, 1 << 20, 1 << 20, 4 << 20, 4 << 20, 4 << 20, 4 << 20, 4 << 20}
	b.deliverySamples = 3
	b.capacityIndex = 3
	if role := s.schedulerTargetLocked(a, time.Now()); role != RoleActive {
		t.Fatalf("real equal-rate route classified %s by an unmeasured startup prior", role)
	}
}
