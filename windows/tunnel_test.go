//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"mptcp-desktop/engine/tunnelconfig"
)

func validWindowsTunnel() tunnelconfig.Config {
	c := defaultTunnelConfig()
	c.Tunnel.TransportKey = strings.Repeat("ab", 32)
	c.Tunnel.Relays = []Relay{{Host: "192.0.2.1", Port: 24001}, {Host: "192.0.2.2", Port: 24001}, {Host: "192.0.2.3", Port: 24001}, {Host: "192.0.2.4", Port: 24001}, {Host: "192.0.2.5", Port: 24001}, {Host: "192.0.2.6", Port: 24001}}
	c.Inlets[1].UOTEnabled = true
	return c
}
func isolatedWindowsSettings(t *testing.T) {
	t.Helper()
	t.Setenv("APPDATA", t.TempDir())
}
func TestTunnelLaunchContainsOneSharedTransport(t *testing.T) {
	isolatedWindowsSettings(t)
	c := validWindowsTunnel()
	cipher, err := protectString(c.Tunnel.TransportKey)
	if err != nil {
		t.Fatal(err)
	}
	stored := c
	stored.Tunnel.TransportKey = ""
	a := &App{settings: defaultSettings()}
	a.settings.Tunnel = stored
	a.settings.TransportKeyCipher = cipher
	plan, err := a.prepareLaunchLocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Args) != 1 || plan.Args[0] != "run-tunnel" {
		t.Fatalf("wrong launch: %v", plan.Args)
	}
	decoded, err := tunnelconfig.Decode(bytes.NewReader(plan.Input))
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Inlets) != 3 || len(decoded.Tunnel.Relays) != 6 {
		t.Fatal("not one three-inlet/six-carrier configuration")
	}
	if strings.Contains(string(plan.Input), "profiles") || strings.Contains(string(plan.Input), "userspace_multipath") {
		t.Fatal("legacy launch surface survived")
	}
	a.refreshSettingsViewLocked()
	view, _ := json.Marshal(a.state.Settings)
	if bytes.Contains(view, []byte(c.Tunnel.TransportKey)) || a.state.Settings.Tunnel.Tunnel.TransportKey != "" {
		t.Fatal("plaintext key leaked to settings view")
	}
	p := tunnelconfig.ManagedProfile{SchemaVersion: 4, Kind: "tunnel_profile", ProfileID: "managed", Revision: "r1", DisplayName: "Managed", Config: c}
	a.managed = &managedDocument{Kind: "tunnel_profile", Tunnel: &p}
	a.settings.ConfigurationSource = "managed"
	a.settings.SelectedProfileIDs = []string{"hkt"}
	plan, err = a.prepareLaunchLocked()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = tunnelconfig.Decode(bytes.NewReader(plan.Input))
	if err != nil || len(decoded.Inlets) != 1 || decoded.Inlets[0].ID != "hkt" || len(decoded.Tunnel.Relays) != 6 {
		t.Fatal("selection created another transport or lost relay group")
	}
	a.settings.SelectedProfileIDs = nil
	if _, err = a.prepareLaunchLocked(); err == nil {
		t.Fatal("empty managed selection accepted")
	}
	a.managed = &managedDocument{Kind: "bundle", Bundle: &managedBundle{}}
	if _, err = a.prepareLaunchLocked(); err == nil {
		t.Fatal("legacy bundle launched")
	}
}
func TestTunnelSettingsMigrationPreservesOldData(t *testing.T) {
	isolatedWindowsSettings(t)
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"configuration_source":"local","desired_running":true,"profile":{"schema_version":3,"mode":"userspace_multipath","listen_port":1081},"transport_key_cipher":"old-secret","managed_cache_cipher":"old-cache"}`)
	if err = os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !s.MigrationRequired || s.DesiredRunning || s.TransportKeyCipher != "" || s.ManagedCacheCipher != "" || s.SchemaVersion != 2 {
		t.Fatal("legacy data silently started or reused")
	}
	backup, err := os.ReadFile(path + ".pre-1.2.backup")
	if err != nil || !bytes.Equal(backup, old) {
		t.Fatal("original settings were not safely backed up")
	}
	actual, _ := os.ReadFile(path)
	if !bytes.Equal(actual, old) {
		t.Fatal("reading settings mutated original")
	}
	if err = saveSettings(s); err != nil {
		t.Fatal(err)
	}
	next, err := loadSettings()
	if err != nil || !next.MigrationRequired || next.DesiredRunning {
		t.Fatal("migration guard lost after restart")
	}
	backup, _ = os.ReadFile(path + ".pre-1.2.backup")
	if !bytes.Equal(backup, old) {
		t.Fatal("backup overwritten")
	}
}
func TestTunnelLegacyPublicSaveRejected(t *testing.T) {
	a := &App{settings: defaultSettings()}
	if a.SaveLocalProfile(LocalProfile{}) == nil {
		t.Fatal("legacy Wails save accepted")
	}
	if a.ImportTunnelJSON(`{"schema_version":3,"mode":"userspace_multipath"}`) == nil {
		t.Fatal("legacy import accepted")
	}
}
func TestTunnelRemoteControlIgnoresOldAndUnauthorizedMessages(t *testing.T) {
	isolatedWindowsSettings(t)
	a := &App{settings: defaultSettings()}
	a.applyRemoteDesired(remoteDesiredState{Revision: 12, DesiredState: "running", AssignmentType: "bundle", AssignmentID: "old"})
	if a.settings.RemoteRevision != 0 || a.settings.DesiredRunning {
		t.Fatal("disabled remote management took action")
	}
	a.settings.RemoteEnabled = true
	a.settings.RemoteRevision = 50
	a.applyRemoteDesired(remoteDesiredState{Revision: 49, DesiredState: "running", AssignmentType: "bundle"})
	if a.settings.RemoteRevision != 50 || a.settings.DesiredRunning {
		t.Fatal("stale control applied")
	}
	a.applyRemoteDesired(remoteDesiredState{Revision: 51, DesiredState: "running", AssignmentType: "profile", ProvisioningURL: "https://example.invalid/old"})
	if a.settings.DesiredRunning || a.state.Problem == "" {
		t.Fatal("legacy remote assignment launched")
	}
}
func TestTunnelMigrationRefusesUnwritableBackup(t *testing.T) {
	isolatedWindowsSettings(t)
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"desired_running":true,"profile":{"mode":"userspace_multipath"}}`)
	if err = os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	// A directory at the backup location must not be treated as a valid backup.
	if err = os.MkdirAll(path+".pre-1.2.backup", 0700); err != nil {
		t.Fatal(err)
	}
	_, err = loadSettings()
	if err == nil {
		t.Fatal("non-file backup accepted")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, old) {
		t.Fatal("failed migration modified original")
	}
}
