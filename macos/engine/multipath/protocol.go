// Package multipath implements the MPX/4 application transport.
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
	"sync"
	"time"
	"unicode/utf8"
)

const (
	Version             = "1.1.3"
	CapabilityRevision  = 8 // MPX/4 Protocol Version 4 Stable; RC8 product fast-start/UoT capability
	ProtocolRelease     = "protocol-v4.0.0"
	ProtocolReleaseSHA  = "44f587fd279ed2238b070dd68114c76822353f4d"
	MaxPayload          = 32768
	MaxRecordSize       = 65536
	StreamWindow        = 32 << 10
	MaxStreams          = 2048
	MaxCarriers         = 8 // local active-Carrier implementation limit; Carrier IDs use the full MPX VarInt space
	MaxPending          = 32768
	MaxDataPendingBytes = 1024 << 20
	MaxBuffered         = 128 << 20
	frameHeader         = 32
	helloSize           = 5
	preHandshakeTimeout = 1500 * time.Millisecond
	handshakeTimeout    = 5 * time.Second
	mpx4RecordLimit     = 1 << 24
	mpx4VarIntMax       = uint64(1<<62 - 1)
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
	kindTransmissionRetire
	kindCarrierClose
	kindSessionClose
)

const (
	mpx4HSClientInit         uint64 = 0x01
	mpx4HSServerInit         uint64 = 0x02
	mpx4HSClientFinished     uint64 = 0x03
	mpx4HSServerFinished     uint64 = 0x04
	mpx4HSVersionNegotiation uint64 = 0x05
	mpx4HSHandshakeReject    uint64 = 0x06

	mpx4ParamSessionID           uint64 = 0x01
	mpx4ParamSessionAction       uint64 = 0x02
	mpx4ParamCarrierID           uint64 = 0x03
	mpx4ParamCarrierGeneration   uint64 = 0x04
	mpx4ParamClientNonce         uint64 = 0x05
	mpx4ParamServerNonce         uint64 = 0x06
	mpx4ParamMaxFramePayload     uint64 = 0x07
	mpx4ParamMaxRecordSize       uint64 = 0x08
	mpx4ParamMaxStreams          uint64 = 0x09
	mpx4ParamMaxCarriers         uint64 = 0x0a
	mpx4ParamReceiveCapacityHint uint64 = 0x40

	mpx4FramePing               uint64 = 0x01
	mpx4FramePong               uint64 = 0x02
	mpx4FrameCarrierClose       uint64 = 0x03
	mpx4FrameSessionClose       uint64 = 0x04
	mpx4FrameStreamOpen         uint64 = 0x10
	mpx4FrameStreamOpenOK       uint64 = 0x11
	mpx4FrameStreamOpenReject   uint64 = 0x12
	mpx4FrameStreamData         uint64 = 0x13
	mpx4FrameTransmissionACK    uint64 = 0x14
	mpx4FrameStreamCredit       uint64 = 0x15
	mpx4FrameStreamFIN          uint64 = 0x16
	mpx4FrameResetStream        uint64 = 0x17
	mpx4FrameStopSending        uint64 = 0x18
	mpx4FrameStreamConsumed     uint64 = 0x19
	mpx4FrameTransmissionRetire uint64 = 0x1a
	mpx4FrameSessionCredit      uint64 = 0x20
	mpx4FrameCreditProbe        uint64 = 0x21
)

var (
	ErrProtocol       = errors.New("MPX/4 protocol violation")
	ErrAuthentication = errors.New("MPX/4 authentication failed")
	ErrSessionExpired = errors.New("Landing MPX/4 session missing or expired; restart the client")
	ErrResourceLimit  = errors.New("MPX/4 resource limit")
	ErrNoPaths        = errors.New("all MPX/4 carriers unavailable")
	errSkipFrame      = errors.New("skip MPX/4 extension frame")
)

type sessionID [16]byte

type frame struct {
	kind               byte
	stream, offset, id uint64
	data               []byte
}

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

// Kept for the independent MPU/1 UDP data plane.
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

func nonce(counter uint64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[4:], counter)
	return n
}

func appendV4VarInt(dst []byte, value uint64) ([]byte, error) {
	switch {
	case value <= 63:
		return append(dst, byte(value)), nil
	case value <= 16383:
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(value)|0x4000)
		return append(dst, b[:]...), nil
	case value <= 1073741823:
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(value)|0x80000000)
		return append(dst, b[:]...), nil
	case value <= mpx4VarIntMax:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], value|0xc000000000000000)
		return append(dst, b[:]...), nil
	default:
		return dst, ErrProtocol
	}
}

func readV4VarInt(src []byte) (uint64, int, error) {
	if len(src) == 0 {
		return 0, 0, ErrProtocol
	}
	n := 1 << (src[0] >> 6)
	if len(src) < n {
		return 0, 0, ErrProtocol
	}
	var v uint64
	switch n {
	case 1:
		v = uint64(src[0] & 0x3f)
	case 2:
		v = uint64(binary.BigEndian.Uint16(src[:2]) & 0x3fff)
		if v < 64 {
			return 0, 0, ErrProtocol
		}
	case 4:
		v = uint64(binary.BigEndian.Uint32(src[:4]) & 0x3fffffff)
		if v < 16384 {
			return 0, 0, ErrProtocol
		}
	case 8:
		v = binary.BigEndian.Uint64(src[:8]) & mpx4VarIntMax
		if v < 1073741824 {
			return 0, 0, ErrProtocol
		}
	default:
		return 0, 0, ErrProtocol
	}
	return v, n, nil
}

func readV4VarIntReader(r *bufio.Reader) (uint64, []byte, error) {
	first, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n := 1 << (first >> 6)
	raw := make([]byte, n)
	raw[0] = first
	if n > 1 {
		if _, err := io.ReadFull(r, raw[1:]); err != nil {
			return 0, nil, err
		}
	}
	v, consumed, err := readV4VarInt(raw)
	if err != nil || consumed != n {
		return 0, nil, ErrProtocol
	}
	return v, raw, nil
}

func appendField(dst []byte, fields ...uint64) ([]byte, error) {
	var err error
	for _, v := range fields {
		dst, err = appendV4VarInt(dst, v)
		if err != nil {
			return nil, err
		}
	}
	return dst, nil
}

func readFields(src []byte, count int) ([]uint64, int, error) {
	out := make([]uint64, count)
	off := 0
	for i := 0; i < count; i++ {
		v, n, err := readV4VarInt(src[off:])
		if err != nil {
			return nil, 0, err
		}
		out[i] = v
		off += n
	}
	return out, off, nil
}

func encodeV4Frame(f frame) ([]byte, error) {
	switch f.kind {
	case kindResetStream, kindRST:
		if len(f.data) != 8 {
			return nil, ErrProtocol
		}
	case kindData:
		// validated below
	case kindCarrierClose, kindSessionClose:
		if len(f.data) > 256 || !utf8.Valid(f.data) {
			return nil, ErrProtocol
		}
	default:
		if len(f.data) != 0 {
			return nil, ErrProtocol
		}
	}
	var typ uint64
	var body []byte
	var err error
	switch f.kind {
	case kindOpen:
		typ = mpx4FrameStreamOpen
		body, err = appendField(nil, f.stream, f.id)
	case kindOpenOK:
		typ = mpx4FrameStreamOpenOK
		body, err = appendField(nil, f.stream, f.id)
	case kindOpenReject:
		typ = mpx4FrameStreamOpenReject
		body, err = appendField(nil, f.stream, f.id, f.offset)
	case kindData:
		if len(f.data) == 0 || len(f.data) > MaxPayload {
			return nil, ErrProtocol
		}
		typ = mpx4FrameStreamData
		body, err = appendField(nil, f.stream, f.offset, f.id)
		body = append(body, f.data...)
	case kindACK:
		typ = mpx4FrameTransmissionACK
		body, err = appendField(nil, f.stream, f.id, f.offset)
	case kindWindow:
		typ = mpx4FrameStreamCredit
		body, err = appendField(nil, f.stream, f.offset, f.id)
	case kindFIN:
		typ = mpx4FrameStreamFIN
		body, err = appendField(nil, f.stream, f.id, f.offset)
	case kindResetStream, kindRST:
		typ = mpx4FrameResetStream
		code := uint64(1)
		if len(f.data) == 8 {
			code = binary.BigEndian.Uint64(f.data)
		}
		body, err = appendField(nil, f.stream, f.id, f.offset, code)
	case kindStopReceiving:
		typ = mpx4FrameStopSending
		body, err = appendField(nil, f.stream, f.id, f.offset)
	case kindFinalConsumed:
		typ = mpx4FrameStreamConsumed
		body, err = appendField(nil, f.stream, f.id, f.offset)
	case kindTransmissionRetire:
		typ = mpx4FrameTransmissionRetire
		body, err = appendField(nil, f.offset)
	case kindSessionWindow:
		typ = mpx4FrameSessionCredit
		body, err = appendField(nil, f.offset, f.id)
	case kindCreditProbe:
		typ = mpx4FrameCreditProbe
		body, err = appendField(nil, f.stream)
	case kindPing:
		typ = mpx4FramePing
		body, err = appendField(nil, f.offset)
	case kindPong:
		typ = mpx4FramePong
		body, err = appendField(nil, f.offset)
	case kindCarrierClose, kindSessionClose:
		if f.kind == kindCarrierClose {
			typ = mpx4FrameCarrierClose
		} else {
			typ = mpx4FrameSessionClose
		}
		body, err = appendField(nil, f.offset, f.id, uint64(len(f.data)))
		body = append(body, f.data...)
	default:
		return nil, ErrProtocol
	}
	if err != nil || len(body) > MaxRecordSize {
		return nil, ErrProtocol
	}
	out, err := appendV4VarInt(nil, typ)
	if err != nil {
		return nil, err
	}
	out, err = appendV4VarInt(out, uint64(len(body)))
	if err != nil {
		return nil, err
	}
	return append(out, body...), nil
}

func decodeV4Frame(typ uint64, body []byte) (frame, error) {
	var f frame
	switch typ {
	case mpx4FramePing, mpx4FramePong:
		v, n, err := readFields(body, 1)
		if err != nil || n != len(body) {
			return f, ErrProtocol
		}
		f.kind = kindPing
		if typ == mpx4FramePong {
			f.kind = kindPong
		}
		f.offset = v[0]
	case mpx4FrameStreamOpen, mpx4FrameStreamOpenOK:
		v, n, err := readFields(body, 2)
		if err != nil || n != len(body) || v[0] == 0 || v[1] == 0 {
			return f, ErrProtocol
		}
		f.stream, f.id = v[0], v[1]
		f.kind = kindOpen
		if typ == mpx4FrameStreamOpenOK {
			f.kind = kindOpenOK
		}
	case mpx4FrameStreamOpenReject:
		v, n, err := readFields(body, 3)
		if err != nil || n != len(body) || v[0] == 0 || v[1] == 0 {
			return f, ErrProtocol
		}
		f.kind, f.stream, f.id, f.offset = kindOpenReject, v[0], v[1], v[2]
	case mpx4FrameStreamData:
		v, n, err := readFields(body, 3)
		if err != nil || v[0] == 0 || v[2] == 0 || n >= len(body) || len(body)-n > MaxPayload {
			return f, ErrProtocol
		}
		f.kind, f.stream, f.offset, f.id = kindData, v[0], v[1], v[2]
		f.data = append([]byte(nil), body[n:]...)
	case mpx4FrameTransmissionACK:
		v, n, err := readFields(body, 3)
		if err != nil || n != len(body) || v[0] == 0 || v[1] == 0 {
			return f, ErrProtocol
		}
		f.kind, f.stream, f.id, f.offset = kindACK, v[0], v[1], v[2]
	case mpx4FrameStreamCredit:
		v, n, err := readFields(body, 3)
		if err != nil || n != len(body) || v[0] == 0 || v[2] < v[1] {
			return f, ErrProtocol
		}
		f.kind, f.stream, f.offset, f.id = kindWindow, v[0], v[1], v[2]
	case mpx4FrameStreamFIN:
		v, n, err := readFields(body, 3)
		if err != nil || n != len(body) || v[0] == 0 || v[1] == 0 {
			return f, ErrProtocol
		}
		f.kind, f.stream, f.id, f.offset = kindFIN, v[0], v[1], v[2]
	case mpx4FrameResetStream:
		v, n, err := readFields(body, 4)
		if err != nil || n != len(body) || v[0] == 0 || v[1] == 0 {
			return f, ErrProtocol
		}
		f.kind, f.stream, f.id, f.offset = kindResetStream, v[0], v[1], v[2]
		f.data = make([]byte, 8)
		binary.BigEndian.PutUint64(f.data, v[3])
	case mpx4FrameStopSending:
		v, n, err := readFields(body, 3)
		if err != nil || n != len(body) || v[0] == 0 || v[1] == 0 {
			return f, ErrProtocol
		}
		f.kind, f.stream, f.id, f.offset = kindStopReceiving, v[0], v[1], v[2]
	case mpx4FrameStreamConsumed:
		v, n, err := readFields(body, 3)
		if err != nil || n != len(body) || v[0] == 0 || v[1] == 0 {
			return f, ErrProtocol
		}
		f.kind, f.stream, f.id, f.offset = kindFinalConsumed, v[0], v[1], v[2]
	case mpx4FrameTransmissionRetire:
		v, n, err := readFields(body, 1)
		if err != nil || n != len(body) {
			return f, ErrProtocol
		}
		f.kind, f.offset = kindTransmissionRetire, v[0]
	case mpx4FrameSessionCredit:
		v, n, err := readFields(body, 2)
		if err != nil || n != len(body) || v[1] < v[0] {
			return f, ErrProtocol
		}
		f.kind, f.offset, f.id = kindSessionWindow, v[0], v[1]
	case mpx4FrameCreditProbe:
		v, n, err := readFields(body, 1)
		if err != nil || n != len(body) {
			return f, ErrProtocol
		}
		f.kind, f.stream = kindCreditProbe, v[0]
	case mpx4FrameCarrierClose, mpx4FrameSessionClose:
		v, n, err := readFields(body, 3)
		if err != nil || v[2] > 256 || v[2] > uint64(len(body)-n) || n+int(v[2]) != len(body) {
			return f, ErrProtocol
		}
		reason := body[n:]
		if !utf8.Valid(reason) {
			return f, ErrProtocol
		}
		f.kind = kindCarrierClose
		if typ == mpx4FrameSessionClose {
			f.kind = kindSessionClose
		}
		f.offset, f.id = v[0], v[1]
		f.data = append([]byte(nil), reason...)
	default:
		if typ >= 0x40 && typ <= 0x3fff {
			return f, errSkipFrame
		}
		return f, ErrProtocol
	}
	return f, nil
}

func parseV4Frames(plain []byte) ([]frame, error) {
	if len(plain) == 0 {
		return nil, ErrProtocol
	}
	var out []frame
	for len(plain) > 0 {
		typ, n1, err := readV4VarInt(plain)
		if err != nil {
			return nil, err
		}
		ln, n2, err := readV4VarInt(plain[n1:])
		if err != nil {
			return nil, err
		}
		h := n1 + n2
		if ln > uint64(len(plain)-h) {
			return nil, ErrProtocol
		}
		body := plain[h : h+int(ln)]
		plain = plain[h+int(ln):]
		if typ == 0x00 {
			continue
		}
		f, err := decodeV4Frame(typ, body)
		if errors.Is(err, errSkipFrame) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		// PADDING and extension Frames may legally occupy a whole Secure
		// Record. The record nonce has already advanced; readFrame will
		// continue with the next record until it finds a semantic Frame.
		return nil, nil
	}
	return out, nil
}

type secureConn struct {
	net.Conn
	txMu                                        sync.Mutex
	reader                                      *bufio.Reader
	send, receive                               cipher.AEAD
	txIV, rxIV                                  [12]byte
	txCounter, rxCounter                        uint64
	pendingFrames                               []frame
	configuredRateBPS                           float64
	generation                                  uint64
	peerMaxFrame, peerMaxRecord, peerMaxStreams uint64
	localMaxCarriers, peerMaxCarriers           uint64
	// These scratch buffers are protected by txMu together with the write
	// operation. Keeping them on the connection avoids allocating the encoded
	// frame list and record accumulator for every carrier batch.
	encodedScratch [][]byte
	recordScratch  []byte
}

func xorV4Nonce(iv [12]byte, seq uint64) []byte {
	out := iv
	var s [12]byte
	binary.BigEndian.PutUint64(s[4:], seq)
	for i := range out {
		out[i] ^= s[i]
	}
	return out[:]
}

const carrierBatchBytes = 256 << 10
const carrierBatchFrames = 64

func (c *secureConn) writeFrame(f frame) error { return c.writeFrames([]frame{f}) }

func (c *secureConn) writeRecord(plain []byte) error {
	if len(plain) == 0 || uint64(len(plain)) > c.peerMaxRecord || c.txCounter >= mpx4RecordLimit {
		return ErrProtocol
	}
	header := []byte{0}
	var err error
	header, err = appendV4VarInt(header, uint64(len(plain)))
	if err != nil {
		return err
	}
	sealed := c.send.Seal(nil, xorV4Nonce(c.txIV, c.txCounter), plain, header)
	wire := append(append([]byte(nil), header...), sealed...)
	if err := writeAll(c.Conn, wire); err != nil {
		return err
	}
	c.txCounter++
	return nil
}

func (c *secureConn) writeFrames(frames []frame) error {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	if len(frames) == 0 || len(frames) > carrierBatchFrames {
		return ErrResourceLimit
	}
	limit := int(c.peerMaxRecord)
	if limit <= 0 || limit > MaxRecordSize {
		limit = MaxRecordSize
	}
	encoded := c.encodedScratch[:0]
	if cap(encoded) < len(frames) {
		encoded = make([][]byte, 0, len(frames))
	}
	defer func() {
		// The backing array is reusable, but per-frame encodings should be
		// collected once this batch has been written.
		clear(encoded)
		c.encodedScratch = encoded[:0]
	}()
	total := 0
	for _, f := range frames {
		if f.kind == kindData && c.peerMaxFrame > 0 && uint64(len(f.data)) > c.peerMaxFrame {
			return ErrProtocol
		}
		wire, err := encodeV4Frame(f)
		if err != nil {
			return err
		}
		if len(wire) > limit {
			return ErrProtocol
		}
		total += len(wire) + 18 // conservative per-record header+tag allowance
		if total > carrierBatchBytes {
			return ErrResourceLimit
		}
		encoded = append(encoded, wire)
	}
	record := c.recordScratch[:0]
	defer func() {
		// The record plaintext persists in the reusable backing array even
		// after reslicing. Wipe it before making the allocation reusable.
		clear(record[:cap(record)])
		c.recordScratch = record[:0]
	}()
	for _, wire := range encoded {
		if len(record) > 0 && len(record)+len(wire) > limit {
			if err := c.writeRecord(record); err != nil {
				return err
			}
			record = record[:0]
		}
		record = append(record, wire...)
	}
	if len(record) > 0 {
		return c.writeRecord(record)
	}
	return nil
}

func (c *secureConn) readFrame() (frame, error) {
	if len(c.pendingFrames) > 0 {
		f := c.pendingFrames[0]
		c.pendingFrames = c.pendingFrames[1:]
		return f, nil
	}
	for {
		if c.rxCounter >= mpx4RecordLimit {
			return frame{}, ErrProtocol
		}
		flags, err := c.reader.ReadByte()
		if err != nil {
			return frame{}, err
		}
		if flags != 0 {
			return frame{}, ErrProtocol
		}
		ln, rawLen, err := readV4VarIntReader(c.reader)
		if err != nil || ln == 0 || ln > MaxRecordSize {
			return frame{}, ErrProtocol
		}
		enc := make([]byte, int(ln)+16)
		if _, err := io.ReadFull(c.reader, enc); err != nil {
			return frame{}, err
		}
		header := append([]byte{flags}, rawLen...)
		plain, err := c.receive.Open(nil, xorV4Nonce(c.rxIV, c.rxCounter), enc, header)
		if err != nil {
			return frame{}, ErrAuthentication
		}
		c.rxCounter++
		frames, err := parseV4Frames(plain)
		if err != nil {
			return frame{}, err
		}
		if len(frames) == 0 {
			continue
		}
		c.pendingFrames = frames[1:]
		return frames[0], nil
	}
}

func hkdfExtract(salt, ikm []byte) []byte {
	h := hmac.New(sha256.New, salt)
	h.Write(ikm)
	return h.Sum(nil)
}

func hkdfExpand(secret, info []byte, n int) ([]byte, error) {
	if n < 0 || n > 255*sha256.Size {
		return nil, ErrProtocol
	}
	out := make([]byte, 0, n)
	var prev []byte
	for counter := byte(1); len(out) < n; counter++ {
		h := hmac.New(sha256.New, secret)
		h.Write(prev)
		h.Write(info)
		h.Write([]byte{counter})
		prev = h.Sum(nil)
		need := min(n-len(out), len(prev))
		out = append(out, prev[:need]...)
	}
	return out, nil
}

func mpx4ExpandLabel(secret []byte, label string, context []byte, n int) ([]byte, error) {
	full := []byte("mpx4 " + label)
	if len(full) > 255 || len(context) > 255 || n > 65535 {
		return nil, ErrProtocol
	}
	info := make([]byte, 0, 2+1+len(full)+1+len(context))
	info = append(info, byte(n>>8), byte(n), byte(len(full)))
	info = append(info, full...)
	info = append(info, byte(len(context)))
	info = append(info, context...)
	return hkdfExpand(secret, info, n)
}

func finishedKey(handshakeSecret []byte, client bool) ([]byte, error) {
	label := "server finished"
	if client {
		label = "client finished"
	}
	return mpx4ExpandLabel(handshakeSecret, label, nil, 32)
}

func finishedVerify(key, transcriptHash []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(transcriptHash)
	return h.Sum(nil)
}

// newSecure is retained as an internal test helper. Production TCP carriers
// always enter through the full MPX/4 Finished handshake.
func newSecure(c net.Conn, key, transcript []byte, client bool) (*secureConn, error) {
	h := sha256.Sum256(transcript)
	hs, err := handshakeSecretFor(key, h[:])
	if err != nil {
		return nil, err
	}
	return newV4Secure(c, hs, h[:], client, MaxRecordSize, 0)
}

func newV4Secure(c net.Conn, handshakeSecret, h2 []byte, client bool, peerMaxRecord uint64, generation uint64) (*secureConn, error) {
	cSecret, err := mpx4ExpandLabel(handshakeSecret, "client application", h2, 32)
	if err != nil {
		return nil, err
	}
	sSecret, err := mpx4ExpandLabel(handshakeSecret, "server application", h2, 32)
	if err != nil {
		return nil, err
	}
	txSecret, rxSecret := cSecret, sSecret
	if !client {
		txSecret, rxSecret = sSecret, cSecret
	}
	txKey, err := mpx4ExpandLabel(txSecret, "key", nil, 32)
	if err != nil {
		return nil, err
	}
	rxKey, err := mpx4ExpandLabel(rxSecret, "key", nil, 32)
	if err != nil {
		return nil, err
	}
	txIV, err := mpx4ExpandLabel(txSecret, "iv", nil, 12)
	if err != nil {
		return nil, err
	}
	rxIV, err := mpx4ExpandLabel(rxSecret, "iv", nil, 12)
	if err != nil {
		return nil, err
	}
	txAEAD, err := aeadFor(txKey)
	if err != nil {
		return nil, err
	}
	rxAEAD, err := aeadFor(rxKey)
	if err != nil {
		return nil, err
	}
	sc := &secureConn{Conn: c, reader: bufio.NewReaderSize(c, 64<<10), send: txAEAD, receive: rxAEAD, peerMaxRecord: peerMaxRecord, generation: generation}
	copy(sc.txIV[:], txIV)
	copy(sc.rxIV[:], rxIV)
	return sc, nil
}

func encodeHandshakeMessage(typ uint64, body []byte) ([]byte, error) {
	if len(body) > 4096 {
		return nil, ErrProtocol
	}
	out, err := appendV4VarInt(nil, typ)
	if err != nil {
		return nil, err
	}
	out, err = appendV4VarInt(out, uint64(len(body)))
	if err != nil {
		return nil, err
	}
	return append(out, body...), nil
}

func readHandshakeMessage(r *bufio.Reader) (uint64, []byte, []byte, error) {
	typ, rawType, err := readV4VarIntReader(r)
	if err != nil {
		return 0, nil, nil, err
	}
	ln, rawLen, err := readV4VarIntReader(r)
	if err != nil || ln > 4096 {
		return 0, nil, nil, ErrProtocol
	}
	body := make([]byte, int(ln))
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, nil, err
	}
	raw := append(append([]byte(nil), rawType...), rawLen...)
	raw = append(raw, body...)
	return typ, body, raw, nil
}

func encodeParam(typ uint64, flags byte, value []byte) ([]byte, error) {
	if flags&0xfe != 0 {
		return nil, ErrProtocol
	}
	out, err := appendV4VarInt(nil, typ)
	if err != nil {
		return nil, err
	}
	out = append(out, flags)
	out, err = appendV4VarInt(out, uint64(len(value)))
	if err != nil {
		return nil, err
	}
	return append(out, value...), nil
}

func paramVarInt(v uint64) ([]byte, error) { return appendV4VarInt(nil, v) }

type handshakeLimits struct {
	maxFrame, maxRecord, maxStreams, maxCarriers uint64
}

type handshakeParams struct {
	sid                             sessionID
	action, carrier, generation     uint64
	clientNonce, serverNonce        [32]byte
	maxFrame, maxRecord, maxStreams uint64
	maxCarriers                     uint64
	receiveCapacityHint             uint64
	hasReceiveCapacityHint          bool
}

func (p handshakeParams) limits() handshakeLimits {
	return handshakeLimits{maxFrame: p.maxFrame, maxRecord: p.maxRecord, maxStreams: p.maxStreams, maxCarriers: p.maxCarriers}
}

type handshakeParamItem struct {
	typ   uint64
	flags byte
	val   []byte
}

func encodeHandshakeParams(items []handshakeParamItem) ([]byte, error) {
	var body []byte
	var last uint64
	for i, it := range items {
		if i > 0 && it.typ <= last {
			return nil, ErrProtocol
		}
		last = it.typ
		enc, err := encodeParam(it.typ, it.flags, it.val)
		if err != nil {
			return nil, err
		}
		body = append(body, enc...)
	}
	return body, nil
}

func encodeClientParams(p handshakeParams) ([]byte, error) {
	if p.maxCarriers == 0 {
		p.maxCarriers = MaxCarriers
	}
	items := []handshakeParamItem{{typ: mpx4ParamSessionID, val: p.sid[:]}}
	v, _ := paramVarInt(p.action)
	items = append(items, handshakeParamItem{typ: mpx4ParamSessionAction, val: v})
	v, _ = paramVarInt(p.carrier)
	items = append(items, handshakeParamItem{typ: mpx4ParamCarrierID, val: v})
	v, _ = paramVarInt(p.generation)
	items = append(items, handshakeParamItem{typ: mpx4ParamCarrierGeneration, val: v})
	items = append(items, handshakeParamItem{typ: mpx4ParamClientNonce, val: p.clientNonce[:]})
	v, _ = paramVarInt(MaxPayload)
	items = append(items, handshakeParamItem{typ: mpx4ParamMaxFramePayload, val: v})
	v, _ = paramVarInt(MaxRecordSize)
	items = append(items, handshakeParamItem{typ: mpx4ParamMaxRecordSize, val: v})
	v, _ = paramVarInt(MaxStreams)
	items = append(items, handshakeParamItem{typ: mpx4ParamMaxStreams, val: v})
	v, _ = paramVarInt(p.maxCarriers)
	items = append(items, handshakeParamItem{typ: mpx4ParamMaxCarriers, flags: 1, val: v})
	if p.hasReceiveCapacityHint {
		if p.receiveCapacityHint == 0 || p.receiveCapacityHint > 65535 {
			return nil, ErrProtocol
		}
		v, _ = paramVarInt(p.receiveCapacityHint)
		items = append(items, handshakeParamItem{typ: mpx4ParamReceiveCapacityHint, val: v})
	}
	return encodeHandshakeParams(items)
}

func encodeServerParams(p handshakeParams) ([]byte, error) {
	if p.maxCarriers == 0 {
		p.maxCarriers = MaxCarriers
	}
	items := []handshakeParamItem{{typ: mpx4ParamServerNonce, val: p.serverNonce[:]}}
	v, _ := paramVarInt(MaxPayload)
	items = append(items, handshakeParamItem{typ: mpx4ParamMaxFramePayload, val: v})
	v, _ = paramVarInt(MaxRecordSize)
	items = append(items, handshakeParamItem{typ: mpx4ParamMaxRecordSize, val: v})
	v, _ = paramVarInt(MaxStreams)
	items = append(items, handshakeParamItem{typ: mpx4ParamMaxStreams, val: v})
	v, _ = paramVarInt(p.maxCarriers)
	items = append(items, handshakeParamItem{typ: mpx4ParamMaxCarriers, flags: 1, val: v})
	if p.hasReceiveCapacityHint {
		if p.receiveCapacityHint == 0 || p.receiveCapacityHint > 65535 {
			return nil, ErrProtocol
		}
		v, _ = paramVarInt(p.receiveCapacityHint)
		items = append(items, handshakeParamItem{typ: mpx4ParamReceiveCapacityHint, val: v})
	}
	return encodeHandshakeParams(items)
}

func parseParams(body []byte, client bool) (handshakeParams, error) {
	var p handshakeParams
	var last uint64
	first := true
	seen := map[uint64]bool{}
	for len(body) > 0 {
		typ, n1, err := readV4VarInt(body)
		if err != nil || len(body) <= n1 {
			return p, ErrProtocol
		}
		flags := body[n1]
		if flags&0xfe != 0 {
			return p, ErrProtocol
		}
		ln, n2, err := readV4VarInt(body[n1+1:])
		if err != nil {
			return p, err
		}
		h := n1 + 1 + n2
		if ln > uint64(len(body)-h) || seen[typ] || (!first && typ <= last) {
			return p, ErrProtocol
		}
		first = false
		last = typ
		seen[typ] = true
		val := body[h : h+int(ln)]
		body = body[h+int(ln):]
		readOne := func() (uint64, error) {
			v, n, e := readV4VarInt(val)
			if e != nil || n != len(val) {
				return 0, ErrProtocol
			}
			return v, nil
		}
		coreFlags := func(expected byte) error {
			if flags != expected {
				return ErrProtocol
			}
			return nil
		}
		switch typ {
		case mpx4ParamSessionID:
			if !client || coreFlags(0) != nil || len(val) != 16 {
				return p, ErrProtocol
			}
			copy(p.sid[:], val)
		case mpx4ParamSessionAction:
			if !client || coreFlags(0) != nil {
				return p, ErrProtocol
			}
			p.action, err = readOne()
			if err != nil || p.action > 1 {
				return p, ErrProtocol
			}
		case mpx4ParamCarrierID:
			if !client || coreFlags(0) != nil {
				return p, ErrProtocol
			}
			p.carrier, err = readOne()
			if err != nil || p.carrier == 0 || p.carrier > mpx4VarIntMax {
				return p, ErrProtocol
			}
		case mpx4ParamCarrierGeneration:
			if !client || coreFlags(0) != nil {
				return p, ErrProtocol
			}
			p.generation, err = readOne()
			if err != nil {
				return p, err
			}
		case mpx4ParamClientNonce:
			if !client || coreFlags(0) != nil || len(val) != 32 {
				return p, ErrProtocol
			}
			copy(p.clientNonce[:], val)
		case mpx4ParamServerNonce:
			if client || coreFlags(0) != nil || len(val) != 32 {
				return p, ErrProtocol
			}
			copy(p.serverNonce[:], val)
		case mpx4ParamMaxFramePayload:
			if coreFlags(0) != nil {
				return p, ErrProtocol
			}
			p.maxFrame, err = readOne()
			if err != nil || p.maxFrame < 1 || p.maxFrame > MaxPayload {
				return p, ErrProtocol
			}
		case mpx4ParamMaxRecordSize:
			if coreFlags(0) != nil {
				return p, ErrProtocol
			}
			p.maxRecord, err = readOne()
			if err != nil || p.maxRecord < 1024 || p.maxRecord > MaxRecordSize {
				return p, ErrProtocol
			}
		case mpx4ParamMaxStreams:
			if coreFlags(0) != nil {
				return p, ErrProtocol
			}
			p.maxStreams, err = readOne()
			if err != nil || p.maxStreams < 1 || p.maxStreams > MaxStreams {
				return p, ErrProtocol
			}
		case mpx4ParamMaxCarriers:
			if coreFlags(1) != nil {
				return p, ErrProtocol
			}
			p.maxCarriers, err = readOne()
			if err != nil || p.maxCarriers == 0 || p.maxCarriers > mpx4VarIntMax {
				return p, ErrProtocol
			}
		case mpx4ParamReceiveCapacityHint:
			if flags != 0 {
				return p, ErrProtocol
			}
			p.receiveCapacityHint, err = readOne()
			if err != nil || p.receiveCapacityHint == 0 || p.receiveCapacityHint > 65535 {
				return p, ErrProtocol
			}
			p.hasReceiveCapacityHint = true
		default:
			if flags&1 != 0 {
				return p, &mpx4Failure{code: mpx4ErrUnsupportedParameter, scope: mpx4ScopePreEstablishmentCarrier, reason: "unknown critical handshake Parameter", cause: ErrProtocol}
			}
		}
	}
	if p.maxFrame == 0 || p.maxRecord == 0 || p.maxStreams == 0 || p.maxCarriers == 0 {
		return p, ErrProtocol
	}
	if client {
		if p.sid == (sessionID{}) || p.carrier == 0 || p.clientNonce == ([32]byte{}) {
			return p, ErrProtocol
		}
	} else if p.serverNonce == ([32]byte{}) {
		return p, ErrProtocol
	}
	return p, nil
}

func handshakeSecretFor(key, h0 []byte) ([]byte, error) {
	early := hkdfExtract(make([]byte, 32), key)
	return mpx4ExpandLabel(early, "handshake", h0, 32)
}

func bytesJoin(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func encodeHandshakeCodeMessage(typ, code uint64) ([]byte, error) {
	body, err := appendV4VarInt(nil, code)
	if err != nil {
		return nil, err
	}
	return encodeHandshakeMessage(typ, body)
}

func decodeHandshakeCode(body []byte) (uint64, error) {
	code, n, err := readV4VarInt(body)
	if err != nil || n != len(body) {
		return 0, ErrProtocol
	}
	return code, nil
}

func validCoreHandshakeRejectCode(code uint64) bool {
	switch code {
	case mpx4ErrInternal, mpx4ErrProtocolViolation, mpx4ErrAuthenticationFailed,
		mpx4ErrResourceLimit, mpx4ErrSessionNotFound, mpx4ErrSessionConflict,
		mpx4ErrCarrierConflict, mpx4ErrUnsupportedParameter:
		return true
	default:
		return false
	}
}

func handshakeRejectError(code uint64) error {
	if code < 0x40 && !validCoreHandshakeRejectCode(code) {
		return fmt.Errorf("%w: invalid Core HANDSHAKE_REJECT error code 0x%x", ErrProtocol, code)
	}
	switch code {
	case mpx4ErrSessionNotFound:
		return ErrSessionExpired
	case mpx4ErrResourceLimit:
		return &ResourceLimitError{Reason: "handshake_resource_limit"}
	case mpx4ErrAuthenticationFailed:
		return ErrAuthentication
	default:
		return &mpx4Failure{code: code, scope: mpx4ScopePreEstablishmentCarrier, reason: "candidate handshake rejected", cause: ErrProtocol}
	}
}

func writeHandshakeReject(c net.Conn, code uint64) error {
	if !validCoreHandshakeRejectCode(code) {
		return ErrProtocol
	}
	wire, err := encodeHandshakeCodeMessage(mpx4HSHandshakeReject, code)
	if err != nil {
		return err
	}
	return writeAll(c, wire)
}

func encodeVersionNegotiation(versions []uint64) ([]byte, error) {
	if len(versions) == 0 {
		return nil, ErrProtocol
	}
	body, err := appendV4VarInt(nil, uint64(len(versions)))
	if err != nil {
		return nil, err
	}
	for i, version := range versions {
		if version == 0 || (i > 0 && version >= versions[i-1]) {
			return nil, ErrProtocol
		}
		body, err = appendV4VarInt(body, version)
		if err != nil {
			return nil, err
		}
	}
	return encodeHandshakeMessage(mpx4HSVersionNegotiation, body)
}

func parseVersionNegotiation(body []byte) ([]uint64, error) {
	count, n, err := readV4VarInt(body)
	if err != nil || count == 0 || count > uint64(len(body)-n) {
		return nil, ErrProtocol
	}
	body = body[n:]
	versions := make([]uint64, 0, int(count))
	for i := uint64(0); i < count; i++ {
		version, consumed, err := readV4VarInt(body)
		if err != nil || version == 0 {
			return nil, ErrProtocol
		}
		if len(versions) > 0 && version >= versions[len(versions)-1] {
			return nil, ErrProtocol
		}
		versions = append(versions, version)
		body = body[consumed:]
	}
	if len(body) != 0 {
		return nil, ErrProtocol
	}
	return versions, nil
}

func clientHandshake(c net.Conn, key []byte, sid sessionID, carrier uint64, create bool) (*secureConn, error) {
	return clientHandshakeMode(c, key, sid, carrier, create, SchedulerAuto)
}
func clientHandshakeMode(c net.Conn, key []byte, sid sessionID, carrier uint64, create bool, mode SchedulerMode) (*secureConn, error) {
	return clientHandshakePolicy(c, key, sid, carrier, create, mode, PathCapacity{})
}
func clientHandshakePolicy(c net.Conn, key []byte, sid sessionID, carrier uint64, create bool, mode SchedulerMode, capacity PathCapacity) (*secureConn, error) {
	return clientHandshakePolicyGeneration(c, key, sid, carrier, 0, create, mode, capacity)
}

func clientHandshakePolicyGeneration(c net.Conn, key []byte, sid sessionID, carrier, generation uint64, create bool, mode SchedulerMode, capacity PathCapacity) (*secureConn, error) {
	return clientHandshakePolicyGenerationExpected(c, key, sid, carrier, generation, create, mode, capacity, nil)
}

func clientHandshakePolicyGenerationExpected(c net.Conn, key []byte, sid sessionID, carrier, generation uint64, create bool, mode SchedulerMode, capacity PathCapacity, expected *handshakeLimits) (*secureConn, error) {
	if carrier == 0 || carrier > mpx4VarIntMax || generation > mpx4VarIntMax || len(key) != 32 {
		return nil, ErrProtocol
	}
	var receiveHint uint16
	if mode == SchedulerWeighted {
		if err := capacity.validateWeighted(); err != nil {
			return nil, err
		}
		var err error
		receiveHint, err = capacityUnits(capacity.DownloadMbps, true)
		if err != nil {
			return nil, err
		}
	}
	if err := c.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return nil, err
	}
	p := handshakeParams{sid: sid, carrier: carrier, generation: generation, maxCarriers: MaxCarriers}
	if mode == SchedulerWeighted {
		p.receiveCapacityHint = uint64(receiveHint)
		p.hasReceiveCapacityHint = true
	}
	if create {
		p.action = 0
	} else {
		p.action = 1
	}
	if _, err := rand.Read(p.clientNonce[:]); err != nil {
		return nil, err
	}
	body, err := encodeClientParams(p)
	if err != nil {
		return nil, err
	}
	clientInit, err := encodeHandshakeMessage(mpx4HSClientInit, body)
	if err != nil {
		return nil, err
	}
	version, _ := appendV4VarInt(nil, WireProtocol)
	preface := append([]byte{'M', 'P', 'X', 0}, version...)
	if err := writeAll(c, bytesJoin(preface, clientInit)); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(c, 64<<10)
	typ, serverBody, serverInit, err := readHandshakeMessage(r)
	if err != nil {
		return nil, err
	}
	if typ == mpx4HSHandshakeReject {
		code, err := decodeHandshakeCode(serverBody)
		if err != nil {
			return nil, err
		}
		return nil, handshakeRejectError(code)
	}
	if typ == mpx4HSVersionNegotiation {
		versions, err := parseVersionNegotiation(serverBody)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: peer does not accept Protocol Version %d; offered versions %v", ErrProtocol, WireProtocol, versions)
	}
	if typ != mpx4HSServerInit {
		return nil, fmt.Errorf("%w: expected SERVER_INIT", ErrProtocol)
	}
	serverParams, err := parseParams(serverBody, false)
	if err != nil {
		return nil, err
	}
	if expected != nil && serverParams.limits() != *expected {
		return nil, &mpx4Failure{code: mpx4ErrSessionConflict, scope: mpx4ScopePreEstablishmentCarrier, reason: "JOIN peer receive limits differ from CREATE", cause: ErrProtocol}
	}
	h0 := sha256.Sum256(bytesJoin(preface, clientInit, serverInit))
	hs, err := handshakeSecretFor(key, h0[:])
	if err != nil {
		return nil, err
	}
	cfk, _ := finishedKey(hs, true)
	clientFinished, err := encodeHandshakeMessage(mpx4HSClientFinished, finishedVerify(cfk, h0[:]))
	if err != nil {
		return nil, err
	}
	if err := writeAll(c, clientFinished); err != nil {
		return nil, err
	}
	h1 := sha256.Sum256(bytesJoin(preface, clientInit, serverInit, clientFinished))
	typ, serverFinishedBody, serverFinished, err := readHandshakeMessage(r)
	if err != nil || typ != mpx4HSServerFinished || len(serverFinishedBody) != 32 {
		return nil, ErrAuthentication
	}
	sfk, _ := finishedKey(hs, false)
	if !hmac.Equal(serverFinishedBody, finishedVerify(sfk, h1[:])) {
		return nil, ErrAuthentication
	}
	h2 := sha256.Sum256(bytesJoin(preface, clientInit, serverInit, clientFinished, serverFinished))
	if err := c.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	sc, err := newV4Secure(c, hs, h2[:], true, serverParams.maxRecord, generation)
	if err != nil {
		return nil, err
	}
	sc.reader = r
	sc.peerMaxFrame = serverParams.maxFrame
	sc.peerMaxStreams = serverParams.maxStreams
	sc.localMaxCarriers = p.maxCarriers
	sc.peerMaxCarriers = serverParams.maxCarriers
	sc.configuredRateBPS = capacity.txRateBPS(false)
	return sc, nil
}

type incomingHandshake struct {
	conn                net.Conn
	reader              *bufio.Reader
	id                  sessionID
	carrier, generation uint64
	create              bool
	clientInit, preface []byte
	client              handshakeParams
}

func readHandshake(c net.Conn, key []byte) (incomingHandshake, error) {
	var out incomingHandshake
	if len(key) != 32 {
		return out, ErrAuthentication
	}
	if err := c.SetDeadline(time.Now().Add(preHandshakeTimeout)); err != nil {
		return out, err
	}
	r := bufio.NewReaderSize(c, 64<<10)
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil {
		return out, err
	}
	if string(magic) != "MPX\x00" {
		return out, ErrProtocol
	}
	version, rawVersion, err := readV4VarIntReader(r)
	if err != nil {
		return out, err
	}
	if version != WireProtocol {
		wire, encErr := encodeVersionNegotiation([]uint64{WireProtocol})
		if encErr == nil {
			_ = writeAll(c, wire)
		}
		return out, fmt.Errorf("%w: unsupported Protocol Version %d", ErrProtocol, version)
	}
	preface := append(append([]byte(nil), magic...), rawVersion...)
	typ, body, raw, err := readHandshakeMessage(r)
	if err != nil || typ != mpx4HSClientInit {
		_ = writeHandshakeReject(c, mpx4ErrProtocolViolation)
		return out, ErrProtocol
	}
	p, err := parseParams(body, true)
	if err != nil {
		code := mpx4ErrProtocolViolation
		var failure *mpx4Failure
		if errors.As(err, &failure) && failure.scope == mpx4ScopePreEstablishmentCarrier {
			code = failure.code
		}
		_ = writeHandshakeReject(c, code)
		return out, err
	}
	out = incomingHandshake{conn: c, reader: r, id: p.sid, carrier: p.carrier, generation: p.generation, create: p.action == 0, clientInit: raw, preface: preface, client: p}
	return out, nil
}

func (h incomingHandshake) finish(key []byte, rejectCode uint64) (*secureConn, error) {
	if rejectCode != 0 {
		_ = h.conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
		_ = writeHandshakeReject(h.conn, rejectCode)
		_ = h.conn.Close()
		return nil, handshakeRejectError(rejectCode)
	}
	if err := h.conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return nil, err
	}
	server := handshakeParams{maxCarriers: MaxCarriers}
	if _, err := rand.Read(server.serverNonce[:]); err != nil {
		return nil, err
	}
	serverBody, err := encodeServerParams(server)
	if err != nil {
		return nil, err
	}
	serverInit, err := encodeHandshakeMessage(mpx4HSServerInit, serverBody)
	if err != nil {
		return nil, err
	}
	if err := writeAll(h.conn, serverInit); err != nil {
		return nil, err
	}
	h0 := sha256.Sum256(bytesJoin(h.preface, h.clientInit, serverInit))
	hs, err := handshakeSecretFor(key, h0[:])
	if err != nil {
		return nil, err
	}
	typ, clientFinishedBody, clientFinished, err := readHandshakeMessage(h.reader)
	if err != nil || typ != mpx4HSClientFinished || len(clientFinishedBody) != 32 {
		_ = writeHandshakeReject(h.conn, mpx4ErrAuthenticationFailed)
		return nil, ErrAuthentication
	}
	cfk, _ := finishedKey(hs, true)
	if !hmac.Equal(clientFinishedBody, finishedVerify(cfk, h0[:])) {
		_ = writeHandshakeReject(h.conn, mpx4ErrAuthenticationFailed)
		return nil, ErrAuthentication
	}
	h1 := sha256.Sum256(bytesJoin(h.preface, h.clientInit, serverInit, clientFinished))
	sfk, _ := finishedKey(hs, false)
	serverFinished, err := encodeHandshakeMessage(mpx4HSServerFinished, finishedVerify(sfk, h1[:]))
	if err != nil {
		return nil, err
	}
	if err := writeAll(h.conn, serverFinished); err != nil {
		return nil, err
	}
	h2 := sha256.Sum256(bytesJoin(h.preface, h.clientInit, serverInit, clientFinished, serverFinished))
	if err := h.conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	sc, err := newV4Secure(h.conn, hs, h2[:], false, h.client.maxRecord, h.generation)
	if err != nil {
		return nil, err
	}
	sc.reader = h.reader
	sc.peerMaxFrame = h.client.maxFrame
	sc.peerMaxStreams = h.client.maxStreams
	sc.localMaxCarriers = server.maxCarriers
	sc.peerMaxCarriers = h.client.maxCarriers
	if h.client.hasReceiveCapacityHint {
		sc.configuredRateBPS = configuredRateBPS(capacityFromUnits(uint16(h.client.receiveCapacityHint)))
	}
	return sc, nil
}

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
