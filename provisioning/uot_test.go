package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestUOTProvisioningTransportSelection(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		tcp, udp, uot, valid bool
	}{
		{"tcp", true, false, false, true},
		{"native-udp", false, true, false, true},
		{"uot", false, false, true, true},
		{"tcp-uot", true, false, true, true},
		{"tcp-native-udp", true, true, false, true},
		{"empty", false, false, false, false},
		{"udp-uot", false, true, true, false},
		{"all", true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			c.TCPEnabled, c.UDPEnabled, c.UOTEnabled = tc.tcp, tc.udp, tc.uot
			if err := validateInput("selection", c); (err == nil) != tc.valid {
				t.Fatalf("validate=%v; want valid=%t", err, tc.valid)
			}
		})
	}
	c := validConfig()
	c.Mode, c.UDPEnabled, c.UOTEnabled = "native_mptcp", false, true
	if err := validateInput("native", c); err == nil {
		t.Fatal("Native mode accepted UoT")
	}
	raw, err := json.Marshal(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err = json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	if _, exists := legacy["uot_enabled"]; exists {
		t.Fatal("old profiles unexpectedly publish a new disabled field")
	}
	var decoded provisionConfig
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded.UOTEnabled {
		t.Fatalf("old profile migration: %+v, %v", decoded, err)
	}
}

func TestUOTProvisioningEncryptedProfileAndBundleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	st, err := newStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c := validConfig()
	c.TCPEnabled, c.UDPEnabled, c.UOTEnabled = false, false, true
	rec, err := st.create("UoT only", c)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise persisted configs as well as both encrypted public API shapes.
	st, err = newStore(path)
	if err != nil {
		t.Fatal(err)
	}
	bs, err := newBundleStore(filepath.Join(dir, "bundles.json"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := bs.create("UoT bundle", "parallel", []string{rec.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{store: st, bundles: bs}
	profileResponse := httptest.NewRecorder()
	a.publicConfig(profileResponse, httptest.NewRequest(http.MethodGet, "/v1/config/"+rec.Token, nil))
	if profileResponse.Code != http.StatusOK {
		t.Fatalf("profile status=%d", profileResponse.Code)
	}
	var profile publicPayload
	decodeEncryptedForTest(t, rec.Token, profileResponse.Body, &profile)
	if !profile.UOTEnabled || profile.UDPEnabled || profile.TCPEnabled {
		t.Fatalf("profile selection=%+v", profile)
	}
	bundleResponse := httptest.NewRecorder()
	a.publicBundle(bundleResponse, httptest.NewRequest(http.MethodGet, "/v1/bundle/"+bundle.Token, nil))
	if bundleResponse.Code != http.StatusOK {
		t.Fatalf("bundle status=%d", bundleResponse.Code)
	}
	var payload bundlePublicPayload
	decodeEncryptedForTest(t, bundle.Token, bundleResponse.Body, &payload)
	if len(payload.Profiles) != 1 || !payload.Profiles[0].UOTEnabled || payload.Profiles[0].UDPEnabled || payload.Profiles[0].TCPEnabled {
		t.Fatalf("bundle selection=%+v", payload.Profiles)
	}
}
