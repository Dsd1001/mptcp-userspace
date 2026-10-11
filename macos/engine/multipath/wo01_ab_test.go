package multipath

// WO-01 isolated experimental fixture. No production networking and no WAN:
// backend, carrier listener, and six shaped relays bind 127.0.0.1 only.
// Usage: WO01_RUN=1 WO01_SCENARIO=hetero12 go test ./multipath -run '^TestWO01LocalAB$' -count=1 -v

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"runtime"
	"runtime/metrics"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type wo01Backend struct {
	addr           string
	closedNormally atomic.Int64
	failed         atomic.Int64
}

func wo01Payload(seq uint64) []byte {
	p := bytes.Repeat([]byte{0xa5}, 32<<10)
	binary.LittleEndian.PutUint64(p[:8], seq)
	return p
}
func wo01BackendStart(t *testing.T, bulkBytes int) *wo01Backend {
	t.Helper()
	l, err := PlainListen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	state := &wo01Backend{addr: l.Addr().String()}
	var mu sync.Mutex
	sockets := map[net.Conn]bool{}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			sockets[c] = true
			mu.Unlock()
			go func(conn net.Conn) {
				defer func() { mu.Lock(); delete(sockets, conn); mu.Unlock(); conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(115 * time.Second))
				var trigger [5]byte
				if _, err := io.ReadFull(conn, trigger[:]); err != nil {
					state.failed.Add(1)
					return
				}
				size := bulkBytes
				if trigger[0] == 2 {
					size = 32 << 10
				}
				chunk := wo01Payload(0)
				binary.LittleEndian.PutUint64(chunk[8:16], uint64(binary.LittleEndian.Uint32(trigger[1:])))
				for offset := 0; offset < size; offset += 32 << 10 {
					binary.LittleEndian.PutUint64(chunk[:8], uint64(offset/(32<<10)))
					if err := writeAll(conn, chunk[:min(len(chunk), size-offset)]); err != nil {
						state.failed.Add(1)
						return
					}
				}
				// Preserve the TCP receive half and wait for the peer FIN. A successful
				// Write does not imply all bytes were acknowledged by the kernel.
				if hc, ok := conn.(HalfConn); ok {
					if err := hc.CloseWrite(); err != nil {
						state.failed.Add(1)
						return
					}
				} else {
					state.failed.Add(1)
					return
				}
				_, err = io.Copy(io.Discard, conn)
				if err != nil {
					state.failed.Add(1)
					return
				}
				state.closedNormally.Add(1)
			}(c)
		}
	}()
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		for c := range sockets {
			c.Close()
		}
		mu.Unlock()
	})
	return state
}

func wo01ReadPayload(st *Stream, size int, streamID uint32, recv *atomic.Int64) error {
	buf := make([]byte, 32<<10)
	want := wo01Payload(0)
	binary.LittleEndian.PutUint64(want[8:16], uint64(streamID))
	for offset := 0; offset < size; offset += len(buf) {
		n := min(len(buf), size-offset)
		if _, err := io.ReadFull(st, buf[:n]); err != nil {
			return fmt.Errorf("received=%d/%d: %w", offset, size, err)
		}
		binary.LittleEndian.PutUint64(want[:8], uint64(offset/len(buf)))
		if !bytes.Equal(buf[:n], want[:n]) {
			return fmt.Errorf("payload mismatch offset=%d", offset)
		}
		recv.Add(int64(n))
	}
	return nil
}

// The exact same fixture compiles against frozen v1.1.5 (without B1 fields).
// Reflective extraction prevents accidentally changing baseline production code.
func wo01OptionalUintMetric(snap Stats, field string) uint64 {
	v := reflect.ValueOf(snap).FieldByName(field)
	if v.IsValid() && v.Kind() == reflect.Uint64 {
		return v.Uint()
	}
	return 0
}

func wo01FloatPercentile(sorted []float64, pct float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := int(float64(len(sorted)-1)*pct + 0.999999)
	return sorted[min(len(sorted)-1, pos)]
}

type wo01Result struct {
	Scenario              string  `json:"scenario"`
	Round                 string  `json:"round"`
	Streams               int     `json:"streams"`
	CompletedStreams      int     `json:"completed_streams"`
	CompletionTimingValid bool    `json:"completion_timing_valid"`
	BytesPerStream        int     `json:"bytes_per_stream"`
	Paths                 int     `json:"paths"`
	DelaysMS              []int   `json:"one_way_delay_ms"`
	RatesMbps             []int   `json:"configured_rates_mbps"`
	DeliveredBytes        int64   `json:"delivered_bytes"`
	ExpectedBytes         int64   `json:"expected_bytes"`
	Mbps                  float64 `json:"mbps"`
	ElapsedSeconds        float64 `json:"elapsed_seconds"`
	T50Seconds            float64 `json:"t50_seconds"`
	T90Seconds            float64 `json:"t90_seconds"`
	T99Seconds            float64 `json:"t99_seconds"`
	T100Seconds           float64 `json:"t100_seconds"`
	TailT99To100Seconds   float64 `json:"tail_t99_to_100_seconds"`
	ProbeP50MS            float64 `json:"probe_p50_ms"`
	ProbeP95MS            float64 `json:"probe_p95_ms"`
	// Historical Probe*MS values are 32KiB completion, not TTFB.
	ProbeTTFBP50MS          float64  `json:"probe_ttfb_p50_ms"`
	ProbeTTFBP95MS          float64  `json:"probe_ttfb_p95_ms"`
	ProbeTTFBP99MS          float64  `json:"probe_ttfb_p99_ms"`
	ProbeTTFBMaxMS          float64  `json:"probe_ttfb_max_ms"`
	ProbeOpenP95MS          float64  `json:"probe_open_p95_ms"`
	ProbeWriteP95MS         float64  `json:"probe_write_p95_ms"`
	ProbeWaitFirstP95MS     float64  `json:"probe_wait_first_p95_ms"`
	ClientFirstDataPriority uint64   `json:"client_first_data_priority_frames"`
	ProbeP99MS              float64  `json:"probe_p99_ms"`
	ProbeMaxMS              float64  `json:"probe_max_ms"`
	ProbeFailures           int      `json:"probe_failures"`
	Errors                  []string `json:"errors"`
	Retransmits             uint64   `json:"mpx_retransmits"`
	CostDeferral            uint64   `json:"cost_deferral"`
	FlightLimited           uint64   `json:"flight_limited"`
	CarrierQueueLimited     uint64   `json:"carrier_queue_limited"`
	PeakOutstanding         int      `json:"peak_outstanding"`
	PeakReady               int      `json:"peak_ready_bytes"`
	PeakPending             int      `json:"peak_pending_bytes"`
	PeakBudget              int      `json:"peak_budget_bytes"`
	PeakHeap                uint64   `json:"peak_heap_bytes"`
	WriterStallMaxMS        int      `json:"writer_stall_max_ms"`
	RuntimeCPUSeconds       float64  `json:"runtime_cpu_seconds"`
	Samples                 int      `json:"100ms_samples"`
	FirstDataPriority       uint64   `json:"first_data_priority_frames"`
	FirstDataFallbacks      uint64   `json:"first_data_fair_fallbacks"`
	PathSentBytes           []uint64 `json:"path_sent_bytes"`
	PathErrors              []uint64 `json:"path_errors"`
	BackendGraceful         int64    `json:"backend_graceful"`
	BackendFailures         int64    `json:"backend_failures"`
}

func wo01Scenario() (int, int, []int, []int, int, error) {
	scene := os.Getenv("WO01_SCENARIO")
	streams, mib, delay, pathN := 12, 16, 25, 6
	switch scene {
	case "single":
		streams = 1
		mib = 64
	case "uniform12":
		streams = 12
	case "hetero12":
		streams = 12
	case "synch150", "stagger150":
		streams = 150
		mib = 4
	case "high100":
		streams = 12
		delay = 100
	case "high300":
		streams = 12
		delay = 300
		mib = 8
	case "onecarrier":
		streams = 12
		pathN = 1
	case "eightcarriers":
		streams = 12
		pathN = 8
	default:
		return 0, 0, nil, nil, 0, fmt.Errorf("unknown scenario %q", scene)
	}
	if raw := os.Getenv("WO01_STREAM_MIB"); raw != "" {
		x, e := strconv.Atoi(raw)
		if e != nil || x < 1 || x > 128 {
			return 0, 0, nil, nil, 0, fmt.Errorf("invalid WO01_STREAM_MIB")
		}
		mib = x
	}
	ds := make([]int, pathN)
	rs := make([]int, pathN)
	for i := range ds {
		ds[i] = delay
		rs[i] = 92
	}
	if scene == "hetero12" {
		copy(ds, []int{5, 15, 25, 40, 65, 100})
	}
	if raw := os.Getenv("WO01_DELAYS"); raw != "" {
		parts := strings.Split(raw, ",")
		if len(parts) != pathN {
			return 0, 0, nil, nil, 0, fmt.Errorf("invalid delay count")
		}
		for i, x := range parts {
			v, e := strconv.Atoi(x)
			if e != nil || v < 0 || v > 1000 {
				return 0, 0, nil, nil, 0, fmt.Errorf("bad delay")
			}
			ds[i] = v
		}
	}
	if raw := os.Getenv("WO01_RATES"); raw != "" {
		parts := strings.Split(raw, ",")
		if len(parts) != pathN {
			return 0, 0, nil, nil, 0, fmt.Errorf("bad rates count")
		}
		for i, x := range parts {
			v, e := strconv.Atoi(x)
			if e != nil || v < 1 || v > 500 {
				return 0, 0, nil, nil, 0, fmt.Errorf("bad rate")
			}
			rs[i] = v
		}
	}
	return streams, mib, ds, rs, pathN, nil
}
func TestWO01LocalAB(t *testing.T) {
	if os.Getenv("WO01_RUN") != "1" {
		t.Skip("explicit opt-in: WO01_RUN=1")
	}
	streams, mib, ds, rs, pathN, err := wo01Scenario()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	size := mib << 20
	backend := wo01BackendStart(t, size)
	server, err := NewServer(ctx, testToken, backend.addr, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	landing, err := PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer landing.Close()
	go server.Serve(landing)
	addr := make([]string, pathN)
	cap := make([]PathCapacity, pathN)
	for i := range addr {
		addr[i] = highBDPRelay(t, landing.Addr().String(), int64(rs[i])*1000000/8, time.Duration(ds[i])*time.Millisecond)
		cap[i] = PathCapacity{DownloadMbps: float64(rs[i]), UploadMbps: float64(rs[i])}
	}
	client, err := DialClientWithPolicy(ctx, addr, testToken, SchedulerWeighted, cap)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	wait, wcancel := context.WithTimeout(ctx, 15*time.Second)
	if err = client.WaitPaths(wait, pathN); err != nil {
		wcancel()
		t.Fatal(err)
	}
	wcancel()
	peer := server.session(client.id)
	if peer == nil {
		t.Fatal("server session absent")
	}
	cpuBefore := []metrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}}
	metrics.Read(cpuBefore)
	result := wo01Result{
		Scenario: os.Getenv("WO01_SCENARIO"), Round: os.Getenv("WO01_ROUND"), Streams: streams,
		BytesPerStream: size, Paths: pathN, DelaysMS: ds, RatesMbps: rs,
		ExpectedBytes: int64(streams) * int64(size), PathSentBytes: make([]uint64, pathN), PathErrors: make([]uint64, pathN),
	}
	var recv atomic.Int64
	start := make(chan struct{})
	finish := make([]float64, streams)
	var wg sync.WaitGroup
	errCh := make(chan string, streams+32)
	started := time.Now()
	stopCapture := make(chan struct{})
	captureDone := make(chan struct{})
	go func() {
		defer close(captureDone)
		// started is written before start is closed. Waiting on this channel
		// establishes a happens-before edge for the timing base on all goroutines.
		<-start
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				snap := peer.Snapshot()
				ready, pending, flight, budget := snap.Resources.ReadyDataBytes, snap.Resources.DataPendingBytes, 0, 0
				for _, c := range snap.PathStats {
					flight += c.Outstanding
					budget += c.Budget
				}
				result.PeakOutstanding = max(result.PeakOutstanding, flight)
				result.PeakReady = max(result.PeakReady, ready)
				result.PeakPending = max(result.PeakPending, pending)
				result.PeakBudget = max(result.PeakBudget, budget)
				result.Samples++
				sample, _ := json.Marshal(map[string]any{
					"t_ms": time.Since(started).Milliseconds(), "delivered_bytes": recv.Load(),
					"ready_bytes": ready, "pending_bytes": pending, "flight_bytes": flight,
					"budget_bytes": budget, "cost_deferral": snap.Resources.PathSelection.CostDeferral,
					"flight_limited":        snap.Resources.PathSelection.FlightBudget,
					"carrier_queue_limited": snap.Resources.PathSelection.CarrierQueue,
					"mpx_retransmits":       snap.Retransmits, "path_stats": snap.PathStats,
					"first_data_priority_frames": wo01OptionalUintMetric(snap, "FirstDataPriorityFrames"),
					"first_data_fair_fallbacks":  wo01OptionalUintMetric(snap, "FirstDataFairFallbacks"),
				})
				t.Logf("WO01_SAMPLE %s", sample)
				var mem runtime.MemStats
				runtime.ReadMemStats(&mem)
				result.PeakHeap = max(result.PeakHeap, mem.HeapAlloc)
				peer.mu.Lock()
				for _, c := range peer.paths {
					ns := c.writeStartedNS.Load()
					if ns > 0 {
						result.WriterStallMaxMS = max(result.WriterStallMaxMS, int(time.Since(time.Unix(0, ns)).Milliseconds()))
					}
				}
				peer.mu.Unlock()
			case <-stopCapture:
				return
			}
		}
	}()
	for i := 0; i < streams; i++ {
		st, e := client.Open(ctx)
		if e != nil {
			close(stopCapture)
			<-captureDone
			t.Fatalf("open %d: %v", i, e)
		}
		if e = st.SetDeadline(time.Now().Add(110 * time.Second)); e != nil {
			t.Fatal(e)
		}
		wg.Add(1)
		go func(index int, stream *Stream) {
			defer wg.Done()
			defer stream.Close()
			<-start
			if result.Scenario == "stagger150" {
				time.Sleep(time.Duration(index*10) * time.Millisecond)
			}
			var trigger [5]byte
			trigger[0] = 1
			binary.LittleEndian.PutUint32(trigger[1:], uint32(index+1))
			if _, e := stream.Write(trigger[:]); e != nil {
				errCh <- fmt.Sprintf("bulk %d trigger: %v", index, e)
				return
			}
			if e := wo01ReadPayload(stream, size, uint32(index+1), &recv); e != nil {
				errCh <- fmt.Sprintf("bulk %d read: %v", index, e)
				return
			}
			finish[index] = time.Since(started).Seconds()
			// Do not close the client before its sender-side FIN and remote FIN are
			// both observed. Prevents a synthetic early close from becoming a false RST.
			if e := stream.CloseWrite(); e != nil {
				errCh <- fmt.Sprintf("bulk %d closewrite: %v", index, e)
				return
			}
			var b [1]byte
			if _, e := stream.Read(b[:]); e != io.EOF {
				errCh <- fmt.Sprintf("bulk %d eof: %v", index, e)
				return
			}
		}(i, st)
	}
	started = time.Now()
	close(start)
	probeValues := make([]float64, 0, 12)
	probeTTFB, probeOpen, probeWrite, probeWaitFirst := []float64{}, []float64{}, []float64{}, []float64{}
	for j := 0; j < 12; j++ {
		time.Sleep(170 * time.Millisecond)
		t0 := time.Now()
		st, e := client.Open(ctx)
		if e != nil {
			result.ProbeFailures++
			errCh <- fmt.Sprintf("probe %d open: %v", j, e)
			continue
		}
		openedAt := time.Now()
		st.SetDeadline(time.Now().Add(12 * time.Second))
		var probeTrigger [5]byte
		probeTrigger[0] = 2
		binary.LittleEndian.PutUint32(probeTrigger[1:], uint32(streams+j+1))
		if _, e = st.Write(probeTrigger[:]); e != nil {
			result.ProbeFailures++
			errCh <- fmt.Sprintf("probe %d write: %v", j, e)
			st.Close()
			continue
		}
		writeDoneAt := time.Now()
		buf := make([]byte, 32<<10)
		probeExpect := wo01Payload(0)
		binary.LittleEndian.PutUint64(probeExpect[8:16], uint64(streams+j+1))
		got, firstAt := 0, time.Time{}
		var readErr error
		for got < len(buf) {
			var n int
			n, readErr = st.Read(buf[got:])
			if n > 0 && firstAt.IsZero() {
				firstAt = time.Now()
			}
			got += n
			if readErr != nil {
				break
			}
		}
		fullAt := time.Now()
		if got != len(buf) || !bytes.Equal(buf, probeExpect) {
			result.ProbeFailures++
			errCh <- fmt.Sprintf("probe %d read %d/32768: %v", j, got, readErr)
		} else {
			fullMS := fullAt.Sub(t0).Seconds() * 1000
			firstMS := firstAt.Sub(t0).Seconds() * 1000
			probeValues = append(probeValues, fullMS)
			probeTTFB = append(probeTTFB, firstMS)
			probeOpen = append(probeOpen, openedAt.Sub(t0).Seconds()*1000)
			probeWrite = append(probeWrite, writeDoneAt.Sub(openedAt).Seconds()*1000)
			probeWaitFirst = append(probeWaitFirst, firstAt.Sub(writeDoneAt).Seconds()*1000)
			t.Logf("WO01_PROBE_DETAIL idx=%d open_ms=%.3f write_ms=%.3f wait_first_ms=%.3f ttfb_ms=%.3f full32_ms=%.3f",
				j, probeOpen[len(probeOpen)-1], probeWrite[len(probeWrite)-1], probeWaitFirst[len(probeWaitFirst)-1], firstMS, fullMS)
		}
		if e = st.CloseWrite(); e != nil {
			errCh <- fmt.Sprintf("probe %d closewrite: %v", j, e)
		}
		var rest [1]byte
		if _, e = st.Read(rest[:]); e != io.EOF {
			errCh <- fmt.Sprintf("probe %d eof: %v", j, e)
		}
		st.Close()
	}
	wg.Wait()
	close(stopCapture)
	<-captureDone
	close(errCh)
	for e := range errCh {
		result.Errors = append(result.Errors, e)
	}
	result.DeliveredBytes = recv.Load()
	completed := make([]float64, 0, len(finish))
	for _, sec := range finish {
		if sec > 0 {
			completed = append(completed, sec)
		}
	}
	result.CompletedStreams = len(completed)
	result.CompletionTimingValid = result.CompletedStreams == streams
	sort.Float64s(completed)
	result.T50Seconds = wo01FloatPercentile(completed, .5)
	result.T90Seconds = wo01FloatPercentile(completed, .9)
	result.T99Seconds = wo01FloatPercentile(completed, .99)
	result.T100Seconds = wo01FloatPercentile(completed, 1)
	result.TailT99To100Seconds = result.T100Seconds - result.T99Seconds
	result.ElapsedSeconds = result.T100Seconds
	if result.ElapsedSeconds > 0 {
		result.Mbps = float64(result.DeliveredBytes) * 8 / 1e6 / result.ElapsedSeconds
	}
	sort.Float64s(probeValues)
	sort.Float64s(probeTTFB)
	sort.Float64s(probeOpen)
	sort.Float64s(probeWrite)
	sort.Float64s(probeWaitFirst)
	result.ProbeTTFBP50MS = wo01FloatPercentile(probeTTFB, .50)
	result.ProbeTTFBP95MS = wo01FloatPercentile(probeTTFB, .95)
	result.ProbeTTFBP99MS = wo01FloatPercentile(probeTTFB, .99)
	result.ProbeTTFBMaxMS = wo01FloatPercentile(probeTTFB, 1)
	result.ProbeOpenP95MS = wo01FloatPercentile(probeOpen, .95)
	result.ProbeWriteP95MS = wo01FloatPercentile(probeWrite, .95)
	result.ProbeWaitFirstP95MS = wo01FloatPercentile(probeWaitFirst, .95)
	result.ClientFirstDataPriority = wo01OptionalUintMetric(client.Snapshot(), "FirstDataPriorityFrames")
	result.ProbeP50MS = wo01FloatPercentile(probeValues, .50)
	result.ProbeP95MS = wo01FloatPercentile(probeValues, .95)
	result.ProbeP99MS = wo01FloatPercentile(probeValues, .99)
	result.ProbeMaxMS = wo01FloatPercentile(probeValues, 1)
	snap := peer.Snapshot()
	result.FirstDataPriority = wo01OptionalUintMetric(snap, "FirstDataPriorityFrames")
	result.FirstDataFallbacks = wo01OptionalUintMetric(snap, "FirstDataFairFallbacks")
	result.Retransmits = snap.Retransmits
	result.CostDeferral = snap.Resources.PathSelection.CostDeferral
	result.FlightLimited = snap.Resources.PathSelection.FlightBudget
	result.CarrierQueueLimited = snap.Resources.PathSelection.CarrierQueue
	for i, c := range snap.PathStats {
		if i < pathN {
			result.PathSentBytes[i] = c.Sent
			result.PathErrors[i] = c.Errors
		}
	}
	// Allow the kernel TCP peer-close path to finish after all client FINs.
	deadline := time.Now().Add(3 * time.Second)
	for backend.closedNormally.Load()+backend.failed.Load() < int64(streams+12) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cpuAfter := []metrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}}
	metrics.Read(cpuAfter)
	if cpuBefore[0].Value.Kind() == metrics.KindFloat64 && cpuAfter[0].Value.Kind() == metrics.KindFloat64 {
		result.RuntimeCPUSeconds = cpuAfter[0].Value.Float64() - cpuBefore[0].Value.Float64()
	}
	result.BackendGraceful = backend.closedNormally.Load()
	result.BackendFailures = backend.failed.Load()
	if result.DeliveredBytes != result.ExpectedBytes {
		result.Errors = append(result.Errors, "delivery mismatch")
	}
	if !result.CompletionTimingValid {
		result.Errors = append(result.Errors, "incomplete Stream completion timing")
	}
	if result.BackendFailures != 0 {
		result.Errors = append(result.Errors, "backend close errors")
	}
	if result.BackendGraceful != int64(streams+12) {
		result.Errors = append(result.Errors, "incomplete backend close handshakes")
	}
	out, _ := json.Marshal(result)
	t.Logf("WO01_RESULT %s", out)
	if len(result.Errors) != 0 {
		t.Fatalf("WO01 failed: %v", result.Errors)
	}
}
