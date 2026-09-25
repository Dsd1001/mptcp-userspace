package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemdEnvironmentCannotRelaxOrdinaryFilePrivacy(t *testing.T) {
	c, err := defaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "config")
	if err = os.WriteFile(path, configBytes(c), 0440); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", directory)
	if _, err = readConfig(path); err == nil {
		t.Fatal("environment variable bypassed ordinary file privacy")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readConfig(path); err != nil {
		t.Fatal(err)
	}
}
func TestSystemdReadyNotification(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "mpx-notify-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "notify")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", path)
	if err = notifyReady(); err != nil {
		t.Fatal(err)
	}
	listener.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1024)
	n, _, err := listener.ReadFromUnix(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buffer[:n]), "READY=1\n") {
		t.Fatal("missing readiness message")
	}
	t.Setenv("NOTIFY_SOCKET", "relative-invalid")
	if notifyReady() == nil {
		t.Fatal("invalid notification endpoint accepted")
	}
}
