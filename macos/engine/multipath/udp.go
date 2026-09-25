package multipath

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

func tuneUDP(c *net.UDPConn) error {
	if err := c.SetReadBuffer(4 << 20); err != nil {
		return err
	}
	return c.SetWriteBuffer(1 << 20)
}
func listenUDP(address string) (*net.UDPConn, error) {
	a, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	c, err := net.ListenUDP("udp", a)
	if err != nil {
		return nil, err
	}
	if err = tuneUDP(c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func udpWrite(c *net.UDPConn, data []byte, address *net.UDPAddr) error {
	if err := c.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		return err
	}
	var err error
	if address == nil {
		_, err = c.Write(data)
	} else {
		_, err = c.WriteToUDP(data, address)
	}
	return err
}

type clientUDPFlow struct {
	id      uint64
	address netip.AddrPort
	last    time.Time
}

type UDPClient struct {
	ctx      context.Context
	cancel   context.CancelFunc
	peer     *udpPeer
	listener *net.UDPConn
	carriers []*net.UDPConn
	mu       sync.Mutex
	flows    map[netip.AddrPort]*clientUDPFlow
	byID     map[uint64]*clientUDPFlow
	nextFlow uint64
	wg       sync.WaitGroup
	stopOnce sync.Once
	done     chan struct{}
	err      error
}

// StartClientUDP is intentionally allowed only once per TCP Session, preventing
// reuse of directional encryption counters after a local UDP restart.
func StartClientUDP(s *Session, token, listen string, addresses []string) (*UDPClient, error) {
	if len(addresses) < 1 || len(addresses) > 8 {
		return nil, ErrProtocol
	}
	key, err := ParseKey(token)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.udpStarted || s.closed {
		s.mu.Unlock()
		return nil, errors.New("UDP already started or session closed; create a new TCP session")
	}
	s.udpStarted = true
	s.mu.Unlock()
	p, err := newUDPPeer(key, s.id, true)
	if err != nil {
		return nil, err
	}
	listener, err := listenUDP(listen)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	u := &UDPClient{ctx: ctx, cancel: cancel, peer: p, listener: listener, flows: make(map[netip.AddrPort]*clientUDPFlow), byID: make(map[uint64]*clientUDPFlow), done: make(chan struct{})}
	for i, address := range addresses {
		a, e := net.ResolveUDPAddr("udp", address)
		var c *net.UDPConn
		if e == nil {
			c, e = net.DialUDP("udp", nil, a)
		}
		if e == nil {
			e = tuneUDP(c)
		}
		if e != nil {
			if c != nil {
				c.Close()
			}
			u.stop()
			return nil, e
		}
		u.carriers = append(u.carriers, c)
		p.setPath(byte(i+1), address, func(data []byte) error { return udpWrite(c, data, nil) })
	}
	u.wg.Add(2 + len(u.carriers))
	go u.readLocal()
	for i, c := range u.carriers {
		go u.readCarrier(byte(i+1), c)
	}
	go u.monitor()
	go func() { u.wg.Wait(); close(u.done) }()
	return u, nil
}

func (u *UDPClient) Addr() net.Addr        { return u.listener.LocalAddr() }
func (u *UDPClient) Done() <-chan struct{} { return u.done }
func (u *UDPClient) Err() error            { u.mu.Lock(); defer u.mu.Unlock(); return u.err }
func (u *UDPClient) stop() {
	u.stopOnce.Do(func() {
		u.cancel()
		u.listener.Close()
		for _, c := range u.carriers {
			c.Close()
		}
	})
}
func (u *UDPClient) Close() error { u.stop(); <-u.done; return nil }

func (u *UDPClient) readLocal() {
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
		if n > udpMax {
			u.peer.drop()
			continue
		}
		now := time.Now()
		u.mu.Lock()
		flow := u.flows[address]
		if flow == nil && len(u.flows) < udpFlowsMax && u.nextFlow != ^uint64(0) {
			u.nextFlow++
			flow = &clientUDPFlow{id: u.nextFlow, address: address}
			u.flows[address] = flow
			u.byID[flow.id] = flow
		}
		var id uint64
		if flow != nil {
			flow.last = now
			id = flow.id
		}
		u.mu.Unlock()
		if id == 0 {
			u.peer.drop()
			continue
		}
		_ = u.peer.send(id, buffer[:n])
	}
}

func (u *UDPClient) readCarrier(id byte, c *net.UDPConn) {
	defer u.wg.Done()
	buffer := make([]byte, udpWireMax+1)
	for {
		n, err := c.Read(buffer)
		if err != nil {
			if u.ctx.Err() != nil {
				return
			}
			u.peer.noteError(id, udpReadError(err))
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-u.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		f, complete, err := u.peer.receive(buffer[:n], id, "", nil)
		if err != nil || !complete {
			continue
		}
		u.mu.Lock()
		flow := u.byID[f.flow]
		var address netip.AddrPort
		if flow != nil {
			flow.last = time.Now()
			address = flow.address
		}
		u.mu.Unlock()
		if !address.IsValid() {
			u.peer.drop()
			continue
		}
		if _, err = u.listener.WriteToUDPAddrPort(f.data, address); err != nil {
			u.peer.drop()
		}
	}
}

func (u *UDPClient) expire(now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for address, flow := range u.flows {
		if now.Sub(flow.last) >= udpIdle {
			delete(u.flows, address)
			delete(u.byID, flow.id)
		}
	}
}
func (u *UDPClient) monitor() {
	defer u.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	u.peer.tick(time.Now())
	for {
		select {
		case <-u.ctx.Done():
			u.stop()
			return
		case now := <-ticker.C:
			u.peer.tick(now)
			u.expire(now)
		}
	}
}
func (u *UDPClient) Snapshot() UDPStats {
	out := u.peer.snapshot()
	u.mu.Lock()
	out.Connections = len(u.flows)
	u.mu.Unlock()
	return out
}
func (u *UDPClient) WaitPaths(ctx context.Context, n int) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if u.Snapshot().Paths >= n {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-u.done:
			return net.ErrClosed
		case <-ticker.C:
		}
	}
}

type backendUDPFlow struct {
	conn *net.UDPConn
	last atomic.Int64
}
type serverUDPPeer struct {
	peer    *udpPeer
	session *Session
	flows   map[uint64]*backendUDPFlow
}

type UDPServer struct {
	ctx       context.Context
	cancel    context.CancelFunc
	server    *Server
	listener  *net.UDPConn
	backend   *net.UDPAddr
	mu        sync.Mutex
	peers     map[sessionID]*serverUDPPeer
	flowCount int
	wg        sync.WaitGroup
	stopOnce  sync.Once
	done      chan struct{}
	err       error
}

func StartUDPServer(srv *Server, listen, backend string) (*UDPServer, error) {
	srv.mu.Lock()
	if srv.udpStarted || srv.ctx.Err() != nil {
		srv.mu.Unlock()
		return nil, errors.New("UDP already started or server closed")
	}
	srv.udpStarted = true
	srv.mu.Unlock()
	a, err := net.ResolveUDPAddr("udp", backend)
	if err != nil {
		return nil, err
	}
	c, err := listenUDP(listen)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(srv.ctx)
	u := &UDPServer{ctx: ctx, cancel: cancel, server: srv, listener: c, backend: a, peers: make(map[sessionID]*serverUDPPeer), done: make(chan struct{})}
	u.wg.Add(2)
	go u.read()
	go u.monitor()
	go func() { u.wg.Wait(); close(u.done) }()
	return u, nil
}
func (u *UDPServer) Addr() net.Addr        { return u.listener.LocalAddr() }
func (u *UDPServer) Done() <-chan struct{} { return u.done }
func (u *UDPServer) Err() error            { u.mu.Lock(); defer u.mu.Unlock(); return u.err }
func (u *UDPServer) stop() {
	u.stopOnce.Do(func() {
		u.cancel()
		u.listener.Close()
		u.mu.Lock()
		for _, p := range u.peers {
			for _, flow := range p.flows {
				flow.conn.Close()
			}
		}
		u.mu.Unlock()
	})
}
func (u *UDPServer) Close() error { u.stop(); <-u.done; return nil }

func (u *UDPServer) getPeer(id sessionID) *serverUDPPeer {
	s := u.server.session(id)
	if s == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.ctx.Err() != nil {
		return nil
	}
	p := u.peers[id]
	if p == nil {
		peer, err := newUDPPeer(u.server.key, id, false)
		if err != nil {
			return nil
		}
		p = &serverUDPPeer{peer: peer, session: s, flows: make(map[uint64]*backendUDPFlow)}
		u.peers[id] = p
	}
	return p
}

func (u *UDPServer) read() {
	defer u.wg.Done()
	buffer := make([]byte, udpWireMax+1)
	for {
		n, address, err := u.listener.ReadFromUDP(buffer)
		if err != nil {
			if u.ctx.Err() == nil {
				u.mu.Lock()
				u.err = err
				u.mu.Unlock()
			}
			u.stop()
			return
		}
		header, err := parseUDPHeader(buffer[:n])
		if err != nil {
			continue
		}
		peer := u.getPeer(header.sid)
		if peer == nil {
			continue
		}
		f, complete, err := peer.peer.receive(buffer[:n], 0, address.String(), func(data []byte) error { return udpWrite(u.listener, data, address) })
		if err != nil || !complete {
			continue
		}
		flow := u.getFlow(peer, f.flow)
		if flow == nil {
			peer.peer.drop()
			continue
		}
		flow.last.Store(time.Now().UnixNano())
		if err = udpWrite(flow.conn, f.data, nil); err != nil {
			peer.peer.drop()
			u.removeFlow(peer, f.flow, flow)
		}
	}
}

func (u *UDPServer) getFlow(p *serverUDPPeer, id uint64) *backendUDPFlow {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.ctx.Err() != nil {
		return nil
	}
	flow := p.flows[id]
	if flow != nil {
		return flow
	}
	if u.flowCount >= udpFlowsMax {
		return nil
	}
	c, err := net.DialUDP("udp", nil, u.backend)
	if err != nil {
		return nil
	}
	if err = tuneUDP(c); err != nil {
		c.Close()
		return nil
	}
	flow = &backendUDPFlow{conn: c}
	flow.last.Store(time.Now().UnixNano())
	p.flows[id] = flow
	u.flowCount++
	u.wg.Add(1)
	go u.readBackend(p, id, flow)
	return flow
}

func (u *UDPServer) removeFlow(p *serverUDPPeer, id uint64, flow *backendUDPFlow) {
	u.mu.Lock()
	if p.flows[id] == flow {
		delete(p.flows, id)
		u.flowCount--
	}
	u.mu.Unlock()
	flow.conn.Close()
}
func (u *UDPServer) readBackend(p *serverUDPPeer, id uint64, flow *backendUDPFlow) {
	defer u.wg.Done()
	defer u.removeFlow(p, id, flow)
	buffer := make([]byte, 65536)
	for {
		n, err := flow.conn.Read(buffer)
		if err != nil {
			return
		}
		if n > udpMax {
			p.peer.drop()
			continue
		}
		flow.last.Store(time.Now().UnixNano())
		_ = p.peer.send(id, buffer[:n])
	}
}

func (u *UDPServer) expire(now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for id, p := range u.peers {
		closed := false
		select {
		case <-p.session.Done():
			closed = true
		default:
		}
		for flowID, flow := range p.flows {
			if closed || now.Sub(time.Unix(0, flow.last.Load())) >= udpIdle {
				delete(p.flows, flowID)
				u.flowCount--
				flow.conn.Close()
			}
		}
		if closed {
			delete(u.peers, id)
		}
	}
}
func (u *UDPServer) monitor() {
	defer u.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-u.ctx.Done():
			u.stop()
			return
		case now := <-ticker.C:
			u.expire(now)
			u.mu.Lock()
			peers := make([]*udpPeer, 0, len(u.peers))
			for _, p := range u.peers {
				peers = append(peers, p.peer)
			}
			u.mu.Unlock()
			for _, p := range peers {
				p.tick(now)
			}
		}
	}
}
func (u *UDPServer) Snapshots() []UDPStats {
	u.mu.Lock()
	peers := make([]*serverUDPPeer, 0, len(u.peers))
	counts := make([]int, 0, len(u.peers))
	for _, p := range u.peers {
		peers = append(peers, p)
		counts = append(counts, len(p.flows))
	}
	u.mu.Unlock()
	out := make([]UDPStats, 0, len(peers))
	for i, p := range peers {
		s := p.peer.snapshot()
		s.Connections = counts[i]
		out = append(out, s)
	}
	return out
}
