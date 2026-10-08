package main

import (
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
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	maximumProfileResponseBytes = 64 * 1024
	maximumManagedResponseBytes = 512 * 1024
)

type encryptedEnvelope struct {
	V int    `json:"v"`
	N string `json:"n"`
	D string `json:"d"`
}

func validateProvisioningURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("请输入 Provisioning API URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return nil, errors.New("请输入有效的 Provisioning API URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("Provisioning URL 不能包含账号密码或 fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if scheme != "https" && !(scheme == "http" && loopback) {
		return nil, errors.New("Provisioning API 必须使用 HTTPS；仅 localhost 调试允许 HTTP")
	}
	parsed.Scheme = scheme
	return parsed, nil
}

func base64URLDecode(value string) ([]byte, error) {
	if m := len(value) % 4; m != 0 {
		value += strings.Repeat("=", 4-m)
	}
	return base64.URLEncoding.DecodeString(value)
}

func decryptManagedEnvelope(data []byte, endpoint *url.URL) ([]byte, error) {
	var env encryptedEnvelope
	if err := json.Unmarshal(data, &env); err != nil || env.V == 0 || env.N == "" || env.D == "" {
		return data, nil
	}
	if env.V != 1 {
		return nil, errors.New("Provisioning 加密封装版本不受支持")
	}
	parts := strings.Split(strings.Trim(endpoint.Path, "/"), "/")
	if len(parts) == 0 {
		return nil, errors.New("Provisioning 加密响应需要有效的 API Secret")
	}
	token := parts[len(parts)-1]
	tokenData, err := hex.DecodeString(token)
	if err != nil || len(tokenData) != 32 {
		return nil, errors.New("Provisioning 加密响应需要有效的 API Secret")
	}
	nonce, err := base64URLDecode(env.N)
	if err != nil || len(nonce) != 12 {
		return nil, errors.New("Provisioning 加密响应 nonce 无效")
	}
	sealed, err := base64URLDecode(env.D)
	if err != nil || len(sealed) < 16 {
		return nil, errors.New("Provisioning 加密响应密文无效")
	}

	mac := hmac.New(sha256.New, tokenData)
	_, _ = mac.Write([]byte("mpx-provision-config-envelope-v1"))
	key := mac.Sum(nil)
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

func fetchManagedDocument(endpointRaw string) (*managedDocument, []byte, error) {
	endpoint, err := validateProvisioningURL(endpointRaw)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MPTCP-Desk-Windows/provisioning")
	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("Provisioning 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("Provisioning API HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maximumManagedResponseBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) == 0 || len(raw) > maximumManagedResponseBytes {
		return nil, nil, errors.New("Provisioning API 响应为空或超过 512 KiB")
	}
	payload, err := decryptManagedEnvelope(raw, endpoint)
	if err != nil {
		return nil, nil, err
	}
	if len(payload) == 0 || len(payload) > maximumManagedResponseBytes {
		return nil, nil, errors.New("Provisioning 解密配置为空或超过 512 KiB")
	}

	var header struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
	}
	if err := json.Unmarshal(payload, &header); err != nil {
		return nil, nil, errors.New("Provisioning API JSON 无效")
	}
	now := time.Now().UTC()
	switch {
	case header.SchemaVersion == 1:
		if len(payload) > maximumProfileResponseBytes {
			return nil, nil, errors.New("单 Profile Provisioning 响应超过 64 KiB")
		}
		var profile managedProfile
		if err := json.Unmarshal(payload, &profile); err != nil {
			return nil, nil, err
		}
		if err := profile.validateWindows(); err != nil {
			return nil, nil, err
		}
		return &managedDocument{Kind: "profile", Profile: &profile, FetchedAt: now}, raw, nil
	case header.SchemaVersion == 2 && header.Kind == "bundle":
		var bundle managedBundle
		if err := json.Unmarshal(payload, &bundle); err != nil {
			return nil, nil, err
		}
		if err := validateWindowsBundle(&bundle); err != nil {
			return nil, nil, err
		}
		return &managedDocument{Kind: "bundle", Bundle: &bundle, FetchedAt: now}, raw, nil
	default:
		return nil, nil, errors.New("Provisioning API schema/kind 不受支持")
	}
}

func validateWindowsBundle(bundle *managedBundle) error {
	if bundle.SchemaVersion != 2 || bundle.Kind != "bundle" {
		return errors.New("Bundle 仅支持 schema_version=2 / kind=bundle")
	}
	if bundle.BundleID == "" || len(bundle.BundleID) > 128 || len(bundle.DisplayName) > 128 || len(bundle.Revision) > 128 {
		return errors.New("Bundle 标识、名称或 revision 无效")
	}
	if bundle.Mode != "single_select" && bundle.Mode != "parallel" {
		return errors.New("Bundle mode 仅支持 single_select 或 parallel")
	}
	if len(bundle.Profiles) < 1 || len(bundle.Profiles) > 32 {
		return errors.New("Bundle 需要 1–32 个 Profile")
	}
	seen := map[string]bool{}
	windowsProfiles := make([]managedProfile, 0, len(bundle.Profiles))
	for _, p := range bundle.Profiles {
		if p.ProfileID == "" || seen[p.ProfileID] {
			return errors.New("Bundle Profile ID 必须非空且唯一")
		}
		seen[p.ProfileID] = true
		if p.Mode != "userspace_multipath" {
			// Windows intentionally has no Native MPTCP fallback. Mac-only
			// profiles may coexist in a shared Bundle, but they are hidden.
			continue
		}
		if err := p.validateWindows(); err != nil {
			return fmt.Errorf("Profile %s: %w", p.DisplayName, err)
		}
		windowsProfiles = append(windowsProfiles, p)
	}
	if len(windowsProfiles) == 0 {
		return errors.New("Bundle 中没有 Windows 可用的 userspace_multipath Profile")
	}
	bundle.Profiles = windowsProfiles
	return nil
}

func (a *App) loadManagedCacheLocked() {
	if a.settings.ManagedCacheCipher == "" {
		return
	}
	text, err := unprotectString(a.settings.ManagedCacheCipher)
	if err != nil {
		a.appendLogLocked("LKG 解密失败：" + err.Error())
		return
	}
	var cached struct {
		Endpoint string           `json:"endpoint"`
		Raw      []byte           `json:"raw"`
		Doc      *managedDocument `json:"doc"`
	}
	if err := json.Unmarshal([]byte(text), &cached); err != nil || cached.Doc == nil {
		return
	}
	current, _ := unprotectString(a.settings.ProvisioningCipher)
	if current == "" || current != cached.Endpoint {
		return
	}
	switch cached.Doc.Kind {
	case "profile":
		if cached.Doc.Profile == nil || cached.Doc.Profile.validateWindows() != nil {
			return
		}
	case "bundle":
		if cached.Doc.Bundle == nil || validateWindowsBundle(cached.Doc.Bundle) != nil {
			return
		}
	default:
		return
	}
	cached.Doc.FromCache = true
	a.managed = cached.Doc
	a.reconcileManagedSelectionLocked()
}

func (a *App) reconcileManagedSelectionLocked() {
	if a.managed == nil {
		a.settings.SelectedProfileIDs = nil
		return
	}
	if a.managed.Profile != nil {
		a.settings.SelectedProfileIDs = []string{a.managed.Profile.ProfileID}
		return
	}
	if a.managed.Bundle == nil {
		a.settings.SelectedProfileIDs = nil
		return
	}
	available := map[string]bool{}
	for _, p := range a.managed.Bundle.Profiles {
		available[p.ProfileID] = true
	}
	selected := make([]string, 0, len(a.settings.SelectedProfileIDs))
	seen := map[string]bool{}
	for _, id := range a.settings.SelectedProfileIDs {
		if available[id] && !seen[id] {
			selected = append(selected, id)
			seen[id] = true
		}
	}
	if a.managed.Bundle.Mode == "single_select" {
		if len(selected) == 0 {
			selected = []string{a.managed.Bundle.Profiles[0].ProfileID}
		}
		selected = selected[:1]
	} else if len(selected) == 0 {
		for _, p := range a.managed.Bundle.Profiles {
			selected = append(selected, p.ProfileID)
		}
	}
	sort.Strings(selected)
	a.settings.SelectedProfileIDs = selected
}

func (a *App) refreshManagedSummaryLocked() {
	endpoint, _ := unprotectString(a.settings.ProvisioningCipher)
	summary := ManagedSummary{
		Configured:      endpoint != "",
		DisplayEndpoint: displayProvisioningEndpoint(endpoint),
		SelectedIDs:     append([]string(nil), a.settings.SelectedProfileIDs...),
		Status:          "未配置",
	}
	if endpoint != "" {
		summary.Status = "等待同步"
	}
	if a.managed == nil {
		a.state.Managed = summary
		return
	}
	summary.Kind = a.managed.Kind
	summary.FromCache = a.managed.FromCache
	if !a.managed.FetchedAt.IsZero() {
		summary.FetchedAt = a.managed.FetchedAt.Local().Format("2006-01-02 15:04:05")
	}
	if a.managed.Profile != nil {
		p := a.managed.Profile
		summary.DisplayName = p.DisplayName
		summary.Revision = p.Revision
		summary.Profiles = []ManagedProfileChoice{profileChoice(*p)}
	} else if a.managed.Bundle != nil {
		b := a.managed.Bundle
		summary.DisplayName = b.DisplayName
		summary.Revision = b.Revision
		summary.BundleMode = b.Mode
		for _, p := range b.Profiles {
			summary.Profiles = append(summary.Profiles, profileChoice(p))
		}
	}
	if summary.FromCache {
		summary.Status = "LKG 已加载"
	} else {
		summary.Status = "已同步"
	}
	a.state.Managed = summary
}

func profileChoice(p managedProfile) ManagedProfileChoice {
	scheduler := p.SchedulerMode
	if scheduler == "" {
		scheduler = "auto"
	}
	return ManagedProfileChoice{
		ID:                 p.ProfileID,
		Name:               p.DisplayName,
		ListenPort:         p.ListenPort,
		SchedulerMode:      scheduler,
		TCPEnabled:         p.TCPEnabled,
		UDPEnabled:         p.UDPEnabled,
		UOTEnabled:         p.UOTEnabled,
		BackgroundResident: p.BackgroundResident,
	}
}

func displayProvisioningEndpoint(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "已配置"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if len(last) == 64 {
			if _, err := hex.DecodeString(last); err == nil {
				parts[len(parts)-1] = "••••••••"
			}
		}
	}
	u.Path = "/" + strings.Join(parts, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
