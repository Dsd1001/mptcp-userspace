package multipath

import (
	"testing"
	"time"
)

func TestControlRoutingProtectDoesNotPutWINDOWOnBackup(t *testing.T) {
	s, fast, backup := modeFixture(SchedulerProtect)
	s.setSchedulerRoleLocked(backup, RoleBackup, "test", time.Now())
	fast.outstanding = fast.flightBudget() // DATA gate must not block control.
	backup.minRTT = time.Millisecond       // A misleading low estimate cannot bypass role.
	for i := 0; i < 128; i++ {
		s.controlLocked(nil, frame{kind: kindWindow, stream: 1, id: StreamWindow})
		select {
		case f := <-fast.control:
			if f.kind != kindWindow {
				t.Fatal("wrong control")
			}
		default:
			t.Fatal("WINDOW was sent over a BACKUP while ACTIVE was available")
		}
	}
	s.controlLocked(backup, frame{kind: kindACK, stream: 1, id: 1})
	select {
	case <-backup.control:
	default:
		t.Fatal("directed ACK did not preserve owning carrier")
	}
	for len(fast.control) < cap(fast.control) {
		fast.control <- frame{kind: kindPing}
	}
	s.controlLocked(nil, frame{kind: kindWindow, stream: 1, id: StreamWindow})
	select {
	case <-backup.control:
	default:
		t.Fatal("all ACTIVE control queues full must still allow a bounded fallback")
	}
	if s.controlDrops != 0 {
		t.Fatal("control dropped despite available queue")
	}
}
func TestControlRoutingUsesEstimatedLatencyWithoutDATAAdmission(t *testing.T) {
	s, fast, queued := modeFixture(SchedulerAggregate)
	fast.outstanding = fast.flightBudget()
	queued.outstanding = 8 << 20
	fast.minRTT = 20 * time.Millisecond
	queued.minRTT = 100 * time.Millisecond
	for i := 0; i < 128; i++ {
		s.controlLocked(nil, frame{kind: kindWindow, stream: 1, id: StreamWindow})
		select {
		case <-fast.control:
		default:
			t.Fatal("generic control took a higher-delay available carrier")
		}
	}
}
