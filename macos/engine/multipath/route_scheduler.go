package multipath

import (
	"container/list"
	"errors"
	"sort"
	"time"
)

// Each route owns a ring of references to the existing per-Stream ready queues.
// No DATA is copied and no second reliable ledger is introduced. Group 0 holds
// setup/probe traffic until a product Stream has selected an allowed route.
type routeGroup struct {
	receiveUsed, receiveAllocated, receiveFloor, receiveLimit int
	receivePressureResets, receiveWindowDeferrals             uint64
	id                                                        uint32
	weight                                                    int
	ready                                                     list.List
	firstReady                                                list.List // per-route first DATA
	entry                                                     *list.Element
	deficit                                                   int
	readyBytes                                                int
	dispatched                                                uint64
}

type RouteStats struct {
	ReceiveUsed            int    `json:"receive_unconsumed_bytes"`
	ReceiveAllocated       int    `json:"receive_allocated_bytes"`
	ReceiveLimit           int    `json:"receive_limit_bytes"`
	ReceivePressureResets  uint64 `json:"receive_pressure_resets"`
	ReceiveWindowDeferrals uint64 `json:"receive_window_deferrals"`
	ID                     uint32 `json:"route_id"`
	Weight                 int    `json:"weight"`
	Streams                int    `json:"streams"`
	ClosingStreams         int    `json:"closing_streams"`
	ReadyDataBytes         int    `json:"ready_data_bytes"`
	DispatchedPayloadBytes uint64 `json:"dispatched_payload_bytes"`
}

func ValidateRoutePolicies(policies []RoutePolicy) error {
	if len(policies) < 1 || len(policies) > MaxTCPRoutes {
		return errors.New("require 1..64 route policies")
	}
	seen := make(map[uint32]bool, len(policies))
	for _, p := range policies {
		if p.ID == 0 || seen[p.ID] {
			return errors.New("route policy IDs must be nonzero and unique")
		}
		seen[p.ID] = true
		if _, err := normalizedRouteWeight(p.Weight); err != nil {
			return err
		}
	}
	return nil
}

// ConfigureRoutePolicies must run before the first service DATA. The table is
// immutable for this Session; runtime configuration changes use a new Session.
func (s *Session) ConfigureRoutePolicies(policies []RoutePolicy) error {
	if err := ValidateRoutePolicies(policies); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.productMux || s.routeGroups != nil || s.dataPendingFrames != 0 || s.receiveCredit != 0 || s.receiveAllocated != 0 {
		return errors.New("route policies require a fresh product-service Session")
	}
	s.routeGroups = make(map[uint32]*routeGroup, len(policies)+1)
	s.routeGroups[0] = &routeGroup{id: 0, weight: 1}
	for _, p := range policies {
		weight, _ := normalizedRouteWeight(p.Weight)
		s.routeGroups[p.ID] = &routeGroup{id: p.ID, weight: weight}
	}
	s.initRouteReceiveLocked()
	return nil
}

func (s *Session) bindRouteLocked(st *Stream, id uint32) error {
	if st == nil || st.closed || s.closed || id == 0 || s.routeGroups[id] == nil {
		return ErrRouteNotFound
	}
	if st.routeID == id {
		return nil
	}
	if st.routeID != 0 || st.txNext != 0 || s.ready[st.id] != nil {
		return errors.New("a Stream route cannot be changed after sending DATA")
	}
	if err := s.moveRouteReceiveLocked(st, id); err != nil {
		return err
	}
	st.routeID = id
	st.advertiseCreditLocked(time.Now())
	return nil
}

func (s *Session) routeReadyLocked(rq *dataReadyQueue, cost int) {
	if s.routeGroups == nil {
		return
	}
	if rq.route == nil {
		id := uint32(0)
		if st := s.streamForCreditLocked(rq.stream); st != nil {
			id = st.routeID
		}
		rq.route = s.routeGroups[id]
		if rq.route == nil {
			rq.route = s.routeGroups[0]
		}
	}
	r := rq.route
	r.readyBytes += cost
	if rq.routeEntry == nil {
		rq.routeEntry = r.ready.PushBack(rq)
	}
	if r.entry == nil {
		r.deficit = 0
		r.entry = s.routeReady.PushBack(r)
		s.routeReadyWeight += r.weight
	}
}

func (s *Session) routeUnreadyLocked(rq *dataReadyQueue, cost int) {
	if rq.route == nil {
		return
	}
	r := rq.route
	r.readyBytes = max(0, r.readyBytes-cost)
	if rq.frames.Len() == 0 && rq.routeEntry != nil {
		r.ready.Remove(rq.routeEntry)
		rq.routeEntry = nil
	}
	if r.ready.Len() == 0 && r.entry != nil {
		s.routeReady.Remove(r.entry)
		r.entry = nil
		r.deficit = 0
		s.routeReadyWeight -= r.weight
	}
}

// Byte-based deficit round robin is work-conserving. It chooses the route and
// Stream only; the existing MPX scheduler still chooses among ALL Carriers.
func (s *Session) nextDataReadyLocked() *dataReadyQueue {
	if s.routeGroups == nil {
		if e := s.readyStreams.Front(); e != nil {
			return e.Value.(*dataReadyQueue)
		}
		return nil
	}
	for e := s.routeReady.Front(); e != nil; e = s.routeReady.Front() {
		r := e.Value.(*routeGroup)
		rq := r.ready.Front().Value.(*dataReadyQueue)
		cost := rq.frames.Front().Value.(*outbound).cost
		if r.deficit < cost {
			r.deficit += (MaxPayload + 64) * r.weight
			s.routeReady.MoveToBack(e)
			continue
		}
		return rq
	}
	return nil
}

func (s *Session) routeDispatchedLocked(rq *dataReadyQueue, p *outbound) {
	if rq.route == nil {
		return
	}
	r := rq.route
	// This counts scheduling attempts, including reinjection, not business goodput.
	r.dispatched += uint64(len(p.f.data))
	if r.entry != nil {
		r.deficit = max(0, r.deficit-p.cost)
	}
	if rq.routeEntry != nil {
		r.ready.MoveToBack(rq.routeEntry)
	}
}

// This is local unscheduled-queue protection, not a second flow-control pool.
// When other routes have queued DATA, a busy route cannot refill the whole
// queue. Previously admitted bytes are never revoked. Idle shares are usable.
func (s *Session) routeAdmissionRoomLocked(st *Stream) int {
	if s.routeGroups == nil || st == nil || s.queueAdmissionLimit <= 0 {
		return MaxDataPendingBytes
	}
	r := s.routeGroups[st.routeID]
	if r == nil {
		return 0
	}
	weight := s.routeReadyWeight
	if r.entry == nil {
		weight += r.weight
	}
	if weight <= r.weight {
		return MaxDataPendingBytes
	}
	reserve := min(queueAdmissionBootstrapReserve, s.queueAdmissionLimit/4)
	share := max(4*(MaxPayload+64), (s.queueAdmissionLimit-reserve)*r.weight/weight)
	if st.txNext-st.peerConsumed < StreamWindow {
		share += StreamWindow + 64
	}
	return max(0, share-r.readyBytes-64)
}

func (s *Session) routeSnapshotLocked() []RouteStats {
	if s.routeGroups == nil {
		return nil
	}
	counts, closing := make(map[uint32]int), make(map[uint32]int)
	for _, st := range s.streams {
		counts[st.routeID]++
	}
	for _, st := range s.closing {
		closing[st.routeID]++
	}
	out := make([]RouteStats, 0, len(s.routeGroups))
	for _, r := range s.routeGroups {
		out = append(out, RouteStats{ReceiveUsed: r.receiveUsed, ReceiveAllocated: r.receiveAllocated, ReceiveLimit: r.receiveLimit, ReceivePressureResets: r.receivePressureResets, ReceiveWindowDeferrals: r.receiveWindowDeferrals, ID: r.id, Weight: r.weight, Streams: counts[r.id], ClosingStreams: closing[r.id], ReadyDataBytes: r.readyBytes, DispatchedPayloadBytes: r.dispatched})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
