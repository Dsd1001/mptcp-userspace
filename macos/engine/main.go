package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mptcp-desktop/engine/multipath"
)

type Relay struct {
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	DownloadMbps *float64 `json:"download_mbps,omitempty"`
	UploadMbps   *float64 `json:"upload_mbps,omitempty"`
}
type Config struct {
	SchedulerMode *string `json:"scheduler_mode,omitempty"`
	SchemaVersion int     `json:"schema_version"`
	Mode          string  `json:"mode"`
	ListenPort    int     `json:"listen_port"`
	Relays        []Relay `json:"relays"`
	UDPEnabled    bool    `json:"udp_enabled,omitempty"`
	TCPEnabled    *bool   `json:"tcp_enabled,omitempty"`
	TransportKey  string  `json:"transport_key,omitempty"`
}

type BundleProfile struct {
	SchemaVersion      int     `json:"schema_version"`
	ProfileID          string  `json:"profile_id"`
	Revision           string  `json:"revision"`
	DisplayName        string  `json:"display_name"`
	Mode               string  `json:"mode"`
	ListenPort         int     `json:"listen_port"`
	SchedulerMode      string  `json:"scheduler_mode,omitempty"`
	TCPEnabled         bool    `json:"tcp_enabled"`
	UDPEnabled         bool    `json:"udp_enabled"`
	BackgroundResident bool    `json:"background_resident"`
	TransportKey       string  `json:"transport_key,omitempty"`
	Relays             []Relay `json:"relays"`
}

type BundlePayload struct {
	SchemaVersion int             `json:"schema_version"`
	Kind          string          `json:"kind"`
	BundleID      string          `json:"bundle_id"`
	Revision      string          `json:"revision"`
	DisplayName   string          `json:"display_name"`
	Mode          string          `json:"mode"`
	Profiles      []BundleProfile `json:"profiles"`
}

type ManagedInput struct {
	URL        string   `json:"url"`
	ProfileIDs []string `json:"profile_ids,omitempty"`
}

type managedDocument struct {
	profile *BundleProfile
	bundle  *BundlePayload
}

func (p BundleProfile) config() Config {
	var scheduler *string
	if p.Mode == "userspace_multipath" {
		value := p.SchedulerMode
		if value == "" {
			value = string(multipath.SchedulerAuto)
		}
		scheduler = &value
	}
	tcp := p.TCPEnabled
	return Config{SchemaVersion: 3, Mode: p.Mode, ListenPort: p.ListenPort, Relays: p.Relays, UDPEnabled: p.UDPEnabled, TCPEnabled: &tcp, TransportKey: p.TransportKey, SchedulerMode: scheduler}
}

func (c Config) schedulerMode() (multipath.SchedulerMode, error) {
	if c.SchedulerMode == nil {
		return multipath.SchedulerAuto, nil
	}
	if *c.SchedulerMode == "" {
		return "", errors.New("scheduler_mode cannot be empty when present")
	}
	return multipath.ParseSchedulerMode(*c.SchedulerMode)
}

func (c Config) userspace() bool  { return c.SchemaVersion == 3 && c.Mode == "userspace_multipath" }
func (c Config) tcpEnabled() bool { return c.TCPEnabled == nil || *c.TCPEnabled }

func (c Config) weightedCapacities() ([]multipath.PathCapacity, error) {
	mode, err := c.schedulerMode()
	if err != nil || mode != multipath.SchedulerWeighted {
		return nil, err
	}
	out := make([]multipath.PathCapacity, len(c.Relays))
	for i, r := range c.Relays {
		if r.DownloadMbps == nil {
			return nil, fmt.Errorf("Relay %d Weighted 下行 Mbps 必填", i+1)
		}
		out[i].DownloadMbps = *r.DownloadMbps
		if r.UploadMbps != nil {
			if *r.UploadMbps == 0 {
				return nil, fmt.Errorf("Relay %d Weighted 上行 Mbps 留空表示自动估算；显式 0 无效", i+1)
			}
			out[i].UploadMbps = *r.UploadMbps
		}
	}
	return out, nil
}

type Event struct {
	CapabilityRevision   int    `json:"capability_revision,omitempty"`
	ProfileID            string `json:"profile_id,omitempty"`
	ProfileName          string `json:"profile_name,omitempty"`
	BundleID             string `json:"bundle_id,omitempty"`
	BundleName           string `json:"bundle_name,omitempty"`
	ListenPort           int    `json:"listen_port,omitempty"`
	TotalProfiles        int    `json:"total_profiles,omitempty"`
	ActiveProfiles       int    `json:"active_profiles,omitempty"`
	ConnectingProfiles   int    `json:"connecting_profiles,omitempty"`
	ReconnectingProfiles int    `json:"reconnecting_profiles,omitempty"`
	FailedProfiles       int    `json:"failed_profiles,omitempty"`
	RetryAfterSeconds    int    `json:"retry_after_seconds,omitempty"`
	RetryAttempt         int    `json:"retry_attempt,omitempty"`
	multipath.SchedulerStats
	Kind             string                    `json:"kind"`
	Message          string                    `json:"message,omitempty"`
	Paths            int                       `json:"paths"`
	Connections      int64                     `json:"connections"`
	Sent             int64                     `json:"sent"`
	Received         int64                     `json:"received"`
	Mode             string                    `json:"mode,omitempty"`
	PathStats        []multipath.PathStats     `json:"path_stats,omitempty"`
	ReorderBytes     int                       `json:"reorder_bytes,omitempty"`
	ReorderPeak      int                       `json:"reorder_peak,omitempty"`
	PendingBytes     int                       `json:"pending_bytes,omitempty"`
	Retransmits      uint64                    `json:"retransmits,omitempty"`
	Dropped          uint64                    `json:"dropped,omitempty"`
	WindowWaits      uint64                    `json:"window_waits,omitempty"`
	ReceiveCredit    int                       `json:"receive_credit_bytes,omitempty"`
	ReceiveAllocated int                       `json:"receive_allocated_bytes,omitempty"`
	WindowTarget     int                       `json:"max_stream_window_target,omitempty"`
	ReadyFrames      int                       `json:"ready_frames,omitempty"`
	Version          string                    `json:"version,omitempty"`
	SourceID         string                    `json:"source_id,omitempty"`
	WireProtocol     int                       `json:"wire_protocol,omitempty"`
	ResourceReason   string                    `json:"resource_reason,omitempty"`
	Resources        *multipath.ResourceStats  `json:"resources,omitempty"`
	Lifecycle        *multipath.LifecycleStats `json:"lifecycle,omitempty"`
}

var eventMu sync.Mutex

func emit(e Event) {
	e.CapabilityRevision = multipath.CapabilityRevision
	eventMu.Lock()
	defer eventMu.Unlock()
	json.NewEncoder(os.Stdout).Encode(e)
}

func (c Config) validate() error { return c.validateForOS(runtime.GOOS) }

func (c Config) validateForOS(goos string) error {
	legacy := c.SchemaVersion == 2 && c.Mode == "tcp_forward"
	if !legacy && !(c.SchemaVersion == 3 && (c.Mode == "native_mptcp" || c.Mode == "userspace_multipath")) {
		return errors.New("支持 schema 2/tcp_forward（保持 Native）或 schema 3/userspace_multipath、native_mptcp；不支持旧 SOCKS5 配置")
	}
	if goos != "darwin" && !c.userspace() {
		return errors.New("Linux client 仅支持 userspace_multipath；Native MPTCP fallback 仅供 macOS 使用")
	}
	if c.userspace() {
		mode, err := c.schedulerMode()
		if err != nil {
			return err
		}
		if mode == multipath.SchedulerWeighted {
			caps, err := c.weightedCapacities()
			if err != nil {
				return err
			}
			for i, cap := range caps {
				if err := multipath.ValidatePathCapacity(cap); err != nil {
					return fmt.Errorf("Relay %d: %w", i+1, err)
				}
			}
		}
		if _, err := multipath.ParseKey(c.TransportKey); err != nil {
			return err
		}
	}
	if !c.tcpEnabled() && !c.UDPEnabled {
		return errors.New("TCP 和 UDP 不能同时关闭")
	}
	if !c.userspace() && !c.tcpEnabled() {
		return errors.New("Native 兼容模式需保留 TCP；仅 UDP 可使用 Userspace 模式")
	}
	if c.ListenPort < 1024 || c.ListenPort > 65535 {
		return errors.New("本地端口必须为 1024-65535")
	}
	if len(c.Relays) < 2 || len(c.Relays) > 8 {
		return errors.New("请配置 2-8 台 Relay")
	}
	seen := map[string]bool{}
	for _, r := range c.Relays {
		ip := net.ParseIP(r.Host)
		identity := r.Host
		if c.userspace() {
			identity = net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
		}
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() || r.Port < 1 || r.Port > 65535 || seen[identity] {
			return errors.New("Relay 需要有效且不重复的 IPv4 和端口")
		}
		if ip.IsLoopback() && r.Port == c.ListenPort {
			return errors.New("Relay 不能指向本地转发入口")
		}
		seen[identity] = true
	}
	return nil
}
func readConfig(reader io.Reader) (Config, error) {
	var c Config
	data, err := io.ReadAll(io.LimitReader(reader, 32769))
	if err != nil {
		return c, err
	}
	if len(data) > 32768 {
		return c, errors.New("配置文件过大")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return c, errors.New("配置必须是单个 JSON 对象")
	}
	return c, c.validate()
}

func validateProvisioningURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("Provisioning URL 必须是没有 userinfo/fragment 的绝对 URL")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("远程 Provisioning URL 必须使用 HTTPS；HTTP 仅允许 localhost/loopback")
	}
	return u, nil
}

func readManagedInput(reader io.Reader) (ManagedInput, error) {
	var in ManagedInput
	data, err := io.ReadAll(io.LimitReader(reader, 16385))
	if err != nil {
		return in, err
	}
	if len(data) > 16384 {
		return in, errors.New("managed input 超过 16 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return in, errors.New("managed input 必须是单个 JSON 对象")
	}
	if _, err := validateProvisioningURL(in.URL); err != nil {
		return in, err
	}
	seen := map[string]bool{}
	for _, id := range in.ProfileIDs {
		if id == "" || seen[id] {
			return in, errors.New("profile_ids 必须非空且唯一")
		}
		seen[id] = true
	}
	return in, nil
}

type managedEncryptedEnvelope struct {
	Version int    `json:"v"`
	Nonce   string `json:"n"`
	Data    string `json:"d"`
}

func decryptManagedEnvelope(data []byte, u *url.URL) ([]byte, error) {
	var envelope managedEncryptedEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version == 0 || envelope.Nonce == "" || envelope.Data == "" {
		return data, nil
	}
	if envelope.Version != 1 {
		return nil, errors.New("Provisioning 加密封装版本不受支持")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return nil, errors.New("Provisioning 加密响应缺少 API Secret")
	}
	token := parts[len(parts)-1]
	raw, err := hex.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("Provisioning 加密响应需要有效的 API Secret")
	}
	mac := hmac.New(sha256.New, raw)
	_, _ = mac.Write([]byte("mpx-provision-config-envelope-v1"))
	key := mac.Sum(nil)
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, errors.New("Provisioning 加密 nonce 无效")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(envelope.Data)
	if err != nil {
		return nil, errors.New("Provisioning 加密 data 无效")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, sealed, []byte("mpx-provision-envelope-v1"))
	if err != nil {
		return nil, errors.New("Provisioning 配置解密失败")
	}
	return plain, nil
}

func fetchManaged(ctx context.Context, rawURL string) (managedDocument, error) {
	u, err := validateProvisioningURL(rawURL)
	if err != nil {
		return managedDocument{}, err
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return errors.New("Provisioning API redirects are disabled")
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return managedDocument{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	resp, err := client.Do(req)
	if err != nil {
		return managedDocument{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return managedDocument{}, fmt.Errorf("Provisioning API HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 524289))
	if err != nil {
		return managedDocument{}, err
	}
	if len(data) == 0 || len(data) > 524288 {
		return managedDocument{}, errors.New("Provisioning API 响应为空或超过 512 KiB")
	}
	data, err = decryptManagedEnvelope(data, u)
	if err != nil {
		return managedDocument{}, err
	}
	if len(data) == 0 || len(data) > 524288 {
		return managedDocument{}, errors.New("Provisioning 解密配置为空或超过 512 KiB")
	}
	var header struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return managedDocument{}, fmt.Errorf("Provisioning API JSON 无效: %w", err)
	}
	if header.SchemaVersion == 1 {
		if len(data) > 65536 {
			return managedDocument{}, errors.New("单 Profile Provisioning 响应超过 64 KiB")
		}
		var p BundleProfile
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return managedDocument{}, fmt.Errorf("Provisioning Profile JSON 无效: %w", err)
		}
		if err := p.config().validate(); err != nil {
			return managedDocument{}, err
		}
		return managedDocument{profile: &p}, nil
	}
	if header.SchemaVersion == 2 && header.Kind == "bundle" {
		var b BundlePayload
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&b); err != nil {
			return managedDocument{}, fmt.Errorf("Provisioning Bundle JSON 无效: %w", err)
		}
		if err := b.validate(); err != nil {
			return managedDocument{}, err
		}
		return managedDocument{bundle: &b}, nil
	}
	return managedDocument{}, errors.New("Provisioning API schema/kind 不受支持")
}

func runManaged(ctx context.Context, in ManagedInput, validateOnly bool) error {
	doc, err := fetchManaged(ctx, in.URL)
	if err != nil {
		return err
	}
	if doc.profile != nil {
		if len(in.ProfileIDs) > 0 {
			return errors.New("单 Profile URL 不接受 profile_ids 选择")
		}
		if validateOnly {
			emit(Event{Kind: "ready", Message: "Provisioning 单 Profile 配置有效"})
			return nil
		}
		return runClient(ctx, doc.profile.config())
	}
	if doc.bundle == nil {
		return errors.New("Provisioning API 未返回配置")
	}
	if validateOnly {
		selected, err := doc.bundle.selected(in.ProfileIDs)
		if err != nil {
			return err
		}
		if err := validateSelectedForRuntime(selected); err != nil {
			return err
		}
		emit(Event{Kind: "ready", BundleID: doc.bundle.BundleID, BundleName: doc.bundle.DisplayName, Message: "Provisioning Bundle 与所选 Profile 有效"})
		return nil
	}
	return runBundle(ctx, *doc.bundle, in.ProfileIDs)
}

func readBundle(reader io.Reader) (BundlePayload, error) {
	var b BundlePayload
	data, err := io.ReadAll(io.LimitReader(reader, 524289))
	if err != nil {
		return b, err
	}
	if len(data) > 524288 {
		return b, errors.New("Bundle 配置超过 512 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return b, errors.New("Bundle 配置必须是单个 JSON 对象")
	}
	if err := b.validate(); err != nil {
		return b, err
	}
	return b, nil
}

func (b BundlePayload) validate() error {
	if b.SchemaVersion != 2 || b.Kind != "bundle" {
		return errors.New("Bundle 仅支持 schema_version=2 且 kind=bundle")
	}
	if b.BundleID == "" || len(b.BundleID) > 128 || len([]byte(b.DisplayName)) > 128 || len([]byte(b.Revision)) > 128 {
		return errors.New("Bundle 标识、名称或 revision 无效")
	}
	if b.Mode != "single_select" && b.Mode != "parallel" {
		return errors.New("Bundle mode 仅支持 single_select 或 parallel")
	}
	if len(b.Profiles) < 1 || len(b.Profiles) > 32 {
		return errors.New("Bundle 需要 1-32 个 Profile")
	}
	ids := map[string]bool{}
	ports := map[int]string{}
	for _, p := range b.Profiles {
		if p.SchemaVersion != 1 || p.ProfileID == "" || len(p.ProfileID) > 128 || ids[p.ProfileID] {
			return errors.New("Bundle Profile ID 必须非空且唯一")
		}
		ids[p.ProfileID] = true
		if len([]byte(p.DisplayName)) > 128 || len([]byte(p.Revision)) > 128 {
			return errors.New("Bundle Profile 名称或 revision 过长")
		}
		if err := p.config().validateForOS("darwin"); err != nil {
			return fmt.Errorf("Profile %s: %w", p.DisplayName, err)
		}
		if b.Mode == "parallel" {
			if other, ok := ports[p.ListenPort]; ok {
				return fmt.Errorf("并行 Bundle 端口冲突：%s 与 %s 都使用 %d", other, p.DisplayName, p.ListenPort)
			}
			ports[p.ListenPort] = p.DisplayName
		}
	}
	return nil
}

func (b BundlePayload) selected(ids []string) ([]BundleProfile, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	byID := map[string]BundleProfile{}
	for _, p := range b.Profiles {
		byID[p.ProfileID] = p
	}
	if len(ids) == 0 {
		if b.Mode == "single_select" {
			ids = []string{b.Profiles[0].ProfileID}
		} else {
			for _, p := range b.Profiles {
				ids = append(ids, p.ProfileID)
			}
		}
	}
	if b.Mode == "single_select" && len(ids) != 1 {
		return nil, errors.New("single_select Bundle 必须且只能选择 1 个 Profile")
	}
	if len(ids) < 1 {
		return nil, errors.New("至少选择 1 个 Profile")
	}
	seenID, ports := map[string]bool{}, map[int]string{}
	out := make([]BundleProfile, 0, len(ids))
	for _, id := range ids {
		if seenID[id] {
			return nil, fmt.Errorf("重复选择 Profile %s", id)
		}
		seenID[id] = true
		p, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("Bundle 不包含 Profile %s", id)
		}
		if other, ok := ports[p.ListenPort]; ok {
			return nil, fmt.Errorf("所选 Profile 端口冲突：%s 与 %s 都使用 %d", other, p.DisplayName, p.ListenPort)
		}
		ports[p.ListenPort] = p.DisplayName
		out = append(out, p)
	}
	return out, nil
}

func validateSelectedForRuntime(selected []BundleProfile) error {
	for _, p := range selected {
		if err := p.config().validate(); err != nil {
			return fmt.Errorf("Profile %s: %w", p.DisplayName, err)
		}
	}
	return nil
}

func preflightBundlePorts(selected []BundleProfile) error {
	closers := make([]io.Closer, 0, len(selected)*2)
	closeAll := func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}
	defer closeAll()
	for _, p := range selected {
		cfg := p.config()
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.ListenPort))
		if cfg.tcpEnabled() {
			ln, err := net.Listen("tcp4", address)
			if err != nil {
				return fmt.Errorf("Profile %s TCP 端口 %d 不可用: %w", p.DisplayName, cfg.ListenPort, err)
			}
			closers = append(closers, ln)
		}
		if cfg.UDPEnabled {
			udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: cfg.ListenPort})
			if err != nil {
				return fmt.Errorf("Profile %s UDP 端口 %d 不可用: %w", p.DisplayName, cfg.ListenPort, err)
			}
			closers = append(closers, udp)
		}
	}
	return nil
}

type bundleChildEvent struct {
	profile    BundleProfile
	event      Event
	scanErr    error
	generation uint64
}
type bundleChildExit struct {
	profile BundleProfile
	err     error
}

type bundleChildLauncher func(context.Context, string, BundleProfile, []byte) (*exec.Cmd, io.ReadCloser, error)

func defaultBundleChildLauncher(ctx context.Context, exe string, profile BundleProfile, cfgData []byte) (*exec.Cmd, io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, exe, "run")
	cmd.Stdin = bytes.NewReader(cfgData)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, stdout, nil
}

func runBundle(ctx context.Context, b BundlePayload, ids []string) error {
	return runBundleWithLauncher(ctx, b, ids, defaultBundleChildLauncher)
}

var bundleProfileRetrySchedule = [...]time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
}

func bundleProfileRetryDelay(failures int) time.Duration {
	if failures < 0 {
		failures = 0
	}
	if failures >= len(bundleProfileRetrySchedule) {
		return bundleProfileRetrySchedule[len(bundleProfileRetrySchedule)-1]
	}
	return bundleProfileRetrySchedule[failures]
}

type bundleProfileLifecycle struct {
	profile    BundleProfile
	generation uint64
	kind       string
	err        error
	retryAfter time.Duration
	attempt    int
}

func waitBundleRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func superviseParallelBundleProfile(
	ctx context.Context,
	exe string,
	profile BundleProfile,
	cfgData []byte,
	launch bundleChildLauncher,
	retryDelay func(int) time.Duration,
	events chan<- bundleChildEvent,
	lifecycle chan<- bundleProfileLifecycle,
) {
	failures := 0
	var generation uint64
	sendLifecycle := func(item bundleProfileLifecycle) bool {
		select {
		case lifecycle <- item:
			return true
		case <-ctx.Done():
			return false
		}
	}
	sendEvent := func(item bundleChildEvent) bool {
		select {
		case events <- item:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for ctx.Err() == nil {
		generation++
		cmd, stdout, err := launch(ctx, exe, profile, cfgData)
		if err != nil {
			delay := retryDelay(failures)
			attempt := failures + 1
			failures++
			if !sendLifecycle(bundleProfileLifecycle{
				profile: profile, generation: generation, kind: "retrying",
				err: err, retryAfter: delay, attempt: attempt,
			}) {
				return
			}
			if !waitBundleRetry(ctx, delay) {
				return
			}
			continue
		}
		if !sendLifecycle(bundleProfileLifecycle{profile: profile, generation: generation, kind: "started"}) {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
			return
		}

		hadListening := false
		var lastEventError error
		scanner := bufio.NewScanner(stdout)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 256*1024)
		for scanner.Scan() {
			var e Event
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				lastEventError = fmt.Errorf("engine event JSON: %w", err)
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				break
			}
			if e.Kind == "listening" {
				hadListening = true
				failures = 0
			}
			if e.Kind == "error" && e.Message != "" {
				lastEventError = errors.New(e.Message)
			}
			if !sendEvent(bundleChildEvent{profile: profile, event: e, generation: generation}) {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				_ = cmd.Wait()
				return
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			lastEventError = scanErr
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if waitErr != nil {
			lastEventError = waitErr
		}
		if lastEventError == nil {
			lastEventError = errors.New("Profile runtime exited")
		}
		if hadListening {
			failures = 0
		}
		delay := retryDelay(failures)
		attempt := failures + 1
		failures++
		if !sendLifecycle(bundleProfileLifecycle{
			profile: profile, generation: generation, kind: "retrying",
			err: lastEventError, retryAfter: delay, attempt: attempt,
		}) {
			return
		}
		if !waitBundleRetry(ctx, delay) {
			return
		}
	}
}

func runParallelBundleWithLauncherRetry(
	ctx context.Context,
	b BundlePayload,
	ids []string,
	launch bundleChildLauncher,
	retryDelay func(int) time.Duration,
) error {
	selected, err := b.selected(ids)
	if err != nil {
		return err
	}
	if b.Mode != "parallel" {
		return errors.New("parallel Bundle supervisor requires mode=parallel")
	}
	if err := validateSelectedForRuntime(selected); err != nil {
		return err
	}
	if err := preflightBundlePorts(selected); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	cfgByID := make(map[string][]byte, len(selected))
	for _, profile := range selected {
		cfgData, err := json.Marshal(profile.config())
		if err != nil {
			return fmt.Errorf("Profile %s 配置编码失败: %w", profile.DisplayName, err)
		}
		cfgByID[profile.ProfileID] = cfgData
	}

	groupCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan bundleChildEvent, 256)
	lifecycle := make(chan bundleProfileLifecycle, 256)

	for _, profile := range selected {
		profile := profile
		go superviseParallelBundleProfile(
			groupCtx,
			exe,
			profile,
			cfgByID[profile.ProfileID],
			launch,
			retryDelay,
			events,
			lifecycle,
		)
	}

	currentGeneration := make(map[string]uint64, len(selected))
	runtimeActive := make(map[string]bool, len(selected))
	ready := make(map[string]bool, len(selected))
	retrying := make(map[string]bool, len(selected))
	tcpStats := make(map[string]Event, len(selected))
	udpStats := make(map[string]Event, len(selected))

	readyCount := func() int {
		n := 0
		for id := range ready {
			if runtimeActive[id] {
				n++
			}
		}
		return n
	}
	connectingCount := func() int {
		n := 0
		for id, active := range runtimeActive {
			if active && !ready[id] {
				n++
			}
		}
		return n
	}
	reconnectingCount := func() int {
		n := 0
		for _, value := range retrying {
			if value {
				n++
			}
		}
		return n
	}
	emitBundleState := func(kind, message string) {
		emit(Event{
			Kind: kind, BundleID: b.BundleID, BundleName: b.DisplayName,
			TotalProfiles: len(selected), ActiveProfiles: readyCount(),
			ConnectingProfiles: connectingCount(), ReconnectingProfiles: reconnectingCount(),
			Message: message,
		})
	}
	emitAggregate := func(kind string, stats map[string]Event) {
		agg := Event{
			Kind: kind, BundleID: b.BundleID, BundleName: b.DisplayName,
			TotalProfiles: len(selected), ActiveProfiles: readyCount(),
			ConnectingProfiles: connectingCount(), ReconnectingProfiles: reconnectingCount(),
		}
		for id, event := range stats {
			if !runtimeActive[id] {
				continue
			}
			agg.Paths += event.Paths
			agg.Connections += event.Connections
			agg.Sent += event.Sent
			agg.Received += event.Received
			agg.Retransmits += event.Retransmits
			agg.Dropped += event.Dropped
		}
		emit(agg)
	}
	emitCurrentBundleState := func() {
		active := readyCount()
		connecting := connectingCount()
		reconnecting := reconnectingCount()
		switch {
		case active > 0 && reconnecting > 0:
			emitBundleState("bundle_degraded", fmt.Sprintf("%d 个 Profile 正常运行，%d 个正在自动重连", active, reconnecting))
		case active > 0:
			message := fmt.Sprintf("%d/%d 个 Profile 已启动", active, len(selected))
			if connecting > 0 {
				message += fmt.Sprintf("，%d 个仍在连接", connecting)
			}
			emitBundleState("bundle_listening", message)
		case reconnecting > 0:
			emitBundleState("bundle_reconnecting", fmt.Sprintf("当前无可用 Profile；%d 个正在自动重连", reconnecting))
		default:
			emitBundleState("bundle_connecting", fmt.Sprintf("%d 个 Profile 正在连接", connecting))
		}
	}

	for {
		select {
		case <-ctx.Done():
			cancel()
			return ctx.Err()

		case state := <-lifecycle:
			id := state.profile.ProfileID
			if state.generation < currentGeneration[id] {
				continue
			}
			currentGeneration[id] = state.generation
			switch state.kind {
			case "started":
				runtimeActive[id] = true
				retrying[id] = false
				delete(ready, id)
				emit(Event{
					Kind: "reconnect_attempt", ProfileID: id, ProfileName: state.profile.DisplayName,
					BundleID: b.BundleID, BundleName: b.DisplayName, ListenPort: state.profile.ListenPort,
					Message: "Profile 正在重新连接",
				})
				emitCurrentBundleState()

			case "retrying":
				runtimeActive[id] = false
				retrying[id] = true
				delete(ready, id)
				delete(tcpStats, id)
				delete(udpStats, id)
				seconds := int(state.retryAfter / time.Second)
				if seconds < 1 {
					seconds = 1
				}
				emit(Event{
					Kind: "reconnecting", ProfileID: id, ProfileName: state.profile.DisplayName,
					BundleID: b.BundleID, BundleName: b.DisplayName, ListenPort: state.profile.ListenPort,
					Message:           fmt.Sprintf("%d 秒后自动重连", seconds),
					RetryAfterSeconds: seconds, RetryAttempt: state.attempt,
				})
				emitAggregate("bundle_stats", tcpStats)
				emitAggregate("bundle_udp_stats", udpStats)
				emitCurrentBundleState()
			}

		case childEvent := <-events:
			id := childEvent.profile.ProfileID
			generation := childEvent.generation
			if generation < currentGeneration[id] {
				continue
			}
			if generation > currentGeneration[id] {
				currentGeneration[id] = generation
				runtimeActive[id] = true
				retrying[id] = false
			} else if !runtimeActive[id] {
				continue
			}
			event := childEvent.event
			event.ProfileID = id
			event.ProfileName = childEvent.profile.DisplayName
			event.BundleID = b.BundleID
			event.BundleName = b.DisplayName
			event.ListenPort = childEvent.profile.ListenPort
			emit(event)
			switch event.Kind {
			case "listening":
				ready[id] = true
				retrying[id] = false
				emitCurrentBundleState()
			case "stats":
				tcpStats[id] = event
				emitAggregate("bundle_stats", tcpStats)
			case "udp_stats":
				udpStats[id] = event
				emitAggregate("bundle_udp_stats", udpStats)
			}
		}
	}
}

func runBundleWithLauncher(ctx context.Context, b BundlePayload, ids []string, launch bundleChildLauncher) error {
	if b.Mode == "parallel" {
		return runParallelBundleWithLauncherRetry(ctx, b, ids, launch, bundleProfileRetryDelay)
	}
	selected, err := b.selected(ids)
	if err != nil {
		return err
	}
	if err := validateSelectedForRuntime(selected); err != nil {
		return err
	}
	if err := preflightBundlePorts(selected); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	groupCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan bundleChildEvent, 128)
	exits := make(chan bundleChildExit, len(selected))
	cmds := make(map[string]*exec.Cmd, len(selected))
	started := make(map[string]bool, len(selected))
	exitedProfiles := make(map[string]bool, len(selected))
	failedStarts := make(map[string]string, len(selected))
	profileErrors := make(map[string]string, len(selected))

	emitProfileError := func(p BundleProfile, message string) {
		if message == "" {
			message = "Profile 不可用"
		}
		profileErrors[p.ProfileID] = message
		emit(Event{Kind: "error", ProfileID: p.ProfileID, ProfileName: p.DisplayName, BundleID: b.BundleID, BundleName: b.DisplayName, ListenPort: p.ListenPort, Message: message})
	}

	for _, p := range selected {
		cfgData, err := json.Marshal(p.config())
		if err != nil {
			failedStarts[p.ProfileID] = err.Error()
			emitProfileError(p, fmt.Sprintf("Profile %s 配置编码失败: %v", p.DisplayName, err))
			continue
		}
		cmd, stdout, err := launch(groupCtx, exe, p, cfgData)
		if err != nil {
			failedStarts[p.ProfileID] = err.Error()
			emitProfileError(p, fmt.Sprintf("Profile %s 启动失败: %v", p.DisplayName, err))
			continue
		}
		cmds[p.ProfileID] = cmd
		started[p.ProfileID] = true
		go func(profile BundleProfile, r io.Reader) {
			send := func(event bundleChildEvent) bool {
				select {
				case events <- event:
					return true
				case <-groupCtx.Done():
					return false
				}
			}
			scanner := bufio.NewScanner(r)
			buf := make([]byte, 0, 64*1024)
			scanner.Buffer(buf, 256*1024)
			for scanner.Scan() {
				var e Event
				if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
					_ = send(bundleChildEvent{profile: profile, scanErr: fmt.Errorf("engine event JSON: %w", err)})
					return
				}
				if !send(bundleChildEvent{profile: profile, event: e}) {
					return
				}
			}
			if err := scanner.Err(); err != nil {
				_ = send(bundleChildEvent{profile: profile, scanErr: err})
			}
		}(p, stdout)
		go func(profile BundleProfile, c *exec.Cmd) { exits <- bundleChildExit{profile: profile, err: c.Wait()} }(p, cmd)
	}

	startedCount := len(started)
	if startedCount == 0 {
		message := fmt.Sprintf("所有 %d 个所选 Profile 均启动失败", len(selected))
		emit(Event{Kind: "bundle_failed", BundleID: b.BundleID, BundleName: b.DisplayName, TotalProfiles: len(selected), FailedProfiles: len(selected), Message: message})
		return errors.New(message)
	}

	ready := map[string]bool{}
	tcpStats := map[string]Event{}
	udpStats := map[string]Event{}
	exited := 0
	aliveCount := func() int {
		n := 0
		for id := range started {
			if !exitedProfiles[id] {
				n++
			}
		}
		return n
	}
	readyCount := func() int {
		n := 0
		for id := range ready {
			if !exitedProfiles[id] {
				n++
			}
		}
		return n
	}
	failedCount := func() int { return len(failedStarts) + len(exitedProfiles) }
	connectingCount := func() int {
		n := aliveCount() - readyCount()
		if n < 0 {
			return 0
		}
		return n
	}
	emitBundleState := func(kind, message string) {
		emit(Event{Kind: kind, BundleID: b.BundleID, BundleName: b.DisplayName, TotalProfiles: len(selected), ActiveProfiles: readyCount(), ConnectingProfiles: connectingCount(), FailedProfiles: failedCount(), Message: message})
	}
	emitAggregate := func(kind string, stats map[string]Event) {
		agg := Event{Kind: kind, BundleID: b.BundleID, BundleName: b.DisplayName, TotalProfiles: len(selected), ActiveProfiles: readyCount(), ConnectingProfiles: connectingCount(), FailedProfiles: failedCount()}
		for id, e := range stats {
			if exitedProfiles[id] {
				continue
			}
			agg.Paths += e.Paths
			agg.Connections += e.Connections
			agg.Sent += e.Sent
			agg.Received += e.Received
			agg.Retransmits += e.Retransmits
			agg.Dropped += e.Dropped
		}
		emit(agg)
	}
	if len(failedStarts) > 0 {
		emitBundleState("bundle_connecting", fmt.Sprintf("%d 个 Profile 启动失败；其余 %d 个继续连接", len(failedStarts), aliveCount()))
	}
	for {
		select {
		case <-ctx.Done():
			cancel()
			for exited < startedCount {
				<-exits
				exited++
			}
			return ctx.Err()
		case ce := <-events:
			if ce.scanErr != nil {
				if _, seen := profileErrors[ce.profile.ProfileID]; !seen {
					emitProfileError(ce.profile, fmt.Sprintf("Profile %s 输出读取失败: %v", ce.profile.DisplayName, ce.scanErr))
				}
				if cmd := cmds[ce.profile.ProfileID]; cmd != nil && cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				continue
			}
			e := ce.event
			e.ProfileID = ce.profile.ProfileID
			e.ProfileName = ce.profile.DisplayName
			e.BundleID = b.BundleID
			e.BundleName = b.DisplayName
			e.ListenPort = ce.profile.ListenPort
			emit(e)
			if e.Kind == "error" {
				profileErrors[ce.profile.ProfileID] = e.Message
			}
			// Wait may win the race with draining a child's final stdout. Forward
			// late diagnostics, but never let a dead child re-enter ready/stats state.
			if exitedProfiles[ce.profile.ProfileID] {
				continue
			}
			switch e.Kind {
			case "listening":
				ready[ce.profile.ProfileID] = true
				active, failed, connecting := readyCount(), failedCount(), connectingCount()
				message := fmt.Sprintf("%d/%d 个 Profile 已启动", active, len(selected))
				if failed > 0 {
					message += fmt.Sprintf("，%d 个失败；可用配置继续运行", failed)
				} else if connecting > 0 {
					message += fmt.Sprintf("，%d 个仍在连接", connecting)
				}
				emitBundleState("bundle_listening", message)
			case "stats":
				tcpStats[ce.profile.ProfileID] = e
				emitAggregate("bundle_stats", tcpStats)
			case "udp_stats":
				udpStats[ce.profile.ProfileID] = e
				emitAggregate("bundle_udp_stats", udpStats)
			}
		case ex := <-exits:
			exited++
			if groupCtx.Err() != nil {
				if exited == startedCount {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return errors.New("Bundle 运行已停止")
				}
				continue
			}
			id := ex.profile.ProfileID
			exitedProfiles[id] = true
			delete(ready, id)
			delete(tcpStats, id)
			delete(udpStats, id)
			if _, seen := profileErrors[id]; !seen {
				message := fmt.Sprintf("Profile %s 意外退出", ex.profile.DisplayName)
				if ex.err != nil {
					message = fmt.Sprintf("Profile %s 已退出: %v", ex.profile.DisplayName, ex.err)
				}
				emitProfileError(ex.profile, message)
			}
			emitAggregate("bundle_stats", tcpStats)
			emitAggregate("bundle_udp_stats", udpStats)
			if aliveCount() == 0 {
				message := fmt.Sprintf("所有 %d 个所选 Profile 均不可用", len(selected))
				emitBundleState("bundle_failed", message)
				cancel()
				return errors.New(message)
			}
			if readyCount() > 0 {
				emitBundleState("bundle_degraded", fmt.Sprintf("%d 个 Profile 正常运行，%d 个失败；可用配置继续运行", readyCount(), failedCount()))
			} else {
				emitBundleState("bundle_connecting", fmt.Sprintf("%d 个 Profile 失败；其余 %d 个仍在连接", failedCount(), aliveCount()))
			}
		}
	}
}

type streamConn interface {
	net.Conn
	CloseWrite() error
}
type forwardConn interface {
	streamConn
	paths() int
}
type tunnelDialer func(context.Context) (forwardConn, error)
type counters struct{ sent, received atomic.Int64 }
type activityWriter struct {
	conn        net.Conn
	count, last *atomic.Int64
}

func (w activityWriter) Write(data []byte) (int, error) {
	if err := w.conn.SetWriteDeadline(time.Now().Add(45 * time.Second)); err != nil {
		return 0, err
	}
	n, err := w.conn.Write(data)
	if n > 0 {
		w.count.Add(int64(n))
		w.last.Store(time.Now().UnixNano())
	}
	return n, err
}

// Each accepted byte is copied unchanged. No TLS, SOCKS greeting or framing is added.
func bridge(ctx context.Context, a, b streamConn, totals *counters, idle time.Duration) {
	defer a.Close()
	defer b.Close()
	var activity atomic.Int64
	activity.Store(time.Now().UnixNano())
	done := make(chan error, 2)
	copyDirection := func(dst, src streamConn, count *atomic.Int64) {
		_, err := io.CopyBuffer(activityWriter{conn: dst, count: count, last: &activity}, src, make([]byte, 32*1024))
		if err == nil {
			err = dst.CloseWrite()
		}
		done <- err
	}
	go copyDirection(b, a, &totals.sent)
	go copyDirection(a, b, &totals.received)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	cancelled := ctx.Done()
	completed := 0
	for completed < 2 {
		select {
		case err := <-done:
			completed++
			if err != nil {
				a.Close()
				b.Close()
			}
		case <-cancelled:
			a.Close()
			b.Close()
			cancelled = nil
		case now := <-ticker.C:
			if idle > 0 && now.Sub(time.Unix(0, activity.Load())) >= idle {
				a.Close()
				b.Close()
			}
		}
	}
}

func serveForward(ctx context.Context, listener net.Listener, dial tunnelDialer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	var mu sync.Mutex
	locals := map[net.Conn]bool{}
	active := map[forwardConn]bool{}
	var totals counters
	closeConnections := func() {
		listener.Close()
		mu.Lock()
		connections := make([]net.Conn, 0, len(locals)+len(active))
		for c := range locals {
			connections = append(connections, c)
		}
		for c := range active {
			connections = append(connections, c)
		}
		mu.Unlock()
		for _, c := range connections {
			c.Close()
		}
	}
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var retryStatsAt time.Time
		lastDiagnostic := ""
		for {
			select {
			case <-ctx.Done():
				closeConnections()
				return
			case <-ticker.C:
				mu.Lock()
				count := len(active)
				connections := make([]forwardConn, 0, count)
				for c := range active {
					connections = append(connections, c)
				}
				mu.Unlock()
				paths := 0
				if count > 0 {
					paths = 8
				}
				if count > 0 {
					if time.Now().Before(retryStatsAt) {
						paths = -1
					} else {
						counts, diagnostic := nativePathCounts(connections)
						paths = minimumPathCount(counts)
						if diagnostic != nil {
							if errors.Is(diagnostic, errPCBRead) {
								retryStatsAt = time.Now().Add(30 * time.Second)
							}
							if diagnostic.Error() != lastDiagnostic {
								emit(Event{Kind: "warning", Message: fmt.Sprintf("子流统计暂不可用（不影响转发，将自动重试）: %v", diagnostic)})
							}
							lastDiagnostic = diagnostic.Error()
						} else {
							lastDiagnostic = ""
						}
					}
				}
				emit(Event{Kind: "stats", Paths: paths, Connections: int64(count), Sent: totals.sent.Load(), Received: totals.received.Load()})
			}
		}
	}()
	defer func() { cancel(); <-monitorDone; workers.Wait() }()
	slots := make(chan struct{}, 128)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		mu.Lock()
		if ctx.Err() != nil {
			mu.Unlock()
			conn.Close()
			<-slots
			return nil
		}
		locals[conn] = true
		mu.Unlock()
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			defer func() { conn.Close(); mu.Lock(); delete(locals, conn); mu.Unlock() }()
			local, ok := conn.(streamConn)
			if !ok {
				return
			}
			remote, err := dial(ctx)
			if err != nil {
				if ctx.Err() == nil {
					emit(Event{Kind: "warning", Message: err.Error()})
				}
				return
			}
			defer remote.Close()
			mu.Lock()
			if ctx.Err() != nil {
				mu.Unlock()
				return
			}
			active[remote] = true
			mu.Unlock()
			defer func() { mu.Lock(); delete(active, remote); mu.Unlock() }()
			bridge(ctx, local, remote, &totals, 15*time.Minute)
		}()
	}
}

func runClient(ctx context.Context, c Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := c.validate(); err != nil {
		return err
	}
	if c.userspace() {
		return runUserspace(ctx, c)
	}
	if err := nativePreflight(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.ListenPort)))
	if err != nil {
		return err
	}
	defer listener.Close()
	emit(Event{Kind: "connecting", Message: "正在检查原生 MPTCP v1 握手"})
	probe, err := nativeDial(ctx, c.Relays)
	if err != nil {
		return err
	}
	// nativeDial already verifies this socket's MPTCP v1 handshake. The global
	// PCB list is optional telemetry and must never gate forwarding startup.
	probe.Close()
	emit(Event{Kind: "ready", Message: "MPTCP v1 握手通过；多路径聚合尚未验证"})
	if c.UDPEnabled {
		udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: c.ListenPort})
		if err != nil {
			return fmt.Errorf("UDP 入口启动失败: %w", err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := serveUDP(ctx, udp, c.Relays, 60*time.Second, 512); err != nil && ctx.Err() == nil {
				emit(Event{Kind: "error", Message: fmt.Sprintf("UDP 转发已停止: %v", err)})
				cancel()
			}
		}()
		defer func() { cancel(); udp.Close(); <-done }()
		emit(Event{Kind: "udp_listening", Message: "UDP 轮询入口 " + udp.LocalAddr().String()})
	}
	emit(Event{Kind: "listening", Message: listener.Addr().String()})
	return serveForward(ctx, listener, func(ctx context.Context) (forwardConn, error) { return nativeDial(ctx, c.Relays) })
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch {
	case len(os.Args) == 2 && os.Args[1] == "doctor":
		err = nativePreflight()
		if err == nil {
			emit(Event{Kind: "ready", Message: "原生 MPTCP 聚合接口可用"})
		}
	case len(os.Args) == 2 && os.Args[1] == "doctor-userspace":
		err = ensureUserspaceFileLimit()
		if err == nil {
			emit(Event{Kind: "ready", Mode: "userspace_multipath", Message: fmt.Sprintf("Userspace 引擎可用；RLIMIT_NOFILE 已提升/满足 %d，可承载 %d 业务流；尚未检查 Relay、Landing 密钥和端口", userspaceDesiredNOFILE, multipath.MaxStreams)})
		}
	case len(os.Args) == 2 && os.Args[1] == "version":
		name := "mptcp-desktop-engine"
		message := name + " " + multipath.Version + " MPX/4 Draft 04 + Native fallback"
		if runtime.GOOS == "linux" {
			name = "mptcp-client"
			message = name + " " + multipath.Version + " MPX/4 Draft 04 userspace client"
		}
		emit(Event{Kind: "ready", Version: multipath.Version, SourceID: multipath.SourceID, WireProtocol: multipath.WireProtocol, Message: message})
	case len(os.Args) == 2 && (os.Args[1] == "run-managed" || os.Args[1] == "validate-managed"):
		var in ManagedInput
		in, err = readManagedInput(os.Stdin)
		if err == nil {
			err = runManaged(ctx, in, os.Args[1] == "validate-managed")
		}
	case len(os.Args) >= 2 && os.Args[1] == "run-bundle":
		var b BundlePayload
		b, err = readBundle(os.Stdin)
		if err == nil {
			err = runBundle(ctx, b, os.Args[2:])
		}
	case len(os.Args) >= 2 && os.Args[1] == "validate-bundle":
		var b BundlePayload
		b, err = readBundle(os.Stdin)
		if err == nil {
			var selected []BundleProfile
			selected, err = b.selected(os.Args[2:])
			if err == nil {
				err = validateSelectedForRuntime(selected)
			}
		}
		if err == nil {
			emit(Event{Kind: "ready", Message: "Bundle 配置与所选 Profile 有效"})
		}
	case len(os.Args) == 2 && os.Args[1] == "run":
		var c Config
		c, err = readConfig(os.Stdin)
		if err == nil {
			err = runClient(ctx, c)
		}
	case len(os.Args) == 2 && os.Args[1] == "validate":
		_, err = readConfig(os.Stdin)
		if err == nil {
			emit(Event{Kind: "ready", Message: "TCP 转发配置有效"})
		}
	default:
		err = fmt.Errorf("usage: mptcp-client doctor-userspace | version | run | validate | run-managed | validate-managed | run-bundle [profile-id...] | validate-bundle [profile-id...] (JSON stdin); Native doctor is macOS-only")
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		emit(Event{Kind: "error", Message: err.Error()})
		os.Exit(1)
	}
}
