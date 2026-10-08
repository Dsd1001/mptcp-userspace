package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const windowsUpdateManifestURL = "https://github.com/Dsd1001/mptcp-userspace/releases/latest/download/windows-update.json"

type windowsUpdateManifest struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Signature string `json:"ed25519_signature"`
}

func (a *App) CheckForUpdates(manual bool) (UpdateView, error) {
	a.mu.Lock()
	a.state.Update.Status = "正在检查更新…"
	a.mu.Unlock()
	a.emitState()

	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, windowsUpdateManifestURL, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MPTCP-Desk-Windows/"+appVersion)
	resp, err := client.Do(req)
	if err != nil {
		return a.finishUpdateCheck(nil, fmt.Errorf("检查更新失败: %w", err), manual)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return a.finishUpdateCheck(nil, errors.New("当前 Release 尚未提供 Windows 更新清单"), manual)
	}
	if resp.StatusCode != http.StatusOK {
		return a.finishUpdateCheck(nil, fmt.Errorf("更新服务器 HTTP %d", resp.StatusCode), manual)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return a.finishUpdateCheck(nil, err, manual)
	}
	var manifest windowsUpdateManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return a.finishUpdateCheck(nil, errors.New("Windows 更新清单格式无效"), manual)
	}
	if err := validateUpdateManifest(manifest); err != nil {
		return a.finishUpdateCheck(nil, err, manual)
	}
	return a.finishUpdateCheck(&manifest, nil, manual)
}

func (a *App) finishUpdateCheck(manifest *windowsUpdateManifest, checkErr error, manual bool) (UpdateView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if checkErr != nil {
		if manual {
			a.state.Update.Status = checkErr.Error()
		} else {
			a.state.Update.Status = "自动检查已启用"
		}
		view := a.state.Update
		go a.emitState()
		return view, checkErr
	}
	a.updateManifest = manifest
	available := compareVersions(manifest.Version, appVersion) > 0
	a.state.Update.Available = available
	a.state.Update.Version = manifest.Version
	if available {
		a.state.Update.Status = "发现新版本 " + manifest.Version
	} else {
		a.state.Update.Status = "已是最新版本"
	}
	view := a.state.Update
	go a.emitState()
	return view, nil
}

func validateUpdateManifest(m windowsUpdateManifest) error {
	if strings.TrimSpace(m.Version) == "" || m.Size <= 0 || m.Size > 500*1024*1024 {
		return errors.New("Windows 更新清单版本或大小无效")
	}
	if len(m.SHA256) != 64 {
		return errors.New("Windows 更新清单 SHA256 无效")
	}
	if _, err := hex.DecodeString(m.SHA256); err != nil {
		return errors.New("Windows 更新清单 SHA256 无效")
	}
	parsed, err := url.Parse(m.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return errors.New("Windows 更新下载地址无效")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("Windows 更新 Ed25519 签名格式无效")
	}
	return nil
}

func (a *App) InstallUpdate() error {
	a.mu.Lock()
	manifest := a.updateManifest
	a.mu.Unlock()
	if manifest == nil {
		if _, err := a.CheckForUpdates(true); err != nil {
			return err
		}
		a.mu.Lock()
		manifest = a.updateManifest
		a.mu.Unlock()
	}
	if manifest == nil || compareVersions(manifest.Version, appVersion) <= 0 {
		return errors.New("没有可安装的 Windows 更新")
	}

	a.mu.Lock()
	a.state.Update.Status = "正在下载 " + manifest.Version + "…"
	a.mu.Unlock()
	a.emitState()
	installer, err := downloadAndVerifyUpdate(*manifest)
	if err != nil {
		a.mu.Lock()
		a.state.Update.Status = "更新下载/校验失败"
		a.state.Problem = err.Error()
		a.mu.Unlock()
		a.emitState()
		return err
	}
	a.mu.Lock()
	a.state.Update.Status = "更新已校验，正在安装…"
	ctx := a.ctx
	a.mu.Unlock()
	a.emitState()
	_ = a.Stop()
	cmd := exec.Command(installer, "/S")
	// Installer owns its own UI/process lifetime. Do not tie it to App context.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 Windows 更新安装器失败: %w", err)
	}
	if ctx != nil {
		runtime.Quit(ctx)
	}
	return nil
}

func downloadAndVerifyUpdate(m windowsUpdateManifest) (string, error) {
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Get(m.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("更新下载 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, m.Size+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) != m.Size {
		return "", fmt.Errorf("更新文件大小不匹配: expected %d got %d", m.Size, len(data))
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), m.SHA256) {
		return "", errors.New("Windows 更新 SHA256 校验失败")
	}
	pub, err := base64.StdEncoding.DecodeString(updatePublicKeyBase64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", errors.New("内置 Windows 更新公钥无效")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), data, sig) {
		return "", errors.New("Windows 更新 Ed25519 签名验证失败")
	}
	name := fmt.Sprintf("MPTCP-Desk-%s-Windows-Setup.exe", m.Version)
	path := filepath.Join(os.TempDir(), name)
	if err := os.WriteFile(path, data, 0700); err != nil {
		return "", err
	}
	return path, nil
}

func compareVersions(a, b string) int {
	pa := parseVersion(a)
	pb := parseVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return strings.Compare(strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v"))
}

func parseVersion(value string) [3]int {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	value = strings.SplitN(value, "-", 2)[0]
	parts := strings.Split(value, ".")
	var result [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		result[i], _ = strconv.Atoi(parts[i])
	}
	return result
}

// Used by release tooling/tests to verify that a Sparkle-compatible Ed25519
// signature is over the exact Windows installer bytes.
func verifyUpdateSignature(data []byte, signature string) bool {
	pub, err := base64.StdEncoding.DecodeString(updatePublicKeyBase64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	return err == nil && ed25519.Verify(ed25519.PublicKey(pub), data, sig)
}
