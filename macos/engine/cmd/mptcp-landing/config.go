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
	Kind          string               `json:"kind"`
	TCPRoutes     []multipath.TCPRoute `json:"routes"`
	SchemaVersion int                  `json:"schema_version"`
	ListenTCP     string               `json:"listen_tcp"`
	ListenUDP     string               `json:"-"`
	BackendTCP    string               `json:"-"`
	BackendUDP    string               `json:"-"`
	UDPEnabled    bool                 `json:"-"`
	UOTEnabled    bool                 `json:"-"`
	TransportKey  string               `json:"transport_key"`
	MaxSessions   int                  `json:"max_sessions"`
	SchedulerMode string               `json:"scheduler_mode,omitempty"`
}

func defaultConfig() (Config, error) {
	key, err := multipath.NewKey()
	return Config{SchemaVersion: 2, Kind: "mpx4_tunnel_egress", ListenTCP: "0.0.0.0:24001", TransportKey: key, MaxSessions: 8, SchedulerMode: "auto",
		TCPRoutes: []multipath.TCPRoute{{ID: 1, Name: "default", Backend: "127.0.0.1:8388", UDPBackend: "127.0.0.1:8388"}}}, err
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
func (c Config) validate() error { return c.validateRouted() }

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
	srv, err := multipath.NewRoutedServerWithScheduler(ctx, c.TransportKey, c.TCPRoutes, c.MaxSessions, multipath.SchedulerMode(c.SchedulerMode))
	if err != nil {
		return err
	}
	defer srv.Close()
	listener, err := multipath.PlainListen(ctx, c.ListenTCP)
	if err != nil {
		return err
	}
	defer listener.Close()
	stopped := make(chan error, 1)
	go func() { stopped <- srv.Serve(listener) }()
	if err = notifyReady(); err != nil {
		return err
	}
	mode, _ := multipath.ParseSchedulerMode(c.SchedulerMode)
	fmt.Fprintf(out, "MPTCP Tunnel Egress %s: TCP=%s routes=%d scheduler=%s; UDP uses routed UoT, no native UDP listener.\n", multipath.Version, listener.Addr(), len(c.TCPRoutes), mode)
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
		case <-ticker.C:
			if statusFile != "" {
				status := struct {
					Kind          string                   `json:"kind"`
					RouteCount    int                      `json:"route_count"`
					Version       string                   `json:"version"`
					SourceID      string                   `json:"source_id"`
					WireProtocol  int                      `json:"wire_protocol"`
					Admission     multipath.AdmissionStats `json:"admission"`
					Updated       string                   `json:"updated"`
					UptimeSeconds int64                    `json:"uptime_seconds"`
					TCP           []multipath.Stats        `json:"tcp"`
					UDP           []multipath.UDPStats     `json:"udp,omitempty"`
				}{Kind: c.Kind, RouteCount: len(c.TCPRoutes), Version: multipath.Version, SourceID: multipath.SourceID, WireProtocol: multipath.WireProtocol, Admission: srv.AdmissionSnapshot(), Updated: time.Now().UTC().Format(time.RFC3339), UptimeSeconds: int64(time.Since(started).Seconds()), TCP: srv.Snapshots()}
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
