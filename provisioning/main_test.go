package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

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
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
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
