package tunnelconfig

import (
	"bytes"
	"context"
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
	"sync/atomic"
	"testing"
)

func validConfig() Config {
	return Config{SchemaVersion: 1, Kind: Kind, Tunnel: Transport{TransportKey: strings.Repeat("ab", 32), SchedulerMode: "aggregate", Relays: []Relay{{Host: "127.0.0.1", Port: 24001}, {Host: "127.0.0.1", Port: 24002}}},
		Inlets: []Inlet{{ID: "hkbn", Name: "HKBN", ListenPort: 1081, RouteID: 1}, {ID: "hkt", Name: "HKT", ListenPort: 1082, RouteID: 2}, {ID: "icable", Name: "iCable", ListenPort: 1083, RouteID: 3}}}
}
func managedFixture() ManagedProfile {
	return ManagedProfile{SchemaVersion: 4, Kind: ManagedKind, ProfileID: "tunnel-test", Revision: "r1", DisplayName: "Shared six-path tunnel", Config: validConfig()}
}
func TestConfigContractAndSelection(t *testing.T) {
	c := validConfig()
	b, _ := json.Marshal(c)
	decoded, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	one, err := decoded.Select([]string{"hkt"})
	if err != nil || len(one.Inlets) != 1 || one.Inlets[0].RouteID != 2 {
		t.Fatalf("selection: %+v %v", one, err)
	}
	if len(decoded.Inlets) != 3 {
		t.Fatal("selection mutated original configuration")
	}
	if _, err = decoded.Select([]string{"hkbn", "hkbn"}); err == nil {
		t.Fatal("duplicate selection accepted")
	}
	if _, err = decoded.Select([]string{"unknown"}); err == nil {
		t.Fatal("unknown selection accepted")
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Kind = "bundle" }, func(c *Config) { c.SchemaVersion = 3 },
		func(c *Config) { c.Tunnel.TransportKey = "secret" }, func(c *Config) { c.Inlets[1].ListenPort = 1081 },
		func(c *Config) { c.Inlets[1].RouteID = 1 }, func(c *Config) { c.Inlets[1].ID = "hkbn" },
		func(c *Config) { c.Inlets[1].ID = "../bad" }, func(c *Config) { c.Inlets[1].Weight = 17 },
		func(c *Config) { c.Tunnel.Relays[1] = c.Tunnel.Relays[0] },
		func(c *Config) { c.Tunnel.Relays[0].Host = "0.0.0.0" },
		func(c *Config) { c.Tunnel.Relays[0].Host = "localhost"; c.Tunnel.Relays[0].Port = 1081 },
		func(c *Config) { c.Tunnel.SchedulerMode = "weighted" },
		func(c *Config) { no := false; c.Inlets[0].TCPEnabled = &no },
	} {
		bad := validConfig()
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("invalid configuration accepted: %+v", bad)
		}
	}
	c = validConfig()
	no := false
	c.Inlets[0].TCPEnabled = &no
	c.Inlets[0].UOTEnabled = true
	if err := c.Validate(); err != nil {
		t.Fatalf("UDP-only tunnel inlet rejected: %v", err)
	}
	c.Tunnel.Relays[0].Host = "relay.example.invalid"
	c.Tunnel.Relays[1].Host = "::1"
	if err := c.Validate(); err != nil {
		t.Fatalf("DNS/IPv6 relay rejected: %v", err)
	}
}
func TestStrictConfigDecoding(t *testing.T) {
	b, _ := json.Marshal(validConfig())
	cases := [][]byte{
		append(append([]byte(nil), b...), []byte(" {}")...),
		[]byte(strings.Repeat(" ", MaxDocumentBytes+1)),
		[]byte(`{"schema_version":3,"mode":"userspace_multipath","listen_port":1081}`),
		bytes.Replace(b, []byte(`"schema_version":1`), []byte(`"schema_version":1,"unexpected":true`), 1),
	}
	for _, b := range cases {
		if _, err := Decode(bytes.NewReader(b)); err == nil {
			t.Fatal("invalid/legacy document accepted")
		}
	}
}

const testAPIToken = "2222222222222222222222222222222222222222222222222222222222222222"

func encryptedTestProfile(t *testing.T) []byte {
	t.Helper()
	raw, _ := hex.DecodeString(testAPIToken)
	m := hmac.New(sha256.New, raw)
	m.Write([]byte("mpx-provision-config-envelope-v1"))
	block, err := aes.NewCipher(m.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	plain, _ := json.Marshal(managedFixture())
	sealed := gcm.Seal(nil, nonce, plain, []byte("mpx-provision-envelope-v1"))
	b, _ := json.Marshal(map[string]any{"v": 1, "n": base64.RawURLEncoding.EncodeToString(nonce), "d": base64.RawURLEncoding.EncodeToString(sealed)})
	return b
}
func TestProvisioningEnvelopeAndNonceValidation(t *testing.T) {
	endpoint := "https://example.invalid/v1/tunnel/" + testAPIToken
	data := encryptedTestProfile(t)
	p, err := DecodeResponse(data, endpoint)
	if err != nil || p.ProfileID != "tunnel-test" {
		t.Fatalf("decode: %v", err)
	}
	var envelope map[string]any
	json.Unmarshal(data, &envelope)
	envelope["n"] = "AA"
	bad, _ := json.Marshal(envelope)
	if _, err = DecodeResponse(bad, endpoint); err == nil {
		t.Fatal("invalid nonce accepted")
	}
	if strings.Contains(err.Error(), testAPIToken) {
		t.Fatal("error leaked bearer URL")
	}
	plain, _ := json.Marshal(managedFixture())
	if _, err = DecodeResponse(plain, endpoint); err == nil {
		t.Fatal("plaintext remote document accepted")
	}
	for _, endpoint := range []string{"http://example.invalid/v1/tunnel/" + testAPIToken, "https://user:password@example.invalid/" + testAPIToken, "https://example.invalid/" + testAPIToken + "?secret=yes", "https://example.invalid/not-a-token"} {
		if _, err := ValidateEndpoint(endpoint); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}
func TestProvisioningFetchNeverFollowsCredentialRedirect(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write(encryptedTestProfile(t)) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/"+testAPIToken, http.StatusFound)
	}))
	defer redirect.Close()
	_, err := Fetch(context.Background(), redirect.URL+"/v1/tunnel/"+testAPIToken)
	if err == nil || calls.Load() != 0 || strings.Contains(err.Error(), testAPIToken) {
		t.Fatalf("redirect/token policy failed: %v calls=%d", err, calls.Load())
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(encryptedTestProfile(t)) }))
	defer server.Close()
	if p, err := Fetch(context.Background(), server.URL+"/v1/tunnel/"+testAPIToken); err != nil || len(p.Config.Inlets) != 3 {
		t.Fatalf("fetch failed: %v", err)
	}
}
func FuzzConfigDecode(f *testing.F) {
	b, _ := json.Marshal(validConfig())
	f.Add(b)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema_version":3}`))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = Decode(bytes.NewReader(b)) })
}
