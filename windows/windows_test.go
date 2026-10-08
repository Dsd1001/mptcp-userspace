//go:build windows

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func validWindowsProfile(id string) managedProfile {
	key := strings.Repeat("11", 32)
	return managedProfile{
		SchemaVersion: 1,
		ProfileID:     id,
		Revision:      "r1",
		DisplayName:   id,
		Mode:          "userspace_multipath",
		ListenPort:    1081,
		SchedulerMode: "auto",
		TCPEnabled:    true,
		TransportKey:  key,
		Relays:        []Relay{{Host: "192.0.2.1", Port: 24001}, {Host: "192.0.2.2", Port: 24002}},
	}
}

func TestManagedProfileRejectsNativeMPTCP(t *testing.T) {
	p := validWindowsProfile("native")
	p.Mode = "native_mptcp"
	if err := p.validateWindows(); err == nil {
		t.Fatal("Windows accepted native_mptcp")
	}
}

func TestBundleFiltersMacNativeProfiles(t *testing.T) {
	userspace := validWindowsProfile("win")
	native := validWindowsProfile("mac")
	native.Mode = "native_mptcp"
	b := managedBundle{
		SchemaVersion: 2,
		Kind:          "bundle",
		BundleID:      "mixed",
		Revision:      "r1",
		DisplayName:   "Mixed",
		Mode:          "parallel",
		Profiles:      []managedProfile{native, userspace},
	}
	if err := validateWindowsBundle(&b); err != nil {
		t.Fatal(err)
	}
	if len(b.Profiles) != 1 || b.Profiles[0].ProfileID != "win" || b.Profiles[0].Mode != "userspace_multipath" {
		t.Fatalf("unexpected Windows filtered bundle: %+v", b.Profiles)
	}
}

func TestBundleWithOnlyNativeProfilesIsRejected(t *testing.T) {
	native := validWindowsProfile("mac")
	native.Mode = "native_mptcp"
	b := managedBundle{SchemaVersion: 2, Kind: "bundle", BundleID: "native-only", Revision: "r1", DisplayName: "Native", Mode: "single_select", Profiles: []managedProfile{native}}
	if err := validateWindowsBundle(&b); err == nil {
		t.Fatal("native-only bundle accepted on Windows")
	}
}

func TestAutostartCommandQuotesExecutablePath(t *testing.T) {
	path := `C:\Users\Test User\MPTCP Desk\MPTCP-Desk-Windows.exe`
	got := autostartCommand(path)
	want := `"` + path + `" --background`
	if got != want {
		t.Fatalf("autostartCommand=%q want %q", got, want)
	}
	if strings.Contains(got, `\"`) {
		t.Fatalf("autostart command contains literal backslash-quote: %q", got)
	}
}

func TestDPAPIRoundTrip(t *testing.T) {
	secret := "transport-key-should-not-be-plaintext"
	ciphertext, err := protectString(secret)
	if err != nil {
		t.Fatal(err)
	}
	if ciphertext == "" || strings.Contains(ciphertext, secret) {
		t.Fatalf("unexpected DPAPI ciphertext %q", ciphertext)
	}
	plaintext, err := unprotectString(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != secret {
		t.Fatalf("round trip = %q, want %q", plaintext, secret)
	}
}

func TestProvisioningEncryptedEnvelope(t *testing.T) {
	tokenBytes := bytesRepeat(0x23, 32)
	token := hex.EncodeToString(tokenBytes)
	profile := validWindowsProfile("encrypted")
	payload, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	sealed := encryptProvisioningForTest(t, payload, tokenBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, token) {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sealed)
	}))
	defer srv.Close()

	doc, _, err := fetchManagedDocument(srv.URL + "/v1/config/" + token)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Profile == nil || doc.Profile.ProfileID != "encrypted" || doc.Profile.Mode != "userspace_multipath" {
		t.Fatalf("unexpected document: %+v", doc)
	}
}

func TestProvisioningRejectsPublicHTTP(t *testing.T) {
	if _, err := validateProvisioningURL("http://example.com/v1/config/" + strings.Repeat("0", 64)); err == nil {
		t.Fatal("public HTTP provisioning URL accepted")
	}
	if _, err := validateProvisioningURL("http://127.0.0.1/v1/config/" + strings.Repeat("0", 64)); err != nil {
		t.Fatalf("loopback HTTP provisioning rejected: %v", err)
	}
}

func TestRemoteRootRequiresHTTPSExceptLoopback(t *testing.T) {
	if _, err := validateRemoteRoot("http://example.com"); err == nil {
		t.Fatal("public HTTP remote root accepted")
	}
	if _, err := validateRemoteRoot("http://localhost:8080"); err != nil {
		t.Fatalf("localhost remote root rejected: %v", err)
	}
}

func TestVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.1.2", "1.1.1", 1}, {"1.1.1", "1.1.1", 0}, {"1.0.9", "1.1.0", -1}, {"v2.0.0", "1.9.9", 1},
	} {
		got := compareVersions(tc.a, tc.b)
		if (got < 0 && tc.want >= 0) || (got > 0 && tc.want <= 0) || (got == 0 && tc.want != 0) {
			t.Fatalf("compareVersions(%q,%q)=%d want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestUpdateManifestValidation(t *testing.T) {
	sig := base64.StdEncoding.EncodeToString(make([]byte, 64))
	good := windowsUpdateManifest{Version: "9.9.9", URL: "https://example.com/update.exe", SHA256: strings.Repeat("a", 64), Size: 123, Signature: sig}
	if err := validateUpdateManifest(good); err != nil {
		t.Fatal(err)
	}
	good.URL = "http://example.com/update.exe"
	if err := validateUpdateManifest(good); err == nil {
		t.Fatal("HTTP update URL accepted")
	}
}

func encryptProvisioningForTest(t *testing.T, payload, token []byte) []byte {
	t.Helper()
	mac := hmac.New(sha256.New, token)
	_, _ = mac.Write([]byte("mpx-provision-config-envelope-v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytesRepeat(0x42, gcm.NonceSize())
	sealed := gcm.Seal(nil, nonce, payload, []byte("mpx-provision-envelope-v1"))
	data, err := json.Marshal(encryptedEnvelope{V: 1, N: base64.RawURLEncoding.EncodeToString(nonce), D: base64.RawURLEncoding.EncodeToString(sealed)})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func bytesRepeat(v byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = v
	}
	return out
}
