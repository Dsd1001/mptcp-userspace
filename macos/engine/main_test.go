package main

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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func validProfile() Config {
	return Config{SchemaVersion: 3, Mode: "userspace_multipath", ListenPort: 1081, TransportKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Relays: []Relay{{Host: "192.0.2.1", Port: 21001}, {Host: "192.0.2.2", Port: 21002}}}
}
func TestProfileValidation(t *testing.T) {
	c := validProfile()
	if err := c.validateForOS("darwin"); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*Config){
		func(c *Config) { c.SchemaVersion = 1 },
		func(c *Config) { c.Mode = "socks5" },
		func(c *Config) { c.Mode = "native_mptcp" },
		func(c *Config) { c.SchemaVersion = 2; c.Mode = "tcp_forward" },
		func(c *Config) { c.ListenPort = 80 },
		func(c *Config) { c.Relays = []Relay{c.Relays[0], c.Relays[0]} },
		func(c *Config) { c.Relays = []Relay{{Host: "127.0.0.1", Port: 1081}, {Host: "192.0.2.2", Port: 21002}} },
	} {
		invalid := validProfile()
		modify(&invalid)
		if invalid.validateForOS("darwin") == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
func TestStrictConfig(t *testing.T) {
	c := bundleProfile("strict", "Strict", 1081).config()
	raw, _ := json.Marshal(c)
	if _, err := readConfig(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		string(raw) + "{}",
		strings.TrimSuffix(string(raw), "}") + ",\"tls_name\":\"old-backend\"}",
		strings.Repeat(" ", 32769),
		"{\"listen_port\":1080,\"relays\":[]}",
	} {
		if _, err := readConfig(strings.NewReader(data)); err == nil {
			t.Fatal("old or malformed config accepted")
		}
	}
}

func bundleProfile(id, name string, port int) BundleProfile {
	return BundleProfile{
		SchemaVersion: 1, ProfileID: id, Revision: "r1-test", DisplayName: name,
		Mode: "userspace_multipath", ListenPort: port, SchedulerMode: "auto",
		TCPEnabled: true, UDPEnabled: false, TransportKey: strings.Repeat("a", 64),
		Relays: []Relay{{Host: "192.0.2.10", Port: 8849}, {Host: "198.51.100.20", Port: 8849}},
	}
}

func TestBundleValidationAndSelection(t *testing.T) {
	b := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "bundle-1", Revision: "r1", DisplayName: "Main", Mode: "parallel", Profiles: []BundleProfile{bundleProfile("a", "A", 1081), bundleProfile("b", "B", 1082)}}
	if err := b.validate(); err != nil {
		t.Fatal(err)
	}
	selected, err := b.selected(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].ProfileID != "a" || selected[1].ProfileID != "b" {
		t.Fatalf("default parallel selection=%+v", selected)
	}
	selected, err = b.selected([]string{"b"})
	if err != nil || len(selected) != 1 || selected[0].ListenPort != 1082 {
		t.Fatalf("explicit selection=%+v err=%v", selected, err)
	}
	if _, err := b.selected([]string{"missing"}); err == nil {
		t.Fatal("accepted unknown profile")
	}
	if _, err := b.selected([]string{"a", "a"}); err == nil {
		t.Fatal("accepted duplicate selection")
	}

	conflict := b
	conflict.Profiles = append([]BundleProfile(nil), b.Profiles...)
	conflict.Profiles[1].ListenPort = 1081
	if err := conflict.validate(); err == nil {
		t.Fatal("parallel bundle accepted duplicate listen_port")
	}
	conflict.Mode = "single_select"
	if err := conflict.validate(); err != nil {
		t.Fatalf("single-select should allow shared ports: %v", err)
	}
	if _, err := conflict.selected([]string{"a", "b"}); err == nil {
		t.Fatal("single-select accepted multiple active profiles")
	}
	one, err := conflict.selected([]string{"b"})
	if err != nil || len(one) != 1 {
		t.Fatalf("single-select explicit profile failed: %v", err)
	}
}

func TestStrictBundleJSON(t *testing.T) {
	b := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "bundle-1", Revision: "r1", DisplayName: "Main", Mode: "single_select", Profiles: []BundleProfile{bundleProfile("a", "A", 1081)}}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := readBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.BundleID != b.BundleID {
		t.Fatal("bundle id changed")
	}
	withUnknown := strings.TrimSuffix(string(raw), "}") + ",\"unexpected\":true}"
	if _, err := readBundle(strings.NewReader(withUnknown)); err == nil {
		t.Fatal("bundle accepted unknown field")
	}
	if _, err := readBundle(strings.NewReader(strings.Repeat(" ", 524289))); err == nil {
		t.Fatal("oversized bundle accepted")
	}
}

func freeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestBundlePortPreflightIsAtomic(t *testing.T) {
	p1 := bundleProfile("a", "A", freeTestPort(t))
	p2 := bundleProfile("b", "B", freeTestPort(t))
	if err := preflightBundlePorts([]BundleProfile{p1, p2}); err != nil {
		t.Fatalf("free ports rejected: %v", err)
	}

	occupied, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(p2.ListenPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err := preflightBundlePorts([]BundleProfile{p1, p2}); err == nil {
		t.Fatal("occupied parallel port accepted")
	}

	// The first port was only probed and released; no Profile process was started.
	probe, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(p1.ListenPort)))
	if err != nil {
		t.Fatalf("preflight leaked first port: %v", err)
	}
	_ = probe.Close()
}

func TestManagedProvisioningFetchProfileAndBundle(t *testing.T) {
	profile := bundleProfile("", "Single", 1081)
	profile.ProfileID = ""
	bundle := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "bundle-1", Revision: "r1", DisplayName: "Main", Mode: "parallel", Profiles: []BundleProfile{bundleProfile("a", "A", 1081), bundleProfile("b", "B", 1082)}}
	mux := http.NewServeMux()
	mux.HandleFunc("/profile", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(profile) })
	mux.HandleFunc("/bundle", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(bundle) })
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/bundle", http.StatusFound) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	doc, err := fetchManaged(ctx, srv.URL+"/profile")
	if err != nil {
		t.Fatal(err)
	}
	if doc.profile == nil || doc.bundle != nil || doc.profile.DisplayName != "Single" {
		t.Fatalf("bad profile doc: %+v", doc)
	}
	doc, err = fetchManaged(ctx, srv.URL+"/bundle")
	if err != nil {
		t.Fatal(err)
	}
	if doc.bundle == nil || doc.profile != nil || len(doc.bundle.Profiles) != 2 {
		t.Fatalf("bad bundle doc: %+v", doc)
	}
	if _, err := doc.bundle.selected([]string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchManaged(ctx, srv.URL+"/redirect"); err == nil {
		t.Fatal("redirect was followed")
	}
}

func encryptedManagedWire(t *testing.T, token string, payload any) []byte {
	t.Helper()
	plain, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, raw)
	_, _ = mac.Write([]byte("mpx-provision-config-envelope-v1"))
	key := mac.Sum(nil)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{0x5a}, gcm.NonceSize())
	sealed := gcm.Seal(nil, nonce, plain, []byte("mpx-provision-envelope-v1"))
	wire, err := json.Marshal(managedEncryptedEnvelope{Version: 1, Nonce: base64.RawURLEncoding.EncodeToString(nonce), Data: base64.RawURLEncoding.EncodeToString(sealed)})
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestManagedProvisioningFetchEncryptedBundleAndPlaintextCompatibility(t *testing.T) {
	token := strings.Repeat("ab", 32)
	bundle := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "enc", Revision: "r1", DisplayName: "Encrypted", Mode: "parallel", Profiles: []BundleProfile{bundleProfile("a", "A", 1081), bundleProfile("b", "B", 1082)}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/bundle/"+token, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(encryptedManagedWire(t, token, bundle)) })
	mux.HandleFunc("/legacy", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(bundle) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	doc, err := fetchManaged(ctx, srv.URL+"/v1/bundle/"+token)
	if err != nil {
		t.Fatal(err)
	}
	if doc.bundle == nil || doc.bundle.DisplayName != "Encrypted" {
		t.Fatalf("bad encrypted doc: %+v", doc)
	}
	doc, err = fetchManaged(ctx, srv.URL+"/legacy")
	if err != nil {
		t.Fatal(err)
	}
	if doc.bundle == nil || len(doc.bundle.Profiles) != 2 {
		t.Fatalf("legacy plaintext broke: %+v", doc)
	}
}

func TestManagedEncryptedEnvelopeRejectsWrongURLSecret(t *testing.T) {
	good := strings.Repeat("ab", 32)
	bad := strings.Repeat("cd", 32)
	payload := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "enc", Revision: "r1", DisplayName: "Encrypted", Mode: "single_select", Profiles: []BundleProfile{bundleProfile("a", "A", 1081)}}
	wire := encryptedManagedWire(t, good, payload)
	u, _ := url.Parse("https://cfg.example.test/v1/bundle/" + bad)
	if _, err := decryptManagedEnvelope(wire, u); err == nil || !strings.Contains(err.Error(), "解密失败") {
		t.Fatalf("wrong secret err=%v", err)
	}
}

func TestManagedInputAndURLPolicy(t *testing.T) {
	for _, good := range []string{"https://cfg.example.test/v1/bundle/x/y", "http://127.0.0.1:8088/v1/config/x", "http://localhost:8088/v1/config/x"} {
		if _, err := validateProvisioningURL(good); err != nil {
			t.Fatalf("%s rejected: %v", good, err)
		}
	}
	for _, bad := range []string{"http://cfg.example.test/x", "https://u:p@cfg.example.test/x", "https://cfg.example.test/x#secret"} {
		if _, err := validateProvisioningURL(bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	input := `{"url":"https://cfg.example.test/v1/bundle/x/y","profile_ids":["a","b"]}`
	got, err := readManagedInput(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ProfileIDs) != 2 {
		t.Fatal("profile ids lost")
	}
	if _, err := readManagedInput(strings.NewReader(`{"url":"https://cfg.example.test/x","profile_ids":["a","a"]}`)); err == nil {
		t.Fatal("duplicate profile ids accepted")
	}
}

func TestBundleFakeChildProcess(t *testing.T) {
	if os.Getenv("MPTCP_BUNDLE_HELPER") != "1" {
		return
	}
	switch os.Getenv("MPTCP_BUNDLE_HELPER_KIND") {
	case "good":
		fmt.Println(`{"kind":"listening","paths":1,"connections":0,"sent":0,"received":0}`)
		for {
			time.Sleep(time.Hour)
		}
	case "bad":
		fmt.Println(`{"kind":"error","message":"synthetic relay unreachable"}`)
		os.Exit(1)
	case "flap":
		fmt.Println(`{"kind":"listening","paths":1,"connections":0,"sent":0,"received":0}`)
		time.Sleep(25 * time.Millisecond)
		os.Exit(1)
	default:
		fmt.Println(`{"kind":"error","message":"unknown helper mode"}`)
		os.Exit(2)
	}
}

func fakeBundleLauncher(modes map[string]string) bundleChildLauncher {
	return func(ctx context.Context, _ string, profile BundleProfile, _ []byte) (*exec.Cmd, io.ReadCloser, error) {
		mode, ok := modes[profile.ProfileID]
		if !ok {
			return nil, nil, errors.New("no fake mode for profile")
		}
		if mode == "START_ERROR" {
			return nil, nil, errors.New("synthetic start failure")
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBundleFakeChildProcess$")
		cmd.Env = append(os.Environ(), "MPTCP_BUNDLE_HELPER=1", "MPTCP_BUNDLE_HELPER_KIND="+mode)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, err
		}
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		return cmd, stdout, nil
	}
}

type scriptedBundleLauncherState struct {
	mu      sync.Mutex
	scripts map[string][]string
	counts  map[string]int
}

func newScriptedBundleLauncher(scripts map[string][]string) (*scriptedBundleLauncherState, bundleChildLauncher) {
	state := &scriptedBundleLauncherState{scripts: scripts, counts: map[string]int{}}
	launcher := func(ctx context.Context, _ string, profile BundleProfile, _ []byte) (*exec.Cmd, io.ReadCloser, error) {
		state.mu.Lock()
		script := state.scripts[profile.ProfileID]
		index := state.counts[profile.ProfileID]
		state.counts[profile.ProfileID] = index + 1
		state.mu.Unlock()
		if len(script) == 0 {
			return nil, nil, errors.New("no scripted fake mode for profile")
		}
		if index >= len(script) {
			index = len(script) - 1
		}
		mode := script[index]
		if mode == "START_ERROR" {
			return nil, nil, errors.New("synthetic start failure")
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBundleFakeChildProcess$")
		cmd.Env = append(os.Environ(), "MPTCP_BUNDLE_HELPER=1", "MPTCP_BUNDLE_HELPER_KIND="+mode)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, err
		}
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		return cmd, stdout, nil
	}
	return state, launcher
}

func (s *scriptedBundleLauncherState) count(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[id]
}

func waitForBundleTest(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

func fastBundleRetryDelay(int) time.Duration { return 15 * time.Millisecond }

func TestBundleProfileRetrySchedule(t *testing.T) {
	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		5 * time.Second,
		10 * time.Second,
		30 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for i, expected := range want {
		if got := bundleProfileRetryDelay(i); got != expected {
			t.Fatalf("retry %d=%s want %s", i, got, expected)
		}
	}
	if got := bundleProfileRetryDelay(-1); got != time.Second {
		t.Fatalf("negative retry delay=%s", got)
	}
}

func TestParallelBundleKeepsHealthyProfileRunningWhenPeerFails(t *testing.T) {
	p1 := bundleProfile("good", "Good", freeTestPort(t))
	p2 := bundleProfile("bad", "Bad", freeTestPort(t))
	b := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "bundle-resilient", Revision: "r1", DisplayName: "Resilient", Mode: "parallel", Profiles: []BundleProfile{p1, p2}}
	launcher := fakeBundleLauncher(map[string]string{
		"good": "good",
		"bad":  "bad",
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runBundleWithLauncher(ctx, b, nil, launcher) }()
	select {
	case err := <-done:
		t.Fatalf("healthy Profile was stopped by failed peer: %v", err)
	case <-time.After(700 * time.Millisecond):
		// Expected: the healthy child remains alive after the bad child exits.
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bundle did not stop after cancellation")
	}
}

func TestParallelBundleAllDownRecoversWithoutRestartingBundle(t *testing.T) {
	p1 := bundleProfile("a", "A", freeTestPort(t))
	p2 := bundleProfile("b", "B", freeTestPort(t))
	b := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "bundle-all-recover", Revision: "r1", DisplayName: "All Recover", Mode: "parallel", Profiles: []BundleProfile{p1, p2}}
	state, launcher := newScriptedBundleLauncher(map[string][]string{
		"a": {"bad", "good"},
		"b": {"bad", "good"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runParallelBundleWithLauncherRetry(ctx, b, nil, launcher, fastBundleRetryDelay) }()
	waitForBundleTest(t, 2*time.Second, func() bool {
		return state.count("a") >= 2 && state.count("b") >= 2
	}, "all-down Profiles did not retry and recover")
	select {
	case err := <-done:
		t.Fatalf("parallel supervisor exited while recovered children should remain alive: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parallel supervisor did not stop after cancellation")
	}
}

func TestParallelBundleFailedPeerRecoversWithoutRestartingHealthyPeer(t *testing.T) {
	good := bundleProfile("good", "Good", freeTestPort(t))
	recovering := bundleProfile("recover", "Recover", freeTestPort(t))
	b := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "bundle-peer-recover", Revision: "r1", DisplayName: "Peer Recover", Mode: "parallel", Profiles: []BundleProfile{good, recovering}}
	state, launcher := newScriptedBundleLauncher(map[string][]string{
		"good":    {"good"},
		"recover": {"bad", "good"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runParallelBundleWithLauncherRetry(ctx, b, nil, launcher, fastBundleRetryDelay) }()
	waitForBundleTest(t, 2*time.Second, func() bool { return state.count("recover") >= 2 }, "failed peer did not retry")
	if got := state.count("good"); got != 1 {
		t.Fatalf("healthy peer restarted %d times; want exactly 1 launch", got)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result=%v", err)
	}
}

func TestParallelBundleRuntimeCrashReconnects(t *testing.T) {
	p := bundleProfile("flap", "Flap", freeTestPort(t))
	b := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "bundle-flap", Revision: "r1", DisplayName: "Flap", Mode: "parallel", Profiles: []BundleProfile{p}}
	state, launcher := newScriptedBundleLauncher(map[string][]string{
		"flap": {"flap", "good"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runParallelBundleWithLauncherRetry(ctx, b, nil, launcher, fastBundleRetryDelay) }()
	waitForBundleTest(t, 2*time.Second, func() bool { return state.count("flap") >= 2 }, "runtime crash did not reconnect")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result=%v", err)
	}
}

func TestParallelBundlePermanentFailureUsesBackoffNotBusyLoop(t *testing.T) {
	p := bundleProfile("bad", "Bad", freeTestPort(t))
	b := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "bundle-backoff", Revision: "r1", DisplayName: "Backoff", Mode: "parallel", Profiles: []BundleProfile{p}}
	state, launcher := newScriptedBundleLauncher(map[string][]string{
		"bad": {"START_ERROR"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	const delay = 30 * time.Millisecond
	go func() {
		done <- runParallelBundleWithLauncherRetry(ctx, b, nil, launcher, func(int) time.Duration { return delay })
	}()
	time.Sleep(125 * time.Millisecond)
	attempts := state.count("bad")
	if attempts < 3 || attempts > 6 {
		t.Fatalf("permanent failure attempts=%d; expected bounded retries around 30ms cadence", attempts)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result=%v", err)
	}
}

func TestParallelBundleStartFailureDoesNotStopStartedPeer(t *testing.T) {
	p1 := bundleProfile("good", "Good", freeTestPort(t))
	p2 := bundleProfile("start-bad", "Start Bad", freeTestPort(t))
	b := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "bundle-start-failure", Revision: "r1", DisplayName: "Start Failure", Mode: "parallel", Profiles: []BundleProfile{p1, p2}}
	launcher := fakeBundleLauncher(map[string]string{
		"good":      "good",
		"start-bad": "START_ERROR",
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runBundleWithLauncher(ctx, b, nil, launcher) }()
	select {
	case err := <-done:
		t.Fatalf("start failure stopped healthy peer: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result=%v", err)
	}
}

func TestSingleSelectStillFailsWhenSelectedProfileFails(t *testing.T) {
	p := bundleProfile("only", "Only", freeTestPort(t))
	b := BundlePayload{SchemaVersion: 2, Kind: "bundle", BundleID: "bundle-single", Revision: "r1", DisplayName: "Single", Mode: "single_select", Profiles: []BundleProfile{p}}
	launcher := fakeBundleLauncher(map[string]string{"only": "bad"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := runBundleWithLauncher(ctx, b, []string{"only"}, launcher)
	if err == nil || !strings.Contains(err.Error(), "所有 1 个所选 Profile 均不可用") {
		t.Fatalf("single-select err=%v", err)
	}
}
