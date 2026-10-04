package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func deviceRequest(t *testing.T, client *http.Client, method, rawURL, id, secret string, body any) *http.Response {
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
	req.Header.Set("X-MPX-Device-ID", id)
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestDeviceControlPairDesiredReportAndRevoke(t *testing.T) {
	dir := t.TempDir()
	st, err := newStore(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	bs, err := newBundleStore(filepath.Join(dir, "bundles.json"))
	if err != nil {
		t.Fatal(err)
	}
	devicePath := filepath.Join(dir, "devices.json")
	ds, err := newDeviceStore(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := st.create("Remote Profile", validConfig())
	if err != nil {
		t.Fatal(err)
	}
	a := &app{
		store: st, bundles: bs, devices: ds,
		adminUser: "admin", adminPass: "secret",
		publicBase: "https://cfg.example.test",
	}
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	client := srv.Client()

	create := adminDeviceInput{
		Name: "Home MacBook", AssignmentType: "profile",
		AssignmentID: profile.ID, DesiredState: "stopped",
	}
	resp := adminJSON(t, client, http.MethodPost, srv.URL+"/admin/api/devices", create)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create status=%d body=%s", resp.StatusCode, body)
	}
	var admin adminDevice
	if err := json.NewDecoder(resp.Body).Decode(&admin); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if admin.ID == "" || admin.PairingCode == "" || admin.Paired {
		t.Fatalf("unexpected created device: %+v", admin)
	}

	raw, err := os.ReadFile(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(admin.PairingCode)) {
		t.Fatal("plaintext pairing code persisted")
	}
	if info, err := os.Stat(devicePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("device file permissions err=%v mode=%v", err, func() any {
			if info == nil {
				return nil
			}
			return info.Mode().Perm()
		}())
	}

	pairBody := devicePairInput{Code: admin.PairingCode, DeviceName: "Home MacBook", AppVersion: "0.10.6"}
	pairData, _ := json.Marshal(pairBody)
	pairResp, err := client.Post(srv.URL+"/v1/device/pair", "application/json", bytes.NewReader(pairData))
	if err != nil {
		t.Fatal(err)
	}
	if pairResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(pairResp.Body)
		pairResp.Body.Close()
		t.Fatalf("pair status=%d body=%s", pairResp.StatusCode, body)
	}
	var paired devicePairResponse
	if err := json.NewDecoder(pairResp.Body).Decode(&paired); err != nil {
		pairResp.Body.Close()
		t.Fatal(err)
	}
	pairResp.Body.Close()
	if paired.DeviceID != admin.ID || len(paired.DeviceSecret) != 64 {
		t.Fatalf("unexpected pair response: %+v", paired)
	}
	raw, err = os.ReadFile(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(paired.DeviceSecret)) {
		t.Fatal("plaintext device secret persisted")
	}

	bad := deviceRequest(t, client, http.MethodGet, srv.URL+"/v1/device/poll?since=0", paired.DeviceID, "wrong-secret", nil)
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong secret status=%d", bad.StatusCode)
	}
	bad.Body.Close()

	poll := deviceRequest(t, client, http.MethodGet, srv.URL+"/v1/device/poll?since=0", paired.DeviceID, paired.DeviceSecret, nil)
	if poll.StatusCode != http.StatusOK {
		t.Fatalf("initial poll status=%d", poll.StatusCode)
	}
	var desired deviceControl
	if err := json.NewDecoder(poll.Body).Decode(&desired); err != nil {
		poll.Body.Close()
		t.Fatal(err)
	}
	poll.Body.Close()
	if desired.DesiredState != "stopped" || desired.AssignmentType != "profile" || desired.AssignmentID != profile.ID {
		t.Fatalf("bad desired state: %+v", desired)
	}
	if !strings.HasPrefix(desired.ProvisioningURL, "https://cfg.example.test/v1/config/") {
		t.Fatalf("bad provisioning URL: %q", desired.ProvisioningURL)
	}
	oldRevision := desired.Revision

	update := adminDeviceInput{
		Name: "Home MacBook", AssignmentType: "profile",
		AssignmentID: profile.ID, DesiredState: "running",
	}
	adminUpdate := adminJSON(t, client, http.MethodPut, srv.URL+"/admin/api/devices/"+admin.ID, update)
	if adminUpdate.StatusCode != http.StatusOK {
		t.Fatalf("admin update status=%d", adminUpdate.StatusCode)
	}
	adminUpdate.Body.Close()

	start := time.Now()
	poll = deviceRequest(t, client, http.MethodGet, srv.URL+"/v1/device/poll?since="+strconv.FormatUint(oldRevision, 10), paired.DeviceID, paired.DeviceSecret, nil)
	_ = start
	if poll.StatusCode != http.StatusOK {
		t.Fatalf("changed poll status=%d", poll.StatusCode)
	}
	if err := json.NewDecoder(poll.Body).Decode(&desired); err != nil {
		poll.Body.Close()
		t.Fatal(err)
	}
	poll.Body.Close()
	if desired.DesiredState != "running" || desired.Revision <= oldRevision {
		t.Fatalf("desired state did not advance: %+v", desired)
	}

	report := deviceObserved{
		AppVersion: "0.10.6", Running: true, Status: "多配置入口已启动",
		ConfigRevision: "r9", BundleID: "bundle-a",
		ProfileStatus: map[string]string{"a": "已启动"},
	}
	reportResp := deviceRequest(t, client, http.MethodPost, srv.URL+"/v1/device/report", paired.DeviceID, paired.DeviceSecret, report)
	if reportResp.StatusCode != http.StatusNoContent {
		t.Fatalf("report status=%d", reportResp.StatusCode)
	}
	reportResp.Body.Close()

	viewResp := adminJSON(t, client, http.MethodGet, srv.URL+"/admin/api/devices/"+admin.ID, nil)
	var view adminDevice
	if err := json.NewDecoder(viewResp.Body).Decode(&view); err != nil {
		viewResp.Body.Close()
		t.Fatal(err)
	}
	viewResp.Body.Close()
	if !view.Paired || view.Observed.AppVersion != "0.10.6" || !view.Observed.Running || view.LastSeen == nil {
		t.Fatalf("observed state missing: %+v", view)
	}

	restart := adminJSON(t, client, http.MethodPost, srv.URL+"/admin/api/devices/"+admin.ID+"/restart", nil)
	if restart.StatusCode != http.StatusOK {
		t.Fatalf("restart status=%d", restart.StatusCode)
	}
	var restarted adminDevice
	if err := json.NewDecoder(restart.Body).Decode(&restarted); err != nil {
		restart.Body.Close()
		t.Fatal(err)
	}
	restart.Body.Close()
	if restarted.RestartGeneration != 1 {
		t.Fatalf("restart generation=%d", restarted.RestartGeneration)
	}

	syncResp := adminJSON(t, client, http.MethodPost, srv.URL+"/admin/api/devices/"+admin.ID+"/sync", nil)
	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("sync status=%d", syncResp.StatusCode)
	}
	var synced adminDevice
	if err := json.NewDecoder(syncResp.Body).Decode(&synced); err != nil {
		syncResp.Body.Close()
		t.Fatal(err)
	}
	syncResp.Body.Close()
	if synced.SyncGeneration != 1 {
		t.Fatalf("sync generation=%d", synced.SyncGeneration)
	}

	reloaded, err := newDeviceStore(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reloaded.get(admin.ID)
	if !ok || persisted.DesiredState != "running" || persisted.SyncGeneration != 1 || persisted.RestartGeneration != 1 {
		t.Fatalf("desired state did not persist across store reopen: %+v ok=%v", persisted, ok)
	}

	updateResp := adminJSON(t, client, http.MethodPost, srv.URL+"/admin/api/devices/"+admin.ID+"/update", map[string]string{"version": "latest"})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d", updateResp.StatusCode)
	}
	var updateView adminDevice
	if err := json.NewDecoder(updateResp.Body).Decode(&updateView); err != nil {
		updateResp.Body.Close()
		t.Fatal(err)
	}
	updateResp.Body.Close()
	if updateView.UpdateGeneration != 1 || updateView.DesiredVersion != "latest" {
		t.Fatalf("update generation=%d version=%q", updateView.UpdateGeneration, updateView.DesiredVersion)
	}

	unpair := deviceRequest(t, client, http.MethodPost, srv.URL+"/v1/device/unpair", paired.DeviceID, paired.DeviceSecret, struct{}{})
	if unpair.StatusCode != http.StatusNoContent {
		t.Fatalf("unpair status=%d", unpair.StatusCode)
	}
	unpair.Body.Close()
	after := deviceRequest(t, client, http.MethodGet, srv.URL+"/v1/device/poll?since=0", paired.DeviceID, paired.DeviceSecret, nil)
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked credential status=%d", after.StatusCode)
	}
	after.Body.Close()
}

func TestDeviceControlRejectsUnknownCommandAndCrossDeviceSecret(t *testing.T) {
	dir := t.TempDir()
	st, _ := newStore(filepath.Join(dir, "profiles.json"))
	bs, _ := newBundleStore(filepath.Join(dir, "bundles.json"))
	ds, _ := newDeviceStore(filepath.Join(dir, "devices.json"))
	a := &app{store: st, bundles: bs, devices: ds, adminUser: "admin", adminPass: "secret"}
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	client := srv.Client()

	createAndPair := func(name string) devicePairResponse {
		t.Helper()
		resp := adminJSON(t, client, http.MethodPost, srv.URL+"/admin/api/devices", adminDeviceInput{Name: name, DesiredState: "stopped"})
		var row adminDevice
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		payload, _ := json.Marshal(devicePairInput{Code: row.PairingCode, DeviceName: name, AppVersion: "0.10.6"})
		pair, err := client.Post(srv.URL+"/v1/device/pair", "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		var out devicePairResponse
		if err := json.NewDecoder(pair.Body).Decode(&out); err != nil {
			pair.Body.Close()
			t.Fatal(err)
		}
		pair.Body.Close()
		return out
	}
	aCred := createAndPair("A")
	bCred := createAndPair("B")

	cross := deviceRequest(t, client, http.MethodGet, srv.URL+"/v1/device/poll?since=0", aCred.DeviceID, bCred.DeviceSecret, nil)
	if cross.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-device secret status=%d", cross.StatusCode)
	}
	cross.Body.Close()

	raw := []byte(`{"name":"A","desired_state":"running","assignment_type":"","assignment_id":"","command":"rm -rf /"}`)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/admin/api/devices/"+aCred.DeviceID, bytes.NewReader(raw))
	req.SetBasicAuth("admin", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown command field status=%d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestDeviceControlLongPollWakesOnDesiredChange(t *testing.T) {
	dir := t.TempDir()
	st, _ := newStore(filepath.Join(dir, "profiles.json"))
	bs, _ := newBundleStore(filepath.Join(dir, "bundles.json"))
	ds, _ := newDeviceStore(filepath.Join(dir, "devices.json"))
	a := &app{store: st, bundles: bs, devices: ds, adminUser: "admin", adminPass: "secret"}
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	client := srv.Client()

	createdResp := adminJSON(t, client, http.MethodPost, srv.URL+"/admin/api/devices", adminDeviceInput{Name: "Long Poll", DesiredState: "stopped"})
	var created adminDevice
	if err := json.NewDecoder(createdResp.Body).Decode(&created); err != nil {
		createdResp.Body.Close()
		t.Fatal(err)
	}
	createdResp.Body.Close()
	payload, _ := json.Marshal(devicePairInput{Code: created.PairingCode, DeviceName: "Long Poll", AppVersion: "0.10.6"})
	pairResp, err := client.Post(srv.URL+"/v1/device/pair", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	var paired devicePairResponse
	if err := json.NewDecoder(pairResp.Body).Decode(&paired); err != nil {
		pairResp.Body.Close()
		t.Fatal(err)
	}
	pairResp.Body.Close()

	initial := deviceRequest(t, client, http.MethodGet, srv.URL+"/v1/device/poll?since=0", paired.DeviceID, paired.DeviceSecret, nil)
	var control deviceControl
	if err := json.NewDecoder(initial.Body).Decode(&control); err != nil {
		initial.Body.Close()
		t.Fatal(err)
	}
	initial.Body.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/device/poll?since="+strconv.FormatUint(control.Revision, 10), nil)
	req.Header.Set("X-MPX-Device-ID", paired.DeviceID)
	req.Header.Set("Authorization", "Bearer "+paired.DeviceSecret)
	type pollResult struct {
		resp *http.Response
		err  error
	}
	result := make(chan pollResult, 1)
	go func() {
		resp, err := client.Do(req)
		result <- pollResult{resp: resp, err: err}
	}()
	time.Sleep(100 * time.Millisecond)

	changed := adminJSON(t, client, http.MethodPut, srv.URL+"/admin/api/devices/"+created.ID, adminDeviceInput{Name: "Long Poll", DesiredState: "running"})
	if changed.StatusCode != http.StatusOK {
		t.Fatalf("desired update status=%d", changed.StatusCode)
	}
	changed.Body.Close()

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		defer got.resp.Body.Close()
		if got.resp.StatusCode != http.StatusOK {
			t.Fatalf("long poll status=%d", got.resp.StatusCode)
		}
		var awakened deviceControl
		if err := json.NewDecoder(got.resp.Body).Decode(&awakened); err != nil {
			t.Fatal(err)
		}
		if awakened.DesiredState != "running" || awakened.Revision <= control.Revision {
			t.Fatalf("long poll returned stale desired state: %+v", awakened)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long poll did not wake promptly after desired-state change")
	}
}
