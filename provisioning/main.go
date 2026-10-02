package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/index.html
var webFS embed.FS

type relay struct {
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	DownloadMbps *float64 `json:"download_mbps,omitempty"`
	UploadMbps   *float64 `json:"upload_mbps,omitempty"`
}

type provisionConfig struct {
	Mode               string  `json:"mode"`
	ListenPort         int     `json:"listen_port"`
	SchedulerMode      string  `json:"scheduler_mode,omitempty"`
	TCPEnabled         bool    `json:"tcp_enabled"`
	UDPEnabled         bool    `json:"udp_enabled"`
	BackgroundResident bool    `json:"background_resident"`
	TransportKey       string  `json:"transport_key,omitempty"`
	Relays             []relay `json:"relays"`
}

type record struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Token     string          `json:"token"`
	Revision  uint64          `json:"revision"`
	UpdatedAt time.Time       `json:"updated_at"`
	Config    provisionConfig `json:"config"`
}

type publicPayload struct {
	SchemaVersion      int     `json:"schema_version"`
	Revision           string  `json:"revision"`
	DisplayName        string  `json:"display_name"`
	Mode               string  `json:"mode"`
	ListenPort         int     `json:"listen_port"`
	SchedulerMode      string  `json:"scheduler_mode,omitempty"`
	TCPEnabled         bool    `json:"tcp_enabled"`
	UDPEnabled         bool    `json:"udp_enabled"`
	BackgroundResident bool    `json:"background_resident"`
	TransportKey       string  `json:"transport_key,omitempty"`
	Relays             []relay `json:"relays"`
}

type adminRecord struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Revision  uint64          `json:"revision"`
	UpdatedAt time.Time       `json:"updated_at"`
	APIURL    string          `json:"api_url"`
	Config    provisionConfig `json:"config"`
}

type adminInput struct {
	Name   string          `json:"name"`
	Config provisionConfig `json:"config"`
}

type store struct {
	mu      sync.RWMutex
	path    string
	records map[string]record
}

func newStore(path string) (*store, error) {
	s := &store{path: path, records: map[string]record{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("secure data file permissions: %w", err)
	}
	var rows []record
	if err := json.Unmarshal(data, &rows); err != nil {
		return fmt.Errorf("decode data file: %w", err)
	}
	for _, r := range rows {
		if r.ID == "" || r.Token == "" {
			return errors.New("data file contains record without id/token")
		}
		s.records[r.ID] = r
	}
	return nil
}

func (s *store) saveLocked() error {
	rows := make([]record, 0, len(s.records))
	for _, r := range s.records {
		rows = append(rows, r)
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func normalizeConfig(cfg provisionConfig) provisionConfig {
	for i := range cfg.Relays {
		cfg.Relays[i].Host = strings.TrimSpace(cfg.Relays[i].Host)
	}
	if cfg.Mode == "native_mptcp" {
		cfg.SchedulerMode = ""
		cfg.TransportKey = ""
	}
	return cfg
}

func (s *store) create(name string, cfg provisionConfig) (record, error) {
	cfg = normalizeConfig(cfg)
	if err := validateInput(name, cfg); err != nil {
		return record{}, err
	}
	id, err := randomHex(8)
	if err != nil {
		return record{}, err
	}
	token, err := randomHex(32)
	if err != nil {
		return record{}, err
	}
	now := time.Now().UTC()
	r := record{ID: id, Name: strings.TrimSpace(name), Token: token, Revision: 1, UpdatedAt: now, Config: cfg}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[id] = r
	if err := s.saveLocked(); err != nil {
		delete(s.records, id)
		return record{}, err
	}
	return r, nil
}

func (s *store) update(id, name string, cfg provisionConfig) (record, error) {
	cfg = normalizeConfig(cfg)
	if err := validateInput(name, cfg); err != nil {
		return record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return record{}, os.ErrNotExist
	}
	old := r
	r.Name = strings.TrimSpace(name)
	r.Config = cfg
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	s.records[id] = r
	if err := s.saveLocked(); err != nil {
		s.records[id] = old
		return record{}, err
	}
	return r, nil
}

func (s *store) rotate(id string) (record, error) {
	token, err := randomHex(32)
	if err != nil {
		return record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return record{}, os.ErrNotExist
	}
	old := r
	r.Token = token
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	s.records[id] = r
	if err := s.saveLocked(); err != nil {
		s.records[id] = old
		return record{}, err
	}
	return r, nil
}

func (s *store) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return os.ErrNotExist
	}
	delete(s.records, id)
	if err := s.saveLocked(); err != nil {
		s.records[id] = r
		return err
	}
	return nil
}

func (s *store) byToken(token string) (record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.records {
		if subtle.ConstantTimeCompare([]byte(r.Token), []byte(token)) == 1 {
			return r, true
		}
	}
	return record{}, false
}

func (s *store) list() []record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]record, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func validMbps(v *float64) bool {
	if v == nil {
		return true
	}
	if math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0.1 || *v > 6553.5 {
		return false
	}
	return math.Abs(math.Round(*v*10)-*v*10) < 0.000001
}

func validateInput(name string, cfg provisionConfig) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]byte(name)) > 128 {
		return errors.New("name must be 1-128 UTF-8 bytes")
	}
	if cfg.Mode != "userspace_multipath" && cfg.Mode != "native_mptcp" {
		return errors.New("mode must be userspace_multipath or native_mptcp")
	}
	if cfg.ListenPort < 1024 || cfg.ListenPort > 65535 {
		return errors.New("listen_port must be 1024-65535")
	}
	if len(cfg.Relays) < 2 || len(cfg.Relays) > 8 {
		return errors.New("relays must contain 2-8 entries")
	}
	if !cfg.TCPEnabled && !cfg.UDPEnabled {
		return errors.New("TCP and UDP cannot both be disabled")
	}
	if cfg.Mode == "native_mptcp" && !cfg.TCPEnabled {
		return errors.New("native_mptcp requires TCP")
	}
	if cfg.Mode == "userspace_multipath" {
		if len(cfg.TransportKey) != 64 {
			return errors.New("transport_key must contain 64 hexadecimal characters")
		}
		if _, err := hex.DecodeString(cfg.TransportKey); err != nil {
			return errors.New("transport_key must contain 64 hexadecimal characters")
		}
		switch cfg.SchedulerMode {
		case "auto", "aggregate", "protect", "weighted":
		default:
			return errors.New("invalid scheduler_mode")
		}
	}
	seen := map[string]bool{}
	for i, r := range cfg.Relays {
		ip := net.ParseIP(strings.TrimSpace(r.Host))
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("relay %d must use IPv4", i+1)
		}
		first := ip.To4()[0]
		if first >= 224 || r.Host == "0.0.0.0" {
			return fmt.Errorf("relay %d must use unicast IPv4", i+1)
		}
		if r.Port < 1 || r.Port > 65535 {
			return fmt.Errorf("relay %d port invalid", i+1)
		}
		identity := net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
		if cfg.Mode == "native_mptcp" {
			identity = r.Host
		}
		if seen[identity] {
			return fmt.Errorf("relay %d duplicates another relay", i+1)
		}
		seen[identity] = true
		if strings.HasPrefix(r.Host, "127.") && r.Port == cfg.ListenPort {
			return fmt.Errorf("relay %d points to local listen port", i+1)
		}
		if !validMbps(r.DownloadMbps) || !validMbps(r.UploadMbps) {
			return fmt.Errorf("relay %d capacity must be 0.1-6553.5 Mbps with at most one decimal", i+1)
		}
		if cfg.Mode == "userspace_multipath" && cfg.SchedulerMode == "weighted" && r.DownloadMbps == nil {
			return fmt.Errorf("relay %d download_mbps required for weighted", i+1)
		}
	}
	return nil
}

type app struct {
	store      *store
	adminUser  string
	adminPass  string
	publicBase string
}

func (a *app) authOK(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(user), []byte(a.adminUser)) == 1 && subtle.ConstantTimeCompare([]byte(pass), []byte(a.adminPass)) == 1
}

func (a *app) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authOK(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="MPX Provisioning"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (a *app) baseURL(r *http.Request) string {
	if a.publicBase != "" {
		return strings.TrimRight(a.publicBase, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (a *app) adminView(r *http.Request, rec record) adminRecord {
	return adminRecord{ID: rec.ID, Name: rec.Name, Revision: rec.Revision, UpdatedAt: rec.UpdatedAt, APIURL: a.baseURL(r) + "/v1/config/" + rec.Token, Config: rec.Config}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, v any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func (a *app) publicConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/v1/config/")
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	rec, ok := a.store.byToken(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p := publicPayload{
		SchemaVersion: 1, Revision: fmt.Sprintf("r%d-%s", rec.Revision, rec.UpdatedAt.Format("20060102T150405Z")), DisplayName: rec.Name,
		Mode: rec.Config.Mode, ListenPort: rec.Config.ListenPort, SchedulerMode: rec.Config.SchedulerMode,
		TCPEnabled: rec.Config.TCPEnabled, UDPEnabled: rec.Config.UDPEnabled, BackgroundResident: rec.Config.BackgroundResident,
		TransportKey: rec.Config.TransportKey, Relays: rec.Config.Relays,
	}
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, http.StatusOK, p)
}

func (a *app) adminPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	_, _ = w.Write(data)
}

func (a *app) adminProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows := a.store.list()
		out := make([]adminRecord, 0, len(rows))
		for _, rec := range rows {
			out = append(out, a.adminView(r, rec))
		}
		writeJSON(w, 200, out)
	case http.MethodPost:
		var in adminInput
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		rec, err := a.store.create(in.Name, in.Config)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, 201, a.adminView(r, rec))
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (a *app) adminProfileByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/api/profiles/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "rotate" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		rec, err := a.store.rotate(id)
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, 200, a.adminView(r, rec))
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var in adminInput
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		rec, err := a.store.update(id, in.Name, in.Config)
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, 200, a.adminView(r, rec))
	case http.MethodDelete:
		if err := a.store.delete(id); errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/v1/config/", a.publicConfig)
	mux.HandleFunc("/admin", a.requireAdmin(a.adminPage))
	mux.HandleFunc("/admin/", a.requireAdmin(a.adminPage))
	mux.HandleFunc("/admin/api/profiles", a.requireAdmin(a.adminProfiles))
	mux.HandleFunc("/admin/api/profiles/", a.requireAdmin(a.adminProfileByID))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		mux.ServeHTTP(w, r)
	})
}

func validatePublicBase(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("MPX_PROVISION_PUBLIC_BASE_URL must be an absolute URL without userinfo or fragment")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return errors.New("MPX_PROVISION_PUBLIC_BASE_URL must use HTTPS except for loopback development")
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func main() {
	listen := getenv("MPX_PROVISION_LISTEN", "127.0.0.1:8088")
	user := getenv("MPX_PROVISION_ADMIN_USER", "admin")
	pass := os.Getenv("MPX_PROVISION_ADMIN_PASSWORD")
	if len(pass) < 24 {
		log.Fatal("MPX_PROVISION_ADMIN_PASSWORD must be at least 24 characters")
	}
	publicBase := strings.TrimRight(strings.TrimSpace(os.Getenv("MPX_PROVISION_PUBLIC_BASE_URL")), "/")
	if err := validatePublicBase(publicBase); err != nil {
		log.Fatal(err)
	}
	dataPath := getenv("MPX_PROVISION_DATA", "./provisioning-data/profiles.json")
	st, err := newStore(dataPath)
	if err != nil {
		log.Fatal(err)
	}
	a := &app{store: st, adminUser: user, adminPass: pass, publicBase: publicBase}
	srv := &http.Server{Addr: listen, Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	log.Printf("MPX Provisioning listening on %s (admin /admin)", listen)
	log.Fatal(srv.ListenAndServe())
}
