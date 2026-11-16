package multipath

import (
	"net"
	"os"
	"strconv"
	"strings"
)

// MPX_ROUTED_BACKEND_RCVBUF_KIB is an opt-in experiment for the
// iCable-to-HKBN cold TCP receive-window ramp. It is disabled by default:
// explicit SO_RCVBUF disables automatic receive buffer growth on Linux,
// and a fixed size may hurt high-BDP links or increase memory pressure.
func routedBackendReceiveBufferBytes() int {
	raw := strings.TrimSpace(os.Getenv("MPX_ROUTED_BACKEND_RCVBUF_KIB"))
	if raw == "" || raw == "0" {
		return 0
	}
	kib, err := strconv.Atoi(raw)
	if err != nil || kib < 128 || kib > 4096 {
		return 0
	}
	return kib << 10
}

// Best effort: socket tuning must never prevent a backend route connecting.
// UoT and clients are not tuned by this helper.
func tuneRoutedBackendReadBuffer(conn net.Conn) bool {
	size := routedBackendReceiveBufferBytes()
	tcp, ok := conn.(*net.TCPConn)
	if !ok || size == 0 {
		return false
	}
	return tcp.SetReadBuffer(size) == nil
}
