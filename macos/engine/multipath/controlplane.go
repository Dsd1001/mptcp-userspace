package multipath

import "time"

const (
	controlCarrierQueue = 64
	controlPathBudget   = 16 << 10
	controlBurstLimit   = 16
)

// Control packets do not borrow DATA's pending or carrier-flight budget.
func (s *Session) dispatchControlsLocked() {
	for count := 0; count < 128 && s.controlReady.Len() > 0; count++ {
		p := s.controlReady.Front().Value.(*outbound)
		var best *carrier
		for _, c := range s.paths {
			if !c.active || len(c.reliableControl) >= controlCarrierQueue || c.controlOutstanding+p.cost > controlPathBudget {
				continue
			}
			if best == nil || c.controlOutstanding < best.controlOutstanding || (c.controlOutstanding == best.controlOutstanding && c.id < best.id) {
				best = c
			}
		}
		if best == nil {
			return
		}
		s.unreadyLocked(p)
		p.path = best
		p.generation++
		p.sentAt = time.Time{}
		best.controlOutstanding += p.cost
		best.controlQueued += p.cost
		select {
		case best.reliableControl <- sendTask{p: p, generation: p.generation}:
		default:
			best.controlOutstanding -= p.cost
			best.controlQueued -= p.cost
			p.path = nil
			s.readyLocked(p, true)
			return
		}
	}
}

func (c *carrier) releaseFlight(p *outbound) {
	if p.f.kind == kindData {
		c.outstanding = max(0, c.outstanding-p.cost)
	} else {
		c.controlOutstanding = max(0, c.controlOutstanding-p.cost)
	}
}
func (c *carrier) releaseQueued(p *outbound) {
	if p.f.kind == kindData {
		c.queued = max(0, c.queued-p.cost)
	} else {
		c.controlQueued = max(0, c.controlQueued-p.cost)
	}
}

// One writer owns GCM counters. Alternate the two control sources, and offer
// ready DATA a turn after at most 16 controls. This bounds mutual starvation
// without creating another payload copy or waiting to assemble a batch.
func (s *Session) writeCarrier(c *carrier) {
	streak, turn := 0, 0
	next := func(block bool) (frame, sendTask, bool, bool) {
		if streak >= controlBurstLimit {
			select {
			case task := <-c.queue:
				streak = 0
				return frame{}, task, true, true
			default:
			}
		}
		turn++
		if turn%2 == 0 {
			select {
			case task := <-c.reliableControl:
				streak++
				return frame{}, task, true, true
			default:
			}
		}
		select {
		case f := <-c.control:
			streak++
			return f, sendTask{}, false, true
		default:
		}
		select {
		case task := <-c.reliableControl:
			streak++
			return frame{}, task, true, true
		default:
		}
		select {
		case task := <-c.queue:
			streak = 0
			return frame{}, task, true, true
		default:
		}
		if !block {
			return frame{}, sendTask{}, false, false
		}
		select {
		case <-s.ctx.Done():
			return frame{}, sendTask{}, false, false
		case <-c.done:
			return frame{}, sendTask{}, false, false
		case f := <-c.control:
			streak++
			return f, sendTask{}, false, true
		case task := <-c.reliableControl:
			streak++
			return frame{}, task, true, true
		case task := <-c.queue:
			streak = 0
			return frame{}, task, true, true
		}
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-c.done:
			return
		default:
		}
		f, task, reliable, ok := next(true)
		if !ok {
			return
		}
		frames := make([]frame, 0, 16)
		wire, payload := 0, 0
		for {
			valid := true
			extra := frame{}
			hasExtra := false
			if reliable {
				s.mu.Lock()
				p := task.p
				valid = c.active && s.pending[p.f.id] == p && p.path == c && p.generation == task.generation
				if valid {
					p.sentAt = time.Now()
					// Before releasing the queue reservation, equality means
					// there is no previously sent DATA awaiting an MPX ACK.
					// Do not carry the preceding idle interval into stale tests.
					if p.f.kind == kindData && c.outstanding == c.queued {
						c.scheduler.flightStartedAt = p.sentAt
					}
					c.releaseQueued(p)
					p.attempts++
					f = p.f
					// A distinct, authenticated WINDOW follows OPEN on this
					// carrier, so it cannot be lost merely by overtaking the
					// receiver's identity creation. No grant is implicit in
					// OPEN and no credit availability check can block it.
					if f.kind == kindOpen && !s.server {
						if st := s.streams[f.stream]; st != nil && !st.closed {
							st.grantCreditLocked(st.rxRead + StreamWindow)
							extra = frame{kind: kindWindow, stream: st.id, offset: st.rxRead, id: st.rxLimit}
							hasExtra = true
						}
					}
				}
				select {
				case s.kick <- struct{}{}:
				default:
				}
				s.mu.Unlock()
			}
			if valid {
				frames = append(frames, f)
				wire += frameHeader + 16 + len(f.data)
				if f.kind == kindData {
					payload += len(f.data)
				}
				if hasExtra {
					frames = append(frames, extra)
					wire += frameHeader + 16
				}
			}
			if len(frames) >= carrierBatchFrames-1 || wire+MaxPayload+frameHeader+16 > carrierBatchBytes {
				break
			}
			f, task, reliable, ok = next(false)
			if !ok {
				break
			}
		}
		if len(frames) == 0 {
			continue
		}
		if err := c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			s.carrierFailure(c, err)
			return
		}
		if err := c.conn.writeFrames(frames); err != nil {
			s.carrierFailure(c, err)
			return
		}
		s.mu.Lock()
		c.sent += uint64(payload)
		s.mu.Unlock()
	}
}
