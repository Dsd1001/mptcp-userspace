//go:build !windows

package main

import (
	"fmt"
	"syscall"

	"mptcp-desktop/engine/multipath"
)

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

func userspaceEnvironmentMessage() string {
	return fmt.Sprintf("Userspace 引擎可用；RLIMIT_NOFILE 已提升/满足 %d，可承载 %d 业务流；尚未检查 Relay、Landing 密钥和端口", userspaceDesiredNOFILE, multipath.MaxStreams)
}
