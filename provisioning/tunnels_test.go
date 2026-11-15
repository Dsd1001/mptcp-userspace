package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"mptcp-desktop/engine/tunnelconfig"
)

func validTunnelInput() tunnelInput {
	return tunnelInput{Name: "Shared tunnel", APIAlias: "test-tunnel", Config: tunnelconfig.Config{
		SchemaVersion: 1, Kind: tunnelconfig.Kind, Tunnel: tunnelconfig.Transport{TransportKey: strings.Repeat("ab", 32), SchedulerMode: "aggregate", Relays: []tunnelconfig.Relay{{Host: "192.0.2.1", Port: 24001}, {Host: "192.0.2.2", Port: 24001}}},
		Inlets: []tunnelconfig.Inlet{{ID: "hkbn", Name: "HKBN", ListenPort: 1081, RouteID: 1}, {ID: "hkt", Name: "HKT", ListenPort: 1082, RouteID: 2, UOTEnabled: true}, {ID: "icable", Name: "iCable", ListenPort: 1083, RouteID: 3}},
	}}
}
func tunnelTestApp(t *testing.T) (*app, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	st, err := newTunnelStore(filepath.Join(dir, "tunnels.json"))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := newDeviceStore(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{tunnels: st, devices: ds, adminUser: "admin", adminPass: "test-only-password", passwordFile: filepath.Join(dir, "admin-password")}
	server := httptest.NewServer(a.handler())
	t.Cleanup(server.Close)
	return a, server
}
func tunnelAdminRequest(t *testing.T, server *httptest.Server, method, path string, body any, want int) []byte {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("admin", "test-only-password")
	req.Header.Set("Content-Type", "application/json")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, res.StatusCode, want, b)
	}
	return b
}
func createAPITunnel(t *testing.T, server *httptest.Server) adminTunnel {
	t.Helper()
	b := tunnelAdminRequest(t, server, http.MethodPost, "/admin/api/tunnels", validTunnelInput(), 201)
	var out adminTunnel
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestTunnelAPIEncryptedCRUDAndBreakingContract(t *testing.T) {
	a, server := tunnelTestApp(t)
	rec := createAPITunnel(t, server)
	if rec.Revision != 1 || rec.APIURL == "" || rec.ID == "" {
		t.Fatalf("missing identity: %+v", rec)
	}
	got, err := tunnelconfig.Fetch(context.Background(), rec.APIURL)
	if err != nil || got.SchemaVersion != 4 || got.Kind != "tunnel_profile" || len(got.Config.Inlets) != 3 {
		t.Fatalf("encrypted client fetch: %+v %v", got, err)
	}
	res, err := server.Client().Get(rec.APIURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if bytes.Contains(body, []byte(rec.Config.Tunnel.TransportKey)) || bytes.Contains(body, []byte("HKBN")) {
		t.Fatal("public response exposes plaintext configuration")
	}
	in := validTunnelInput()
	in.Name = "Updated"
	in.ExpectedRevision = &rec.Revision
	changed := tunnelAdminRequest(t, server, http.MethodPut, "/admin/api/tunnels/"+rec.ID, in, 200)
	var updated adminTunnel
	json.Unmarshal(changed, &updated)
	if updated.Revision != 2 || updated.Name != "Updated" {
		t.Fatal("update not committed")
	}
	tunnelAdminRequest(t, server, http.MethodPut, "/admin/api/tunnels/"+rec.ID, in, 409)
	rotated := tunnelAdminRequest(t, server, http.MethodPost, "/admin/api/tunnels/"+rec.ID+"/rotate", map[string]any{"expected_revision": updated.Revision}, 200)
	var next adminTunnel
	json.Unmarshal(rotated, &next)
	if next.APIURL == updated.APIURL {
		t.Fatal("token not rotated")
	}
	if _, err = tunnelconfig.Fetch(context.Background(), updated.APIURL); err == nil {
		t.Fatal("old access token still accepted")
	}
	if _, err = tunnelconfig.Fetch(context.Background(), next.APIURL); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newTunnelStore(a.tunnels.path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reloaded.get(rec.ID)
	if !ok || persisted.Revision != next.Revision {
		t.Fatal("reloaded store diverged")
	}
	stat, _ := os.Stat(a.tunnels.path)
	if stat.Mode().Perm() != 0600 {
		t.Fatal("store permissions are not private")
	}
	for _, path := range []string{"/v1/config/old", "/v1/bundle/old", "/admin/api/profiles", "/admin/api/bundles"} {
		tunnelAdminRequest(t, server, http.MethodGet, path, nil, 410)
	}
	var sys systemInfo
	json.Unmarshal(tunnelAdminRequest(t, server, http.MethodGet, "/admin/api/system", nil, 200), &sys)
	if sys.APISchema != 4 {
		t.Fatalf("controller reports wrong schema: %d", sys.APISchema)
	}
	tunnelAdminRequest(t, server, http.MethodDelete, "/admin/api/tunnels/"+rec.ID, map[string]any{"expected_revision": next.Revision}, 204)
	if len(a.tunnels.list()) != 0 {
		t.Fatal("deleted tunnel retained")
	}
}
func TestTunnelAdminAuthenticationCSRFAndUnknownTargets(t *testing.T) {
	_, server := tunnelTestApp(t)
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/admin/api/tunnels", nil)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("unauthenticated admin request accepted")
	}
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/admin/api/tunnels", strings.NewReader(`{}`))
	req.SetBasicAuth("admin", "test-only-password")
	req.Header.Set("Content-Type", "text/plain")
	res, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 415 {
		t.Fatal("cross-site form compatible mutation accepted")
	}
	bad := validTunnelInput()
	bad.Config.Inlets[1].RouteID = 1
	tunnelAdminRequest(t, server, http.MethodPost, "/admin/api/tunnels", bad, 400)
	tunnelAdminRequest(t, server, http.MethodPost, "/admin/api/devices", adminDeviceInput{Name: "old", AssignmentType: "profile", AssignmentID: "anything", DesiredState: "running"}, 400)
	tunnelAdminRequest(t, server, http.MethodPost, "/admin/api/devices", adminDeviceInput{Name: "missing", AssignmentType: "tunnel", AssignmentID: "missing", DesiredState: "running"}, 400)
}
func TestTunnelStoreOptimisticConcurrencyAndFailedWrite(t *testing.T) {
	st, err := newTunnelStore(filepath.Join(t.TempDir(), "tunnels.json"))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := st.upsert("", validTunnelInput())
	if err != nil {
		t.Fatal(err)
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := validTunnelInput()
			in.ExpectedRevision = &rec.Revision
			_, err := st.upsert(rec.ID, in)
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, errTunnelConflict) {
				t.Errorf("update error: %v", err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("revision did not serialize updates: %d", success.Load())
	}
	copy, _ := st.get(rec.ID)
	copy.Config.Inlets[0].Name = "mutated snapshot"
	original, _ := st.get(rec.ID)
	if original.Config.Inlets[0].Name == "mutated snapshot" {
		t.Fatal("store snapshot aliases mutable data")
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	os.WriteFile(file, []byte("x"), 0600)
	invalid := &tunnelStore{path: filepath.Join(file, "tunnels.json"), records: map[string]tunnelRecord{}}
	if _, err := invalid.upsert("", validTunnelInput()); err == nil || len(invalid.records) != 0 {
		t.Fatal("failed durable write published new state")
	}
	legacy := filepath.Join(t.TempDir(), "profiles.json")
	old := []byte(`[{"id":"old-profile"}]`)
	os.WriteFile(legacy, old, 0600)
	if _, err := newTunnelStore(legacy); err == nil {
		t.Fatal("legacy store silently converted")
	}
	after, _ := os.ReadFile(legacy)
	if !bytes.Equal(after, old) {
		t.Fatal("legacy data was overwritten")
	}
}
func TestTunnelDeviceAssignmentPairPollSyncAndRevocation(t *testing.T) {
	a, server := tunnelTestApp(t)
	rec := createAPITunnel(t, server)
	body := tunnelAdminRequest(t, server, http.MethodPost, "/admin/api/devices", adminDeviceInput{Name: "test-mac", AssignmentType: "tunnel", AssignmentID: rec.ID, DesiredState: "running"}, 201)
	var device adminDevice
	json.Unmarshal(body, &device)
	if device.PairingCode == "" {
		t.Fatal("no pairing code")
	}
	pairBody, _ := json.Marshal(devicePairInput{Code: device.PairingCode, DeviceName: "test-mac", AppVersion: "1.2.0"})
	res, err := server.Client().Post(server.URL+"/v1/device/pair", "application/json", bytes.NewReader(pairBody))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("pair: %s", data)
	}
	var pair devicePairResponse
	json.Unmarshal(data, &pair)
	poll := func(want int) deviceControl {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/device/poll?since=0", nil)
		req.Header.Set("X-MPX-Device-ID", pair.DeviceID)
		req.Header.Set("Authorization", "Bearer "+pair.DeviceSecret)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("poll %d want %d: %s", res.StatusCode, want, b)
		}
		var c deviceControl
		if want == 200 {
			if err := json.Unmarshal(b, &c); err != nil {
				t.Fatal(err)
			}
		}
		return c
	}
	control := poll(200)
	if control.AssignmentType != "tunnel" || control.ProvisioningURL != rec.APIURL || control.DesiredState != "running" {
		t.Fatalf("wrong tunnel assignment: %+v", control)
	}
	tunnelAdminRequest(t, server, http.MethodDelete, "/admin/api/tunnels/"+rec.ID, map[string]any{"expected_revision": rec.Revision}, 409)
	for _, action := range []string{"restart", "sync", "update"} {
		tunnelAdminRequest(t, server, http.MethodPost, "/admin/api/devices/"+device.ID+"/"+action, map[string]any{}, 200)
	}
	control = poll(200)
	if control.RestartGeneration == 0 || control.SyncGeneration == 0 || control.UpdateGeneration == 0 {
		t.Fatal("remote action generations missing")
	}
	// Old stored assignments are retained for manual reassignment but never
	// authorize running a cached independent transport in the 1.2 client.
	a.devices.mu.Lock()
	old := a.devices.records[device.ID]
	old.AssignmentType = "bundle"
	old.AssignmentID = "legacy"
	a.devices.records[device.ID] = old
	a.devices.mu.Unlock()
	control = poll(200)
	if control.DesiredState != "stopped" || control.ProvisioningURL != "" || control.AssignmentType != "" {
		t.Fatal("legacy assignment still runs")
	}
	tunnelAdminRequest(t, server, http.MethodDelete, "/admin/api/devices/"+device.ID, nil, 204)
	poll(401)
}
