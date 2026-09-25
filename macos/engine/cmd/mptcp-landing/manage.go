package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"mptcp-desktop/engine/multipath"
)

const (
	serviceName         = "mptcp-userspace-landing.service"
	binRel              = "usr/local/bin/mptcp-landing"
	backupRel           = "usr/local/lib/mptcp-userspace/mptcp-landing.previous"
	configRel           = "etc/mptcp-userspace/config.json"
	configBackupRel     = "etc/mptcp-userspace/config.previous.json"
	configEditBackupRel = "etc/mptcp-userspace/config.before-edit.json"
	unitRel             = "etc/systemd/system/" + serviceName
	manifestRel         = "etc/mptcp-userspace/managed.json"
	statusRel           = "run/mptcp-userspace-landing/status.json"
	lockRel             = "run/mptcp-userspace-manager.lock"
)

const unitText = `[Unit]
Description=MPX Userspace Multipath Landing (independent of Native MPTCP)
Wants=network-online.target
After=network-online.target

[Service]
Type=notify
NotifyAccess=main
TimeoutStartSec=20
ExecStart=/usr/local/bin/mptcp-landing server --config ${CREDENTIALS_DIRECTORY}/config --status-file /run/mptcp-userspace-landing/status.json
LoadCredential=config:/etc/mptcp-userspace/config.json
DynamicUser=yes
RuntimeDirectory=mptcp-userspace-landing
RuntimeDirectoryMode=0700
UMask=0077
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
RestrictRealtime=yes
LockPersonality=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
CapabilityBoundingSet=
AmbientCapabilities=
LimitNOFILE=16384
TasksMax=4096
MemoryMax=1G
Restart=on-failure
RestartSec=2
TimeoutStopSec=15
KillSignal=SIGTERM
SyslogIdentifier=mptcp-userspace-landing

[Install]
WantedBy=multi-user.target
`

type manifest struct {
	Owner             string `json:"owner"`
	Format            int    `json:"format"`
	Installed         bool   `json:"installed"`
	Version           string `json:"version"`
	BinarySHA         string `json:"binary_sha256"`
	UnitSHA           string `json:"unit_sha256"`
	PreviousBinarySHA string `json:"previous_binary_sha256,omitempty"`
	PreviousConfigSHA string `json:"previous_config_sha256,omitempty"`
}
type manager struct {
	root    string
	staged  bool
	out     io.Writer
	command func(string, ...string) ([]byte, error)
}

func newManager(root string, out io.Writer) (*manager, error) {
	m := &manager{root: "/", out: out}
	if root != "" {
		if !filepath.IsAbs(root) || filepath.Clean(root) == "/" {
			return nil, errors.New("--root 必须是非 / 的绝对暂存目录")
		}
		m.root = filepath.Clean(root)
		m.staged = true
	}
	m.command = func(name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	return m, nil
}
func (m *manager) writable() error {
	if !m.staged && (runtime.GOOS != "linux" || runtime.GOARCH != "amd64") {
		return errors.New("系统安装/服务管理仅支持 Linux amd64；--root 仅用于无 systemd 的文件暂存测试")
	}
	if !m.staged && os.Geteuid() != 0 {
		return errors.New("该操作需要 root，请使用 sudo 运行 Landing 二进制")
	}
	return nil
}

// Reject symlinks anywhere below the explicitly selected root. No operation
// follows a managed path into another service, user's home or arbitrary file.
func (m *manager) path(rel string, createParents bool) (string, error) {
	if filepath.IsAbs(rel) || filepath.Clean(rel) != rel || strings.HasPrefix(rel, "..") {
		return "", errors.New("invalid managed relative path")
	}
	if createParents && m.staged {
		if err := os.MkdirAll(m.root, 0700); err != nil {
			return "", err
		}
	}
	rootInfo, err := os.Lstat(m.root)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("暂存根目录必须为真实目录，不能是符号链接")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	current := m.root
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if os.IsNotExist(e) {
			if createParents && i < len(parts)-1 {
				if e = os.Mkdir(current, 0755); e != nil && !os.IsExist(e) {
					return "", e
				}
				info, e = os.Lstat(current)
			} else {
				continue
			}
		}
		if e != nil {
			return "", e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("拒绝符号链接：%s", current)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("父路径不是目录：%s", current)
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return "", fmt.Errorf("目标不是普通文件：%s", current)
		}
	}
	return current, nil
}
func (m *manager) lock() (func(), error) {
	if err := m.writable(); err != nil {
		return nil, err
	}
	path, err := m.path(lockRel, true)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("另一个 Landing 管理操作正在进行")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func readLimited(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, fmt.Errorf("不是大小合规的普通文件：%s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("文件读取期间超过大小上限")
	}
	return b, err
}
func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (m *manager) read(rel string, limit int64) ([]byte, error) {
	path, err := m.path(rel, false)
	if err != nil {
		return nil, err
	}
	return readLimited(path, limit)
}
func (m *manager) manifest() (manifest, error) {
	var record manifest
	b, err := m.read(manifestRel, 65536)
	if err != nil {
		return record, err
	}
	if err = json.Unmarshal(b, &record); err != nil {
		return record, err
	}
	if record.Owner != "mptcp-userspace-landing" || record.Format != 1 {
		return record, errors.New("目录不属于本程序或管理记录版本不兼容")
	}
	return record, nil
}
func (m *manager) owned() (manifest, error) {
	record, err := m.manifest()
	if err != nil {
		return record, err
	}
	if !record.Installed {
		return record, errors.New("尚未安装；保留配置可用于重新安装")
	}
	for _, item := range []struct{ path, hash string }{{binRel, record.BinarySHA}, {unitRel, record.UnitSHA}} {
		b, e := m.read(item.path, 128<<20)
		if e != nil {
			return record, e
		}
		if hashBytes(b) != item.hash {
			return record, fmt.Errorf("受管文件已被外部修改，拒绝覆盖/删除：%s", item.path)
		}
	}
	return record, nil
}
func manifestBytes(binary, unit []byte, installed bool, previous ...[]byte) []byte {
	record := manifest{Owner: "mptcp-userspace-landing", Format: 1, Installed: installed, Version: multipath.Version, BinarySHA: hashBytes(binary), UnitSHA: hashBytes(unit)}
	if len(previous) >= 2 {
		record.PreviousBinarySHA = hashBytes(previous[0])
		record.PreviousConfigSHA = hashBytes(previous[1])
	}
	b, _ := json.MarshalIndent(record, "", "  ")
	return append(b, '\n')
}

type fileChange struct {
	rel    string
	data   []byte
	mode   os.FileMode
	remove bool
}
type savedFile struct {
	path    string
	data    []byte
	mode    os.FileMode
	existed bool
}

func (m *manager) transaction(changes []fileChange) (func() error, error) {
	saved := make([]savedFile, 0, len(changes))
	for _, change := range changes {
		path, err := m.path(change.rel, true)
		if err != nil {
			return nil, err
		}
		s := savedFile{path: path}
		info, e := os.Lstat(path)
		if e == nil {
			s.existed = true
			s.mode = info.Mode().Perm()
			s.data, e = readLimited(path, 128<<20)
		}
		if e != nil && !os.IsNotExist(e) {
			return nil, e
		}
		saved = append(saved, s)
	}
	undo := func() error {
		var errs []error
		for i := len(saved) - 1; i >= 0; i-- {
			s := saved[i]
			var e error
			if s.existed {
				e = atomicFile(s.path, s.data, s.mode)
			} else {
				e = os.Remove(s.path)
				if os.IsNotExist(e) {
					e = nil
				}
			}
			if e != nil {
				errs = append(errs, e)
			}
		}
		return errors.Join(errs...)
	}
	for i, change := range changes {
		var err error
		if change.remove {
			err = os.Remove(saved[i].path)
			if os.IsNotExist(err) {
				err = nil
			}
		} else {
			err = atomicFile(saved[i].path, change.data, change.mode)
		}
		if err != nil {
			return nil, errors.Join(err, undo())
		}
	}
	return undo, nil
}
func (m *manager) system(args ...string) error {
	if m.staged {
		fmt.Fprintf(m.out, "[暂存模式：未调用 systemd] systemctl %s\n", strings.Join(args, " "))
		return nil
	}
	out, err := m.command("/usr/bin/systemctl", args...)
	if len(out) > 0 {
		fmt.Fprint(m.out, string(out))
	}
	if err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
func (m *manager) active() bool {
	if m.staged {
		return false
	}
	_, err := m.command("/usr/bin/systemctl", "is-active", "--quiet", serviceName)
	return err == nil
}
func (m *manager) restartIf(active bool) error {
	if !active {
		return nil
	}
	// This is an explicit operator transaction, not the daemon's automatic
	// crash-restart loop. Clear only this unit's prior start-rate counter.
	if err := m.system("reset-failed", serviceName); err != nil {
		return err
	}
	if err := m.system("restart", serviceName); err != nil {
		return err
	}
	if !m.staged && !m.active() {
		return errors.New("服务重启后未处于 active 状态")
	}
	return nil
}
func (m *manager) validateBinary(path string) ([]byte, error) {
	b, err := readLimited(path, 128<<20)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errors.New("空二进制")
	}
	if m.staged {
		return b, nil
	}
	f, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("升级文件不是 ELF：%w", err)
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_X86_64 {
		return nil, errors.New("必须是 Linux amd64 ELF64")
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return nil, errors.New("交付要求静态二进制，拒绝动态解释器")
		}
	}
	info, err := buildinfo.Read(bytes.NewReader(b))
	if err != nil || info.Path != "mptcp-desktop/engine/cmd/mptcp-landing" {
		return nil, errors.New("文件缺少正确的 mptcp-landing Go 构建标识")
	}
	return b, nil
}

func (m *manager) install(c Config, source string, start bool) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err = c.validate(); err != nil {
		return err
	}
	record, e := m.manifest()
	if e == nil && record.Installed {
		return errors.New("已经安装；使用 upgrade 或 config --edit，不覆盖现有安装")
	}
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	for _, rel := range []string{binRel, unitRel, backupRel} {
		path, e := m.path(rel, false)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if _, e = os.Lstat(path); e == nil {
			return fmt.Errorf("目标已存在但不是当前可安装状态：%s", rel)
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	cfgPath, err := m.path(configRel, true)
	if err != nil {
		return err
	}
	if os.IsNotExist(e) {
		// A new install does not adopt an unrelated private directory.
		entries, e := os.ReadDir(filepath.Dir(cfgPath))
		if e != nil {
			return e
		}
		if len(entries) != 0 {
			return errors.New("配置目录已有未受管文件，拒绝接管")
		}
	}
	if err = os.Chmod(filepath.Dir(cfgPath), 0700); err != nil {
		return err
	}
	binary, err := m.validateBinary(source)
	if err != nil {
		return err
	}
	unit := []byte(unitText)
	undo, err := m.transaction([]fileChange{{binRel, binary, 0755, false}, {configRel, configBytes(c), 0600, false}, {unitRel, unit, 0644, false}, {manifestRel, manifestBytes(binary, unit, true), 0600, false}})
	if err != nil {
		return err
	}
	err = m.system("daemon-reload")
	if err == nil {
		if start {
			err = m.system("enable", "--now", serviceName)
		} else {
			err = m.system("enable", serviceName)
		}
	}
	if err != nil {
		_ = m.system("disable", "--now", serviceName)
		rollback := undo()
		_ = m.system("daemon-reload")
		return errors.Join(err, rollback)
	}
	fmt.Fprintln(m.out, "安装完成；旧 Native MPTCP、Relay 转发和 SS 配置未修改。传输密钥保存在权限 0600 的配置中。")
	if m.staged {
		fmt.Fprintln(m.out, "注意：这是文件暂存验收，不是 Debian/systemd 运行验收。")
	}
	return nil
}

func (m *manager) setConfig(c Config) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = m.owned(); err != nil {
		return err
	}
	if err = c.validate(); err != nil {
		return err
	}
	old, err := m.read(configRel, 65536)
	if err != nil {
		return err
	}
	active := m.active()
	undo, err := m.transaction([]fileChange{{configEditBackupRel, old, 0600, false}, {configRel, configBytes(c), 0600, false}})
	if err != nil {
		return err
	}
	if err = m.restartIf(active); err != nil {
		return errors.Join(err, undo(), m.restartIf(active))
	}
	fmt.Fprintln(m.out, "配置已保存；运行中的服务已重启以重新加载 systemd credential。客户端需使用匹配的密钥和 Relay 端口。")
	return nil
}
func (m *manager) upgrade(source, expected string) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	record, err := m.owned()
	if err != nil {
		return err
	}
	if len(expected) != 64 {
		return errors.New("升级必须提供 --sha256（来自可信交付清单）")
	}
	if _, err = hex.DecodeString(expected); err != nil {
		return err
	}
	next, err := m.validateBinary(source)
	if err != nil {
		return err
	}
	if hashBytes(next) != strings.ToLower(expected) {
		return errors.New("升级 SHA-256 不匹配，未修改安装")
	}
	if record.BinarySHA == hashBytes(next) {
		fmt.Fprintln(m.out, "文件与当前安装一致，无需替换。")
		return nil
	}
	old, err := m.read(binRel, 128<<20)
	if err != nil {
		return err
	}
	cfg, err := m.read(configRel, 65536)
	if err != nil {
		return err
	}
	unit, err := m.read(unitRel, 65536)
	if err != nil {
		return err
	}
	active := m.active()
	undo, err := m.transaction([]fileChange{{backupRel, old, 0755, false}, {configBackupRel, cfg, 0600, false}, {binRel, next, 0755, false}, {manifestRel, manifestBytes(next, unit, true, old, cfg), 0600, false}})
	if err != nil {
		return err
	}
	if err = m.restartIf(active); err != nil {
		return errors.Join(err, undo(), m.restartIf(active))
	}
	fmt.Fprintln(m.out, "升级完成；上一二进制及配置已保留，可执行 rollback。")
	return nil
}
func (m *manager) rollback() error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	record, err := m.owned()
	if err != nil {
		return err
	}
	path, err := m.path(backupRel, false)
	if err != nil {
		return err
	}
	previous, err := m.validateBinary(path)
	if err != nil {
		return err
	}
	current, err := m.read(binRel, 128<<20)
	if err != nil {
		return err
	}
	oldConfig, err := m.read(configBackupRel, 65536)
	if err != nil {
		return err
	}
	if _, err = decodeConfig(oldConfig); err != nil {
		return err
	}
	if record.PreviousBinarySHA != hashBytes(previous) || record.PreviousConfigSHA != hashBytes(oldConfig) {
		return errors.New("回滚备份的 SHA-256 与管理记录不符，未替换安装")
	}
	config, err := m.read(configRel, 65536)
	if err != nil {
		return err
	}
	unit, err := m.read(unitRel, 65536)
	if err != nil {
		return err
	}
	active := m.active()
	undo, err := m.transaction([]fileChange{{binRel, previous, 0755, false}, {backupRel, current, 0755, false}, {configRel, oldConfig, 0600, false}, {configBackupRel, config, 0600, false}, {manifestRel, manifestBytes(previous, unit, true, current, config), 0600, false}})
	if err != nil {
		return err
	}
	if err = m.restartIf(active); err != nil {
		return errors.Join(err, undo(), m.restartIf(active))
	}
	fmt.Fprintln(m.out, "已回滚二进制和对应配置；客户端密钥需与回滚后的配置一致。")
	return nil
}
func (m *manager) uninstall(yes, purge bool) error {
	if !yes {
		return errors.New("卸载需要明确确认：添加 --yes；额外 --purge 才删除保留配置")
	}
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	record, err := m.manifest()
	if err != nil {
		return err
	}
	if record.Installed {
		if _, err = m.owned(); err != nil {
			return err
		}
		if err = m.system("disable", "--now", serviceName); err != nil {
			return err
		}
	}
	var changes []fileChange
	if record.Installed {
		changes = []fileChange{{binRel, nil, 0, true}, {unitRel, nil, 0, true}, {backupRel, nil, 0, true}}
	}
	if purge {
		changes = append(changes, fileChange{configRel, nil, 0, true}, fileChange{configBackupRel, nil, 0, true}, fileChange{configEditBackupRel, nil, 0, true}, fileChange{manifestRel, nil, 0, true})
	} else {
		record.Installed = false
		b, _ := json.MarshalIndent(record, "", "  ")
		changes = append(changes, fileChange{manifestRel, append(b, '\n'), 0600, false})
	}
	undo, err := m.transaction(changes)
	if err != nil {
		return err
	}
	if err = m.system("daemon-reload"); err != nil {
		return errors.Join(err, undo())
	}
	if purge {
		path, _ := m.path(configRel, false)
		_ = os.Remove(filepath.Dir(path))
	}
	fmt.Fprintf(m.out, "卸载完成。保留配置：%t。未递归删除目录，未触碰旧 Native 服务、SS 或防火墙。\n", !purge)
	return nil
}
func (m *manager) service(action string) error {
	if action != "start" && action != "stop" && action != "restart" {
		return errors.New("invalid service action")
	}
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = m.owned(); err != nil {
		return err
	}
	if action != "stop" {
		if err = m.system("reset-failed", serviceName); err != nil {
			return err
		}
	}
	return m.system(action, serviceName)
}
