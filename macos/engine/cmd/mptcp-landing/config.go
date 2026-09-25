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
	"path/filepath"
	"strconv"
	"time"

	"mptcp-desktop/engine/multipath"
)

type Config struct {
	SchemaVersion int    `json:"schema_version"`
	ListenTCP     string `json:"listen_tcp"`
	ListenUDP     string `json:"listen_udp"`
	BackendTCP    string `json:"backend_tcp"`
	BackendUDP    string `json:"backend_udp"`
	UDPEnabled    bool   `json:"udp_enabled"`
	TransportKey  string `json:"transport_key"`
	MaxSessions   int    `json:"max_sessions"`
}

func defaultConfig() (Config, error) {
	key, err := multipath.NewKey()
	return Config{SchemaVersion: 1, ListenTCP: "0.0.0.0:24001", ListenUDP: "0.0.0.0:24001", BackendTCP: "127.0.0.1:8388", BackendUDP: "127.0.0.1:8388", UDPEnabled: true, TransportKey: key, MaxSessions: 4}, err
}

func endpoint(address string, listen bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsMulticast() || (!listen && ip.IsUnspecified()) {
		return fmt.Errorf("地址必须为明确的单播 IP:port（监听地址可为 0.0.0.0）：%s", address)
	}
	n, err := strconv.Atoi(port)
	minimum := 1
	if listen {
		minimum = 1024
	}
	if err != nil || n < minimum || n > 65535 {
		return fmt.Errorf("端口不在 %d..65535：%s", minimum, address)
	}
	return nil
}
func (c Config) validate() error {
	if c.SchemaVersion != 1 {
		return errors.New("Landing 仅接受 schema_version=1")
	}
	if _, err := multipath.ParseKey(c.TransportKey); err != nil {
		return err
	}
	if c.MaxSessions < 1 || c.MaxSessions > 16 {
		return errors.New("max_sessions 必须为 1..16")
	}
	pairs := [][2]string{{c.ListenTCP, c.BackendTCP}}
	if c.UDPEnabled {
		pairs = append(pairs, [2]string{c.ListenUDP, c.BackendUDP})
	}
	for _, pair := range pairs {
		if err := endpoint(pair[0], true); err != nil {
			return err
		}
		if err := endpoint(pair[1], false); err != nil {
			return err
		}
		lh, lp, _ := net.SplitHostPort(pair[0])
		bh, bp, _ := net.SplitHostPort(pair[1])
		if lp == bp && (net.ParseIP(lh).IsUnspecified() || lh == bh) {
			return errors.New("聚合监听端口与 backend 可能形成回环；必须使用独立端口")
		}
	}
	return nil
}
func decodeConfig(b []byte) (Config, error) {
	var c Config
	if len(b) > 64<<10 {
		return c, errors.New("配置超过 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, errors.New("配置必须只有一个 JSON 对象")
	}
	return c, c.validate()
}
func readConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() || (info.Mode().Perm()&0077 != 0 && !systemdCredentialAllowed(path, info)) {
		return c, errors.New("配置包含传输密钥，必须是权限 0600/0400 的普通文件")
	}
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return c, err
	}
	return decodeConfig(b)
}
func configBytes(c Config) []byte { b, _ := json.MarshalIndent(c, "", "  "); return append(b, '\n') }
func (c Config) redacted() Config {
	c.TransportKey = "[已隐藏；使用 config --show-key 明确显示]"
	return c
}

func runServer(ctx context.Context, c Config, statusFile string, out io.Writer) error {
	if err := c.validate(); err != nil {
		return err
	}
	srv, err := multipath.NewServer(ctx, c.TransportKey, c.BackendTCP, c.MaxSessions)
	if err != nil {
		return err
	}
	defer srv.Close()
	listener, err := multipath.PlainListen(ctx, c.ListenTCP)
	if err != nil {
		return err
	}
	defer listener.Close()
	var udp *multipath.UDPServer
	var udpDone <-chan struct{}
	if c.UDPEnabled {
		udp, err = multipath.StartUDPServer(srv, c.ListenUDP, c.BackendUDP)
		if err != nil {
			return err
		}
		defer udp.Close()
		udpDone = udp.Done()
	}
	stopped := make(chan error, 1)
	go func() { stopped <- srv.Serve(listener) }()
	if err = notifyReady(); err != nil {
		return err
	}
	fmt.Fprintf(out, "MPTCP Landing %s：Userspace TCP=%s UDP=%t；不使用内核 MPTCP。\n", multipath.Version, listener.Addr(), c.UDPEnabled)
	started := time.Now()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			srv.Close()
			<-stopped
			return nil
		case err := <-stopped:
			return err
		case <-udpDone:
			if ctx.Err() != nil {
				return nil
			}
			if udp.Err() != nil {
				return udp.Err()
			}
			return errors.New("UDP listener unexpectedly stopped")
		case <-ticker.C:
			if statusFile != "" {
				status := struct {
					Version       string                   `json:"version"`
					SourceID      string                   `json:"source_id"`
					WireProtocol  int                      `json:"wire_protocol"`
					Admission     multipath.AdmissionStats `json:"admission"`
					Updated       string                   `json:"updated"`
					UptimeSeconds int64                    `json:"uptime_seconds"`
					TCP           []multipath.Stats        `json:"tcp"`
					UDP           []multipath.UDPStats     `json:"udp,omitempty"`
				}{Version: multipath.Version, SourceID: multipath.SourceID, WireProtocol: multipath.WireProtocol, Admission: srv.AdmissionSnapshot(), Updated: time.Now().UTC().Format(time.RFC3339), UptimeSeconds: int64(time.Since(started).Seconds()), TCP: srv.Snapshots()}
				if udp != nil {
					status.UDP = udp.Snapshots()
				}
				b, e := json.MarshalIndent(status, "", "  ")
				if e == nil {
					e = atomicFile(statusFile, append(b, '\n'), 0600)
				}
				if e != nil {
					fmt.Fprintf(out, "状态文件写入失败：%v\n", e)
				}
			}
		}
	}
}

func atomicFile(path string, data []byte, mode os.FileMode) error {
	// The caller preflights managed paths. A temp file stays in the same
	// directory so the rename is atomic on the destination filesystem.
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("拒绝覆盖非普通文件：%s", path)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".mptcp-tmp-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	if dir, e := os.Open(filepath.Dir(path)); e == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}
