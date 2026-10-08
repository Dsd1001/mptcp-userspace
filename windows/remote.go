package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type remoteCredential struct {
	DeviceID string `json:"device_id"`
	Secret   string `json:"secret"`
}

type remotePairResponse struct {
	DeviceID        string `json:"device_id"`
	DeviceSecret    string `json:"device_secret"`
	ControlRevision uint64 `json:"control_revision"`
}

type remoteDesiredState struct {
	Revision          uint64 `json:"revision"`
	DesiredState      string `json:"desired_state"`
	AssignmentType    string `json:"assignment_type,omitempty"`
	AssignmentID      string `json:"assignment_id,omitempty"`
	ProvisioningURL   string `json:"provisioning_url,omitempty"`
	RestartGeneration uint64 `json:"restart_generation,omitempty"`
	SyncGeneration    uint64 `json:"sync_generation,omitempty"`
	UpdateGeneration  uint64 `json:"update_generation,omitempty"`
	DesiredVersion    string `json:"desired_version,omitempty"`
}

type remoteDeviceReport struct {
	AppVersion        string            `json:"app_version"`
	Running           bool              `json:"running"`
	Status            string            `json:"status"`
	ConfigRevision    string            `json:"config_revision"`
	BundleID          string            `json:"bundle_id"`
	ProfileStatus     map[string]string `json:"profile_status"`
	UpdateStatus      string            `json:"update_status"`
	LastError         string            `json:"last_error"`
	RestartGeneration uint64            `json:"restart_generation"`
	SyncGeneration    uint64            `json:"sync_generation"`
	UpdateGeneration  uint64            `json:"update_generation"`
}

func validateRemoteRoot(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("远程管理服务器需填写 HTTPS 根地址，例如 https://control.example.com")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("远程管理服务器只能填写根地址，不能包含路径")
	}
	host := strings.ToLower(parsed.Hostname())
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && !(scheme == "http" && loopback) {
		return nil, errors.New("远程管理服务器必须使用 HTTPS")
	}
	parsed.Scheme = scheme
	parsed.Path = ""
	return parsed, nil
}

func remoteURL(base *url.URL, path string, query url.Values) string {
	copyURL := *base
	copyURL.Path = path
	copyURL.RawQuery = query.Encode()
	return copyURL.String()
}

func (a *App) remoteCredentialLocked() (*remoteCredential, error) {
	if a.settings.RemoteCredentialCipher == "" {
		return nil, nil
	}
	text, err := unprotectString(a.settings.RemoteCredentialCipher)
	if err != nil {
		return nil, err
	}
	var credential remoteCredential
	if err := json.Unmarshal([]byte(text), &credential); err != nil {
		return nil, err
	}
	if credential.DeviceID == "" || credential.Secret == "" {
		return nil, errors.New("远程管理凭据无效")
	}
	return &credential, nil
}

func (a *App) refreshRemoteViewLocked() {
	credential, _ := a.remoteCredentialLocked()
	deviceID := ""
	if credential != nil {
		deviceID = credential.DeviceID
	}
	status := a.state.Remote.Status
	if !a.settings.RemoteEnabled {
		status = "关闭"
	}
	if status == "" && a.settings.RemoteEnabled {
		if credential == nil {
			status = "等待配对"
		} else {
			status = "正在连接…"
		}
	}
	a.state.Remote.Enabled = a.settings.RemoteEnabled
	a.state.Remote.Server = a.settings.RemoteServer
	a.state.Remote.DeviceID = deviceID
	a.state.Remote.Status = status
}

func (a *App) SetRemoteManagement(enabled bool, server string) error {
	server = strings.TrimSpace(server)
	if server != "" {
		base, err := validateRemoteRoot(server)
		if err != nil {
			return err
		}
		server = strings.TrimRight(base.String(), "/")
	}
	a.mu.Lock()
	if enabled && server == "" {
		a.mu.Unlock()
		return errors.New("请先填写远程管理服务器")
	}
	a.settings.RemoteEnabled = enabled
	a.settings.RemoteServer = server
	if !enabled && a.remoteCancel != nil {
		a.remoteCancel()
		a.remoteCancel = nil
	}
	if enabled {
		a.state.Remote.Status = "正在连接…"
	} else {
		a.state.Remote.Status = "关闭"
		a.state.Remote.Connected = false
	}
	err := saveSettings(a.settings)
	resident := a.effectiveBackgroundResidentLocked()
	a.refreshSettingsViewLocked()
	a.refreshRemoteViewLocked()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if err := setAutostart(resident || enabled); err != nil {
		return err
	}
	if enabled {
		a.startRemoteControlLoop()
	}
	a.emitState()
	return nil
}

func (a *App) PairRemoteManagement(server, code string) error {
	base, err := validateRemoteRoot(server)
	if err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return errors.New("请输入一次性配对码")
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "Windows PC"
	}
	payload, _ := json.Marshal(map[string]string{
		"code":        code,
		"device_name": hostname,
		"app_version": appVersion,
	})
	req, err := http.NewRequest(http.MethodPost, remoteURL(base, "/v1/device/pair", nil), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("远程管理配对失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden {
			return errors.New("配对码无效或已过期")
		}
		return fmt.Errorf("远程管理配对失败（HTTP %d）", resp.StatusCode)
	}
	var paired remotePairResponse
	if err := json.Unmarshal(body, &paired); err != nil || paired.DeviceID == "" || paired.DeviceSecret == "" {
		return errors.New("远程管理配对响应无效")
	}
	encoded, _ := json.Marshal(remoteCredential{DeviceID: paired.DeviceID, Secret: paired.DeviceSecret})
	cipherText, err := protectString(string(encoded))
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.settings.RemoteServer = strings.TrimRight(base.String(), "/")
	a.settings.RemoteEnabled = true
	a.settings.RemoteCredentialCipher = cipherText
	a.settings.RemoteRevision = paired.ControlRevision
	a.state.Remote.Status = "已配对 · 正在连接"
	a.state.Remote.Connected = false
	err = saveSettings(a.settings)
	resident := a.effectiveBackgroundResidentLocked()
	a.refreshSettingsViewLocked()
	a.refreshRemoteViewLocked()
	a.appendLogLocked("远程管理设备已配对")
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if err := setAutostart(resident || true); err != nil {
		return err
	}
	a.startRemoteControlLoop()
	a.emitState()
	return nil
}

func (a *App) UnpairRemoteManagement() error {
	a.mu.Lock()
	credential, _ := a.remoteCredentialLocked()
	server := a.settings.RemoteServer
	if a.remoteCancel != nil {
		a.remoteCancel()
		a.remoteCancel = nil
	}
	a.settings.RemoteCredentialCipher = ""
	a.settings.RemoteEnabled = false
	a.settings.RemoteRevision = 0
	a.settings.RemoteRestartGeneration = 0
	a.settings.RemoteSyncGeneration = 0
	a.settings.RemoteUpdateGeneration = 0
	a.state.Remote.Connected = false
	a.state.Remote.Status = "未配对"
	err := saveSettings(a.settings)
	resident := a.effectiveBackgroundResidentLocked()
	a.refreshSettingsViewLocked()
	a.refreshRemoteViewLocked()
	a.appendLogLocked("远程管理设备已在本机解除配对")
	a.mu.Unlock()
	if err == nil {
		_ = setAutostart(resident)
	}
	if credential != nil && server != "" {
		go func() { _ = remoteUnpair(server, *credential) }()
	}
	a.emitState()
	return err
}

func authenticatedRemoteRequest(method, raw string, credential remoteCredential, body []byte) (*http.Request, error) {
	req, err := http.NewRequest(method, raw, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MPX-Device-ID", credential.DeviceID)
	req.Header.Set("Authorization", "Bearer "+credential.Secret)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (a *App) startRemoteControlLoop() {
	a.mu.Lock()
	if a.remoteCancel != nil {
		a.remoteCancel()
	}
	if !a.settings.RemoteEnabled {
		a.remoteCancel = nil
		a.mu.Unlock()
		return
	}
	base, err := validateRemoteRoot(a.settings.RemoteServer)
	credential, credentialErr := a.remoteCredentialLocked()
	if err != nil || credentialErr != nil || credential == nil {
		a.remoteCancel = nil
		if credential == nil {
			a.state.Remote.Status = "等待配对"
		} else {
			a.state.Remote.Status = "需要重新配对"
		}
		a.state.Remote.Connected = false
		a.mu.Unlock()
		a.emitState()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.remoteCancel = cancel
	a.state.Remote.Status = "正在连接…"
	a.state.Remote.Connected = false
	a.mu.Unlock()
	a.emitState()
	go a.remoteLoop(ctx, base, *credential)
}

func (a *App) remoteLoop(ctx context.Context, base *url.URL, credential remoteCredential) {
	delays := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	retry := 0
	client := &http.Client{Timeout: 40 * time.Second}
	for ctx.Err() == nil {
		a.mu.Lock()
		since := a.settings.RemoteRevision
		enabled := a.settings.RemoteEnabled
		a.mu.Unlock()
		if !enabled {
			return
		}
		pollURL := remoteURL(base, "/v1/device/poll", url.Values{"since": []string{fmt.Sprint(since)}})
		req, err := authenticatedRemoteRequest(http.MethodGet, pollURL, credential, nil)
		if err == nil {
			var resp *http.Response
			resp, err = client.Do(req.WithContext(ctx))
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
				_ = resp.Body.Close()
				if readErr != nil {
					err = readErr
				} else if resp.StatusCode == http.StatusUnauthorized {
					a.mu.Lock()
					a.state.Remote.Connected = false
					a.state.Remote.Status = "需要重新配对"
					a.mu.Unlock()
					a.emitState()
					if !sleepContext(ctx, 30*time.Second) {
						return
					}
					continue
				} else if resp.StatusCode != http.StatusOK {
					err = fmt.Errorf("远程管理 HTTP %d", resp.StatusCode)
				} else {
					var desired remoteDesiredState
					if decodeErr := json.Unmarshal(body, &desired); decodeErr != nil {
						err = decodeErr
					} else {
						a.mu.Lock()
						a.state.Remote.Connected = true
						a.state.Remote.Status = "已连接"
						a.mu.Unlock()
						a.emitState()
						a.applyRemoteDesired(desired)
						_ = a.remoteReport(base, credential)
						retry = 0
						continue
					}
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		a.state.Remote.Connected = false
		a.state.Remote.Status = "连接中断 · 自动重试"
		if err != nil {
			a.appendLogLocked("远程管理：" + err.Error())
		}
		a.mu.Unlock()
		a.emitState()
		delay := delays[minInt(retry, len(delays)-1)]
		if retry < len(delays)-1 {
			retry++
		}
		if !sleepContext(ctx, delay) {
			return
		}
	}
}

func (a *App) applyRemoteDesired(desired remoteDesiredState) {
	a.mu.Lock()
	if desired.Revision >= a.settings.RemoteRevision {
		a.settings.RemoteRevision = desired.Revision
	}
	restart := desired.RestartGeneration > a.settings.RemoteRestartGeneration
	syncRequested := desired.SyncGeneration > a.settings.RemoteSyncGeneration
	updateRequested := desired.UpdateGeneration > a.settings.RemoteUpdateGeneration
	if restart {
		a.settings.RemoteRestartGeneration = desired.RestartGeneration
	}
	if syncRequested {
		a.settings.RemoteSyncGeneration = desired.SyncGeneration
	}
	if updateRequested {
		a.settings.RemoteUpdateGeneration = desired.UpdateGeneration
	}
	_ = saveSettings(a.settings)
	a.mu.Unlock()

	if strings.TrimSpace(desired.ProvisioningURL) != "" {
		current := ""
		a.mu.Lock()
		current, _ = unprotectString(a.settings.ProvisioningCipher)
		a.mu.Unlock()
		if current != desired.ProvisioningURL {
			if err := a.SetProvisioningURL(desired.ProvisioningURL); err != nil {
				a.setProblem("远程配置分配失败：" + err.Error())
			}
		}
	}
	if syncRequested {
		if _, err := a.SyncProvisioning(); err != nil {
			a.setProblem("远程配置同步失败：" + err.Error())
		}
	}
	if restart {
		_ = a.Stop()
		if desired.DesiredState == "running" {
			_ = a.Start()
		}
	} else {
		switch desired.DesiredState {
		case "running":
			_ = a.Start()
		case "stopped":
			_ = a.Stop()
		}
	}
	if updateRequested && (desired.DesiredVersion == "" || desired.DesiredVersion == "latest") {
		go func() {
			info, err := a.CheckForUpdates(false)
			if err == nil && info.Available {
				_ = a.InstallUpdate()
			}
		}()
	}
}

func (a *App) remoteReport(base *url.URL, credential remoteCredential) error {
	a.mu.Lock()
	revision, bundleID := "", ""
	if a.managed != nil {
		if a.managed.Profile != nil {
			revision = a.managed.Profile.Revision
		}
		if a.managed.Bundle != nil {
			revision = a.managed.Bundle.Revision
			bundleID = a.managed.Bundle.BundleID
		}
	}
	report := remoteDeviceReport{
		AppVersion:        appVersion,
		Running:           a.state.Running,
		Status:            a.state.Status,
		ConfigRevision:    revision,
		BundleID:          bundleID,
		ProfileStatus:     cloneStringMap(a.state.ProfileStatus),
		UpdateStatus:      a.state.Update.Status,
		LastError:         a.state.Problem,
		RestartGeneration: a.settings.RemoteRestartGeneration,
		SyncGeneration:    a.settings.RemoteSyncGeneration,
		UpdateGeneration:  a.settings.RemoteUpdateGeneration,
	}
	a.mu.Unlock()
	body, _ := json.Marshal(report)
	req, err := authenticatedRemoteRequest(http.MethodPost, remoteURL(base, "/v1/device/report", nil), credential, body)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("远程状态上报失败 HTTP %d", resp.StatusCode)
	}
	return nil
}

func remoteUnpair(server string, credential remoteCredential) error {
	base, err := validateRemoteRoot(server)
	if err != nil {
		return err
	}
	req, err := authenticatedRemoteRequest(http.MethodPost, remoteURL(base, "/v1/device/unpair", nil), credential, []byte("{}"))
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func cloneStringMap(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
