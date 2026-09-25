package multipath

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type testRelay struct {
	listener net.Listener
	target   string
	rate     int64
	latency  time.Duration
	mu       sync.Mutex
	conns    map[net.Conn]bool
	disabled atomic.Bool
	closed   chan struct{}
	bytes    atomic.Int64
}

func newTestRelay(t *testing.T, target string, rate int64, latency time.Duration) *testRelay {
	t.Helper()
	l, err := PlainListen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &testRelay{listener: l, target: target, rate: rate, latency: latency, conns: make(map[net.Conn]bool), closed: make(chan struct{})}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			if r.disabled.Load() {
				c.Close()
				continue
			}
			go r.forward(c)
		}
	}()
	t.Cleanup(func() { l.Close(); r.fail(); close(r.closed) })
	return r
}

func (r *testRelay) fail() {
	r.disabled.Store(true)
	r.mu.Lock()
	for c := range r.conns {
		c.Close()
	}
	r.mu.Unlock()
}
func (r *testRelay) recover() { r.disabled.Store(false) }

func (r *testRelay) forward(a net.Conn) {
	defer a.Close()
	b, err := PlainDial(context.Background(), r.target)
	if err != nil {
		return
	}
	defer b.Close()
	r.mu.Lock()
	if r.disabled.Load() {
		r.mu.Unlock()
		return
	}
	r.conns[a] = true
	r.conns[b] = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.conns, a); delete(r.conns, b); r.mu.Unlock() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{}, 2)
	for _, pair := range [][2]net.Conn{{a, b}, {b, a}} {
		go func(dst, src net.Conn) { r.pipe(ctx, dst, src); done <- struct{}{} }(pair[0], pair[1])
	}
	<-done
	cancel()
	a.Close()
	b.Close()
	<-done
}

type relayChunk struct {
	data  []byte
	ready time.Time
}

func (r *testRelay) pipe(ctx context.Context, dst, src net.Conn) {
	q := make(chan relayChunk, 64)
	readerDone := make(chan struct{})
	go func() {
		defer close(q)
		defer close(readerDone)
		buf := make([]byte, 4096)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				chunk := relayChunk{data: append([]byte(nil), buf[:n]...), ready: time.Now().Add(r.latency)}
				select {
				case q <- chunk:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// A bounded packet pipeline models propagation latency independently from
	// serialization delay. It is not a sleep-per-application-request shortcut.
	next := time.Now()
	for chunk := range q {
		if chunk.ready.After(next) {
			next = chunk.ready
		}
		if r.rate > 0 {
			next = next.Add(time.Duration(float64(time.Second) * float64(len(chunk.data)) / float64(r.rate)))
		}
		if delay := time.Until(next); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		if err := writeAll(dst, chunk.data); err != nil {
			return
		}
		r.bytes.Add(int64(len(chunk.data)))
	}
}

func echoBackend(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	l, err := PlainListen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int64
	var mu sync.Mutex
	conns := make(map[net.Conn]bool)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			mu.Lock()
			conns[c] = true
			mu.Unlock()
			go func() {
				defer c.Close()
				defer func() { mu.Lock(); delete(conns, c); mu.Unlock() }()
				c.SetDeadline(time.Now().Add(60 * time.Second))
				io.Copy(c, c)
				c.(HalfConn).CloseWrite()
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		for c := range conns {
			c.Close()
		}
		mu.Unlock()
	})
	return l.Addr().String(), &accepted
}

func testTCP(t *testing.T, n int, rate int64, latency time.Duration) (*Session, *Server, []*testRelay, *atomic.Int64) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	backend, count := echoBackend(t)
	srv, err := NewServer(ctx, testToken, backend, 4)
	if err != nil {
		t.Fatal(err)
	}
	l, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	var relays []*testRelay
	var addresses []string
	for i := 0; i < n; i++ {
		r := newTestRelay(t, l.Addr().String(), rate, latency)
		relays = append(relays, r)
		addresses = append(addresses, r.listener.Addr().String())
	}
	client, err := DialClient(ctx, addresses, testToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	wait, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if err = client.WaitPaths(wait, n); err != nil {
		t.Fatal(err)
	}
	return client, srv, relays, count
}

func transfer(s *Session, size int) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := s.Open(ctx)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(40 * time.Second))
	data := make([]byte, size)
	if _, err = rand.Read(data); err != nil {
		return 0, err
	}
	result := make(chan error, 1)
	started := time.Now()
	go func() {
		for pos := 0; pos < len(data); {
			n := min(len(data)-pos, 1+(pos*17+7919)%65003)
			written, e := st.Write(data[pos : pos+n])
			pos += written
			if e != nil {
				result <- e
				return
			}
		}
		result <- st.CloseWrite()
	}()
	got, err := io.ReadAll(io.LimitReader(st, int64(size)+1))
	if err != nil {
		st.Close()
		<-result
		return 0, err
	}
	if err = <-result; err != nil {
		return 0, err
	}
	if len(got) != size || sha256.Sum256(got) != sha256.Sum256(data) {
		return 0, fmt.Errorf("integrity mismatch: %d/%d", len(got), size)
	}
	return time.Since(started), nil
}

func TestTCPBinaryAndHalfClose(t *testing.T) {
	s, _, _, count := testTCP(t, 2, 4<<20, 2*time.Millisecond)
	for _, size := range []int{0, 1, 32767, 32768, 32769, 2 << 20} {
		if _, err := transfer(s, size); err != nil {
			t.Fatalf("size=%d: %v", size, err)
		}
	}
	stats := s.Snapshot()
	if stats.Paths != 2 || len(stats.PathStats) != 2 {
		t.Fatalf("paths: %+v", stats)
	}
	for _, p := range stats.PathStats {
		if p.Sent == 0 || p.Received == 0 {
			t.Fatalf("carrier unused: %+v", p)
		}
	}
	if count.Load() != 6 {
		t.Fatalf("OPEN duplicate or missing backend: %d", count.Load())
	}
	t.Logf("single-stream paths: %+v", stats.PathStats)
}

func TestTCPMultiplex(t *testing.T) {
	s, _, _, count := testTCP(t, 3, 8<<20, time.Millisecond)
	const streams = 12
	var wg sync.WaitGroup
	errs := make(chan error, streams)
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := transfer(s, 128<<10); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count.Load() != streams {
		t.Fatalf("backend count=%d", count.Load())
	}
	if s.Snapshot().Connections != 0 {
		t.Fatalf("logical stream leak: %+v", s.Snapshot())
	}
}

func TestTCPPathFailureAndRejoin(t *testing.T) {
	s, srv, relays, _ := testTCP(t, 2, 1<<20, 3*time.Millisecond)
	failed := time.AfterFunc(200*time.Millisecond, relays[0].fail)
	defer failed.Stop()
	recovered := time.AfterFunc(1200*time.Millisecond, relays[0].recover)
	defer recovered.Stop()
	elapsed, err := transfer(s, 5<<20)
	if err != nil {
		t.Fatalf("path-churn transfer: %v client=%+v server=%+v", err, s.Snapshot(), srv.Snapshots())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = s.WaitPaths(ctx, 2); err != nil {
		t.Fatal(err)
	}
	stats := s.Snapshot()
	if stats.PathStats[0].Errors == 0 {
		t.Fatal("fault was not observed")
	}
	t.Logf("failover transfer=%v stats=%+v", elapsed, stats)
}

func TestTCPSlowReaderIsolated(t *testing.T) {
	s, srv, _, _ := testTCP(t, 2, 0, time.Millisecond)
	peer := srv.session(s.id)
	peer.mu.Lock()
	peer.onOpen = func(st *Stream) {
		if st.id == 1 {
			// The first application's receive side deliberately never consumes
			// bytes. This excludes variable operating-system socket buffering.
			peer.accept(st)
			<-peer.ctx.Done()
			st.Close()
			return
		}
		srv.openBackend(st)
	}
	peer.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	slow, err := s.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	slow.SetWriteDeadline(time.Now().Add(400 * time.Millisecond))
	result := make(chan error, 1)
	go func() { _, e := slow.Write(make([]byte, 4*StreamWindow)); result <- e }()
	time.Sleep(50 * time.Millisecond)
	if _, err = transfer(s, 32769); err != nil {
		t.Fatalf("independent stream blocked: %v", err)
	}
	if err = <-result; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("slow stream did not backpressure: %v", err)
	}
	stats := s.Snapshot()
	if stats.BufferedBytes > MaxBuffered || stats.PendingBytes > MaxDataPendingBytes+MaxControlBytes {
		t.Fatalf("unbounded buffers: %+v", stats)
	}
}

func TestTCPWrongKeyAndMissingSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend, count := echoBackend(t)
	srv, _ := NewServer(ctx, testToken, backend, 1)
	l, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	defer srv.Close()
	bad, _ := NewKey()
	if s, err := DialClient(ctx, []string{l.Addr().String()}, bad); err == nil {
		s.Close()
		t.Fatal("wrong key accepted")
	}
	c, err := PlainDial(ctx, l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	key, _ := ParseKey(testToken)
	var id sessionID
	rand.Read(id[:])
	if _, err = clientHandshake(c, key, id, 1, false); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("missing-session error: %v", err)
	}
	if count.Load() != 0 {
		t.Fatal("unauthenticated backend access")
	}
}

func TestStreamReorderingOverlapAndWindow(t *testing.T) {
	var id sessionID
	s := newSession(context.Background(), id, false, nil)
	defer s.Close()
	s.mu.Lock()
	st := s.newStreamLocked(1)
	st.open = true
	st.advertiseCreditLocked(time.Now())
	func() {
		defer s.mu.Unlock()
		if err := st.receiveLocked(3, []byte("def")); err != nil {
			t.Fatal(err)
		}
		if err := st.receiveLocked(0, []byte("abc")); err != nil {
			t.Fatal(err)
		}
		if err := st.receiveLocked(2, []byte("cde")); err != nil {
			t.Fatal(err)
		}
		if err := st.receiveLocked(2, []byte("X")); !errors.Is(err, ErrProtocol) {
			t.Fatal("conflicting overlap accepted")
		}
		if err := st.receiveLocked(StreamWindow, []byte("x")); !errors.Is(err, ErrProtocol) {
			t.Fatal("window overflow accepted")
		}
	}()
	out := make([]byte, 6)
	if _, err := io.ReadFull(st, out); err != nil || !bytes.Equal(out, []byte("abcdef")) {
		t.Fatalf("reorder: %q %v", out, err)
	}
	if s.Snapshot().ReorderPeak == 0 {
		t.Fatal("reorder telemetry missing")
	}
	st.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	if _, err := st.Read(out); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
}

func TestShortConnectionChurn(t *testing.T) {
	s, _, _, count := testTCP(t, 2, 0, 0)
	n := 100
	if os.Getenv("MPX_STRESS") == "1" {
		n = 2000
	}
	for i := 0; i < n; i++ {
		if _, err := transfer(s, 31+i%101); err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
	}
	if count.Load() != int64(n) || s.Snapshot().Connections != 0 {
		t.Fatalf("churn leak: count=%d stats=%+v", count.Load(), s.Snapshot())
	}
}
