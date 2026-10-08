package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var backgroundRecoveryDelays = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}

func enginePath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("MPTCP_WINDOWS_ENGINE")); override != "" {
		return override, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "mptcp-engine.exe"), nil
}

func verifyEngineBinary() error {
	path, err := enginePath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("Windows MPX Engine 不可用（%s）: %w", path, err)
	}
	expected := strings.ToLower(strings.TrimSpace(expectedEngineSHA256))
	if expected == "" {
		return nil
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("Windows MPX Engine 校验失败：expected %s, got %s", expected, actual)
	}
	return nil
}

func runEngineOneShot(command string, input []byte) (string, error) {
	if err := verifyEngineBinary(); err != nil {
		return "", err
	}
	path, err := enginePath()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, command)
	prepareEngineCommand(cmd)
	if len(input) > 0 {
		cmd.Stdin = bytes.NewReader(input)
	}
	output, err := cmd.CombinedOutput()
	var last EngineEvent
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event EngineEvent
		if json.Unmarshal(line, &event) == nil && event.Kind != "" {
			last = event
		}
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("Engine %s 超时", command)
	}
	if err != nil {
		if last.Message != "" {
			return "", errors.New(last.Message)
		}
		return "", fmt.Errorf("Engine %s 失败: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	if last.Message != "" {
		return last.Message, nil
	}
	return strings.TrimSpace(string(output)), nil
}

type engineLaunch struct {
	Args  []string
	Input []byte
}

func (a *App) prepareLaunchLocked() (engineLaunch, error) {
	switch a.settings.ConfigurationSource {
	case "local":
		key, err := unprotectString(a.settings.TransportKeyCipher)
		if err != nil {
			return engineLaunch{}, fmt.Errorf("Transport Key 解密失败: %w", err)
		}
		if key == "" {
			return engineLaunch{}, errors.New("尚未配置 Transport Key")
		}
		profile := a.settings.Profile
		if err := validateLocalProfile(profile, true); err != nil {
			return engineLaunch{}, err
		}
		if err := validateTransportKey(key); err != nil {
			return engineLaunch{}, err
		}
		cfg := engineConfig{
			SchemaVersion: 3,
			Mode:          "userspace_multipath",
			ListenPort:    profile.ListenPort,
			TCPEnabled:    profile.TCPEnabled,
			UDPEnabled:    profile.UDPEnabled,
			UOTEnabled:    profile.UOTEnabled,
			SchedulerMode: profile.SchedulerMode,
			Relays:        profile.Relays,
			TransportKey:  key,
		}
		data, err := json.Marshal(cfg)
		return engineLaunch{Args: []string{"run"}, Input: data}, err

	case "managed":
		if a.managed == nil {
			return engineLaunch{}, errors.New("尚无可用 Provisioning/LKG 配置，请先同步")
		}
		if a.managed.Profile != nil {
			if err := a.managed.Profile.validateWindows(); err != nil {
				return engineLaunch{}, err
			}
			data, err := json.Marshal(a.managed.Profile.engineConfig())
			return engineLaunch{Args: []string{"run"}, Input: data}, err
		}
		if a.managed.Bundle != nil {
			if err := validateWindowsBundle(a.managed.Bundle); err != nil {
				return engineLaunch{}, err
			}
			selected := append([]string(nil), a.settings.SelectedProfileIDs...)
			if len(selected) == 0 {
				return engineLaunch{}, errors.New("Bundle 至少选择一个 Windows Profile")
			}
			data, err := json.Marshal(a.managed.Bundle)
			args := append([]string{"run-bundle"}, selected...)
			return engineLaunch{Args: args, Input: data}, err
		}
		return engineLaunch{}, errors.New("Provisioning 配置无效")
	default:
		return engineLaunch{}, errors.New("未知配置来源")
	}
}

func (a *App) Start() error {
	a.mu.Lock()
	if a.state.Running || a.state.Busy {
		a.mu.Unlock()
		return nil
	}
	needSync := a.settings.ConfigurationSource == "managed" && a.managed == nil && a.settings.ProvisioningCipher != ""
	a.mu.Unlock()

	if needSync {
		if _, err := a.SyncProvisioning(); err != nil {
			a.mu.Lock()
			hasCache := a.managed != nil
			a.mu.Unlock()
			if !hasCache {
				return err
			}
		}
	}
	return a.startEngine(false, 0)
}

func (a *App) startEngine(recovery bool, attempt int) error {
	if err := verifyEngineBinary(); err != nil {
		return err
	}

	a.mu.Lock()
	if a.state.Running || a.state.Busy || a.engineCancel != nil {
		a.mu.Unlock()
		return nil
	}
	launch, err := a.prepareLaunchLocked()
	if err != nil {
		a.mu.Unlock()
		return err
	}
	path, err := enginePath()
	if err != nil {
		a.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, path, launch.Args...)
	prepareEngineCommand(cmd)
	cmd.Stdin = bytes.NewReader(launch.Input)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		a.mu.Unlock()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		a.mu.Unlock()
		return err
	}

	a.state.Busy = true
	a.state.Running = false
	if recovery {
		a.recoveryAttempt = attempt
		a.state.Status = fmt.Sprintf("自动恢复中 · 第 %d 次", attempt)
	} else {
		a.recoveryAttempt = 0
		a.state.Status = "正在启动…"
		a.state.Problem = ""
		a.retryToken++
	}
	a.state.ProfileStatus = map[string]string{}
	a.state.ProfileErrors = map[string]string{}
	a.state.ProfileEvents = map[string]EngineEvent{}
	a.state.Event = EngineEvent{}
	token := a.retryToken
	a.mu.Unlock()
	a.emitState()

	if err := cmd.Start(); err != nil {
		cancel()
		a.mu.Lock()
		a.state.Busy = false
		a.state.Status = "启动失败"
		a.state.Problem = err.Error()
		a.appendLogLocked("Engine 启动失败：" + err.Error())
		a.mu.Unlock()
		a.emitState()
		return err
	}

	done := make(chan struct{})
	a.mu.Lock()
	a.engineCancel = cancel
	a.engineDone = done
	if cmd.Process != nil {
		a.enginePID = cmd.Process.Pid
	}
	a.settings.DesiredRunning = true
	a.state.DesiredRunning = true
	_ = saveSettings(a.settings)
	a.appendLogLocked(fmt.Sprintf("Windows Userspace Engine 已启动 · PID %d", a.enginePID))
	a.mu.Unlock()
	a.emitState()

	go a.scanEngineStdout(stdout, token)
	go a.scanEngineStderr(stderr, token)
	go a.waitEngine(cmd, ctx, cancel, done, token)
	return nil
}

func (a *App) Stop() error {
	a.mu.Lock()
	a.retryToken++
	a.settings.DesiredRunning = false
	a.state.DesiredRunning = false
	cancel := a.engineCancel
	done := a.engineDone
	if cancel == nil {
		a.state.Running = false
		a.state.Busy = false
		a.state.Status = "已停止"
		_ = saveSettings(a.settings)
		a.mu.Unlock()
		a.emitState()
		return nil
	}
	a.state.Status = "停止中…"
	a.state.Busy = true
	_ = saveSettings(a.settings)
	a.mu.Unlock()
	a.emitState()
	cancel()
	if done != nil {
		select {
		case <-done:
		case <-time.After(4 * time.Second):
		}
	}
	a.mu.Lock()
	a.state.Running = false
	a.state.Busy = false
	a.state.Status = "已停止"
	a.recoveryAttempt = 0
	a.mu.Unlock()
	a.emitState()
	return nil
}

func (a *App) scanEngineStdout(r io.Reader, token uint64) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)
	for scanner.Scan() {
		var event EngineEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			a.mu.Lock()
			if token == a.retryToken {
				a.appendLogLocked("Engine 输出无法解析：" + string(scanner.Bytes()))
			}
			a.mu.Unlock()
			continue
		}
		a.handleEngineEvent(event, token)
	}
	if err := scanner.Err(); err != nil {
		a.mu.Lock()
		if token == a.retryToken {
			a.appendLogLocked("Engine 输出读取失败：" + err.Error())
		}
		a.mu.Unlock()
		a.emitState()
	}
}

func (a *App) scanEngineStderr(r io.Reader, token uint64) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 32*1024), 256*1024)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		a.mu.Lock()
		if token == a.retryToken {
			a.appendLogLocked("Engine: " + text)
		}
		a.mu.Unlock()
	}
}

func (a *App) handleEngineEvent(event EngineEvent, token uint64) {
	a.mu.Lock()
	if token != a.retryToken {
		a.mu.Unlock()
		return
	}
	if event.Message != "" && event.Kind != "stats" && event.Kind != "udp_stats" && event.Kind != "bundle_stats" && event.Kind != "bundle_udp_stats" {
		prefix := ""
		if event.ProfileName != "" {
			prefix = event.ProfileName + " · "
		}
		a.appendLogLocked(prefix + event.Message)
	}
	if event.ProfileID != "" {
		previous := a.state.ProfileEvents[event.ProfileID]
		if event.Kind == "udp_stats" {
			previous.UDPConnections = event.Connections
			previous.Dropped = event.Dropped
			a.state.ProfileEvents[event.ProfileID] = previous
		} else if event.Kind == "stats" || event.Kind == "listening" || event.Kind == "ready" || event.Kind == "connecting" {
			if event.Kind != "stats" && len(event.PathStats) == 0 && len(previous.PathStats) > 0 {
				event.PathStats = previous.PathStats
			}
			a.state.ProfileEvents[event.ProfileID] = event
		}
		switch event.Kind {
		case "connecting", "reconnect_attempt":
			a.state.ProfileStatus[event.ProfileID] = "连接中"
		case "listening":
			a.state.ProfileStatus[event.ProfileID] = "运行中"
			delete(a.state.ProfileErrors, event.ProfileID)
		case "reconnecting":
			a.state.ProfileStatus[event.ProfileID] = "自动重连"
			if event.Message != "" {
				a.state.ProfileErrors[event.ProfileID] = event.Message
			}
		case "error":
			a.state.ProfileStatus[event.ProfileID] = "异常"
			a.state.ProfileErrors[event.ProfileID] = event.Message
		}
	}

	switch event.Kind {
	case "connecting", "bundle_connecting", "bundle_reconnecting":
		a.state.Busy = true
		if event.Message != "" {
			a.state.Status = event.Message
		} else {
			a.state.Status = "正在连接…"
		}
	case "ready":
		a.state.Busy = true
		if event.Message != "" {
			a.state.Status = event.Message
		}
	case "listening":
		a.recoveryAttempt = 0
		a.state.Running = true
		a.state.Busy = false
		a.state.Status = "运行中 · " + event.Message
	case "bundle_listening":
		a.recoveryAttempt = 0
		a.state.Running = true
		a.state.Busy = false
		a.state.Status = event.Message
	case "bundle_degraded":
		a.state.Running = event.ActiveProfiles > 0
		a.state.Busy = false
		a.state.Status = event.Message
	case "stats", "bundle_stats":
		a.state.Event = event
		if event.Kind == "stats" && event.ProfileID == "" {
			a.state.Running = true
			a.state.Busy = false
		}
	case "udp_stats", "bundle_udp_stats":
		current := a.state.Event
		current.UDPConnections = event.Connections
		current.Dropped = event.Dropped
		a.state.Event = current
	case "error":
		if event.ProfileID == "" {
			a.state.Problem = event.Message
			a.state.Status = "运行异常"
		}
	case "bundle_failed":
		a.state.Problem = event.Message
		a.state.Status = event.Message
		a.state.Running = false
		a.state.Busy = false
	}
	a.mu.Unlock()
	a.emitState()
}

func (a *App) waitEngine(cmd *exec.Cmd, ctx context.Context, cancel context.CancelFunc, done chan struct{}, token uint64) {
	err := cmd.Wait()
	wasCancelled := ctx.Err() != nil
	cancel()
	close(done)

	a.mu.Lock()
	if a.engineDone == done {
		a.engineCancel = nil
		a.engineDone = nil
		a.enginePID = 0
	}
	if token != a.retryToken {
		a.mu.Unlock()
		return
	}
	desired := a.settings.DesiredRunning
	recoverable := desired && (a.effectiveBackgroundResidentLocked() || a.settings.RemoteEnabled)
	recoveryAttempt := a.recoveryAttempt
	if wasCancelled || !desired {
		a.state.Running = false
		a.state.Busy = false
		a.state.Status = "已停止"
		a.mu.Unlock()
		a.emitState()
		return
	}
	a.state.Running = false
	a.state.Busy = false
	message := "Engine 已意外退出"
	if err != nil {
		message += "：" + err.Error()
	}
	a.state.Problem = message
	a.appendLogLocked(message)
	a.mu.Unlock()
	a.emitState()

	if recoverable {
		a.scheduleRecovery(token, recoveryAttempt)
	} else {
		a.mu.Lock()
		a.settings.DesiredRunning = false
		a.state.DesiredRunning = false
		_ = saveSettings(a.settings)
		a.mu.Unlock()
		a.emitState()
	}
}

func (a *App) scheduleRecovery(token uint64, attempt int) {
	if attempt >= len(backgroundRecoveryDelays) {
		a.mu.Lock()
		if token == a.retryToken {
			a.settings.DesiredRunning = false
			a.state.DesiredRunning = false
			a.state.Busy = false
			a.state.Status = "自动恢复失败"
			a.appendLogLocked("后台自动恢复已达到 5 次上限；等待手动启动")
			_ = saveSettings(a.settings)
		}
		a.mu.Unlock()
		a.emitState()
		return
	}
	delay := backgroundRecoveryDelays[attempt]
	a.mu.Lock()
	if token != a.retryToken || !a.settings.DesiredRunning {
		a.mu.Unlock()
		return
	}
	a.state.Busy = true
	a.state.Status = fmt.Sprintf("%d 秒后自动恢复 · 第 %d 次", int(delay/time.Second), attempt+1)
	a.mu.Unlock()
	a.emitState()
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		a.mu.Lock()
		valid := token == a.retryToken && a.settings.DesiredRunning && a.engineCancel == nil
		if valid {
			a.state.Busy = false
		}
		a.mu.Unlock()
		if !valid {
			return
		}
		if err := a.startEngine(true, attempt+1); err != nil {
			a.mu.Lock()
			if token == a.retryToken {
				a.state.Busy = false
				a.state.Problem = err.Error()
				a.appendLogLocked("自动恢复失败：" + err.Error())
			}
			a.mu.Unlock()
			a.emitState()
			a.scheduleRecovery(token, attempt+1)
		}
	}()
}

func (a *App) effectiveBackgroundResidentLocked() bool {
	if a.settings.ConfigurationSource != "managed" || a.managed == nil {
		return a.settings.BackgroundResident
	}
	if a.managed.Profile != nil {
		return a.managed.Profile.BackgroundResident
	}
	if a.managed.Bundle != nil {
		selected := map[string]bool{}
		for _, id := range a.settings.SelectedProfileIDs {
			selected[id] = true
		}
		for _, p := range a.managed.Bundle.Profiles {
			if selected[p.ProfileID] && p.BackgroundResident {
				return true
			}
		}
	}
	return a.settings.BackgroundResident
}
