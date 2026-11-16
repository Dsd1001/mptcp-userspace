package multipath

import (
	"sort"
	"time"
)

const (
	hotStreamSnapshotLimit  = 8
	hotStreamActivityWindow = 15 * time.Second
)

// StreamFlowStats gives bounded, payload-free diagnostics of recently active
// sending Streams. All members are sampled while Session.mu is held. Wait
// totals are cumulative and can be differenced across status snapshots.
type StreamFlowStats struct {
	ID                 uint64                `json:"stream_id"`
	RouteID            uint32                `json:"route_id"`
	SentBytes          uint64                `json:"tx_committed_bytes"`
	PeerConsumed       uint64                `json:"peer_consumed_bytes"`
	PeerLimit          uint64                `json:"peer_stream_limit_bytes"`
	StreamRoom         uint64                `json:"stream_send_room_bytes"`
	SessionRoom        uint64                `json:"session_send_room_bytes"`
	Unconsumed         uint64                `json:"tx_unconsumed_bytes"`
	ReadyFrames        int                   `json:"ready_data_frames"`
	QueueAdmissionRoom int                   `json:"queue_admission_room_bytes"`
	WriteWaiting       bool                  `json:"write_waiting"`
	WaitReason         string                `json:"write_wait_reason,omitempty"`
	CurrentWaitMS      float64               `json:"current_write_wait_ms,omitempty"`
	WriteWaits         map[string]creditWait `json:"write_wait_reasons,omitempty"`
}

func (s *Session) hotStreamSnapshotLocked(now time.Time) []StreamFlowStats {
	var result []StreamFlowStats
	sessionRoom := uint64(0)
	if s.credit.peerLimit > s.credit.txCommitted {
		sessionRoom = s.credit.peerLimit - s.credit.txCommitted
	}
	for _, st := range s.streams {
		if st.txNext < 64<<10 && !st.writeWaiting {
			continue
		}
		if !st.writeWaiting && now.Sub(st.lastActivity) > hotStreamActivityWindow {
			continue
		}
		peerRoom := uint64(0)
		if st.peerLimit > st.txNext {
			peerRoom = st.peerLimit - st.txNext
		}
		unconsumed := uint64(0)
		if st.txNext > st.peerConsumed {
			unconsumed = st.txNext - st.peerConsumed
		}
		ready := 0
		if rq := s.ready[st.id]; rq != nil {
			ready = rq.frames.Len()
		}
		item := StreamFlowStats{
			ID: st.id, RouteID: st.routeID, SentBytes: st.txNext,
			PeerConsumed: st.peerConsumed, PeerLimit: st.peerLimit,
			StreamRoom: peerRoom, SessionRoom: sessionRoom,
			Unconsumed: unconsumed, ReadyFrames: ready,
			QueueAdmissionRoom: s.queueAdmissionRoomLocked(st),
			WriteWaiting:       st.writeWaiting,
		}
		if st.writeWaiting && st.writeWaitReason > waitNone && st.writeWaitReason < len(creditWaitNames) {
			item.WaitReason = creditWaitNames[st.writeWaitReason]
			if !st.diagWriteWaitStarted.IsZero() && now.After(st.diagWriteWaitStarted) {
				item.CurrentWaitMS = float64(now.Sub(st.diagWriteWaitStarted)) / float64(time.Millisecond)
			}
		}
		for i := 1; i < len(st.diagWriteWaits); i++ {
			if st.diagWriteWaits[i].Count == 0 {
				continue
			}
			if item.WriteWaits == nil {
				item.WriteWaits = make(map[string]creditWait)
			}
			item.WriteWaits[creditWaitNames[i]] = st.diagWriteWaits[i]
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].WriteWaiting != result[j].WriteWaiting {
			return result[i].WriteWaiting
		}
		if result[i].Unconsumed != result[j].Unconsumed {
			return result[i].Unconsumed > result[j].Unconsumed
		}
		if result[i].SentBytes != result[j].SentBytes {
			return result[i].SentBytes > result[j].SentBytes
		}
		return result[i].ID < result[j].ID
	})
	if len(result) > hotStreamSnapshotLimit {
		result = result[:hotStreamSnapshotLimit]
	}
	return result
}
