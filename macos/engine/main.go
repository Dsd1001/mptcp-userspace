package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mptcp-desktop/engine/multipath"
)

type Relay struct {
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	DownloadMbps *float64 `json:"download_mbps,omitempty"`
	UploadMbps   *float64 `json:"upload_mbps,omitempty"`
}
type Config struct {
	SchedulerMode *string `json:"scheduler_mode,omitempty"`
	SchemaVersion int     `json:"schema_version"`
	Mode          string  `json:"mode"`
	ListenPort    int     `json:"listen_port"`
	Relays        []Relay `json:"relays"`
	UDPEnabled    bool    `json:"udp_enabled,omitempty"`
	TCPEnabled    *bool   `json:"tcp_enabled,omitempty"`
	TransportKey  string  `json:"transport_key,omitempty"`
}

func (c Config) schedulerMode() (multipath.SchedulerMode, error) {
	if c.SchedulerMode == nil {
		return multipath.SchedulerAuto, nil
	}
	if *c.SchedulerMode == "" {
		return "", errors.New("scheduler_mode cannot be empty when present")
	}
	return multipath.ParseSchedulerMode(*c.SchedulerMode)
}

func (c Config) userspace() bool  { return c.SchemaVersion == 3 && c.Mode == "userspace_multipath" }
func (c Config) tcpEnabled() bool { return c.TCPEnabled == nil || *c.TCPEnabled }

func (c Config) weightedCapacities() ([]multipath.PathCapacity, error) {
	mode, err := c.schedulerMode()
	if err != nil || mode != multipath.SchedulerWeighted {
		return nil, err
	}
	out := make([]multipath.PathCapacity, len(c.Relays))
	for i, r := range c.Relays {
		if r.DownloadMbps == nil {
			return nil, fmt.Errorf("Relay %d Weighted 下行 Mbps 必填", i+1)
		}
		out[i].DownloadMbps = *r.DownloadMbps
		if r.UploadMbps != nil {
			if *r.UploadMbps == 0 {
				return nil, fmt.Errorf("Relay %d Weighted 上行 Mbps 留空表示自动估算；显式 0 无效", i+1)
			}
			out[i].UploadMbps = *r.UploadMbps
		}
	}
	return out, nil
}

type Event struct {
	CapabilityRevision int `json:"capability_revision,omitempty"`
	multipath.SchedulerStats
	Kind             string                    `json:"kind"`
	Message          string                    `json:"message,omitempty"`
	Paths            int                       `json:"paths"`
	Connections      int64                     `json:"connections"`
	Sent             int64                     `json:"sent"`
	Received         int64                     `json:"received"`
	Mode             string                    `json:"mode,omitempty"`
	PathStats        []multipath.PathStats     `json:"path_stats,omitempty"`
	ReorderBytes     int                       `json:"reorder_bytes,omitempty"`
	ReorderPeak      int                       `json:"reorder_peak,omitempty"`
	PendingBytes     int                       `json:"pending_bytes,omitempty"`
	Retransmits      uint64                    `json:"retransmits,omitempty"`
	Dropped          uint64                    `json:"dropped,omitempty"`
	WindowWaits      uint64                    `json:"window_waits,omitempty"`
	ReceiveCredit    int                       `json:"receive_credit_bytes,omitempty"`
	ReceiveAllocated int                       `json:"receive_allocated_bytes,omitempty"`
	WindowTarget     int                       `json:"max_stream_window_target,omitempty"`
	ReadyFrames      int                       `json:"ready_frames,omitempty"`
	Version          string                    `json:"version,omitempty"`
	SourceID         string                    `json:"source_id,omitempty"`
	WireProtocol     int                       `json:"wire_protocol,omitempty"`
	ResourceReason   string                    `json:"resource_reason,omitempty"`
	Resources        *multipath.ResourceStats  `json:"resources,omitempty"`
	Lifecycle        *multipath.LifecycleStats `json:"lifecycle,omitempty"`
}

var eventMu sync.Mutex

func emit(e Event) {
	e.CapabilityRevision = multipath.CapabilityRevision
	eventMu.Lock()
	defer eventMu.Unlock()
	json.NewEncoder(os.Stdout).Encode(e)
}

func (c Config) validate() error {
	legacy := c.SchemaVersion == 2 && c.Mode == "tcp_forward"
	if !legacy && !(c.SchemaVersion == 3 && (c.Mode == "native_mptcp" || c.Mode == "userspace_multipath")) {
		return errors.New("支持 schema 2/tcp_forward（保持 Native）或 schema 3/userspace_multipath、native_mptcp；不支持旧 SOCKS5 配置")
	}
	if c.userspace() {
		mode, err := c.schedulerMode()
		if err != nil {
			return err
		}
		if mode == multipath.SchedulerWeighted {
			caps, err := c.weightedCapacities()
			if err != nil {
				return err
			}
			for i, cap := range caps {
				if err := multipath.ValidatePathCapacity(cap); err != nil {
					return fmt.Errorf("Relay %d: %w", i+1, err)
				}
			}
		}
		if _, err := multipath.ParseKey(c.TransportKey); err != nil {
			return err
		}
	}
	if !c.tcpEnabled() && !c.UDPEnabled {
		return errors.New("TCP 和 UDP 不能同时关闭")
	}
	if !c.userspace() && !c.tcpEnabled() {
		return errors.New("Native 兼容模式需保留 TCP；仅 UDP 可使用 Userspace 模式")
	}
	if c.ListenPort < 1024 || c.ListenPort > 65535 {
		return errors.New("本地端口必须为 1024-65535")
	}
	if len(c.Relays) < 2 || len(c.Relays) > 8 {
		return errors.New("请配置 2-8 台 Relay")
	}
	seen := map[string]bool{}
	for _, r := range c.Relays {
		ip := net.ParseIP(r.Host)
		identity := r.Host
		if c.userspace() {
			identity = net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
		}
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() || r.Port < 1 || r.Port > 65535 || seen[identity] {
			return errors.New("Relay 需要有效且不重复的 IPv4 和端口")
		}
		if ip.IsLoopback() && r.Port == c.ListenPort {
			return errors.New("Relay 不能指向本地转发入口")
		}
		seen[identity] = true
	}
	return nil
}
func readConfig(reader io.Reader) (Config, error) {
	var c Config
	data, err := io.ReadAll(io.LimitReader(reader, 32769))
	if err != nil {
		return c, err
	}
	if len(data) > 32768 {
		return c, errors.New("配置文件过大")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return c, errors.New("配置必须是单个 JSON 对象")
	}
	return c, c.validate()
}

type streamConn interface {
	net.Conn
	CloseWrite() error
}
type forwardConn interface {
	streamConn
	paths() int
}
type tunnelDialer func(context.Context) (forwardConn, error)
type counters struct{ sent, received atomic.Int64 }
type activityWriter struct {
	conn        net.Conn
	count, last *atomic.Int64
}

func (w activityWriter) Write(data []byte) (int, error) {
	if err := w.conn.SetWriteDeadline(time.Now().Add(45 * time.Second)); err != nil {
		return 0, err
	}
	n, err := w.conn.Write(data)
	if n > 0 {
		w.count.Add(int64(n))
		w.last.Store(time.Now().UnixNano())
	}
	return n, err
}

// Each accepted byte is copied unchanged. No TLS, SOCKS greeting or framing is added.
func bridge(ctx context.Context, a, b streamConn, totals *counters, idle time.Duration) {
	defer a.Close()
	defer b.Close()
	var activity atomic.Int64
	activity.Store(time.Now().UnixNano())
	done := make(chan error, 2)
	copyDirection := func(dst, src streamConn, count *atomic.Int64) {
		_, err := io.CopyBuffer(activityWriter{conn: dst, count: count, last: &activity}, src, make([]byte, 32*1024))
		if err == nil {
			err = dst.CloseWrite()
		}
		done <- err
	}
	go copyDirection(b, a, &totals.sent)
	go copyDirection(a, b, &totals.received)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	cancelled := ctx.Done()
	completed := 0
	for completed < 2 {
		select {
		case err := <-done:
			completed++
			if err != nil {
				a.Close()
				b.Close()
			}
		case <-cancelled:
			a.Close()
			b.Close()
			cancelled = nil
		case now := <-ticker.C:
			if idle > 0 && now.Sub(time.Unix(0, activity.Load())) >= idle {
				a.Close()
				b.Close()
			}
		}
	}
}

func serveForward(ctx context.Context, listener net.Listener, dial tunnelDialer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	var mu sync.Mutex
	locals := map[net.Conn]bool{}
	active := map[forwardConn]bool{}
	var totals counters
	closeConnections := func() {
		listener.Close()
		mu.Lock()
		connections := make([]net.Conn, 0, len(locals)+len(active))
		for c := range locals {
			connections = append(connections, c)
		}
		for c := range active {
			connections = append(connections, c)
		}
		mu.Unlock()
		for _, c := range connections {
			c.Close()
		}
	}
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var retryStatsAt time.Time
		lastDiagnostic := ""
		for {
			select {
			case <-ctx.Done():
				closeConnections()
				return
			case <-ticker.C:
				mu.Lock()
				count := len(active)
				connections := make([]forwardConn, 0, count)
				for c := range active {
					connections = append(connections, c)
				}
				mu.Unlock()
				paths := 0
				if count > 0 {
					paths = 8
				}
				if count > 0 {
					if time.Now().Before(retryStatsAt) {
						paths = -1
					} else {
						counts, diagnostic := nativePathCounts(connections)
						paths = minimumPathCount(counts)
						if diagnostic != nil {
							if errors.Is(diagnostic, errPCBRead) {
								retryStatsAt = time.Now().Add(30 * time.Second)
							}
							if diagnostic.Error() != lastDiagnostic {
								emit(Event{Kind: "warning", Message: fmt.Sprintf("子流统计暂不可用（不影响转发，将自动重试）: %v", diagnostic)})
							}
							lastDiagnostic = diagnostic.Error()
						} else {
							lastDiagnostic = ""
						}
					}
				}
				emit(Event{Kind: "stats", Paths: paths, Connections: int64(count), Sent: totals.sent.Load(), Received: totals.received.Load()})
			}
		}
	}()
	defer func() { cancel(); <-monitorDone; workers.Wait() }()
	slots := make(chan struct{}, 128)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		mu.Lock()
		if ctx.Err() != nil {
			mu.Unlock()
			conn.Close()
			<-slots
			return nil
		}
		locals[conn] = true
		mu.Unlock()
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			defer func() { conn.Close(); mu.Lock(); delete(locals, conn); mu.Unlock() }()
			local, ok := conn.(streamConn)
			if !ok {
				return
			}
			remote, err := dial(ctx)
			if err != nil {
				if ctx.Err() == nil {
					emit(Event{Kind: "warning", Message: err.Error()})
				}
				return
			}
			defer remote.Close()
			mu.Lock()
			if ctx.Err() != nil {
				mu.Unlock()
				return
			}
			active[remote] = true
			mu.Unlock()
			defer func() { mu.Lock(); delete(active, remote); mu.Unlock() }()
			bridge(ctx, local, remote, &totals, 15*time.Minute)
		}()
	}
}

func runClient(ctx context.Context, c Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := c.validate(); err != nil {
		return err
	}
	if c.userspace() {
		return runUserspace(ctx, c)
	}
	if err := nativePreflight(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.ListenPort)))
	if err != nil {
		return err
	}
	defer listener.Close()
	emit(Event{Kind: "connecting", Message: "正在检查原生 MPTCP v1 握手"})
	probe, err := nativeDial(ctx, c.Relays)
	if err != nil {
		return err
	}
	// nativeDial already verifies this socket's MPTCP v1 handshake. The global
	// PCB list is optional telemetry and must never gate forwarding startup.
	probe.Close()
	emit(Event{Kind: "ready", Message: "MPTCP v1 握手通过；多路径聚合尚未验证"})
	if c.UDPEnabled {
		udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: c.ListenPort})
		if err != nil {
			return fmt.Errorf("UDP 入口启动失败: %w", err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := serveUDP(ctx, udp, c.Relays, 60*time.Second, 512); err != nil && ctx.Err() == nil {
				emit(Event{Kind: "error", Message: fmt.Sprintf("UDP 转发已停止: %v", err)})
				cancel()
			}
		}()
		defer func() { cancel(); udp.Close(); <-done }()
		emit(Event{Kind: "udp_listening", Message: "UDP 轮询入口 " + udp.LocalAddr().String()})
	}
	emit(Event{Kind: "listening", Message: listener.Addr().String()})
	return serveForward(ctx, listener, func(ctx context.Context) (forwardConn, error) { return nativeDial(ctx, c.Relays) })
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch {
	case len(os.Args) == 2 && os.Args[1] == "doctor":
		err = nativePreflight()
		if err == nil {
			emit(Event{Kind: "ready", Message: "原生 MPTCP 聚合接口可用"})
		}
	case len(os.Args) == 2 && os.Args[1] == "doctor-userspace":
		err = ensureUserspaceFileLimit()
		if err == nil {
			emit(Event{Kind: "ready", Mode: "userspace_multipath", Message: fmt.Sprintf("Userspace 引擎可用；RLIMIT_NOFILE 已提升/满足 %d，可承载 %d 业务流；尚未检查 Relay、Landing 密钥和端口", userspaceDesiredNOFILE, multipath.MaxStreams)})
		}
	case len(os.Args) == 2 && os.Args[1] == "version":
		emit(Event{Kind: "ready", Version: multipath.Version, SourceID: multipath.SourceID, WireProtocol: multipath.WireProtocol, Message: "mptcp-desktop-engine " + multipath.Version + " MPX/3 experimental + Native fallback"})
	case len(os.Args) == 2 && os.Args[1] == "run":
		var c Config
		c, err = readConfig(os.Stdin)
		if err == nil {
			err = runClient(ctx, c)
		}
	case len(os.Args) == 2 && os.Args[1] == "validate":
		_, err = readConfig(os.Stdin)
		if err == nil {
			emit(Event{Kind: "ready", Message: "TCP 转发配置有效"})
		}
	default:
		err = fmt.Errorf("usage: mptcp-desktop-engine doctor | doctor-userspace | version | run | validate (JSON stdin)")
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		emit(Event{Kind: "error", Message: err.Error()})
		os.Exit(1)
	}
}
