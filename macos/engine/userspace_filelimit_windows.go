//go:build windows

package main

import (
	"fmt"

	"mptcp-desktop/engine/multipath"
)

// Windows uses socket HANDLEs rather than POSIX RLIMIT_NOFILE. The normal
// process handle table is dynamically sized, so there is no Unix-style soft
// descriptor limit to raise here.
func ensureUserspaceFileLimit() error { return nil }

func userspaceEnvironmentMessage() string {
	return fmt.Sprintf("Windows Userspace 引擎可用；仅启用应用层 MPX/4，可承载最多 %d 个 Stream；尚未检查 Relay、Landing 密钥和端口", multipath.MaxStreams)
}
