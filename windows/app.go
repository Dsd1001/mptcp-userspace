package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx context.Context
	mu  sync.Mutex

	settings diskSettings
	managed  *managedDocument

	engineCancel    context.CancelFunc
	engineDone      chan struct{}
	enginePID       int
	retryToken      uint64
	recoveryAttempt int

	remoteCancel   context.CancelFunc
	updateManifest *windowsUpdateManifest
	tray           *windowsTray

	backgroundLaunch bool
	state            AppState
}

func NewApp() *App {
	settings, err := loadSettings()
	if err != nil {
		settings = defaultSettings()
	}
	a := &App{settings: settings}
	a.state = AppState{
		Version:        appVersion,
		Platform:       "windows",
		DesiredRunning: settings.DesiredRunning,
		Status:         "已停止",
		ProfileStatus:  map[string]string{},
		ProfileErrors:  map[string]string{},
		ProfileEvents:  map[string]EngineEvent{},
		Update: UpdateView{
			Automatic: settings.AutomaticUpdates,
			Status:    "自动检查已启用",
		},
		Remote: RemoteView{
			Enabled: settings.RemoteEnabled,
			Server:  settings.RemoteServer,
			Status:  "关闭",
		},
	}
	if err != nil {
		a.state.Problem = "读取设置失败：" + err.Error()
	}
	a.loadManagedCacheLocked()
	a.refreshSettingsViewLocked()
	a.refreshManagedSummaryLocked()
	a.refreshRemoteViewLocked()
	return a
}

func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	resident := a.effectiveBackgroundResidentLocked()
	remoteEnabled := a.settings.RemoteEnabled
	desired := a.settings.DesiredRunning
	a.mu.Unlock()

	a.startTray()
	if err := setAutostart(resident || remoteEnabled); err != nil {
		a.setProblem("配置 Windows 登录启动失败：" + err.Error())
	}
	if err := verifyEngineBinary(); err != nil {
		a.setProblem(err.Error())
	}
	if remoteEnabled {
		a.startRemoteControlLoop()
	}
	if (resident || remoteEnabled) && desired {
		go func() {
			time.Sleep(700 * time.Millisecond)
			_ = a.Start()
		}()
	}
	if a.settings.AutomaticUpdates {
		go func() {
			time.Sleep(4 * time.Second)
			_, _ = a.CheckForUpdates(false)
		}()
	}
}

func (a *App) domReady(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
	a.emitState()
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	if a.remoteCancel != nil {
		a.remoteCancel()
		a.remoteCancel = nil
	}
	tray := a.tray
	a.tray = nil
	cancel := a.engineCancel
	a.settings.DesiredRunning = a.state.DesiredRunning
	_ = saveSettings(a.settings)
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if tray != nil {
		tray.Close()
	}
}

func (a *App) GetState() AppState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshotLocked()
}

func (a *App) ShowWindow() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		runtime.WindowUnminimise(ctx)
		runtime.Show(ctx)
	}
}

func (a *App) HideWindow() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		runtime.Hide(ctx)
	}
}

func (a *App) Quit() {
	a.mu.Lock()
	ctx := a.ctx
	a.settings.DesiredRunning = false
	a.state.DesiredRunning = false
	_ = saveSettings(a.settings)
	a.mu.Unlock()
	_ = a.Stop()
	if ctx != nil {
		runtime.Quit(ctx)
	}
}

func (a *App) SaveLocalProfile(profile LocalProfile) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Running || a.state.Busy {
		return errors.New("转发运行中，请先停止后再修改本地配置")
	}
	oldCipher := a.settings.TransportKeyCipher
	key := strings.TrimSpace(profile.TransportKey)
	profile.TransportKey = ""
	if key != "" {
		if err := validateTransportKey(key); err != nil {
			return err
		}
		cipher, err := protectString(key)
		if err != nil {
			return fmt.Errorf("保护 Transport Key 失败: %w", err)
		}
		oldCipher = cipher
	}
	if oldCipher == "" {
		return errors.New("请填写 Transport Key")
	}
	if err := validateLocalProfile(profile, true); err != nil {
		return err
	}
	a.settings.Profile = profile
	a.settings.TransportKeyCipher = oldCipher
	a.settings.ConfigurationSource = "local"
	if err := saveSettings(a.settings); err != nil {
		return err
	}
	a.refreshSettingsViewLocked()
	a.state.Problem = ""
	a.appendLogLocked("本地 Userspace Profile 已保存")
	go a.emitState()
	return nil
}

func (a *App) SetConfigurationSource(source string) error {
	if source != "local" && source != "managed" {
		return errors.New("配置来源只能是 local 或 managed")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Running || a.state.Busy {
		return errors.New("转发运行中，请先停止后再切换配置来源")
	}
	a.settings.ConfigurationSource = source
	if err := saveSettings(a.settings); err != nil {
		return err
	}
	a.refreshSettingsViewLocked()
	go a.emitState()
	return nil
}

func (a *App) SetProvisioningURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if _, err := validateProvisioningURL(raw); err != nil {
		return err
	}
	cipher, err := protectString(raw)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	previous, _ := unprotectString(a.settings.ProvisioningCipher)
	a.settings.ProvisioningCipher = cipher
	a.settings.ConfigurationSource = "managed"
	if previous != raw {
		a.settings.ManagedCacheCipher = ""
		a.settings.SelectedProfileIDs = nil
		a.managed = nil
	}
	if err := saveSettings(a.settings); err != nil {
		return err
	}
	a.refreshSettingsViewLocked()
	a.refreshManagedSummaryLocked()
	a.state.Managed.Status = "等待同步"
	a.state.Problem = ""
	a.appendLogLocked("Provisioning 地址已保存")
	go a.emitState()
	return nil
}

func (a *App) ClearProvisioning() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Running || a.state.Busy {
		return errors.New("转发运行中，请先停止")
	}
	a.settings.ProvisioningCipher = ""
	a.settings.ManagedCacheCipher = ""
	a.settings.SelectedProfileIDs = nil
	a.managed = nil
	a.settings.ConfigurationSource = "local"
	if err := saveSettings(a.settings); err != nil {
		return err
	}
	a.refreshSettingsViewLocked()
	a.refreshManagedSummaryLocked()
	go a.emitState()
	return nil
}

func (a *App) SyncProvisioning() (ManagedSummary, error) {
	a.mu.Lock()
	cipher := a.settings.ProvisioningCipher
	a.state.Managed.Status = "正在同步…"
	a.mu.Unlock()
	a.emitState()

	endpoint, err := unprotectString(cipher)
	if err != nil || endpoint == "" {
		if err == nil {
			err = errors.New("尚未配置 Provisioning URL")
		}
		return ManagedSummary{}, err
	}
	doc, raw, err := fetchManagedDocument(endpoint)
	if err != nil {
		a.mu.Lock()
		a.state.Managed.Status = "同步失败 · 继续使用 LKG"
		a.state.Problem = err.Error()
		a.appendLogLocked("Provisioning 同步失败：" + err.Error())
		summary := a.state.Managed
		a.mu.Unlock()
		a.emitState()
		return summary, err
	}
	cacheBytes, _ := json.Marshal(struct {
		Endpoint string           `json:"endpoint"`
		Raw      []byte           `json:"raw"`
		Doc      *managedDocument `json:"doc"`
	}{Endpoint: endpoint, Raw: raw, Doc: doc})
	cacheCipher, err := protectString(string(cacheBytes))
	if err != nil {
		return ManagedSummary{}, err
	}

	a.mu.Lock()
	a.managed = doc
	a.settings.ManagedCacheCipher = cacheCipher
	a.reconcileManagedSelectionLocked()
	if err := saveSettings(a.settings); err != nil {
		a.mu.Unlock()
		return ManagedSummary{}, err
	}
	a.state.Problem = ""
	a.refreshManagedSummaryLocked()
	a.state.Managed.Status = "已同步"
	a.appendLogLocked("Provisioning 配置已同步；当前运行实例不会被热切换")
	summary := a.state.Managed
	resident := a.effectiveBackgroundResidentLocked()
	remote := a.settings.RemoteEnabled
	a.mu.Unlock()
	_ = setAutostart(resident || remote)
	a.emitState()
	return summary, nil
}

func (a *App) SetManagedSelection(ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Running || a.state.Busy {
		return errors.New("转发运行中，请先停止后再修改 Bundle 选择")
	}
	if a.managed == nil || a.managed.Bundle == nil {
		return errors.New("当前 Provisioning 不是 Bundle")
	}
	allowed := map[string]bool{}
	for _, p := range a.managed.Bundle.Profiles {
		allowed[p.ProfileID] = true
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		if !allowed[id] {
			return fmt.Errorf("Bundle 不包含 Profile %s", id)
		}
		if !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	if a.managed.Bundle.Mode == "single_select" && len(clean) != 1 {
		return errors.New("single_select Bundle 必须选择一个 Profile")
	}
	if len(clean) == 0 {
		return errors.New("至少选择一个 Profile")
	}
	sort.Strings(clean)
	a.settings.SelectedProfileIDs = clean
	if err := saveSettings(a.settings); err != nil {
		return err
	}
	a.refreshManagedSummaryLocked()
	resident := a.effectiveBackgroundResidentLocked()
	remote := a.settings.RemoteEnabled
	go func() {
		_ = setAutostart(resident || remote)
		a.emitState()
	}()
	return nil
}

func (a *App) SetBackgroundResident(enabled bool) error {
	a.mu.Lock()
	a.settings.BackgroundResident = enabled
	remote := a.settings.RemoteEnabled
	if err := saveSettings(a.settings); err != nil {
		a.mu.Unlock()
		return err
	}
	a.refreshSettingsViewLocked()
	a.mu.Unlock()
	if err := setAutostart(enabled || remote); err != nil {
		return err
	}
	a.emitState()
	return nil
}

func (a *App) SetAutomaticUpdates(enabled bool) error {
	a.mu.Lock()
	a.settings.AutomaticUpdates = enabled
	a.state.Update.Automatic = enabled
	if enabled {
		a.state.Update.Status = "自动检查已启用"
	} else {
		a.state.Update.Status = "自动检查已关闭"
	}
	err := saveSettings(a.settings)
	a.refreshSettingsViewLocked()
	a.mu.Unlock()
	a.emitState()
	return err
}

func (a *App) Doctor() (string, error) {
	return runEngineOneShot("doctor-userspace", nil)
}

func (a *App) GetLogs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.state.Logs...)
}

func (a *App) ClearLogs() {
	a.mu.Lock()
	a.state.Logs = nil
	a.mu.Unlock()
	a.emitState()
}

func (a *App) refreshSettingsViewLocked() {
	profile := a.settings.Profile
	profile.TransportKey = ""
	remoteDeviceID := ""
	if credential, err := a.remoteCredentialLocked(); err == nil && credential != nil {
		remoteDeviceID = credential.DeviceID
	}
	a.state.Settings = SettingsView{
		ConfigurationSource: a.settings.ConfigurationSource,
		Profile:             profile,
		TransportKeySet:     a.settings.TransportKeyCipher != "",
		BackgroundResident:  a.effectiveBackgroundResidentLocked(),
		AutomaticUpdates:    a.settings.AutomaticUpdates,
		RemoteEnabled:       a.settings.RemoteEnabled,
		RemoteServer:        a.settings.RemoteServer,
		RemoteDeviceID:      remoteDeviceID,
	}
	a.state.DesiredRunning = a.settings.DesiredRunning
}

func (a *App) appendLogLocked(message string) {
	if message == "" {
		return
	}
	line := time.Now().Format("15:04:05") + "  " + message
	a.state.Logs = append(a.state.Logs, line)
	if len(a.state.Logs) > 160 {
		a.state.Logs = append([]string(nil), a.state.Logs[len(a.state.Logs)-160:]...)
	}
}

func (a *App) setProblem(message string) {
	a.mu.Lock()
	a.state.Problem = message
	a.appendLogLocked(message)
	a.mu.Unlock()
	a.emitState()
}

func (a *App) snapshotLocked() AppState {
	data, _ := json.Marshal(a.state)
	var out AppState
	_ = json.Unmarshal(data, &out)
	return out
}

func (a *App) emitState() {
	a.mu.Lock()
	ctx := a.ctx
	snapshot := a.snapshotLocked()
	a.mu.Unlock()
	if ctx != nil {
		runtime.EventsEmit(ctx, "state", snapshot)
	}
}
