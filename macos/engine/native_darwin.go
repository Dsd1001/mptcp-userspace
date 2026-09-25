package main

/*
#include <sys/socket.h>
#include <sys/ioctl.h>
#include <sys/sysctl.h>
#include <net/if.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <arpa/inet.h>
#include <poll.h>
#include <fcntl.h>
#include <unistd.h>
#include <errno.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    uint32_t cid, flags, ifindex;
    int32_t error;
    struct sockaddr *src;
    socklen_t src_len;
    struct sockaddr *dst;
    socklen_t dst_len;
    uint32_t aux_type;
    void *aux;
    uint32_t aux_len;
} DesktopConnectionInfo;

static int desktop_socket(void) {
    int fd = socket(39, SOCK_STREAM, IPPROTO_TCP);
    if (fd < 0) return -errno;
    int mode = 2, one = 1;
    if (setsockopt(fd, IPPROTO_TCP, 0x213, &mode, sizeof(mode)) < 0) {
        int e = errno; close(fd); return -e;
    }
    if (setsockopt(fd, IPPROTO_TCP, 0x21a, &one, sizeof(one)) < 0 ||
        setsockopt(fd, IPPROTO_TCP, 0x217, &one, sizeof(one)) < 0 ||
        setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one)) < 0 ||
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof(one)) < 0 ||
        fcntl(fd, F_SETFL, O_NONBLOCK) < 0) {
        int e = errno; close(fd); return -e;
    }
    fcntl(fd, F_SETFD, FD_CLOEXEC);
    return fd;
}
static int desktop_connect(int fd, const char *ip, int port, uint32_t *cid) {
    struct sockaddr_in address = {0};
    address.sin_len = sizeof(address);
    address.sin_family = AF_INET;
    address.sin_port = htons(port);
    if (inet_pton(AF_INET, ip, &address.sin_addr) != 1) return EINVAL;
    sa_endpoints_t ep = {0};
    ep.sae_dstaddr = (struct sockaddr *)&address;
    ep.sae_dstaddrlen = sizeof(address);
    if (connectx(fd, &ep, SAE_ASSOCID_ANY, 0, NULL, 0, NULL, cid) < 0) return errno;
    return 0;
}
static int desktop_info(int fd, uint32_t cid, uint32_t *flags) {
    DesktopConnectionInfo info = {0};
    info.cid = cid;
    if (ioctl(fd, _IOWR('s', 152, DesktopConnectionInfo), &info) < 0) return errno;
    *flags = info.flags;
    return info.error;
}
static int desktop_primary(int fd, void *tuple, unsigned *index) {
    DesktopConnectionInfo info = {0};
    struct sockaddr_in src = {0}, dst = {0};
    info.cid = SAE_CONNID_ANY;
    info.src = (struct sockaddr *)&src; info.src_len = sizeof(src);
    info.dst = (struct sockaddr *)&dst; info.dst_len = sizeof(dst);
    if (ioctl(fd, _IOWR('s', 152, DesktopConnectionInfo), &info) < 0) return errno;
    *index = info.ifindex;
    if (tuple) {
        if (src.sin_family != AF_INET || dst.sin_family != AF_INET) return EAFNOSUPPORT;
        unsigned char *p = tuple;
        memcpy(p, &src.sin_addr, 4); memcpy(p+4, &src.sin_port, 2);
        memcpy(p+6, &dst.sin_addr, 4); memcpy(p+10, &dst.sin_port, 2);
    }
    return info.error;
}
static int desktop_pcbs(size_t limit, void **output, size_t *length) {
    *output = NULL;
    *length = 0;
    if (!limit || limit > 16*1024*1024) return EINVAL;
    for (int i = 0; i < 3; i++) {
        size_t size = 0;
        if (sysctlbyname("net.inet.mptcp.pcblist", NULL, &size, NULL, 0) < 0) return errno;
        if (size == 0) { *length = 0; return 0; }
        // Darwin's estimate includes spare flows and can exceed actual output.
        // Try a bounded buffer even when the estimate exceeds our limit.
        if (size > limit) size = limit;
        size_t capacity = size;
        void *buffer = malloc(size);
        if (!buffer) return ENOMEM;
        if (sysctlbyname("net.inet.mptcp.pcblist", buffer, &size, NULL, 0) == 0) {
            *output = buffer; *length = size; return 0;
        }
        int error = errno; free(buffer);
        if (error != ENOMEM) return error;
        if (capacity == limit) return EOVERFLOW;
    }
    return EAGAIN;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
	"unsafe"
)

type nativeConn struct {
	file   *os.File
	remote net.Addr
	tuple  flowTuple
}

func nativePreflight() error {
	fd := int(C.desktop_socket())
	if fd < 0 {
		return nativeError(-fd)
	}
	syscall.Close(fd)
	return nil
}
func nativeError(code int) error {
	if code == int(syscall.EPERM) || code == int(syscall.EACCES) {
		return errors.New("macOS 未允许 MPTCP 聚合。需要管理员开启 net.inet.mptcp.allow_aggregate；不会降级为普通 TCP")
	}
	return fmt.Errorf("MPTCP socket: %w", syscall.Errno(code))
}
func connectPath(fd int, r Relay) (uint32, error) {
	ip := C.CString(r.Host)
	defer C.free(unsafe.Pointer(ip))
	var cid C.uint32_t
	code := int(C.desktop_connect(C.int(fd), ip, C.int(r.Port), &cid))
	if code != 0 && code != int(syscall.EINPROGRESS) {
		return 0, syscall.Errno(code)
	}
	return uint32(cid), nil
}
func pathReady(fd int) (bool, error) {
	var flags C.uint32_t
	// Nonzero individual IDs mean interface indexes in Apple's API, not CIDs.
	code := int(C.desktop_info(C.int(fd), C.uint32_t(^uint32(0)), &flags))
	if code != 0 {
		return false, syscall.Errno(code)
	}
	ready, err := readyFlags(uint32(flags))
	if err != nil {
		var index C.uint
		if C.desktop_primary(C.int(fd), nil, &index) == 0 && index != 0 {
			if iface, e := net.InterfaceByIndex(int(index)); e == nil {
				err = fmt.Errorf("%w（出站网卡 %s）", err, iface.Name)
			}
		}
	}
	return ready, err
}
func nativeDial(ctx context.Context, relays []Relay) (*nativeConn, error) {
	conn, primary, err := raceRelays(ctx, relays, 150*time.Millisecond, func(ctx context.Context, r Relay) (forwardConn, error) {
		c, err := nativeDialPrimary(ctx, r)
		if err != nil {
			return nil, err
		}
		return c, nil
	})
	if err != nil {
		return nil, err
	}
	c := conn.(*nativeConn)
	raw, err := c.file.SyscallConn()
	if err == nil {
		err = raw.Control(func(fd uintptr) {
			for _, r := range relays {
				if r == primary {
					continue
				}
				if _, e := connectPath(int(fd), r); e != nil {
					emit(Event{Kind: "warning", Message: fmt.Sprintf("追加 Relay %s:%d 子流失败: %v", r.Host, r.Port, e)})
				}
			}
		})
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func nativeDialPrimary(ctx context.Context, primary Relay) (*nativeConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd := int(C.desktop_socket())
	if fd < 0 {
		return nil, nativeError(-fd)
	}
	_, err := connectPath(fd, primary)
	if err != nil {
		syscall.Close(fd)
		return nil, err
	}
	deadline := time.Now().Add(4 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		ready, err = pathReady(fd)
		if err != nil || ready {
			break
		}
		select {
		case <-ctx.Done():
			syscall.Close(fd)
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	if !ready || err != nil {
		syscall.Close(fd)
		if err == nil {
			err = context.DeadlineExceeded
		}
		return nil, fmt.Errorf("初始 MPTCP 路径未就绪: %w", err)
	}
	var tuple flowTuple
	var index C.uint
	if code := int(C.desktop_primary(C.int(fd), unsafe.Pointer(&tuple[0]), &index)); code != 0 {
		syscall.Close(fd)
		return nil, syscall.Errno(code)
	}
	c := &nativeConn{file: os.NewFile(uintptr(fd), "mptcp"), remote: &net.TCPAddr{IP: net.ParseIP(primary.Host), Port: primary.Port}, tuple: tuple}
	return c, nil
}
func readPCB() ([]byte, error) {
	return readPCBWithLimit(16 * 1024 * 1024)
}

func readPCBWithLimit(limit int) ([]byte, error) {
	var data unsafe.Pointer
	var length C.size_t
	code := int(C.desktop_pcbs(C.size_t(limit), &data, &length))
	if code != 0 {
		if code == int(syscall.EOVERFLOW) {
			return nil, fmt.Errorf("%w: 系统记录超过统计读取上限（%d 字节）: %w", errPCBRead, limit, syscall.EOVERFLOW)
		}
		return nil, fmt.Errorf("%w: %w", errPCBRead, syscall.Errno(code))
	}
	if data == nil {
		return nil, nil
	}
	defer C.free(data)
	return C.GoBytes(data, C.int(length)), nil
}

// Read the system-wide PCB list once per sampling interval, outside the
// connection registry lock, instead of once for every active connection.
func nativePathCounts(connections []forwardConn) ([]int, error) {
	counts := make([]int, len(connections))
	if len(connections) == 0 {
		return counts, nil
	}
	data, snapshotErr := readPCB()
	var diagnostic error
	for i, conn := range connections {
		if c, ok := conn.(*nativeConn); ok {
			counts[i] = -1
			if snapshotErr == nil {
				n, err := c.pathStatusWithSnapshot(data)
				if err == nil {
					counts[i] = n
				} else if errors.Is(err, os.ErrClosed) {
					// The monitor copies the active set before sampling sockets.
					counts[i] = -2
				} else {
					diagnostic = err
				}
			}
		} else {
			counts[i] = conn.paths()
		}
	}
	if snapshotErr != nil {
		diagnostic = snapshotErr
	}
	return counts, diagnostic
}

func (c *nativeConn) pathStatus() (int, error) {
	data, err := readPCB()
	if err != nil {
		return 0, err
	}
	return c.pathStatusWithSnapshot(data)
}

func (c *nativeConn) pathStatusWithSnapshot(data []byte) (int, error) {
	count := 0
	raw, err := c.file.SyscallConn()
	if err != nil {
		return 0, err
	}
	var statusErr error
	err = raw.Control(func(fd uintptr) {
		if ready, e := pathReady(int(fd)); e != nil {
			statusErr = e
			return
		} else if !ready {
			return
		}
		count, statusErr = readySubflows(data, c.tuple)
	})
	if err != nil {
		return 0, err
	}
	return count, statusErr
}
func (c *nativeConn) paths() int                  { n, _ := c.pathStatus(); return n }
func (c *nativeConn) Read(b []byte) (int, error)  { return c.file.Read(b) }
func (c *nativeConn) Write(b []byte) (int, error) { return c.file.Write(b) }
func (c *nativeConn) Close() error {
	return c.file.Close()
}
func (c *nativeConn) CloseWrite() error {
	raw, err := c.file.SyscallConn()
	if err != nil {
		return err
	}
	var socketErr error
	err = raw.Control(func(fd uintptr) { socketErr = syscall.Shutdown(int(fd), syscall.SHUT_WR) })
	if err != nil {
		return err
	}
	return socketErr
}
func (c *nativeConn) LocalAddr() net.Addr                { return &net.TCPAddr{IP: net.IPv4zero} }
func (c *nativeConn) RemoteAddr() net.Addr               { return c.remote }
func (c *nativeConn) SetDeadline(t time.Time) error      { return c.file.SetDeadline(t) }
func (c *nativeConn) SetReadDeadline(t time.Time) error  { return c.file.SetReadDeadline(t) }
func (c *nativeConn) SetWriteDeadline(t time.Time) error { return c.file.SetWriteDeadline(t) }
