package multipath

import (
	"bytes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	udpHeader           = 56
	udpWireMax          = 1200
	udpFragment         = udpWireMax - udpHeader - 16
	udpMax              = 65507
	udpFlowsMax         = 512
	udpAssemblyMax      = 128
	udpAssemblyBytesMax = 4 << 20
	udpReplaySize       = 4096
	udpIdle             = 60 * time.Second
	udpAssemblyTTL      = 2 * time.Second
	udpHealthy          = 3 * time.Second
)
const (
	udpData byte = iota + 1
	udpPing
	udpPong
	udpReceipt
)

// A fixed sliding replay window tolerates reordering without an unbounded map.
// check never advances state for unauthenticated or malformed packets.
type replayWindow struct {
	highest uint64
	slots   [udpReplaySize]uint64
}

func (r *replayWindow) check(n uint64) bool {
	return n != 0 && !(r.highest >= udpReplaySize && n <= r.highest-udpReplaySize) && r.slots[n%udpReplaySize] != n
}
func (r *replayWindow) commit(n uint64) {
	if n > r.highest {
		r.highest = n
	}
	r.slots[n%udpReplaySize] = n
}

type udpFrame struct {
	kind, path        byte
	sid               sessionID
	flow, id, counter uint64
	index, count      uint16
	total             uint32
	data              []byte
}

type udpAssemblyKey struct{ flow, id uint64 }
type udpAssembly struct {
	at       time.Time
	total    uint32
	count    uint16
	bits     uint64
	data     []byte
	received int
}
type udpReceiptPending struct {
	at   time.Time
	path byte
	flow uint64
	size int
}

type udpPath struct {
	id                            byte
	address                       string
	send                          func([]byte) error
	lastSeen, lastProbe, sampleAt time.Time
	probeID                       uint64
	rtt                           time.Duration
	goodput                       float64
	ackBytes                      uint64
	outstanding                   int
	sent, received, errors        uint64
	lastError                     string
}

type UDPStats struct {
	Paths         int         `json:"paths"`
	Connections   int         `json:"connections"`
	Sent          uint64      `json:"sent"`
	Received      uint64      `json:"received"`
	Dropped       uint64      `json:"dropped"`
	AssemblyBytes int         `json:"assembly_bytes"`
	Assemblies    int         `json:"assemblies"`
	PathStats     []PathStats `json:"path_stats"`
}

// One peer must live for the entire TCP session. Recreating it with the same
// session ID would reset directional GCM counters, and is therefore forbidden.
type udpPeer struct {
	mu                      sync.Mutex
	sid                     sessionID
	tx, rx                  cipher.AEAD
	counter, nextID         uint64
	replay, completed       replayWindow
	paths                   map[byte]*udpPath
	assemblies              map[udpAssemblyKey]*udpAssembly
	pending                 map[uint64]udpReceiptPending
	assemblyBytes           int
	sent, received, dropped uint64
}

func newUDPPeer(key []byte, id sessionID, client bool) (*udpPeer, error) {
	tx, rx := "mpx1/udp/client", "mpx1/udp/server"
	if !client {
		tx, rx = rx, tx
	}
	a, err := aeadFor(mac(key, tx, id[:]))
	if err != nil {
		return nil, err
	}
	b, err := aeadFor(mac(key, rx, id[:]))
	if err != nil {
		return nil, err
	}
	return &udpPeer{sid: id, tx: a, rx: b, paths: make(map[byte]*udpPath), assemblies: make(map[udpAssemblyKey]*udpAssembly), pending: make(map[uint64]udpReceiptPending)}, nil
}

func (p *udpPeer) pathLocked(id byte) *udpPath {
	path := p.paths[id]
	if path == nil {
		path = &udpPath{id: id, rtt: 50 * time.Millisecond, goodput: 1 << 20, sampleAt: time.Now()}
		p.paths[id] = path
	}
	return path
}

func (p *udpPeer) setPath(id byte, address string, send func([]byte) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	path := p.pathLocked(id)
	path.address = address
	path.send = send
}

func (p *udpPeer) sealLocked(f udpFrame) ([]byte, error) {
	if p.counter == ^uint64(0) || f.path < 1 || f.path > 8 || len(f.data) > udpFragment {
		return nil, ErrResourceLimit
	}
	p.counter++
	f.counter = p.counter
	h := make([]byte, udpHeader, udpHeader+len(f.data)+16)
	copy(h, "MPU1")
	h[4] = 1
	h[5] = f.kind
	h[6] = f.path
	copy(h[8:24], p.sid[:])
	binary.BigEndian.PutUint64(h[24:32], f.flow)
	binary.BigEndian.PutUint64(h[32:40], f.id)
	binary.BigEndian.PutUint16(h[40:42], f.index)
	binary.BigEndian.PutUint16(h[42:44], f.count)
	binary.BigEndian.PutUint32(h[44:48], f.total)
	binary.BigEndian.PutUint64(h[48:56], f.counter)
	return p.tx.Seal(h, nonce(f.counter), f.data, h), nil
}

func parseUDPHeader(b []byte) (udpFrame, error) {
	var f udpFrame
	if len(b) < udpHeader+16 || len(b) > udpWireMax || string(b[:4]) != "MPU1" || b[4] != 1 || b[5] < udpData || b[5] > udpReceipt || b[6] < 1 || b[6] > 8 || b[7] != 0 {
		return f, ErrProtocol
	}
	f = udpFrame{kind: b[5], path: b[6], flow: binary.BigEndian.Uint64(b[24:32]), id: binary.BigEndian.Uint64(b[32:40]), index: binary.BigEndian.Uint16(b[40:42]), count: binary.BigEndian.Uint16(b[42:44]), total: binary.BigEndian.Uint32(b[44:48]), counter: binary.BigEndian.Uint64(b[48:56])}
	copy(f.sid[:], b[8:24])
	return f, nil
}

func (p *udpPeer) writeLocked(path *udpPath, f udpFrame) error {
	if path == nil || path.send == nil {
		return ErrNoPaths
	}
	f.path = path.id
	packet, err := p.sealLocked(f)
	if err != nil {
		return err
	}
	if err = path.send(packet); err != nil {
		path.errors++
		path.lastError = err.Error()
		return err
	}
	path.sent += uint64(len(f.data))
	return nil
}

func (p *udpPeer) send(flow uint64, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if flow == 0 || len(data) > udpMax || p.nextID == ^uint64(0) {
		p.dropped++
		return ErrProtocol
	}
	now := time.Now()
	var chosen *udpPath
	score := 1e30
	for _, path := range p.paths {
		if path.send == nil || now.Sub(path.lastSeen) > udpHealthy || path.outstanding+len(data) > 1<<20 {
			continue
		}
		predicted := path.rtt.Seconds()/2 + float64(path.outstanding+len(data)+udpWireMax)/max(65536, path.goodput)
		if chosen == nil || predicted < score || (predicted == score && path.id < chosen.id) {
			chosen = path
			score = predicted
		}
	}
	if chosen == nil || len(p.pending) >= udpReplaySize {
		p.dropped++
		return ErrNoPaths
	}
	p.nextID++
	id := p.nextID
	count := max(1, (len(data)+udpFragment-1)/udpFragment)
	for i := 0; i < count; i++ {
		start := i * udpFragment
		end := min(len(data), start+udpFragment)
		f := udpFrame{kind: udpData, flow: flow, id: id, index: uint16(i), count: uint16(count), total: uint32(len(data)), data: data[start:end]}
		if err := p.writeLocked(chosen, f); err != nil {
			p.dropped++
			return err
		}
	}
	chosen.outstanding += len(data)
	p.pending[id] = udpReceiptPending{at: now, path: chosen.id, flow: flow, size: len(data)}
	p.sent += uint64(len(data))
	return nil
}

// receive authenticates before learning a NAT source or allocating reassembly
// memory. A receipt confirms datagram reconstruction, not backend delivery.
func (p *udpPeer) receive(packet []byte, expectedPath byte, address string, send func([]byte) error) (udpFrame, bool, error) {
	f, err := parseUDPHeader(packet)
	if err != nil {
		return f, false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if f.sid != p.sid || (expectedPath != 0 && f.path != expectedPath) || !p.replay.check(f.counter) {
		p.dropped++
		return f, false, ErrProtocol
	}
	data, err := p.rx.Open(nil, nonce(f.counter), packet[udpHeader:], packet[:udpHeader])
	if err != nil {
		p.dropped++
		return f, false, ErrAuthentication
	}
	if f.kind == udpData {
		count := max(1, (int(f.total)+udpFragment-1)/udpFragment)
		if f.flow == 0 || f.id == 0 || f.total > udpMax || int(f.count) != count || f.index >= f.count || count > 64 {
			return f, false, ErrProtocol
		}
		expected := min(udpFragment, int(f.total)-int(f.index)*udpFragment)
		if len(data) != expected {
			return f, false, ErrProtocol
		}
	} else {
		if len(data) != 0 || f.index != 0 || f.count != 0 || f.total != 0 {
			return f, false, ErrProtocol
		}
		if (f.kind == udpPing || f.kind == udpPong) && f.flow != 0 {
			return f, false, ErrProtocol
		}
		if f.kind == udpReceipt && (f.flow == 0 || f.id == 0) {
			return f, false, ErrProtocol
		}
	}
	p.replay.commit(f.counter)
	path := p.pathLocked(f.path)
	now := time.Now()
	path.lastSeen = now
	path.received += uint64(len(data))
	if send != nil {
		path.send = send
		path.address = address
	}
	switch f.kind {
	case udpPing:
		return f, false, p.writeLocked(path, udpFrame{kind: udpPong, id: f.id})
	case udpPong:
		if f.id == path.probeID && !path.lastProbe.IsZero() {
			sample := now.Sub(path.lastProbe)
			path.rtt = time.Duration(.8*float64(path.rtt) + .2*float64(sample))
		}
		return f, false, nil
	case udpReceipt:
		pending, ok := p.pending[f.id]
		if ok && pending.flow == f.flow {
			delete(p.pending, f.id)
			origin := p.paths[pending.path]
			origin.outstanding = max(0, origin.outstanding-pending.size)
			origin.ackBytes += uint64(pending.size)
			if elapsed := now.Sub(origin.sampleAt); elapsed >= 100*time.Millisecond {
				origin.goodput = max(65536, .7*origin.goodput+.3*float64(origin.ackBytes)/elapsed.Seconds())
				origin.sampleAt = now
				origin.ackBytes = 0
			}
		}
		return f, false, nil
	}
	if !p.completed.check(f.id) {
		p.dropped++
		return f, false, ErrProtocol
	}
	key := udpAssemblyKey{f.flow, f.id}
	a := p.assemblies[key]
	if a == nil {
		if len(p.assemblies) >= udpAssemblyMax || p.assemblyBytes+int(f.total) > udpAssemblyBytesMax {
			p.dropped++
			return f, false, ErrResourceLimit
		}
		a = &udpAssembly{at: now, total: f.total, count: f.count, data: make([]byte, int(f.total))}
		p.assemblies[key] = a
		p.assemblyBytes += int(f.total)
	}
	if a.total != f.total || a.count != f.count {
		return f, false, ErrProtocol
	}
	start := int(f.index) * udpFragment
	bit := uint64(1) << f.index
	if a.bits&bit != 0 {
		if !bytes.Equal(a.data[start:start+len(data)], data) {
			return f, false, ErrProtocol
		}
		return f, false, nil
	}
	copy(a.data[start:], data)
	a.bits |= bit
	a.received++
	if a.received != int(a.count) {
		return f, false, nil
	}
	delete(p.assemblies, key)
	p.assemblyBytes -= int(a.total)
	p.completed.commit(f.id)
	f.data = a.data
	p.received += uint64(len(a.data))
	_ = p.writeLocked(path, udpFrame{kind: udpReceipt, flow: f.flow, id: f.id})
	return f, true, nil
}

func (p *udpPeer) tick(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, a := range p.assemblies {
		if now.Sub(a.at) >= udpAssemblyTTL {
			delete(p.assemblies, key)
			p.assemblyBytes -= int(a.total)
			p.dropped++
		}
	}
	for id, sent := range p.pending {
		if now.Sub(sent.at) >= udpHealthy {
			delete(p.pending, id)
			if path := p.paths[sent.path]; path != nil {
				path.outstanding = max(0, path.outstanding-sent.size)
				path.errors++
				path.goodput = max(65536, path.goodput*.8)
				path.lastError = "UDP receipt timeout (datagram not retransmitted)"
			}
			p.dropped++
		}
	}
	for _, path := range p.paths {
		if path.send != nil && now.Sub(path.lastProbe) >= time.Second {
			path.lastProbe = now
			path.probeID = uint64(now.UnixNano())
			_ = p.writeLocked(path, udpFrame{kind: udpPing, id: path.probeID})
		}
	}
}

func (p *udpPeer) noteError(id byte, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	path := p.pathLocked(id)
	path.errors++
	path.lastError = err.Error()
}
func (p *udpPeer) drop() { p.mu.Lock(); p.dropped++; p.mu.Unlock() }
func (p *udpPeer) snapshot() UDPStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	out := UDPStats{Sent: p.sent, Received: p.received, Dropped: p.dropped, AssemblyBytes: p.assemblyBytes, Assemblies: len(p.assemblies)}
	for _, path := range p.paths {
		healthy := path.send != nil && now.Sub(path.lastSeen) <= udpHealthy
		if healthy {
			out.Paths++
		}
		out.PathStats = append(out.PathStats, PathStats{ID: int(path.id), Address: path.address, Connected: healthy, Sent: path.sent, Received: path.received, RTTMS: float64(path.rtt) / float64(time.Millisecond), GoodputBPS: path.goodput, Outstanding: path.outstanding, Errors: path.errors, LastError: path.lastError})
	}
	sort.Slice(out.PathStats, func(i, j int) bool { return out.PathStats[i].ID < out.PathStats[j].ID })
	return out
}

func validateUDPAddress(address string) error {
	if address == "" {
		return errors.New("missing UDP address")
	}
	return nil
}

func udpReadError(err error) error { return fmt.Errorf("UDP carrier: %w", err) }
