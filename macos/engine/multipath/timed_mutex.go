package multipath

import (
	"sync"
	"sync/atomic"
	"time"
)

type mutexStats struct {
	Locks     uint64
	WaitNS    uint64
	WaitMaxNS uint64
	HoldNS    uint64
	HoldMaxNS uint64
}

// timedMutex preserves sync.Mutex semantics while making Session contention
// observable in production. heldAt is written only by the current owner after
// the underlying mutex has been acquired and read before it is released.
type timedMutex struct {
	mu        sync.Mutex
	heldAt    time.Time
	locks     atomic.Uint64
	waitNS    atomic.Uint64
	waitMaxNS atomic.Uint64
	holdNS    atomic.Uint64
	holdMaxNS atomic.Uint64
}

func atomicMax(dst *atomic.Uint64, v uint64) {
	for {
		old := dst.Load()
		if v <= old || dst.CompareAndSwap(old, v) {
			return
		}
	}
}

func (m *timedMutex) Lock() {
	start := time.Now()
	m.mu.Lock()
	wait := uint64(time.Since(start))
	m.locks.Add(1)
	m.waitNS.Add(wait)
	atomicMax(&m.waitMaxNS, wait)
	m.heldAt = time.Now()
}

func (m *timedMutex) Unlock() {
	hold := uint64(time.Since(m.heldAt))
	m.holdNS.Add(hold)
	atomicMax(&m.holdMaxNS, hold)
	m.heldAt = time.Time{}
	m.mu.Unlock()
}

func (m *timedMutex) snapshot() mutexStats {
	return mutexStats{
		Locks:     m.locks.Load(),
		WaitNS:    m.waitNS.Load(),
		WaitMaxNS: m.waitMaxNS.Load(),
		HoldNS:    m.holdNS.Load(),
		HoldMaxNS: m.holdMaxNS.Load(),
	}
}
