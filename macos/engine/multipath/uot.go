package multipath

// UoT is a product payload carried inside reliable MPX Streams. Its two-byte
// big-endian lengths preserve opaque datagrams, including empty datagrams.
import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

const (
	uotQueuePackets = 64
	uotQueueBytes   = 8 << 20
	uotWriteTimeout = 10 * time.Second
)

func readUOTDatagram(r io.Reader, buffer []byte) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(header[:]))
	if n > udpMax || n > len(buffer) {
		return nil, errors.New("invalid UoT datagram length")
	}
	if _, err := io.ReadFull(r, buffer[:n]); err != nil {
		return nil, err
	}
	return buffer[:n], nil
}

func writeUOTDatagram(w io.Writer, packet []byte) error {
	if len(packet) > udpMax {
		return errors.New("UoT datagram exceeds UDP payload limit")
	}
	// Keep the two-byte length and the common small UDP payload in one Stream
	// Write. Separate writes may be scheduled on different Carriers and create
	// avoidable reassembly HOL before the payload can be delivered.
	framed := make([]byte, 2+len(packet))
	binary.BigEndian.PutUint16(framed[:2], uint16(len(packet)))
	copy(framed[2:], packet)
	return writeAll(w, framed)
}

type clientUOTFlow struct {
	address netip.AddrPort
	ctx     context.Context
	cancel  context.CancelFunc
	packets chan []byte
	last    atomic.Int64
}

// UOTClient forwards local UDP associations over the parent Session's TCP
// Carriers. A failed association discards pending datagrams; none is replayed
// into a replacement Stream or Session.
type UOTClient struct {
	sentBytes, receivedBytes uint64
	externalInput            bool
	routeID                  uint32
	maxFlows                 int
	session                  *Session
	ctx                      context.Context
	cancel                   context.CancelFunc
	listener                 *net.UDPConn
	mu                       sync.Mutex
	flows                    map[netip.AddrPort]*clientUOTFlow
	queuedBytes              int
	sent, received, dropped  uint64
	err                      error
	wg                       sync.WaitGroup
	stopOnce                 sync.Once
	done                     chan struct{}
}

func StartClientUOT(s *Session, listen string) (*UOTClient, error) {
	s.mu.Lock()
	if s.udpStarted || s.closed {
		s.mu.Unlock()
		return nil, errors.New("UDP/UoT already started or session closed; create a new session")
	}
	s.udpStarted = true
	s.mu.Unlock()
	probe, err := s.openProductService(s.ctx, productServiceProbe)
	if err != nil {
		return nil, fmt.Errorf("UoT capability check: %w", err)
	}
	probe.Close()
	listener, err := listenUDP(listen)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	u := &UOTClient{session: s, ctx: ctx, cancel: cancel, listener: listener,
		flows: make(map[netip.AddrPort]*clientUOTFlow), done: make(chan struct{})}
	u.wg.Add(2)
	go u.readLocal()
	go u.monitor()
	go func() { u.wg.Wait(); close(u.done) }()
	return u, nil
}

func (u *UOTClient) Addr() net.Addr        { return u.listener.LocalAddr() }
func (u *UOTClient) Done() <-chan struct{} { return u.done }
func (u *UOTClient) Err() error            { u.mu.Lock(); defer u.mu.Unlock(); return u.err }
func (u *UOTClient) stop() {
	u.stopOnce.Do(func() {
		u.cancel()
		if !u.externalInput {
			u.listener.Close()
		}
	})
}
func (u *UOTClient) Close() error { u.stop(); <-u.done; return nil }
func (u *UOTClient) Snapshot() UDPStats {
	paths := u.session.Snapshot().Paths
	u.mu.Lock()
	defer u.mu.Unlock()
	// Carrier bytes belong to the shared TCP/UoT Session and are intentionally
	// absent here. These counters count only complete UoT application payloads.
	return UDPStats{Paths: paths, Connections: len(u.flows), Sent: u.sent, Received: u.received, Dropped: u.dropped}
}

func (u *UOTClient) readLocal() {
	defer u.wg.Done()
	buffer := make([]byte, 65536)
	for {
		n, address, err := u.listener.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if u.ctx.Err() == nil {
				u.mu.Lock()
				u.err = err
				u.mu.Unlock()
			}
			u.stop()
			return
		}
		u.enqueue(address, buffer[:n])
	}
}

func (u *UOTClient) enqueue(address netip.AddrPort, packet []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	// Charge framing too, so a stream of empty datagrams is also bounded.
	cost := len(packet) + 2
	if u.ctx.Err() != nil || len(packet) > udpMax || cost > uotQueueBytes-u.queuedBytes {
		u.dropped++
		return
	}
	flow := u.flows[address]
	if flow == nil {
		if len(u.flows) >= u.flowLimit() {
			u.dropped++
			return
		}
		if u.routeID != 0 {
			u.session.mu.Lock()
			if u.session.closed || u.session.uotClientFlows >= MaxStreams/2 {
				u.session.mu.Unlock()
				u.dropped++
				return
			}
			u.session.uotClientFlows++
			u.session.mu.Unlock()
		}
		ctx, cancel := context.WithCancel(u.ctx)
		flow = &clientUOTFlow{address: address, ctx: ctx, cancel: cancel, packets: make(chan []byte, uotQueuePackets)}
		u.flows[address] = flow
		flow.last.Store(time.Now().UnixNano())
		u.wg.Add(1)
		go u.runFlow(flow)
	}
	if flow.ctx.Err() != nil || len(flow.packets) >= uotQueuePackets {
		u.dropped++
		return
	}
	if !u.reserveSessionQueueLocked(cost) {
		u.dropped++
		return
	}
	packet = append([]byte(nil), packet...)
	u.queuedBytes += cost
	flow.last.Store(time.Now().UnixNano())
	flow.packets <- packet
}

func (u *UOTClient) runFlow(flow *clientUOTFlow) {
	defer u.wg.Done()
	defer func() {
		flow.cancel()
		u.mu.Lock()
		defer u.mu.Unlock()
		delete(u.flows, flow.address)
		if u.routeID != 0 {
			u.session.mu.Lock()
			u.session.uotClientFlows--
			u.session.mu.Unlock()
		}
		for {
			select {
			case packet := <-flow.packets:
				u.queuedBytes -= len(packet) + 2
				u.releaseSessionQueueLocked(len(packet) + 2)
				u.dropped++
			default:
				return
			}
		}
	}()
	var st *Stream
	var err error
	if u.routeID != 0 {
		st, err = u.session.OpenRoutedUOT(flow.ctx, u.routeID)
	} else {
		st, err = u.session.openProductService(flow.ctx, productServiceUOT)
	}
	if err != nil {
		return
	}
	defer st.Close()
	stop := context.AfterFunc(flow.ctx, func() { st.Close() })
	defer stop()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer flow.cancel()
		buffer := make([]byte, udpMax)
		for {
			packet, err := readUOTDatagram(st, buffer)
			if err != nil {
				return
			}
			flow.last.Store(time.Now().UnixNano())
			// A short local socket deadline prevents a blocked consumer from
			// retaining this association indefinitely.
			u.listener.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
			_, err = u.listener.WriteToUDPAddrPort(packet, flow.address)
			u.mu.Lock()
			if err != nil {
				u.dropped++
			} else {
				u.received++
				u.receivedBytes += uint64(len(packet))
			}
			u.mu.Unlock()
		}
	}()
	defer func() { flow.cancel(); st.Close(); <-readerDone }()
	for {
		select {
		case <-flow.ctx.Done():
			return
		case packet := <-flow.packets:
			st.SetWriteDeadline(time.Now().Add(uotWriteTimeout))
			err = writeUOTDatagram(st, packet)
			u.mu.Lock()
			u.queuedBytes -= len(packet) + 2
			u.releaseSessionQueueLocked(len(packet) + 2)
			if err != nil {
				u.dropped++
			} else {
				u.sent++
				u.sentBytes += uint64(len(packet))
			}
			u.mu.Unlock()
			if err != nil {
				return
			}
			flow.last.Store(time.Now().UnixNano())
		}
	}
}

func (u *UOTClient) expire(now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, flow := range u.flows {
		if now.Sub(time.Unix(0, flow.last.Load())) >= udpIdle {
			flow.cancel()
		}
	}
}
func (u *UOTClient) monitor() {
	defer u.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-u.ctx.Done():
			u.stop()
			return
		case now := <-ticker.C:
			u.expire(now)
		}
	}
}

// serveUOTBackend is invoked only after the authenticated product service
// negotiation succeeds. It owns one connected fixed-destination UDP socket;
// application payloads can never choose arbitrary Landing destinations.
func serveUOTBackend(parent context.Context, st *Stream, conn *net.UDPConn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { st.Close(); conn.Close() })
	defer stop()
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer cancel()
		buffer := make([]byte, 65536)
		for {
			n, err := conn.Read(buffer)
			if err != nil {
				return
			}
			if n > udpMax {
				continue
			}
			st.SetWriteDeadline(time.Now().Add(uotWriteTimeout))
			if err = writeUOTDatagram(st, buffer[:n]); err != nil {
				return
			}
			last.Store(time.Now().UnixNano())
		}
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		buffer := make([]byte, udpMax)
		for {
			packet, err := readUOTDatagram(st, buffer)
			if err != nil {
				return
			}
			if err = udpWrite(conn, packet, nil); err != nil {
				return
			}
			last.Store(time.Now().UnixNano())
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer func() { cancel(); st.Close(); conn.Close(); wg.Wait() }()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if now.Sub(time.Unix(0, last.Load())) >= udpIdle {
				return
			}
		}
	}
}

// StartRoutedClientUOT attaches one UDP inlet to an existing shared Session.
// It does not open native UDP Relay paths or another MPX Session. The caller
// transfers ownership of an already-bound socket, allowing atomic inlet bind.
func StartRoutedClientUOT(s *Session, listener *net.UDPConn, routeID uint32, maxFlows int) (*UOTClient, error) {
	return startRoutedClientUOT(s, listener, routeID, maxFlows, false)
}

// AttachRoutedClientUOT leaves the bound socket owned by the inlet runtime.
// That runtime keeps reading during reconnection, dropping rather than
// replaying stale datagrams into a replacement Session.
func AttachRoutedClientUOT(s *Session, listener *net.UDPConn, routeID uint32, maxFlows int) (*UOTClient, error) {
	return startRoutedClientUOT(s, listener, routeID, maxFlows, true)
}
func startRoutedClientUOT(s *Session, listener *net.UDPConn, routeID uint32, maxFlows int, external bool) (*UOTClient, error) {
	if listener == nil {
		return nil, errors.New("missing UDP inlet")
	}
	s.mu.Lock()
	allowed := !s.closed && !s.server && s.productMux && s.routeGroups[routeID] != nil && routeID != 0
	s.mu.Unlock()
	if !allowed {
		return nil, ErrRouteNotFound
	}
	if maxFlows <= 0 {
		maxFlows = 512
	}
	if maxFlows > MaxStreams {
		return nil, errors.New("UoT flow limit exceeds Session stream limit")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	u := &UOTClient{externalInput: external, session: s, ctx: ctx, cancel: cancel, listener: listener, routeID: routeID, maxFlows: maxFlows,
		flows: make(map[netip.AddrPort]*clientUOTFlow), done: make(chan struct{})}
	u.wg.Add(1)
	if !external {
		u.wg.Add(1)
		go u.readLocal()
	}
	go u.monitor()
	go func() { u.wg.Wait(); close(u.done) }()
	return u, nil
}
func (u *UOTClient) flowLimit() int {
	if u.maxFlows > 0 {
		return u.maxFlows
	}
	return udpFlowsMax
}

func (u *UOTClient) ReceiveDatagram(address netip.AddrPort, packet []byte) {
	u.enqueue(address, packet)
}

// The route client already holds u.mu; no Session path acquires u.mu while
// holding Session.mu. Frame bytes (including empty datagrams) count once.
func (u *UOTClient) reserveSessionQueueLocked(cost int) bool {
	if u.routeID == 0 {
		return true
	}
	if u.queuedBytes+cost > 2<<20 {
		return false
	}
	s := u.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.uotQueuedBytes+cost > uotQueueBytes {
		return false
	}
	s.uotQueuedBytes += cost
	return true
}
func (u *UOTClient) releaseSessionQueueLocked(cost int) {
	if u.routeID == 0 {
		return
	}
	s := u.session
	s.mu.Lock()
	s.uotQueuedBytes -= cost
	s.mu.Unlock()
}

// PayloadSnapshot avoids recursively scanning the entire Session for each
// inlet. Existing Snapshot() keeps legacy datagram counters for core tests.
func (u *UOTClient) PayloadSnapshot() (connections int, sent, received, dropped uint64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.flows), u.sentBytes, u.receivedBytes, u.dropped
}
