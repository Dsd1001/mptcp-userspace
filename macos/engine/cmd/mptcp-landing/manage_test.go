package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mptcp-desktop/engine/multipath"
)

func fixture(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func testManager(t *testing.T) (*manager, Config, string, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	m, err := newManager(filepath.Join(t.TempDir(), "stage"), &out)
	if err != nil {
		t.Fatal(err)
	}
	c, err := defaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	// These staging fixtures are NEVER executed. Separate release checks build
	// and inspect the real Linux amd64 executable and the macOS test executable.
	source := fixture(t, "original", []byte("staging-binary-v1"))
	return m, c, source, &out
}
func assertFile(t *testing.T, m *manager, rel string, expected []byte, mode os.FileMode) {
	t.Helper()
	path, err := m.path(rel, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, expected) {
		t.Fatalf("unexpected content for %s", rel)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("mode %s: %v %v", rel, info, err)
	}
}
func TestStagingInstallUpgradeRollbackUninstall(t *testing.T) {
	m, c, source, out := testManager(t)
	if err := m.install(c, source, true); err != nil {
		t.Fatal(err)
	}
	assertFile(t, m, binRel, []byte("staging-binary-v1"), 0755)
	assertFile(t, m, configRel, configBytes(c), 0600)
	assertFile(t, m, unitRel, []byte(unitText), 0644)
	if strings.Contains(out.String(), c.TransportKey) {
		t.Fatal("installation leaked key")
	}
	if err := m.install(c, source, false); err == nil {
		t.Fatal("duplicate install overwrote files")
	}
	candidate := fixture(t, "candidate", []byte("staging-binary-v2"))
	if err := m.upgrade(candidate, strings.Repeat("0", 64)); err == nil {
		t.Fatal("bad checksum accepted")
	}
	assertFile(t, m, binRel, []byte("staging-binary-v1"), 0755)
	if err := m.upgrade(candidate, hashBytes([]byte("staging-binary-v2"))); err != nil {
		t.Fatal(err)
	}
	changed := c
	changed.ListenTCP = "0.0.0.0:24002"
	changed.ListenUDP = "0.0.0.0:24002"
	if err := m.setConfig(changed); err != nil {
		t.Fatal(err)
	}
	if err := m.rollback(); err != nil {
		t.Fatal(err)
	}
	assertFile(t, m, binRel, []byte("staging-binary-v1"), 0755)
	// A later config edit must not overwrite the binary rollback's paired config.
	assertFile(t, m, configRel, configBytes(c), 0600)
	if err := m.uninstall(false, false); err == nil {
		t.Fatal("unconfirmed uninstall accepted")
	}
	if err := m.uninstall(true, false); err != nil {
		t.Fatal(err)
	}
	assertFile(t, m, configRel, configBytes(c), 0600)
	record, err := m.manifest()
	if err != nil || record.Installed {
		t.Fatalf("retained manifest: %+v %v", record, err)
	}
	if err = m.install(c, source, false); err != nil {
		t.Fatalf("reinstall with retained config: %v", err)
	}
	if err = m.uninstall(true, true); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{binRel, unitRel, configRel, manifestRel, backupRel, configBackupRel, configEditBackupRel} {
		path, _ := m.path(rel, false)
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("not removed: %s %v", rel, err)
		}
	}
}
func TestRollbackRejectsModifiedBackup(t *testing.T) {
	m, c, source, _ := testManager(t)
	if err := m.install(c, source, false); err != nil {
		t.Fatal(err)
	}
	v2 := []byte("staging-v2")
	if err := m.upgrade(fixture(t, "v2", v2), hashBytes(v2)); err != nil {
		t.Fatal(err)
	}
	path, _ := m.path(backupRel, false)
	if err := os.WriteFile(path, []byte("tampered"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := m.rollback(); err == nil {
		t.Fatal("tampered rollback accepted")
	}
	assertFile(t, m, binRel, v2, 0755)
}
func TestOwnedFilesAndSymlinksProtected(t *testing.T) {
	m, c, source, _ := testManager(t)
	if err := m.install(c, source, false); err != nil {
		t.Fatal(err)
	}
	path, _ := m.path(binRel, false)
	replacement := []byte("another-program")
	if err := os.WriteFile(path, replacement, 0755); err != nil {
		t.Fatal(err)
	}
	if err := m.uninstall(true, true); err == nil {
		t.Fatal("foreign replacement deleted")
	}
	assertFile(t, m, binRel, replacement, 0755)
	m2, c2, source2, _ := testManager(t)
	if err := os.MkdirAll(m2.root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(m2.root, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := m2.install(c2, source2, false); err == nil {
		t.Fatal("symlink destination accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("symlink target was modified")
	}
}
func TestRepeatedPurgeDoesNotRemoveNewProgram(t *testing.T) {
	m, c, source, _ := testManager(t)
	if err := m.install(c, source, false); err != nil {
		t.Fatal(err)
	}
	if err := m.uninstall(true, false); err != nil {
		t.Fatal(err)
	}
	path, _ := m.path(binRel, true)
	other := []byte("unrelated-new-program")
	if err := os.WriteFile(path, other, 0755); err != nil {
		t.Fatal(err)
	}
	if err := m.uninstall(true, true); err != nil {
		t.Fatal(err)
	}
	assertFile(t, m, binRel, other, 0755)
}
func TestTransactionUndoAndLock(t *testing.T) {
	m, c, source, _ := testManager(t)
	if err := m.install(c, source, false); err != nil {
		t.Fatal(err)
	}
	unlock, err := m.lock()
	if err != nil {
		t.Fatal(err)
	}
	if release, err := m.lock(); err == nil {
		release()
		t.Fatal("concurrent lock accepted")
	}
	unlock()
	undo, err := m.transaction([]fileChange{{configRel, []byte("temporary"), 0600, false}, {"etc/mptcp-userspace/new-test", []byte("x"), 0600, false}})
	if err != nil {
		t.Fatal(err)
	}
	if err = undo(); err != nil {
		t.Fatal(err)
	}
	assertFile(t, m, configRel, configBytes(c), 0600)
	path, _ := m.path("etc/mptcp-userspace/new-test", false)
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("undo left new file")
	}
}
func TestConfigValidationPrivacyAndCLI(t *testing.T) {
	m, c, source, _ := testManager(t)
	if err := m.install(c, source, false); err != nil {
		t.Fatal(err)
	}
	path, _ := m.path(configRel, false)
	if _, err := readConfig(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(path); err == nil {
		t.Fatal("world-readable key accepted")
	}
	os.Chmod(path, 0600)
	for _, mutate := range []func(*Config){func(v *Config) { v.SchemaVersion = 2 }, func(v *Config) { v.TransportKey = "password" }, func(v *Config) { v.ListenTCP = v.BackendTCP }, func(v *Config) { v.MaxSessions = 17 }} {
		broken := c
		mutate(&broken)
		if broken.validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
	var out bytes.Buffer
	if err := runCLI(context.Background(), []string{"config", "--root", m.root}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), c.TransportKey) {
		t.Fatal("config command leaked key")
	}
	out.Reset()
	if err := runCLI(context.Background(), []string{"--root", m.root}, strings.NewReader("7\n8\n0\n"), &out); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"安装/初始化", "编辑配置", "环境与端口检查", "升级", "回滚", "卸载", "实时日志"} {
		if !strings.Contains(out.String(), label) {
			t.Fatalf("missing menu %s", label)
		}
	}
	if strings.Contains(out.String(), c.TransportKey) {
		t.Fatal("menu/status leaked key")
	}
	if err := runCLI(context.Background(), nil, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("menu EOF: %v", err)
	}
	if err := runCLI(context.Background(), []string{"unknown"}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("unknown command succeeded")
	}
}
func TestInteractiveInstallAndCancel(t *testing.T) {
	root := filepath.Join(t.TempDir(), "interactive")
	var out bytes.Buffer
	input := "1\n" + strings.Repeat("\n", 7) + "yes\n7\n0\n"
	if err := runCLI(context.Background(), []string{"--root", root}, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	m, _ := newManager(root, io.Discard)
	if _, err := m.owned(); err != nil {
		t.Fatalf("interactive install not complete: %v\n%s", err, out.String())
	}
	c, err := loadSelected(m, options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), c.TransportKey) {
		t.Fatal("interactive install key leaked")
	}
	other := filepath.Join(t.TempDir(), "cancelled")
	input = "1\n" + strings.Repeat("\n", 7) + "no\n0\n"
	if err = runCLI(context.Background(), []string{"--root", other}, strings.NewReader(input), io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(other, binRel)); !os.IsNotExist(err) {
		t.Fatal("cancelled install wrote executable")
	}
}

func TestForegroundServerTCPUDPAndStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend, err := multipath.PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		for {
			c, e := backend.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c); c.(interface{ CloseWrite() error }).CloseWrite() }()
		}
	}()
	udpBackend, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udpBackend.Close()
	go func() {
		b := make([]byte, 65536)
		for {
			n, a, e := udpBackend.ReadFromUDP(b)
			if e != nil {
				return
			}
			udpBackend.WriteToUDP(b[:n], a)
		}
	}()
	reserved, err := multipath.PlainListen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	reserved.Close()
	c, _ := defaultConfig()
	c.ListenTCP = address
	c.ListenUDP = address
	c.BackendTCP = backend.Addr().String()
	c.BackendUDP = udpBackend.LocalAddr().String()
	status := filepath.Join(t.TempDir(), "status.json")
	stopped := make(chan error, 1)
	go func() { stopped <- runServer(ctx, c, status, io.Discard) }()
	var client *multipath.Session
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		client, err = multipath.DialClient(ctx, []string{address, address}, c.TransportKey)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if err = client.WaitPaths(request, 2); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Open(request)
	if err != nil {
		t.Fatal(err)
	}
	stream.SetDeadline(time.Now().Add(3 * time.Second))
	defer stream.Close()
	payload := bytes.Repeat([]byte("binary\x00\xff"), 16000)
	writeDone := make(chan error, 1)
	go func() {
		_, e := stream.Write(payload)
		if e == nil {
			e = stream.CloseWrite()
		}
		writeDone <- e
	}()
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-writeDone; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, got) {
		t.Fatal("foreground TCP mismatch")
	}
	stream.Close()
	udp, err := multipath.StartClientUDP(client, c.TransportKey, "127.0.0.1:0", []string{address, address})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	if err = udp.WaitPaths(request, 2); err != nil {
		t.Fatal(err)
	}
	app, err := net.DialUDP("udp", nil, udp.Addr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.SetDeadline(time.Now().Add(3 * time.Second))
	app.Write([]byte("datagram\x00\xff"))
	buffer := make([]byte, 100)
	n, err := app.Read(buffer)
	if err != nil || !bytes.Equal(buffer[:n], []byte("datagram\x00\xff")) {
		t.Fatalf("foreground UDP: %v", err)
	}
	var raw []byte
	for time.Now().Before(deadline) {
		raw, err = os.ReadFile(status)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) || bytes.Contains(raw, []byte(c.TransportKey)) {
		t.Fatal("status missing or leaked key")
	}
	cancel()
	select {
	case err = <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}
