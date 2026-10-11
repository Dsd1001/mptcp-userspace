package multipath

import (
	"testing"
	"time"
)

func TestWO01B1ClientFirstRequestUsesConfiguredUploadFastPath(t *testing.T) {
	s, c := wo01B1Session()
	s.server = false
	s.streams[1] = &Stream{s: s, id: 1}
	for range carrierQueue {
		c.queue <- sendTask{}
	}
	p := s.queueLocked(frame{kind: kindData, stream: 1, offset: 0, data: []byte{1}})
	if p == nil {
		t.Fatal("client request did not enqueue")
	}
	s.dispatchLocked(time.Now())
	if len(c.priorityDATA) != 1 || !p.firstDataTokenCharged || s.firstDataPromoted != 1 {
		t.Fatalf("client first request failed bounded bypass: queued=%d charged=%v promoted=%d", len(c.priorityDATA), p.firstDataTokenCharged, s.firstDataPromoted)
	}
	if len(c.queue) != carrierQueue {
		t.Fatal("client fast request modified normal Bulk queue")
	}
}

func TestWO01B1ClientAutoUploadUsesLegacyFairPath(t *testing.T) {
	s, c := wo01B1Session()
	s.server = false
	c.configuredRateBPS = 0 // upload intentionally omitted by configuration
	s.streams[1] = &Stream{s: s, id: 1}
	p := s.queueLocked(frame{kind: kindData, stream: 1, data: []byte{1}})
	if p == nil {
		t.Fatal("auto-upload DATA queue failed")
	}
	s.dispatchLocked(time.Now())
	if len(c.priorityDATA) != 0 || len(c.queue) != 1 || p.firstDataTokenCharged || s.firstDataPromoted != 0 {
		t.Fatalf("client optional upload budget should use normal DATA: prio=%d fair=%d charged=%v", len(c.priorityDATA), len(c.queue), p.firstDataTokenCharged)
	}
}

func TestWO01B1Client150HerdNeverGetsFastPath(t *testing.T) {
	s, c := wo01B1Session()
	s.server = false
	for i := 0; i < 150; i++ {
		id := uint64(1 + 2*i)
		s.streams[id] = &Stream{s: s, id: id}
	}
	p := s.queueLocked(frame{kind: kindData, stream: 1, data: []byte{1}})
	if p == nil {
		t.Fatal("queue failed")
	}
	s.dispatchLocked(time.Now())
	if len(c.priorityDATA) != 0 || s.firstDataPromoted != 0 || !s.weightedHerdGuard {
		t.Fatalf("150 client OPEN storm escaped herd guard: prio=%d promoted=%d guard=%v", len(c.priorityDATA), s.firstDataPromoted, s.weightedHerdGuard)
	}
}
