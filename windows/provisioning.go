package main

import (
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

	"mptcp-desktop/engine/tunnelconfig"
)

const maximumManagedResponseBytes = 128 << 10

type encryptedEnvelope struct {
	V int    `json:"v"`
	N string `json:"n"`
	D string `json:"d"`
}

func defaultTunnelConfig() tunnelconfig.Config {
	return tunnelconfig.Config{SchemaVersion: 1, Kind: tunnelconfig.Kind,
		Tunnel: tunnelconfig.Transport{SchedulerMode: "auto", Relays: []Relay{{Port: 24001}, {Port: 24001}}},
		Inlets: []tunnelconfig.Inlet{
			{ID: "hkbn", Name: "HKBN", ListenPort: 1081, RouteID: 1},
			{ID: "hkt", Name: "HKT", ListenPort: 1082, RouteID: 2},
			{ID: "icable", Name: "iCable", ListenPort: 1083, RouteID: 3},
		},
	}
}

func validateProvisioningURL(raw string) (*url.URL, error) {
	return tunnelconfig.ValidateEndpoint(raw)
}

func fetchManagedDocument(endpointRaw string) (*managedDocument, []byte, error) {
	endpoint, err := validateProvisioningURL(endpointRaw)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, nil, errors.New("invalid provisioning request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MPTCP-Tunnel-Windows/1.2")
	req.Header.Set("Cache-Control", "no-store")
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are disabled") }}
	res, err := client.Do(req)
	if err != nil {
		return nil, nil, errors.New("Provisioning 请求失败；请检查网络、TLS 和服务器状态")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("Provisioning HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maximumManagedResponseBytes+1))
	if err != nil {
		return nil, nil, errors.New("Provisioning 响应读取失败")
	}
	profile, err := tunnelconfig.DecodeResponse(raw, endpointRaw)
	if err != nil {
		return nil, nil, err
	}
	return &managedDocument{Kind: "tunnel_profile", Tunnel: &profile, FetchedAt: time.Now().UTC()}, raw, nil
}

func (a *App) loadManagedCacheLocked() {
	if a.settings.ManagedCacheCipher == "" {
		return
	}
	text, err := unprotectString(a.settings.ManagedCacheCipher)
	if err != nil {
		a.appendLogLocked("共享隧道缓存解密失败")
		return
	}
	var cached struct {
		Endpoint string           `json:"endpoint"`
		Raw      []byte           `json:"raw"`
		Doc      *managedDocument `json:"doc"`
	}
	if len(text) > 512<<10 || json.Unmarshal([]byte(text), &cached) != nil {
		return
	}
	current, _ := unprotectString(a.settings.ProvisioningCipher)
	if current == "" || current != cached.Endpoint {
		return
	}
	// The persisted decoded object is never the authority. Verify the original
	// authenticated response again using the current API endpoint credential.
	profile, err := tunnelconfig.DecodeResponse(cached.Raw, current)
	if err != nil {
		a.appendLogLocked("缓存不是有效的 1.2.0 共享隧道配置，需要重新同步")
		return
	}
	fetched := time.Time{}
	if cached.Doc != nil {
		fetched = cached.Doc.FetchedAt
	}
	a.managed = &managedDocument{Kind: "tunnel_profile", Tunnel: &profile, FetchedAt: fetched, FromCache: true}
	a.reconcileManagedSelectionLocked()
}

func (a *App) reconcileManagedSelectionLocked() {
	if a.managed == nil || a.managed.Tunnel == nil {
		a.settings.SelectedProfileIDs = nil
		return
	}
	available := map[string]bool{}
	for _, in := range a.managed.Tunnel.Config.Inlets {
		available[in.ID] = true
	}
	selected := make([]string, 0, len(a.settings.SelectedProfileIDs))
	seen := map[string]bool{}
	for _, id := range a.settings.SelectedProfileIDs {
		if available[id] && !seen[id] {
			selected = append(selected, id)
			seen[id] = true
		}
	}
	if len(selected) == 0 {
		for _, in := range a.managed.Tunnel.Config.Inlets {
			selected = append(selected, in.ID)
		}
	}
	sort.Strings(selected)
	a.settings.SelectedProfileIDs = selected
}

func (a *App) refreshManagedSummaryLocked() {
	endpoint, _ := unprotectString(a.settings.ProvisioningCipher)
	summary := ManagedSummary{Configured: endpoint != "", DisplayEndpoint: displayProvisioningEndpoint(endpoint), SelectedIDs: append([]string(nil), a.settings.SelectedProfileIDs...), Status: "未配置"}
	if endpoint != "" {
		summary.Status = "等待同步"
	}
	if a.managed == nil || a.managed.Tunnel == nil {
		a.state.Managed = summary
		return
	}
	p := a.managed.Tunnel
	summary.Kind = "tunnel_profile"
	summary.BundleMode = "shared_tunnel"
	summary.DisplayName = p.DisplayName
	summary.Revision = p.Revision
	summary.RelayCount = len(p.Config.Tunnel.Relays)
	summary.FromCache = a.managed.FromCache
	if !a.managed.FetchedAt.IsZero() {
		summary.FetchedAt = a.managed.FetchedAt.Local().Format("2006-01-02 15:04:05")
	}
	for _, in := range p.Config.Inlets {
		name := in.Name
		if name == "" {
			name = in.ID
		}
		weight := in.Weight
		if weight == 0 {
			weight = 1
		}
		summary.Profiles = append(summary.Profiles, ManagedProfileChoice{
			ID: in.ID, Name: name, ListenPort: in.ListenPort, RouteID: in.RouteID, Weight: weight,
			TCPEnabled: in.TCP(), UOTEnabled: in.UOTEnabled, SchedulerMode: p.Config.Tunnel.SchedulerMode, BackgroundResident: p.BackgroundResident,
		})
	}
	if summary.FromCache {
		summary.Status = "已加载验证过的共享隧道缓存"
	} else {
		summary.Status = "共享隧道已同步"
	}
	a.state.Managed = summary
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
				parts[len(parts)-1] = "[secret]"
			}
		}
	}
	u.Path = "/" + strings.Join(parts, "/")
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}
