package multipath

import (
	"context"
	"net"
	"testing"
	"time"
)

// Use the real encrypted writer rather than injecting a fictitious ACK. An
// idle interval has no unacknowledged DATA and must not consume the next
// flight's no-progress allowance. A later send in the SAME unacknowledged
// flight, on the other hand, must not hide a genuinely stuck path.
func TestSchedulerFreshFlightDoesNotInheritIdleAge(t *testing.T) {
	for _, mode := range []SchedulerMode{SchedulerAuto, SchedulerProtect} {
		t.Run(string(mode), func(t *testing.T) {
			s, c, unused := modeFixture(mode)
			unused.active = false
			ctx, cancel := context.WithCancel(context.Background())
			s.ctx = ctx
			a, b := net.Pipe()
			key, _ := ParseKey(testToken)
			tx, err := newSecure(a, key, []byte("scheduler-idle-flight"), true)
			if err != nil {
				t.Fatal(err)
			}
			rx, err := newSecure(b, key, []byte("scheduler-idle-flight"), false)
			if err != nil {
				t.Fatal(err)
			}
			c.conn, c.done = tx, make(chan struct{})
			old := time.Now().Add(-time.Second)
			c.lastACK, c.scheduler.lastProgressAt = old, old
			beforeSamples, beforeGoodput := c.deliverySamples, c.goodput
			done := make(chan struct{})
			go func() { s.writeCarrier(c); close(done) }()
			defer func() { cancel(); a.Close(); b.Close(); <-done }()
			send := func(text string) {
				t.Helper()
				s.mu.Lock()
				p := s.queueLocked(frame{kind: kindData, stream: 1, data: []byte(text)})
				s.dispatchLocked(time.Now())
				s.mu.Unlock()
				b.SetReadDeadline(time.Now().Add(2 * time.Second))
				f, e := rx.readFrame()
				if e != nil || f.kind != kindData || f.id != p.f.id || string(f.data) != text {
					t.Fatalf("real writer DATA mismatch: %v", e)
				}
			}
			send("fresh DATA after an idle interval")
			s.mu.Lock()
			s.sweepSchedulerLocked(time.Now())
			role := c.scheduler.role
			unchangedLearning := c.deliverySamples == beforeSamples && c.goodput == beforeGoodput && c.lastACK.Equal(old)
			s.mu.Unlock()
			if role != RoleActive {
				t.Fatalf("fresh DATA was falsely declared %s using the previous flight's ACK age", role)
			}
			if !unchangedLearning {
				t.Fatal("starting DATA falsely updated ACK/capacity learning")
			}
			send("a second DATA frame without any ACK")
			s.mu.Lock()
			// Advancing only the sweep clock is deterministic and needs no long
			// sleeps. There has still been NO authenticated DATA ACK.
			s.sweepSchedulerLocked(time.Now().Add(time.Second))
			role = c.scheduler.role
			debt := c.outstanding
			s.mu.Unlock()
			if role != RoleBackup || debt == 0 {
				t.Fatal("continued enqueue hid a real stall or revoked debt", role, debt)
			}
		})
	}
}
