package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

func decodeEncryptedForTest(t *testing.T, token string, r io.Reader, out any) encryptedEnvelope {
	t.Helper()
	var envelope encryptedEnvelope
	if err := json.NewDecoder(r).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Version != 1 || envelope.Nonce == "" || envelope.Data == "" {
		t.Fatalf("bad envelope: %+v", envelope)
	}
	key, err := envelopeKey(token)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(envelope.Data)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, nonce, sealed, []byte("mpx-provision-envelope-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(plain, out); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func validConfig() provisionConfig {
	return provisionConfig{
		Mode: "userspace_multipath", ListenPort: 1081, SchedulerMode: "weighted",
		TCPEnabled: true, UDPEnabled: true, BackgroundResident: true,
		TransportKey: strings.Repeat("a", 64),
		Relays: []relay{
			{Host: "192.0.2.10", Port: 8849, DownloadMbps: f64(94), UploadMbps: f64(20)},
			{Host: "198.51.100.20", Port: 8849, DownloadMbps: f64(94)},
		},
	}
}

func TestValidateInput(t *testing.T) {
	cfg := validConfig()
	if err := validateInput("hk", cfg); err != nil {
		t.Fatal(err)
	}
	cfg.TransportKey = "bad"
	if err := validateInput("hk", cfg); err == nil {
		t.Fatal("accepted invalid key")
	}
	cfg = validConfig()
	cfg.Relays = cfg.Relays[:1]
	if err := validateInput("hk", cfg); err == nil {
		t.Fatal("accepted one relay")
	}
	cfg = validConfig()
	cfg.SchedulerMode = "weighted"
	cfg.Relays[0].DownloadMbps = nil
	if err := validateInput("hk", cfg); err == nil {
		t.Fatal("accepted weighted relay without download capacity")
	}
	cfg = validConfig()
	cfg.Mode = "native_mptcp"
	cfg.TransportKey = ""
	cfg.SchedulerMode = ""
	cfg.TCPEnabled = true
	if err := validateInput("native", cfg); err != nil {
		t.Fatalf("valid native rejected: %v", err)
	}
}

func TestProvisioningHTTPFlow(t *testing.T) {
	st, err := newStore(filepath.Join(t.TempDir(), "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := st.create("Synthetic", validConfig())
	if err != nil {
		t.Fatal(err)
	}
	a := &app{store: st, adminUser: "admin", adminPass: "secret", publicBase: "https://cfg.example.test"}
	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	// Public config needs only the unguessable token and must never be cacheable.
	resp, err := http.Get(srv.URL + "/v1/config/" + rec.Token)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("public status=%d", resp.StatusCode)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control=%q", resp.Header.Get("Cache-Control"))
	}
	var payload publicPayload
	envelope := decodeEncryptedForTest(t, rec.Token, resp.Body, &payload)
	_ = resp.Body.Close()
	if envelope.Data == "" {
		t.Fatal("public config was not encrypted")
	}
	if payload.SchemaVersion != 1 || payload.DisplayName != "Synthetic" || payload.Mode != "userspace_multipath" || len(payload.Relays) != 2 || payload.TransportKey != strings.Repeat("a", 64) {
		t.Fatalf("bad payload: %+v", payload)
	}

	// Admin endpoints require Basic authentication.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/api/profiles", nil)
	unauth, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d", unauth.StatusCode)
	}
	_ = unauth.Body.Close()
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/admin/api/profiles", nil)
	req.SetBasicAuth("admin", "secret")
	adminResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if adminResp.StatusCode != 200 {
		t.Fatalf("admin status=%d", adminResp.StatusCode)
	}
	var rows []adminRecord
	if err := json.NewDecoder(adminResp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	_ = adminResp.Body.Close()
	if len(rows) != 1 || rows[0].APIURL != "https://cfg.example.test/v1/config/"+rec.Token {
		t.Fatalf("bad admin row: %+v", rows)
	}

	// Rotating the URL invalidates the previous token immediately.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/admin/api/profiles/"+rec.ID+"/rotate", nil)
	req.SetBasicAuth("admin", "secret")
	rot, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if rot.StatusCode != 200 {
		t.Fatalf("rotate status=%d", rot.StatusCode)
	}
	var rotated adminRecord
	if err := json.NewDecoder(rot.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	_ = rot.Body.Close()
	if rotated.APIURL == rows[0].APIURL {
		t.Fatal("token did not rotate")
	}
	old, _ := http.Get(srv.URL + "/v1/config/" + rec.Token)
	if old.StatusCode != http.StatusNotFound {
		t.Fatalf("old token status=%d", old.StatusCode)
	}
	_ = old.Body.Close()
}

func TestValidatePublicBase(t *testing.T) {
	for _, good := range []string{"", "https://config.example.test", "http://127.0.0.1:8088", "http://localhost:8088"} {
		if err := validatePublicBase(good); err != nil {
			t.Fatalf("%q rejected: %v", good, err)
		}
	}
	for _, bad := range []string{"http://config.example.test", "https://user:pass@config.example.test", "https://config.example.test/#fragment"} {
		if err := validatePublicBase(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestExistingStorePermissionsAreTightened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newStore(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%#o", got)
	}
}

func adminJSON(t *testing.T, client *http.Client, method, rawURL string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("admin", "secret")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCustomAliasHTTPFlow(t *testing.T) {
	st, err := newStore(filepath.Join(t.TempDir(), "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{store: st, adminUser: "admin", adminPass: "secret", publicBase: "https://cfg.example.test"}
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	client := srv.Client()
	alias := "HKBN-Main"
	create := adminInput{Name: "HKBN Main", APIAlias: &alias, Config: validConfig()}
	resp := adminJSON(t, client, http.MethodPost, srv.URL+"/admin/api/profiles", create)
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status=%d body=%s", resp.StatusCode, data)
	}
	var row adminRecord
	if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if row.APIAlias != "hkbn-main" || !strings.Contains(row.APIURL, "/v1/config/hkbn-main/") {
		t.Fatalf("bad custom URL: %+v", row)
	}
	secret := row.APIURL[strings.LastIndex(row.APIURL, "/")+1:]
	if len(secret) != 64 {
		t.Fatalf("secret len=%d", len(secret))
	}
	good, _ := http.Get(srv.URL + "/v1/config/hkbn-main/" + secret)
	if good.StatusCode != http.StatusOK {
		t.Fatalf("custom URL status=%d", good.StatusCode)
	}
	_ = good.Body.Close()
	legacyShape, _ := http.Get(srv.URL + "/v1/config/" + secret)
	if legacyShape.StatusCode != http.StatusNotFound {
		t.Fatalf("token-only form remained valid for custom alias: %d", legacyShape.StatusCode)
	}
	_ = legacyShape.Body.Close()

	// Aliases are unique after normalization.
	dupAlias := "hkbn-main"
	dup := adminInput{Name: "Duplicate", APIAlias: &dupAlias, Config: validConfig()}
	dupResp := adminJSON(t, client, http.MethodPost, srv.URL+"/admin/api/profiles", dup)
	if dupResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate alias status=%d", dupResp.StatusCode)
	}
	_ = dupResp.Body.Close()

	// Renaming the alias rotates the bearer secret, invalidating the whole old URL.
	newAlias := "hkbn-5line"
	update := adminInput{Name: "HKBN Main", APIAlias: &newAlias, Config: validConfig()}
	up := adminJSON(t, client, http.MethodPut, srv.URL+"/admin/api/profiles/"+row.ID, update)
	if up.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(up.Body)
		t.Fatalf("update status=%d body=%s", up.StatusCode, data)
	}
	var renamed adminRecord
	if err := json.NewDecoder(up.Body).Decode(&renamed); err != nil {
		t.Fatal(err)
	}
	_ = up.Body.Close()
	if renamed.APIAlias != "hkbn-5line" || renamed.APIURL == row.APIURL {
		t.Fatalf("alias change did not replace URL: old=%s new=%s", row.APIURL, renamed.APIURL)
	}
	old, _ := http.Get(srv.URL + "/v1/config/hkbn-main/" + secret)
	if old.StatusCode != http.StatusNotFound {
		t.Fatalf("old custom URL status=%d", old.StatusCode)
	}
	_ = old.Body.Close()

	// Switching back to automatic mode rotates again, so the original token cannot revive.
	auto := ""
	update.APIAlias = &auto
	up2 := adminJSON(t, client, http.MethodPut, srv.URL+"/admin/api/profiles/"+row.ID, update)
	if up2.StatusCode != http.StatusOK {
		t.Fatalf("auto update status=%d", up2.StatusCode)
	}
	var automatic adminRecord
	if err := json.NewDecoder(up2.Body).Decode(&automatic); err != nil {
		t.Fatal(err)
	}
	_ = up2.Body.Close()
	if automatic.APIAlias != "" || strings.Count(strings.TrimPrefix(automatic.APIURL, "https://cfg.example.test/v1/config/"), "/") != 0 {
		t.Fatalf("bad automatic URL: %+v", automatic)
	}
	newSecret := automatic.APIURL[strings.LastIndex(automatic.APIURL, "/")+1:]
	if newSecret == secret {
		t.Fatal("URL identity change reused an old bearer secret")
	}
	autoPublic, _ := http.Get(srv.URL + "/v1/config/" + newSecret)
	if autoPublic.StatusCode != http.StatusOK {
		t.Fatalf("automatic URL status=%d", autoPublic.StatusCode)
	}
	_ = autoPublic.Body.Close()
}

func TestLegacyStoreWithoutAliasRemainsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	token := strings.Repeat("b", 64)
	legacy := []map[string]any{{
		"id": "0011223344556677", "name": "Legacy", "token": token, "revision": 3,
		"updated_at": "2026-10-01T00:00:00Z", "config": validConfig(),
	}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := newStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if rec, ok := st.byAccess("", token); !ok || rec.Name != "Legacy" || rec.APIAlias != "" {
		t.Fatalf("legacy record not accessible: %+v ok=%v", rec, ok)
	}
}

func TestAdminSystemInfo(t *testing.T) {
	st, err := newStore(filepath.Join(t.TempDir(), "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{store: st, adminUser: "admin", adminPass: "secret"}
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	resp := adminJSON(t, srv.Client(), http.MethodGet, srv.URL+"/admin/api/system", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("system status=%d", resp.StatusCode)
	}
	var info systemInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Component != "mpx-provision" || info.Version == "" || info.APISchema != 1 {
		t.Fatalf("bad system info: %+v", info)
	}
}

func TestAdminUIContainsSecondLevelNavigationAndRelayCopy(t *testing.T) {
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, want := range []string{"基础设置", "Relay 路径", "调度与传输", "发放与安全", "duplicateRelay", "自定义标识 + 随机 Secret", "轮换 Secret", "系统", "修改管理员密码", "currentAdminPassword", "changeAdminPassword", "Client Bundles", "多配置并行", "单配置选择", "bundleConflict", "Bundle API URL", "Devices", "一次性配对码", "同步配置", "更新到最新版", "不提供 Shell 或任意命令执行"} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin UI missing %q", want)
		}
	}
}

func adminRequestWithPassword(t *testing.T, client *http.Client, method, rawURL, password string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("admin", password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAdminPasswordChangePersistsAndInvalidatesOldCredentials(t *testing.T) {
	dir := t.TempDir()
	st, err := newStore(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	oldPass := "oldpass8"
	newPass := "newpass8"
	passwordFile := filepath.Join(dir, "admin-password")
	a := &app{store: st, adminUser: "admin", adminPass: oldPass, passwordFile: passwordFile}
	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	wrong := adminRequestWithPassword(t, srv.Client(), http.MethodPost, srv.URL+"/admin/api/password", oldPass, passwordInput{CurrentPassword: "not-the-current-password", NewPassword: newPass})
	if wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong-current status=%d", wrong.StatusCode)
	}
	_ = wrong.Body.Close()

	short := adminRequestWithPassword(t, srv.Client(), http.MethodPost, srv.URL+"/admin/api/password", oldPass, passwordInput{CurrentPassword: oldPass, NewPassword: "short7!"})
	if short.StatusCode != http.StatusBadRequest {
		t.Fatalf("short-password status=%d", short.StatusCode)
	}
	_ = short.Body.Close()

	changed := adminRequestWithPassword(t, srv.Client(), http.MethodPost, srv.URL+"/admin/api/password", oldPass, passwordInput{CurrentPassword: oldPass, NewPassword: newPass})
	if changed.StatusCode != http.StatusNoContent {
		data, _ := io.ReadAll(changed.Body)
		t.Fatalf("change status=%d body=%s", changed.StatusCode, data)
	}
	_ = changed.Body.Close()

	oldAuth := adminRequestWithPassword(t, srv.Client(), http.MethodGet, srv.URL+"/admin/api/system", oldPass, nil)
	if oldAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password still valid: %d", oldAuth.StatusCode)
	}
	_ = oldAuth.Body.Close()
	newAuth := adminRequestWithPassword(t, srv.Client(), http.MethodGet, srv.URL+"/admin/api/system", newPass, nil)
	if newAuth.StatusCode != http.StatusOK {
		t.Fatalf("new password rejected: %d", newAuth.StatusCode)
	}
	_ = newAuth.Body.Close()

	data, err := os.ReadFile(passwordFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != newPass+"\n" {
		t.Fatal("persisted password content mismatch")
	}
	info, err := os.Stat(passwordFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("password file mode=%#o", got)
	}

	// A restart must prefer the persisted password over the bootstrap env value.
	reloaded, err := loadAdminPassword(passwordFile, oldPass)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded != newPass {
		t.Fatal("restart did not retain changed password")
	}
}

func TestAdminPasswordFilePermissionsAreTightened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-password")
	password := "persist8"
	if err := os.WriteFile(path, []byte(password+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadAdminPassword(path, "bootstrp")
	if err != nil {
		t.Fatal(err)
	}
	if got != password {
		t.Fatalf("password=%q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%#o", info.Mode().Perm())
	}
}

func TestAdminPasswordValidation(t *testing.T) {
	for _, bad := range []string{"", "short", strings.Repeat("x", 7), strings.Repeat("x", 513), strings.Repeat("x", 24) + "\n"} {
		if err := validAdminPassword(bad); err == nil {
			t.Fatalf("accepted invalid password len=%d", len(bad))
		}
	}
	if err := validAdminPassword("valid123"); err != nil {
		t.Fatal(err)
	}
}

func newBundleTestApp(t *testing.T) (*app, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	st, err := newStore(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	bs, err := newBundleStore(filepath.Join(dir, "bundles.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{store: st, bundles: bs, adminUser: "admin", adminPass: "secret", publicBase: "https://cfg.example.test"}
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	return a, srv
}

func configWithPort(port int) provisionConfig {
	cfg := validConfig()
	cfg.ListenPort = port
	return cfg
}

func TestBundleParallelHTTPFlow(t *testing.T) {
	a, srv := newBundleTestApp(t)
	p1, err := a.store.create("HKBN", configWithPort(1081))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := a.store.create("HKT", configWithPort(1082))
	if err != nil {
		t.Fatal(err)
	}
	alias := "main-lines"
	in := bundleInput{Name: "Main Lines", APIAlias: &alias, Mode: "parallel", ProfileIDs: []string{p1.ID, p2.ID}}
	resp := adminJSON(t, srv.Client(), http.MethodPost, srv.URL+"/admin/api/bundles", in)
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("create bundle status=%d body=%s", resp.StatusCode, data)
	}
	var b adminBundle
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if b.Mode != "parallel" || b.APIAlias != alias || len(b.ProfileIDs) != 2 || !strings.Contains(b.APIURL, "/v1/bundle/main-lines/") {
		t.Fatalf("bad admin bundle: %+v", b)
	}
	secret := b.APIURL[strings.LastIndex(b.APIURL, "/")+1:]
	pub, err := http.Get(srv.URL + "/v1/bundle/main-lines/" + secret)
	if err != nil {
		t.Fatal(err)
	}
	if pub.StatusCode != http.StatusOK {
		t.Fatalf("public bundle status=%d", pub.StatusCode)
	}
	var payload bundlePublicPayload
	envelope := decodeEncryptedForTest(t, secret, pub.Body, &payload)
	_ = pub.Body.Close()
	if envelope.Data == "" {
		t.Fatal("public bundle was not encrypted")
	}
	if payload.SchemaVersion != 2 || payload.Kind != "bundle" || payload.Mode != "parallel" || payload.BundleID != b.ID || len(payload.Profiles) != 2 {
		t.Fatalf("bad public bundle: %+v", payload)
	}
	if payload.Profiles[0].ProfileID != p1.ID || payload.Profiles[0].ListenPort != 1081 || payload.Profiles[1].ProfileID != p2.ID || payload.Profiles[1].ListenPort != 1082 {
		t.Fatalf("profile order/config lost: %+v", payload.Profiles)
	}
	if payload.Profiles[0].TransportKey == "" || len(payload.Profiles[0].Relays) != 2 {
		t.Fatal("bundle did not include full runtime profile")
	}

	rot := adminJSON(t, srv.Client(), http.MethodPost, srv.URL+"/admin/api/bundles/"+b.ID+"/rotate", nil)
	if rot.StatusCode != http.StatusOK {
		t.Fatalf("rotate status=%d", rot.StatusCode)
	}
	var rotated adminBundle
	if err := json.NewDecoder(rot.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	_ = rot.Body.Close()
	if rotated.APIURL == b.APIURL {
		t.Fatal("bundle secret did not rotate")
	}
	old, _ := http.Get(srv.URL + "/v1/bundle/main-lines/" + secret)
	if old.StatusCode != http.StatusNotFound {
		t.Fatalf("old bundle URL status=%d", old.StatusCode)
	}
	_ = old.Body.Close()
}

func TestParallelBundleRejectsPortConflictsAndProtectsProfileUpdates(t *testing.T) {
	a, srv := newBundleTestApp(t)
	p1, _ := a.store.create("A", configWithPort(1081))
	p2, _ := a.store.create("B", configWithPort(1082))
	p3, _ := a.store.create("C", configWithPort(1081))

	bad := bundleInput{Name: "Conflict", Mode: "parallel", ProfileIDs: []string{p1.ID, p3.ID}}
	resp := adminJSON(t, srv.Client(), http.MethodPost, srv.URL+"/admin/api/bundles", bad)
	if resp.StatusCode != http.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("conflict bundle status=%d body=%s", resp.StatusCode, data)
	}
	_ = resp.Body.Close()

	// The same listen port is valid when the Bundle is explicitly single-select.
	single := bundleInput{Name: "Choose One", Mode: "single_select", ProfileIDs: []string{p1.ID, p3.ID}}
	resp = adminJSON(t, srv.Client(), http.MethodPost, srv.URL+"/admin/api/bundles", single)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("single-select same-port status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	parallel := bundleInput{Name: "Parallel", Mode: "parallel", ProfileIDs: []string{p1.ID, p2.ID}}
	resp = adminJSON(t, srv.Client(), http.MethodPost, srv.URL+"/admin/api/bundles", parallel)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("parallel create status=%d", resp.StatusCode)
	}
	var created adminBundle
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// A later Profile edit may not make an existing parallel Bundle invalid.
	changed := configWithPort(1081)
	up := adminInput{Name: "B", Config: changed}
	resp = adminJSON(t, srv.Client(), http.MethodPut, srv.URL+"/admin/api/profiles/"+p2.ID, up)
	if resp.StatusCode != http.StatusConflict {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("conflicting profile update status=%d body=%s", resp.StatusCode, data)
	}
	_ = resp.Body.Close()

	// A Profile used by any Bundle cannot be deleted until it is removed from that Bundle.
	resp = adminJSON(t, srv.Client(), http.MethodDelete, srv.URL+"/admin/api/profiles/"+p1.ID, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("referenced delete status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if _, ok := a.bundles.get(created.ID); !ok {
		t.Fatal("bundle unexpectedly removed")
	}
}

func TestBundleStorePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundles.json")
	if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newBundleStore(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bundle mode=%#o", info.Mode().Perm())
	}
}

func TestEncryptedEnvelopeHidesSensitiveFieldsAndRejectsWrongSecret(t *testing.T) {
	payload := publicPayload{SchemaVersion: 1, DisplayName: "Hidden", Mode: "userspace_multipath", ListenPort: 1081, TCPEnabled: true, TransportKey: strings.Repeat("c", 64), Relays: []relay{{Host: "203.0.113.7", Port: 8849}, {Host: "203.0.113.8", Port: 8849}}}
	token := strings.Repeat("ab", 32)
	envelope, err := encryptEnvelope(token, payload)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"203.0.113.7", "8849", strings.Repeat("c", 64), "transport_key", "relays"} {
		if bytes.Contains(wire, []byte(secret)) {
			t.Fatalf("encrypted wire exposed %q: %s", secret, wire)
		}
	}
	key, _ := envelopeKey(strings.Repeat("cd", 32))
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce, _ := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	sealed, _ := base64.RawURLEncoding.DecodeString(envelope.Data)
	if _, err := gcm.Open(nil, nonce, sealed, []byte("mpx-provision-envelope-v1")); err == nil {
		t.Fatal("wrong secret decrypted envelope")
	}
}

func TestEncryptedEnvelopeUsesFreshNonce(t *testing.T) {
	token := strings.Repeat("ef", 32)
	payload := map[string]any{"schema_version": 1, "value": "same"}
	a, err := encryptEnvelope(token, payload)
	if err != nil {
		t.Fatal(err)
	}
	b, err := encryptEnvelope(token, payload)
	if err != nil {
		t.Fatal(err)
	}
	if a.Nonce == b.Nonce || a.Data == b.Data {
		t.Fatal("encryption reused nonce/ciphertext")
	}
}
