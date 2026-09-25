// Package multipath implements the experimental MPX/3 application transport.
// It never creates a native MPTCP socket and treats backend bytes as opaque.
package multipath

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	Version             = "0.9.4"
	CapabilityRevision  = 5
	MaxPayload          = 32768
	StreamWindow        = 16 << 10 // bootstrap target; never implicit sending credit
	MaxStreams          = 2048
	MaxPending          = 8192
	MaxDataPendingBytes = 128 << 20 // sender-side queued DATA; independent of receive-page RAM
	MaxBuffered         = 128 << 20 // physical receive-page allocation hard cap
	frameHeader         = 40
	helloSize           = 48
	handshakeTimeout    = 5 * time.Second
)

const (
	kindOpen byte = iota + 1
	kindOpenOK
	kindData
	kindACK
	kindWindow
	kindFIN
	kindRST
	kindPing
	kindPong
	kindSessionWindow
	kindStopReceiving
	kindResetStream
	kindOpenReject
	kindCreditProbe
	kindFinalConsumed
)

var (
	ErrProtocol       = errors.New("MPX/3 protocol violation")
	ErrAuthentication = errors.New("MPX/3 authentication failed")
	ErrSessionExpired = errors.New("Landing session missing or expired; restart the client")
	ErrResourceLimit  = errors.New("MPX/3 resource limit")
	ErrNoPaths        = errors.New("all MPX/3 carriers unavailable")
)

type sessionID [16]byte

type frame struct {
	kind               byte
	stream, offset, id uint64
	data               []byte
}

// ParseKey refuses human passwords. Call NewKey during installation.
func ParseKey(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, errors.New("transport_key must be 64 hexadecimal characters (32 random bytes)")
	}
	return b, nil
}

func NewKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func mac(key []byte, label string, parts ...[]byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(label))
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func aeadFor(key []byte) (cipher.AEAD, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(c)
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) != 0 {
		n, err := w.Write(b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// One reader and one writer may use secureConn concurrently. There must be
// exactly one writer: GCM counters and TCP record boundaries are directional.
type secureConn struct {
	net.Conn
	reader               *bufio.Reader
	send, receive        cipher.AEAD
	txCounter, rxCounter uint64
	configuredRateBPS    float64
}

func newSecure(c net.Conn, key, transcript []byte, client bool) (*secureConn, error) {
	tx, rx := "mpx3/tcp/client", "mpx3/tcp/server"
	if !client {
		tx, rx = rx, tx
	}
	a, err := aeadFor(mac(key, tx, transcript))
	if err != nil {
		return nil, err
	}
	b, err := aeadFor(mac(key, rx, transcript))
	if err != nil {
		return nil, err
	}
	return &secureConn{Conn: c, reader: bufio.NewReaderSize(c, 64<<10), send: a, receive: b}, nil
}

func nonce(counter uint64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[4:], counter)
	return n
}

const carrierBatchBytes = 256 << 10
const carrierBatchFrames = 64

func (c *secureConn) writeFrame(f frame) error { return c.writeFrames([]frame{f}) }

// Coalesce already-ready records only: no batching timer delays small requests.
// The byte/frame caps bound temporary storage independently of the ledger.
func (c *secureConn) writeFrames(frames []frame) error {
	if len(frames) > carrierBatchFrames {
		return ErrResourceLimit
	}
	total := 0
	for _, f := range frames {
		if len(f.data) > MaxPayload || f.kind < kindOpen || f.kind > kindFinalConsumed {
			return ErrProtocol
		}
		if (f.kind == kindResetStream && len(f.data) != 8) || (f.kind != kindData && f.kind != kindResetStream && len(f.data) != 0) {
			return ErrProtocol
		}
		total += frameHeader + len(f.data) + 16
	}
	if total > carrierBatchBytes {
		return ErrResourceLimit
	}
	out := make([]byte, 0, total)
	for _, f := range frames {
		if c.txCounter == ^uint64(0) {
			return ErrProtocol
		}
		start := len(out)
		out = append(out, make([]byte, frameHeader)...)
		h := out[start : start+frameHeader]
		copy(h, "MPT3")
		h[4] = 3
		h[5] = f.kind
		binary.BigEndian.PutUint64(h[8:16], f.stream)
		binary.BigEndian.PutUint64(h[16:24], f.offset)
		binary.BigEndian.PutUint64(h[24:32], f.id)
		binary.BigEndian.PutUint32(h[32:36], uint32(len(f.data)))
		c.txCounter++
		out = c.send.Seal(out, nonce(c.txCounter), f.data, h)
	}
	return writeAll(c.Conn, out)
}

func (c *secureConn) readFrame() (frame, error) {
	var f frame
	h := make([]byte, frameHeader)
	if _, err := io.ReadFull(c.reader, h); err != nil {
		return f, err
	}
	if string(h[:4]) != "MPT3" || h[4] != 3 || h[5] < kindOpen || h[5] > kindFinalConsumed || h[6] != 0 || h[7] != 0 || binary.BigEndian.Uint32(h[36:]) != 0 {
		return f, ErrProtocol
	}
	n := binary.BigEndian.Uint32(h[32:36])
	if n > MaxPayload || c.rxCounter == ^uint64(0) {
		return f, ErrProtocol
	}
	encrypted := make([]byte, int(n)+c.receive.Overhead())
	if _, err := io.ReadFull(c.reader, encrypted); err != nil {
		return f, err
	}
	c.rxCounter++
	plain, err := c.receive.Open(encrypted[:0], nonce(c.rxCounter), encrypted, h)
	if err != nil {
		return f, ErrAuthentication
	}
	f = frame{kind: h[5], stream: binary.BigEndian.Uint64(h[8:16]), offset: binary.BigEndian.Uint64(h[16:24]), id: binary.BigEndian.Uint64(h[24:32]), data: plain}
	if (f.kind == kindResetStream && len(f.data) != 8) || (f.kind != kindData && f.kind != kindResetStream && len(f.data) != 0) {
		return frame{}, ErrProtocol
	}
	return f, nil
}

func clientHandshake(c net.Conn, key []byte, sid sessionID, carrier byte, create bool) (*secureConn, error) {
	return clientHandshakeMode(c, key, sid, carrier, create, SchedulerAuto)
}

func clientHandshakeMode(c net.Conn, key []byte, sid sessionID, carrier byte, create bool, mode SchedulerMode) (*secureConn, error) {
	return clientHandshakePolicy(c, key, sid, carrier, create, mode, PathCapacity{})
}

func clientHandshakePolicy(c net.Conn, key []byte, sid sessionID, carrier byte, create bool, mode SchedulerMode, capacity PathCapacity) (*secureConn, error) {
	if carrier < 1 || carrier > 8 || len(key) != 32 || schedulerWire(mode) == 0 {
		return nil, ErrProtocol
	}
	if mode == SchedulerWeighted {
		if err := capacity.validateWeighted(); err != nil {
			return nil, err
		}
	}
	if err := c.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return nil, err
	}
	h := make([]byte, helloSize)
	copy(h, "MPX3")
	h[4] = 3
	h[6] = carrier
	h[7] = schedulerWire(mode) // authenticated capability revision and session policy
	if create {
		h[5] = 1
	}
	copy(h[8:24], sid[:])
	if _, err := rand.Read(h[24:40]); err != nil {
		return nil, err
	}
	if mode == SchedulerWeighted {
		down, _ := capacityUnits(capacity.DownloadMbps, true)
		up, _ := capacityUnits(capacity.UploadMbps, false)
		binary.BigEndian.PutUint16(h[40:42], down)
		binary.BigEndian.PutUint16(h[42:44], up)
	} else {
		binary.BigEndian.PutUint32(h[40:44], 0) // legacy modes retain Rev4 hello bytes
	}
	binary.BigEndian.PutUint32(h[44:48], MaxPayload)
	if err := writeAll(c, h); err != nil {
		return nil, err
	}
	challenge := make([]byte, 32)
	if _, err := io.ReadFull(c, challenge); err != nil {
		return nil, err
	}
	transcript := append(h, challenge...)
	if err := writeAll(c, mac(key, "mpx3/client-proof", transcript)); err != nil {
		return nil, err
	}
	response := make([]byte, 33)
	if _, err := io.ReadFull(c, response); err != nil {
		return nil, err
	}
	if !hmac.Equal(response[1:], mac(key, "mpx3/server-proof", transcript, response[:1])) {
		return nil, ErrAuthentication
	}
	switch response[0] {
	case 0:
	case 2:
		return nil, ErrSessionExpired
	case 3:
		return nil, &ResourceLimitError{Reason: "session_capacity_or_conflict"}
	case 4:
		return nil, ErrSchedulerMismatch
	default:
		return nil, ErrAuthentication
	}
	if err := c.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	sc, err := newSecure(c, key, transcript, true)
	if err != nil {
		return nil, err
	}
	sc.configuredRateBPS = capacity.txRateBPS(false)
	return sc, nil
}

// Authentication precedes session allocation and backend access. The caller
// chooses a status while holding its session registry lock, then calls finish.
type incomingHandshake struct {
	scheduler  SchedulerMode
	conn       net.Conn
	id         sessionID
	carrier    byte
	create     bool
	transcript []byte
	capacity   PathCapacity
}

func readHandshake(c net.Conn, key []byte) (incomingHandshake, error) {
	var out incomingHandshake
	// A silent or partial hello cannot occupy admission capacity for five seconds.
	if err := c.SetDeadline(time.Now().Add(1500 * time.Millisecond)); err != nil {
		return out, err
	}
	h := make([]byte, helloSize)
	if _, err := io.ReadFull(c, h); err != nil {
		return out, err
	}
	if string(h[:4]) != "MPX3" || h[4] != 3 || h[5] > 1 || h[6] < 1 || h[6] > 8 || binary.BigEndian.Uint32(h[44:]) != MaxPayload {
		return out, ErrProtocol
	}
	mode, modeErr := schedulerFromWire(h[7])
	if modeErr != nil {
		return out, modeErr
	}
	capacity := PathCapacity{}
	if mode == SchedulerWeighted {
		down := binary.BigEndian.Uint16(h[40:42])
		up := binary.BigEndian.Uint16(h[42:44])
		if down == 0 {
			return out, ErrProtocol
		}
		capacity = PathCapacity{DownloadMbps: capacityFromUnits(down), UploadMbps: capacityFromUnits(up)}
	} else if binary.BigEndian.Uint32(h[40:44]) != 0 {
		return out, ErrProtocol
	}
	if err := c.SetDeadline(time.Now().Add(3500 * time.Millisecond)); err != nil {
		return out, err
	}
	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		return out, err
	}
	if err := writeAll(c, challenge); err != nil {
		return out, err
	}
	proof := make([]byte, 32)
	if _, err := io.ReadFull(c, proof); err != nil {
		return out, err
	}
	transcript := append(h, challenge...)
	if !hmac.Equal(proof, mac(key, "mpx3/client-proof", transcript)) {
		return out, ErrAuthentication
	}
	out = incomingHandshake{scheduler: mode, conn: c, carrier: h[6], create: h[5] == 1, transcript: transcript, capacity: capacity}
	copy(out.id[:], h[8:24])
	if out.id == (sessionID{}) {
		return incomingHandshake{}, ErrProtocol
	}
	return out, nil
}

func (h incomingHandshake) finish(key []byte, status byte) (*secureConn, error) {
	response := []byte{status}
	response = append(response, mac(key, "mpx3/server-proof", h.transcript, response)...)
	if err := writeAll(h.conn, response); err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, fmt.Errorf("MPX handshake status %d", status)
	}
	if err := h.conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	sc, err := newSecure(h.conn, key, h.transcript, false)
	if err != nil {
		return nil, err
	}
	sc.configuredRateBPS = h.capacity.txRateBPS(true)
	return sc, nil
}

// PlainDial and PlainListen override GODEBUG/system MPTCP defaults explicitly.
func PlainDial(ctx context.Context, address string) (net.Conn, error) {
	d := net.Dialer{Timeout: 4 * time.Second, KeepAlive: 15 * time.Second}
	d.SetMultipathTCP(false)
	c, err := d.DialContext(ctx, "tcp", address)
	if t, ok := c.(*net.TCPConn); ok {
		_ = t.SetNoDelay(true)
	}
	return c, err
}

func PlainListen(ctx context.Context, address string) (net.Listener, error) {
	var lc net.ListenConfig
	lc.SetMultipathTCP(false)
	return lc.Listen(ctx, "tcp", address)
}
