package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"

	"mptcp-desktop/engine/multipath"
)

// Userspace telemetry never reads native PCB state. All carrier, local and
// backend TCP sockets in this path explicitly disable native MPTCP.
const userspaceDesiredNOFILE = 16384

func ensureUserspaceFileLimit() error {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return fmt.Errorf("读取 RLIMIT_NOFILE 失败: %w", err)
	}
	need := uint64(multipath.MaxStreams + 512)
	if lim.Cur >= need {
		return nil
	}
	if lim.Max < need {
		return fmt.Errorf("macOS/Linux 进程文件描述符硬上限过低: soft=%d hard=%d need>=%d", lim.Cur, lim.Max, need)
	}
	target := uint64(userspaceDesiredNOFILE)
	if target < need {
		target = need
	}
	if target > lim.Max {
		target = lim.Max
	}
	lim.Cur = target
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return fmt.Errorf("提升 RLIMIT_NOFILE 失败: %w", err)
	}
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return fmt.Errorf("复核 RLIMIT_NOFILE 失败: %w", err)
	}
	if lim.Cur < need {
		return fmt.Errorf("进程文件描述符上限不足: soft=%d need>=%d", lim.Cur, need)
	}
	return nil
}

func runUserspace(parent context.Context, c Config) (runErr error) {
	if err := ensureUserspaceFileLimit(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(c.ListenPort))
	var listener net.Listener
	var err error
	if c.tcpEnabled() {
		listener, err = multipath.PlainListen(ctx, address)
		if err != nil {
			return err
		}
		defer listener.Close()
	}
	addresses := make([]string, len(c.Relays))
	for i, r := range c.Relays {
		addresses[i] = net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
	}
	emit(Event{Kind: "connecting", Mode: c.Mode, Message: "正在建立 Userspace 会话并认证独立 Landing 聚合入口；不使用内核 MPTCP"})
	mode, modeErr := c.schedulerMode()
	if modeErr != nil {
		return modeErr
	}
	var capacities []multipath.PathCapacity
	if mode == multipath.SchedulerWeighted {
		capacities, err = c.weightedCapacities()
		if err != nil {
			return err
		}
	}
	session, err := multipath.DialClientWithPolicy(ctx, addresses, c.TransportKey, mode, capacities)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer func() {
		session.Close()
		s := session.Snapshot()
		message := "Userspace forwarding stopped"
		if runErr != nil {
			message = runErr.Error()
		}
		emit(Event{SchedulerStats: s.SchedulerStats, Kind: "transport_closed", Mode: c.Mode, Message: message, Resources: &s.Resources, Lifecycle: &s.Lifecycle, Version: multipath.Version, SourceID: multipath.SourceID, WireProtocol: multipath.WireProtocol})
	}()
	// Startup can succeed with one surviving path. Remaining paths reconnect;
	// telemetry distinguishes partial availability from aggregation success.
	var udp *multipath.UDPClient
	var udpDone <-chan struct{}
	if c.UDPEnabled {
		udp, err = multipath.StartClientUDP(session, c.TransportKey, address, addresses)
		if err != nil {
			return fmt.Errorf("UDP 入口启动失败: %w", err)
		}
		defer udp.Close()
		probe, stop := context.WithTimeout(ctx, 6*time.Second)
		err = udp.WaitPaths(probe, 1)
		stop()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("没有通过独立 UDP 认证的 Relay 路径；检查 UDP 转发端口: %w", err)
		}
		udpDone = udp.Done()
		emit(Event{Kind: "udp_listening", Mode: c.Mode, Message: "UDP 独立数据报入口 " + address})
	}
	emitStats := func() {
		s := session.Snapshot()
		emit(Event{SchedulerStats: s.SchedulerStats, Kind: "stats", Mode: c.Mode, Paths: s.Paths, Connections: int64(s.Connections), Sent: int64(s.Sent), Received: int64(s.Received), PathStats: s.PathStats, ReorderBytes: s.ReorderBytes, ReorderPeak: s.ReorderPeak, PendingBytes: s.PendingBytes, Retransmits: s.Retransmits, WindowWaits: s.WindowWaits, ReceiveCredit: s.ReceiveCredit, ReceiveAllocated: s.ReceiveAllocated, WindowTarget: s.WindowTarget, ReadyFrames: s.ReadyFrames, Resources: &s.Resources, Lifecycle: &s.Lifecycle, Version: multipath.Version, SourceID: multipath.SourceID, WireProtocol: multipath.WireProtocol})
		if udp != nil {
			u := udp.Snapshot()
			emit(Event{Kind: "udp_stats", Mode: c.Mode, Paths: u.Paths, Connections: int64(u.Connections), Sent: int64(u.Sent), Received: int64(u.Received), PathStats: u.PathStats, Dropped: u.Dropped})
		}
	}
	emit(Event{Kind: "ready", Mode: c.Mode, Version: multipath.Version, SourceID: multipath.SourceID, WireProtocol: multipath.WireProtocol, Message: "Userspace 认证通过；实际带宽叠加取决于独立链路容量，当前不作为测速结论"})
	emit(Event{Kind: "listening", Mode: c.Mode, Paths: session.Snapshot().Paths, Message: address})
	emitStats()
	var acceptDone <-chan struct{}
	var acceptErr error
	if listener != nil {
		done := make(chan struct{})
		acceptDone = done
		go func() { acceptErr = serveUserspace(ctx, listener, session); close(done) }()
		defer func() { cancel(); listener.Close(); <-done }()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-session.Done():
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("Userspace 会话结束，现有逻辑连接未被隐式重建；请重新启动: %w", session.Err())
		case <-udpDone:
			if ctx.Err() != nil {
				return nil
			}
			if udp.Err() != nil {
				return udp.Err()
			}
			return errors.New("UDP 数据面意外结束")
		case <-acceptDone:
			return acceptErr
		case <-ticker.C:
			emitStats()
		}
	}
}

func serveUserspace(parent context.Context, listener net.Listener, session *multipath.Session) error {
	ctx, cancel := context.WithCancel(parent)
	var mu sync.Mutex
	locals := make(map[net.Conn]bool)
	var workers sync.WaitGroup
	closeAll := func() {
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for c := range locals {
			c.Close()
		}
	}
	stopClose := context.AfterFunc(ctx, closeAll)
	defer func() { cancel(); closeAll(); workers.Wait(); stopClose() }()
	slots := make(chan struct{}, multipath.MaxStreams)
	var acceptWarningAt time.Time
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if multipath.TemporaryAcceptError(err) {
				session.NoteLocalResource(multipath.LimitLocalAccept, true)
				if time.Since(acceptWarningAt) >= time.Second {
					emit(Event{Kind: "warning", Mode: "userspace_multipath", ResourceReason: multipath.LimitLocalAccept, Message: "Temporary local accept pressure; established session preserved: " + err.Error()})
					acceptWarningAt = time.Now()
				}
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil
				case <-timer.C:
				}
				continue
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			session.NoteLocalResource(multipath.LimitLocalConnections, false)
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
		session.NoteLocalConnection(1)
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() {
				conn.Close()
				mu.Lock()
				delete(locals, conn)
				mu.Unlock()
				session.NoteLocalConnection(-1)
				<-slots
			}()
			local, ok := conn.(multipath.HalfConn)
			if !ok {
				return
			}
			// OPEN consumes only identity/control resources. The reliable OPEN
			// lifetime and backend dial remain bounded; credit waits occur in Write.
			stream, err := session.Open(ctx)
			if err != nil {
				if ctx.Err() == nil {
					emit(Event{Kind: "warning", Mode: "userspace_multipath", Message: err.Error(), ResourceReason: multipath.ResourceReason(err)})
				}
				return
			}
			multipath.Bridge(ctx, local, stream, 15*time.Minute)
		}()
	}
}
