package multipath

import (
	"testing"
	"time"
)

func TestSchedulerRateDemotionRequiresOfferedCapacity(t *testing.T) {
	for _, mode := range []SchedulerMode{SchedulerAuto, SchedulerProtect} {
		t.Run(string(mode), func(t *testing.T) {
			s, fast, limited := modeFixture(mode)
			// Values recorded in the homogeneous 500Mbps/2paths/30ms
			// failure. The second sender can offer only 2.41MB per 176ms,
			// less than the 2.75MB needed to test the 50% rate boundary.
			fast.minRTT, limited.minRTT = 30*time.Millisecond, 30*time.Millisecond
			fast.rtt, limited.rtt = 176*time.Millisecond, 176*time.Millisecond
			fast.goodput, limited.goodput = 31_194_430, 13_389_141
			fast.budget, limited.budget = 6<<20, 2_410_504
			for i := range fast.capacitySamples {
				fast.capacitySamples[i] = fast.goodput
				limited.capacitySamples[i] = limited.goodput
			}
			now := time.Now()
			for i := 0; i < 4; i++ {
				limited.deliverySamples++
				s.schedulerReceiptLocked(limited, nil, now.Add(time.Duration(i)*time.Second))
			}
			if limited.scheduler.role != RoleActive {
				t.Fatal("self-imposed flight limit was misclassified as physical capacity", limited.scheduler.role)
			}
			// Once the SAME low-rate measurements have enough offered-flight
			// headroom, the original 50% threshold and 3-epoch demotion apply.
			limited.budget = 4 << 20
			for i := 0; i < 3; i++ {
				limited.deliverySamples++
				s.schedulerReceiptLocked(limited, nil, now.Add(time.Duration(10+i)*time.Second))
			}
			if limited.scheduler.role != RoleProbe {
				t.Fatal("sufficient-load low-rate evidence did not demote the path")
			}
		})
	}
}

func TestSchedulerRateEvidenceDoesNotDisableRTTProtection(t *testing.T) {
	s, fast, slow := modeFixture(SchedulerAuto)
	fast.minRTT = 30 * time.Millisecond
	slow.minRTT, slow.rtt = 200*time.Millisecond, 250*time.Millisecond
	slow.goodput, slow.budget = 625000, schedulerLearningDebt
	for i := range slow.capacitySamples {
		slow.capacitySamples[i] = slow.goodput
	}
	now := time.Now()
	for i := 0; i < 3; i++ {
		slow.deliverySamples++
		s.schedulerReceiptLocked(slow, nil, now.Add(time.Duration(i)*time.Second))
	}
	if slow.scheduler.role != RoleBackup || s.scheduler.effective != SchedulerProtect {
		t.Fatal("insufficient bandwidth evidence disabled independent RTT safety")
	}
}
