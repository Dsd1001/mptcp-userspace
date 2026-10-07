package multipath

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkReadyRingDispatch(b *testing.B) {
	for _, streams := range []int{1, 2, 8, 32, 128} {
		b.Run(fmt.Sprintf("streams_%d", streams), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				s := schedulerFixture()
				c := schedulerPath(1)
				c.budget = maxPathBudget
				c.goodput = 1 << 30
				s.paths[1] = c
				for j := 0; j < streams; j++ {
					id := uint64(2*j + 1)
					for k := 0; k < 4; k++ {
						if s.queueLocked(frame{kind: kindData, stream: id, data: make([]byte, MaxPayload)}) == nil {
							b.Fatal("queue DATA")
						}
					}
				}
				now := time.Now()
				for s.readyFrames != 0 {
					before := s.readyFrames
					s.dispatchLocked(now)
					for len(c.queue) > 0 {
						task := <-c.queue
						if task.p.path == c {
							c.releaseFlight(task.p)
						}
					}
					if s.readyFrames == before {
						break
					}
				}
			}
		})
	}
}

func BenchmarkBootstrapReserveO1(b *testing.B) {
	for _, writers := range []int{1, 8, 32, 128, 512} {
		b.Run(fmt.Sprintf("writers_%d", writers), func(b *testing.B) {
			s := schedulerFixture()
			for i := 0; i < writers; i++ {
				st := s.newStreamLocked(uint64(2*i + 1))
				st.writeEntry = s.writerReady.PushBack(st)
				s.setWriterRemainingLocked(st, MaxPayload)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.pendingBootstrapReserveLocked(nil)
			}
		})
	}
}

func TestScalabilityTelemetryTracksSessionAndDispatch(t *testing.T) {
	s := schedulerFixture()
	c := schedulerPath(1)
	c.budget = maxPathBudget
	c.goodput = 1 << 30
	s.paths[1] = c
	if s.queueLocked(frame{kind: kindData, stream: 1, data: make([]byte, MaxPayload)}) == nil {
		t.Fatal("queue DATA")
	}

	s.mu.Lock()
	s.dispatchLocked(time.Now())
	r := s.resourceSnapshotLocked()
	s.mu.Unlock()

	if r.SessionLockCount == 0 || r.DispatchRuns == 0 || r.DispatchFrames == 0 {
		t.Fatalf("missing scalability telemetry: %+v", r)
	}
	if r.ReadyStreams != 0 || s.readyFrames != 0 {
		t.Fatalf("dispatch left ready work: ready_streams=%d ready_frames=%d", r.ReadyStreams, s.readyFrames)
	}
}
