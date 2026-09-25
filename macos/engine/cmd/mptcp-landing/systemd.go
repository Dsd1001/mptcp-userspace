package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// systemd 257 exposes a root-owned 0440 credential in a root-owned 0550
// directory, with a per-service POSIX ACL granting the DynamicUser access.
// st_mode's group bits are the ACL mask, NOT an ordinary shared group grant.
// Only this systemd-controlled, read-only namespace gets the exception; normal
// configuration files remain owner-only. A caller-controlled environment alone
// cannot grant the exception to an arbitrary /tmp or home-directory file.
func systemdCredentialAllowed(path string, info os.FileInfo) bool {
	if runtime.GOOS != "linux" || info.Mode().Perm() != 0440 {
		return false
	}
	directory := os.Getenv("CREDENTIALS_DIRECTORY")
	absolute, err := filepath.Abs(path)
	if err != nil || directory == "" || filepath.Clean(directory) != directory || !strings.HasPrefix(directory, "/run/credentials/") || filepath.Dir(absolute) != directory || filepath.Base(absolute) != "config" {
		return false
	}
	direct, err := os.Lstat(absolute)
	if err != nil || !direct.Mode().IsRegular() || !os.SameFile(direct, info) {
		return false
	}
	parent, err := os.Lstat(directory)
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0227 != 0 {
		return false
	}
	fileStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || fileStat.Uid != 0 || fileStat.Gid != 0 {
		return false
	}
	dirStat, ok := parent.Sys().(*syscall.Stat_t)
	return ok && dirStat.Uid == 0 && dirStat.Gid == 0
}

func notifyReady() error {
	address := os.Getenv("NOTIFY_SOCKET")
	if address == "" {
		return nil
	}
	if !strings.HasPrefix(address, "/") && !strings.HasPrefix(address, "@") {
		return errors.New("invalid NOTIFY_SOCKET")
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: address, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("systemd readiness notification: %w", err)
	}
	defer conn.Close()
	if err = conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	_, err = conn.Write([]byte("READY=1\nSTATUS=MPX/3 revision 2 aggregation listeners ready\n"))
	return err
}
