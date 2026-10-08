package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const cryptProtectUIForbidden = 0x1

type dataBlob struct {
	cbData uint32
	pbData *byte
}

var (
	crypt32                = windows.NewLazySystemDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procLocalFree          = kernel32.NewProc("LocalFree")
)

type diskSettings struct {
	ConfigurationSource     string       `json:"configuration_source"`
	Profile                 LocalProfile `json:"profile"`
	TransportKeyCipher      string       `json:"transport_key_cipher,omitempty"`
	ProvisioningCipher      string       `json:"provisioning_cipher,omitempty"`
	ManagedCacheCipher      string       `json:"managed_cache_cipher,omitempty"`
	SelectedProfileIDs      []string     `json:"selected_profile_ids,omitempty"`
	BackgroundResident      bool         `json:"background_resident"`
	DesiredRunning          bool         `json:"desired_running"`
	AutomaticUpdates        bool         `json:"automatic_updates"`
	RemoteEnabled           bool         `json:"remote_enabled"`
	RemoteServer            string       `json:"remote_server,omitempty"`
	RemoteCredentialCipher  string       `json:"remote_credential_cipher,omitempty"`
	RemoteRevision          uint64       `json:"remote_revision,omitempty"`
	RemoteRestartGeneration uint64       `json:"remote_restart_generation,omitempty"`
	RemoteSyncGeneration    uint64       `json:"remote_sync_generation,omitempty"`
	RemoteUpdateGeneration  uint64       `json:"remote_update_generation,omitempty"`
}

func defaultSettings() diskSettings {
	return diskSettings{
		ConfigurationSource: "local",
		Profile: LocalProfile{
			ListenPort:    1081,
			TCPEnabled:    true,
			UDPEnabled:    false,
			UOTEnabled:    false,
			SchedulerMode: "auto",
			Relays:        []Relay{{Port: 24001}, {Port: 24001}},
		},
		AutomaticUpdates: true,
	}
}

func settingsDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "MPTCP Desk")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func settingsPath() (string, error) {
	dir, err := settingsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

func loadSettings() (diskSettings, error) {
	s := defaultSettings()
	path, err := settingsPath()
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return defaultSettings(), err
	}
	if s.ConfigurationSource != "local" && s.ConfigurationSource != "managed" {
		s.ConfigurationSource = "local"
	}
	if s.Profile.ListenPort == 0 {
		s.Profile.ListenPort = 1081
	}
	if s.Profile.SchedulerMode == "" {
		s.Profile.SchedulerMode = "auto"
	}
	if len(s.Profile.Relays) == 0 {
		s.Profile.Relays = []Relay{{Port: 24001}, {Port: 24001}}
	}
	return s, nil
}

func saveSettings(s diskSettings) error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func protectString(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	protected, err := dpapiProtect([]byte(value))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(protected), nil
}

func unprotectString(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	plain, err := dpapiUnprotect(data)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func blob(data []byte) dataBlob {
	if len(data) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(data)), pbData: &data[0]}
}

func copyAndFreeBlob(b dataBlob) []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	out := make([]byte, int(b.cbData))
	copy(out, unsafe.Slice(b.pbData, int(b.cbData)))
	procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
	return out
}

func dpapiProtect(data []byte) ([]byte, error) {
	in := blob(data)
	var out dataBlob
	r, _, e := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0,
		uintptr(cryptProtectUIForbidden), uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData: %w", e)
	}
	return copyAndFreeBlob(out), nil
}

func dpapiUnprotect(data []byte) ([]byte, error) {
	in := blob(data)
	var out dataBlob
	r, _, e := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0,
		uintptr(cryptProtectUIForbidden), uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %w", e)
	}
	return copyAndFreeBlob(out), nil
}

func setAutostart(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	const valueName = "MPTCP Desk"
	if !enabled {
		err := key.DeleteValue(valueName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return key.SetStringValue(valueName, autostartCommand(exe))
}

func autostartCommand(exe string) string {
	return "\"" + strings.ReplaceAll(exe, "\"", "") + "\" --background"
}
