package multipath

import (
	"fmt"
	"sync"
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

func TestConcurrentCarrierDataSameStreamPublishesConsistently(t *testing.T) {
	s, c1 := rev2Fixture()
	c2 := schedulerPath(2)
	s.paths[2] = c2
	st := rev2Stream(s, 1)

	const frames = 32
	errs := make(chan error, frames)
	var wg sync.WaitGroup
	for i := 0; i < frames; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := c1
			if i%2 != 0 {
				c = c2
			}
			data := make([]byte, MaxPayload)
			for j := range data {
				data[j] = byte(i)
			}
			errs <- s.handleDataFrame(c, frame{
				kind:   kindData,
				stream: st.id,
				id:     uint64(i + 1),
				offset: uint64(i * MaxPayload),
				data:   data,
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	want := frames * MaxPayload
	s.mu.Lock()
	if got := int(st.rxContiguous); got != want {
		s.mu.Unlock()
		t.Fatalf("rxContiguous=%d want=%d", got, want)
	}
	if st.buffered != want || s.bufferedBytes != want {
		gotStream, gotSession := st.buffered, s.bufferedBytes
		s.mu.Unlock()
		t.Fatalf("buffered stream/session=%d/%d want=%d", gotStream, gotSession, want)
	}
	if got := int(s.receiveCredit); got != want {
		s.mu.Unlock()
		t.Fatalf("receiveCredit=%d want=%d", got, want)
	}
	if s.peerProcessedThrough != frames {
		got := s.peerProcessedThrough
		s.mu.Unlock()
		t.Fatalf("peerProcessedThrough=%d want=%d", got, frames)
	}
	s.mu.Unlock()

	buf := make([]byte, want)
	n, err := st.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("Read=%d want=%d", n, want)
	}
	for i := 0; i < frames; i++ {
		for _, got := range buf[i*MaxPayload : (i+1)*MaxPayload] {
			if got != byte(i) {
				t.Fatalf("frame %d data mismatch: got=%d", i, got)
			}
		}
	}

	s.mu.Lock()
	if st.buffered != 0 || s.bufferedBytes != 0 || s.receiveAllocated != 0 {
		t.Fatalf("receive storage leaked: stream=%d session=%d allocated=%d", st.buffered, s.bufferedBytes, s.receiveAllocated)
	}
	s.mu.Unlock()
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
