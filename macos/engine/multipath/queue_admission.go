package multipath

import (
	"os"
	"strconv"
	"strings"
)

const (
	// Admission governs only DATA not yet assigned to a Carrier. DATA which
	// was assigned remains in the reliable pending ledger until confirmed.
	queueAdmissionBootstrapReserve = 4 << 20
	queueAdmissionWakeBatch        = 32
)

// Queue-aware admission is endpoint-local, not an MPX/4 wire feature. 0 (also
// unset) enables 32 MiB in v1.1.2. Set 0 to restore v1.1.1 admission.
func queueAdmissionLimitFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("MPX_QUEUE_ADMISSION_MIB"))
	if raw == "" {
		return 32 << 20
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > 64 {
		return 0
	}
	return value << 20
}

// Reserve some capacity for the first payload of newly opened Streams.
// Both bands remain bounded by the local limit + 4 MiB.
func (s *Session) queueAdmissionCeilingLocked(st *Stream) int {
	if s.queueAdmissionLimit <= 0 {
		return 0
	}
	reserve := min(queueAdmissionBootstrapReserve, s.queueAdmissionLimit/4)
	if st != nil && st.txNext-st.peerConsumed < StreamWindow {
		return s.queueAdmissionLimit + reserve
	}
	return s.queueAdmissionLimit - reserve
}

func (s *Session) queueAdmissionRoomLocked(st *Stream) int {
	if s.queueAdmissionLimit <= 0 {
		return MaxPayload
	}
	// Account for the per-frame pending cost in the same units as readyDataBytes.
	return max(0, s.queueAdmissionCeilingLocked(st)-s.readyDataBytes-64)
}

// Called after successful dispatch or removal of unscheduled DATA. Notify
// only writers which can now pass admission, without a global broadcast.
func (s *Session) signalQueueAdmissionLocked(freedBytes int) {
	if s.queueAdmissionWaiters == 0 || freedBytes <= 0 {
		return
	}
	permits := min(queueAdmissionWakeBatch, writerPermits(freedBytes))
	signaled := 0
	for e := s.writerReady.Front(); e != nil && signaled < permits; e = e.Next() {
		st, _ := e.Value.(*Stream)
		if st == nil || !st.writeWaiting || st.writeWaitReason != waitQueueAdmission {
			continue
		}
		if s.queueAdmissionRoomLocked(st) < min(MaxPayload, st.writeRemaining) {
			continue
		}
		if s.signalStreamWriterLocked(st) {
			signaled++
		}
	}
}
