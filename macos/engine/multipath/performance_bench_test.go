package multipath

import (
	"io"
	"net"
	"testing"
	"time"
)

// BenchmarkPendingDataLookup compares the per-Stream counter with the old
// session-wide map scan. The latter is retained only as a benchmark baseline;
// production code uses the counter-backed method.
var benchmarkPendingData bool

func BenchmarkPendingDataLookup(b *testing.B) {
	s := schedulerFixture()
	st := s.newStreamLocked(1)
	for i := uint64(0); i < 4096; i++ {
		s.pending[i+1] = &outbound{f: frame{kind: kindData, stream: 3, id: i + 1}}
	}
	b.Run("counter", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkPendingData = st.hasPendingDataLocked()
		}
	})
	b.Run("session_map_scan_baseline", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			found := false
			for _, p := range s.pending {
				if p.f.stream == st.id && p.f.kind == kindData {
					found = true
					break
				}
			}
			benchmarkPendingData = found
		}
	})
}

type benchmarkSinkConn struct{}

func (benchmarkSinkConn) Read([]byte) (int, error)          { return 0, io.EOF }
func (benchmarkSinkConn) Write(p []byte) (int, error)       { return len(p), nil }
func (benchmarkSinkConn) Close() error                      { return nil }
func (benchmarkSinkConn) LocalAddr() net.Addr               { return benchmarkAddr("local") }
func (benchmarkSinkConn) RemoteAddr() net.Addr              { return benchmarkAddr("remote") }
func (benchmarkSinkConn) SetDeadline(_ time.Time) error     { return nil }
func (benchmarkSinkConn) SetReadDeadline(_ time.Time) error { return nil }
func (benchmarkSinkConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

type benchmarkAddr string

func (a benchmarkAddr) Network() string { return "benchmark" }
func (a benchmarkAddr) String() string  { return string(a) }

func BenchmarkSecureWriteFramesBatch(b *testing.B) {
	key, err := ParseKey(testToken)
	if err != nil {
		b.Fatal(err)
	}
	control := make([]frame, carrierBatchFrames)
	for i := range control {
		control[i] = frame{kind: kindWindow, stream: uint64(2*i + 1), offset: StreamWindow, id: StreamWindow}
	}
	data := make([]frame, 4)
	for i := range data {
		data[i] = frame{kind: kindData, stream: 1, offset: uint64(i * MaxPayload), id: uint64(i + 1), data: make([]byte, MaxPayload)}
	}
	for _, tc := range []struct {
		name   string
		frames []frame
	}{
		{"control_64", control},
		{"data_128KiB", data},
	} {
		b.Run(tc.name, func(b *testing.B) {
			c, err := newSecure(benchmarkSinkConn{}, key, []byte("secure-write-benchmark"), true)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := c.writeFrames(tc.frames); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
