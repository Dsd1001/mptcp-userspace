package multipath

// Receiver-side resource isolation for routed tunnels. Protocol WINDOWs remain
// monotonic and authoritative. These are local allocation limits, not another
// sender credit ledger. As with the existing physical-page limit, already
// authorized DATA that cannot fit is terminated at Stream scope, never by
// shrinking a WINDOW or closing an otherwise healthy Session.
//
// Count actual committed offsets (including holes) and actual page allocation.
// Do NOT reserve every historical WINDOW entitlement: idle connections would
// pin the shared pool indefinitely and recreate the old credit starvation bug.
const (
	routeReceiveLimit         = 64 << 20
	routeSetupReceiveLimit    = 4 << 20
	routeSetupWindow          = 16 << 10
	routeInitialReceiveWindow = 64 << 10
	LimitRouteReceive         = "route_receive_pressure"
)

func (s *Session) initRouteReceiveLocked() {
	n := max(1, len(s.routeGroups)-1)
	for id, r := range s.routeGroups {
		r.receiveFloor = min(1<<20, (32<<20)/n)
		r.receiveLimit = routeReceiveLimit
		if id == 0 {
			r.receiveFloor = routeSetupReceiveLimit
			r.receiveLimit = routeSetupReceiveLimit
		}
		s.routeLogicalProtected += r.receiveFloor
		s.routePhysicalProtected += r.receiveFloor
	}
}

func (s *Session) adjustRouteReceiveLocked(r *routeGroup, logical, physical int) {
	if r == nil {
		return
	}
	oldLogical := max(0, r.receiveFloor-r.receiveUsed)
	oldPhysical := max(0, r.receiveFloor-r.receiveAllocated)
	r.receiveUsed += logical
	r.receiveAllocated += physical
	s.routeLogicalProtected += max(0, r.receiveFloor-r.receiveUsed) - oldLogical
	s.routePhysicalProtected += max(0, r.receiveFloor-r.receiveAllocated) - oldPhysical
}

func (st *Stream) routeReceiveAccountLocked(logical, physical int) {
	s := st.s
	if s.routeGroups != nil {
		s.adjustRouteReceiveLocked(s.routeGroups[st.routeID], logical, physical)
	}
}

func (s *Session) routeLogicalRoomLocked(r *routeGroup) int {
	otherReserve := s.routeLogicalProtected - max(0, r.receiveFloor-r.receiveUsed)
	return min(r.receiveLimit-r.receiveUsed, SessionCreditLimit-s.receiveCredit-otherReserve)
}

func (s *Session) routePhysicalRoomLocked(r *routeGroup) int {
	otherReserve := s.routePhysicalProtected - max(0, r.receiveFloor-r.receiveAllocated)
	return min(r.receiveLimit-r.receiveAllocated, MaxBuffered-s.receiveAllocated-otherReserve)
}

// Called after Core has charged valid offsets, but before allocating payload
// pages. The caller's existing resource-limit path sends directional RESET/STOP
// and discards the charged range exactly once. ACK is not consumption.
func (st *Stream) checkRouteReceiveLocked(newPages int) error {
	s := st.s
	if s.routeGroups == nil {
		return nil
	}
	r := s.routeGroups[st.routeID]
	if r == nil {
		return ErrRouteNotFound
	}
	if s.routeLogicalRoomLocked(r) < 0 || newPages*receivePageCost > s.routePhysicalRoomLocked(r) {
		r.receivePressureResets++
		return s.resourceLocked(LimitRouteReceive, false)
	}
	return nil
}

func (s *Session) moveRouteReceiveLocked(st *Stream, id uint32) error {
	old, next := s.routeGroups[st.routeID], s.routeGroups[id]
	if old == nil || next == nil {
		return ErrRouteNotFound
	}
	logical := int(st.rxHigh - st.rxRead)
	physical := len(st.pages) * receivePageCost
	if logical > s.routeLogicalRoomLocked(next) || physical > s.routePhysicalRoomLocked(next) {
		return ErrRouteBusy
	}
	s.adjustRouteReceiveLocked(old, -logical, -physical)
	s.adjustRouteReceiveLocked(next, logical, physical)
	return nil
}

// Unclassified pre-opens receive only enough room to select a service. Once
// bound, retain Core's 16 MiB entitlement (not allocated memory). Reducing this
// to a consumption-driven tiny window can deadlock existing write-then-read
// echo protocols. Actual buffers remain bounded independently by the route
// guards, and pressure stops future growth without retracting a prior grant.
func (st *Stream) routeWindowTargetLocked() int {
	s := st.s
	if s.routeGroups == nil {
		return MaxStreamWindow
	}
	if st.hasFIN || st.receiveStopped || st.closed {
		return 0
	}
	if st.routeID == 0 {
		return routeSetupWindow
	}
	r := s.routeGroups[st.routeID]
	if r == nil {
		return 0
	}
	room := min(s.routeLogicalRoomLocked(r), s.routePhysicalRoomLocked(r)*MaxPayload/receivePageCost)
	if room < MaxPayload {
		r.receiveWindowDeferrals++
		return 0
	}
	return min(MaxStreamWindow, room)
}
