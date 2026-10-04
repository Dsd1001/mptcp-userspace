package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/index.html
var webFS embed.FS

var (
	Version  = "dev"
	SourceID = "unbound"
)

var apiAliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

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
	APIAlias  string          `json:"api_alias,omitempty"`
	Revision  uint64          `json:"revision"`
	UpdatedAt time.Time       `json:"updated_at"`
	Config    provisionConfig `json:"config"`
}

type encryptedEnvelope struct {
	Version int    `json:"v"`
	Nonce   string `json:"n"`
	Data    string `json:"d"`
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
	APIAlias  string          `json:"api_alias,omitempty"`
	Config    provisionConfig `json:"config"`
}

type adminInput struct {
	Name     string          `json:"name"`
	APIAlias *string         `json:"api_alias,omitempty"`
	Config   provisionConfig `json:"config"`
}

type bundleRecord struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Token      string    `json:"token"`
	APIAlias   string    `json:"api_alias,omitempty"`
	Mode       string    `json:"mode"`
	ProfileIDs []string  `json:"profile_ids"`
	Revision   uint64    `json:"revision"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type bundleInput struct {
	Name       string   `json:"name"`
	APIAlias   *string  `json:"api_alias,omitempty"`
	Mode       string   `json:"mode"`
	ProfileIDs []string `json:"profile_ids"`
}

type adminBundle struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Revision   uint64    `json:"revision"`
	UpdatedAt  time.Time `json:"updated_at"`
	APIURL     string    `json:"api_url"`
	APIAlias   string    `json:"api_alias,omitempty"`
	Mode       string    `json:"mode"`
	ProfileIDs []string  `json:"profile_ids"`
}

type bundlePublicProfile struct {
	SchemaVersion      int     `json:"schema_version"`
	ProfileID          string  `json:"profile_id"`
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

type bundlePublicPayload struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	BundleID      string                `json:"bundle_id"`
	Revision      string                `json:"revision"`
	DisplayName   string                `json:"display_name"`
	Mode          string                `json:"mode"`
	Profiles      []bundlePublicProfile `json:"profiles"`
}

type systemInfo struct {
	Component      string `json:"component"`
	Version        string `json:"version"`
	SourceID       string `json:"source_id"`
	APISchema      int    `json:"api_schema"`
	PasswordChange bool   `json:"password_change"`
}

type passwordInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type store struct {
	mu      sync.RWMutex
	path    string
	records map[string]record
}

type bundleStore struct {
	mu      sync.RWMutex
	path    string
	records map[string]bundleRecord
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
	aliases := map[string]bool{}
	tokens := map[string]bool{}
	for _, r := range rows {
		if r.ID == "" || r.Token == "" {
			return errors.New("data file contains record without id/token")
		}
		alias, err := normalizeAPIAlias(r.APIAlias)
		if err != nil {
			return fmt.Errorf("profile %s api_alias: %w", r.ID, err)
		}
		r.APIAlias = alias
		if alias != "" && aliases[alias] {
			return fmt.Errorf("duplicate api_alias %q in data file", alias)
		}
		if tokens[r.Token] {
			return errors.New("duplicate token in data file")
		}
		aliases[alias] = alias != ""
		tokens[r.Token] = true
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

func normalizeAPIAlias(raw string) (string, error) {
	alias := strings.ToLower(strings.TrimSpace(raw))
	if alias == "" {
		return "", nil
	}
	if !apiAliasPattern.MatchString(alias) {
		return "", errors.New("api_alias must be 1-64 lowercase letters, digits, hyphen or underscore and start with a letter or digit")
	}
	return alias, nil
}

func (s *store) aliasInUseLocked(alias, exceptID string) bool {
	if alias == "" {
		return false
	}
	for id, r := range s.records {
		if id != exceptID && r.APIAlias == alias {
			return true
		}
	}
	return false
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
	return s.createWithAlias(name, cfg, "")
}

func (s *store) createWithAlias(name string, cfg provisionConfig, rawAlias string) (record, error) {
	cfg = normalizeConfig(cfg)
	if err := validateInput(name, cfg); err != nil {
		return record{}, err
	}
	alias, err := normalizeAPIAlias(rawAlias)
	if err != nil {
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
	r := record{ID: id, Name: strings.TrimSpace(name), Token: token, APIAlias: alias, Revision: 1, UpdatedAt: now, Config: cfg}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aliasInUseLocked(alias, "") {
		return record{}, errors.New("api_alias is already in use")
	}
	s.records[id] = r
	if err := s.saveLocked(); err != nil {
		delete(s.records, id)
		return record{}, err
	}
	return r, nil
}

func (s *store) update(id, name string, cfg provisionConfig) (record, error) {
	return s.updateWithAlias(id, name, cfg, nil)
}

func (s *store) updateWithAlias(id, name string, cfg provisionConfig, rawAlias *string) (record, error) {
	cfg = normalizeConfig(cfg)
	if err := validateInput(name, cfg); err != nil {
		return record{}, err
	}
	var requestedAlias *string
	if rawAlias != nil {
		alias, err := normalizeAPIAlias(*rawAlias)
		if err != nil {
			return record{}, err
		}
		requestedAlias = &alias
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return record{}, os.ErrNotExist
	}
	old := r
	targetAlias := r.APIAlias
	if requestedAlias != nil {
		targetAlias = *requestedAlias
	}
	if s.aliasInUseLocked(targetAlias, id) {
		return record{}, errors.New("api_alias is already in use")
	}
	if targetAlias != r.APIAlias {
		// Changing URL identity automatically rotates the bearer secret so no
		// previously issued URL can become valid again after a later mode change.
		token, err := randomHex(32)
		if err != nil {
			return record{}, err
		}
		r.Token = token
		r.APIAlias = targetAlias
	}
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

func (s *store) byAccess(alias, token string) (record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.records {
		if r.APIAlias != alias {
			continue
		}
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

func (s *store) get(id string) (record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[id]
	return r, ok
}

func newBundleStore(path string) (*bundleStore, error) {
	s := &bundleStore{path: path, records: map[string]bundleRecord{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure bundle data file permissions: %w", err)
	}
	var rows []bundleRecord
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decode bundle data file: %w", err)
	}
	aliases, tokens := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		if r.ID == "" || r.Token == "" {
			return nil, errors.New("bundle data file contains record without id/token")
		}
		alias, err := normalizeAPIAlias(r.APIAlias)
		if err != nil {
			return nil, fmt.Errorf("bundle %s api_alias: %w", r.ID, err)
		}
		r.APIAlias = alias
		if alias != "" && aliases[alias] {
			return nil, fmt.Errorf("duplicate bundle api_alias %q", alias)
		}
		if tokens[r.Token] {
			return nil, errors.New("duplicate bundle token")
		}
		aliases[alias] = alias != ""
		tokens[r.Token] = true
		s.records[r.ID] = r
	}
	return s, nil
}

func (s *bundleStore) saveLocked() error {
	rows := make([]bundleRecord, 0, len(s.records))
	for _, r := range s.records {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name == rows[j].Name {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].Name < rows[j].Name
	})
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

func normalizeBundleInput(name, mode string, ids []string) (string, string, []string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]byte(name)) > 128 {
		return "", "", nil, errors.New("name must be 1-128 UTF-8 bytes")
	}
	mode = strings.TrimSpace(mode)
	if mode != "single_select" && mode != "parallel" {
		return "", "", nil, errors.New("mode must be single_select or parallel")
	}
	if len(ids) < 1 || len(ids) > 32 {
		return "", "", nil, errors.New("profile_ids must contain 1-32 entries")
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return "", "", nil, errors.New("profile_ids must be non-empty and unique")
		}
		seen[id] = true
		clean = append(clean, id)
	}
	return name, mode, clean, nil
}

func (s *bundleStore) aliasInUseLocked(alias, exceptID string) bool {
	if alias == "" {
		return false
	}
	for id, r := range s.records {
		if id != exceptID && r.APIAlias == alias {
			return true
		}
	}
	return false
}

func (s *bundleStore) create(name, mode string, ids []string, rawAlias string) (bundleRecord, error) {
	name, mode, ids, err := normalizeBundleInput(name, mode, ids)
	if err != nil {
		return bundleRecord{}, err
	}
	alias, err := normalizeAPIAlias(rawAlias)
	if err != nil {
		return bundleRecord{}, err
	}
	id, err := randomHex(8)
	if err != nil {
		return bundleRecord{}, err
	}
	token, err := randomHex(32)
	if err != nil {
		return bundleRecord{}, err
	}
	r := bundleRecord{ID: id, Name: name, Token: token, APIAlias: alias, Mode: mode, ProfileIDs: ids, Revision: 1, UpdatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aliasInUseLocked(alias, "") {
		return bundleRecord{}, errors.New("api_alias is already in use")
	}
	s.records[id] = r
	if err := s.saveLocked(); err != nil {
		delete(s.records, id)
		return bundleRecord{}, err
	}
	return r, nil
}

func (s *bundleStore) update(id, name, mode string, ids []string, rawAlias *string) (bundleRecord, error) {
	name, mode, ids, err := normalizeBundleInput(name, mode, ids)
	if err != nil {
		return bundleRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return bundleRecord{}, os.ErrNotExist
	}
	old := r
	target := r.APIAlias
	if rawAlias != nil {
		target, err = normalizeAPIAlias(*rawAlias)
		if err != nil {
			return bundleRecord{}, err
		}
	}
	if s.aliasInUseLocked(target, id) {
		return bundleRecord{}, errors.New("api_alias is already in use")
	}
	if target != r.APIAlias {
		token, e := randomHex(32)
		if e != nil {
			return bundleRecord{}, e
		}
		r.Token = token
		r.APIAlias = target
	}
	r.Name = name
	r.Mode = mode
	r.ProfileIDs = ids
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	s.records[id] = r
	if err := s.saveLocked(); err != nil {
		s.records[id] = old
		return bundleRecord{}, err
	}
	return r, nil
}

func (s *bundleStore) rotate(id string) (bundleRecord, error) {
	token, err := randomHex(32)
	if err != nil {
		return bundleRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return bundleRecord{}, os.ErrNotExist
	}
	old := r
	r.Token = token
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	s.records[id] = r
	if err := s.saveLocked(); err != nil {
		s.records[id] = old
		return bundleRecord{}, err
	}
	return r, nil
}
func (s *bundleStore) delete(id string) error {
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
func (s *bundleStore) get(id string) (bundleRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[id]
	return r, ok
}
func (s *bundleStore) list() []bundleRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]bundleRecord, 0, len(s.records))
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
func (s *bundleStore) byAccess(alias, token string) (bundleRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.records {
		if r.APIAlias != alias {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(r.Token), []byte(token)) == 1 {
			return r, true
		}
	}
	return bundleRecord{}, false
}

func validateBundleProfiles(mode string, ids []string, profiles *store, replacementID string, replacement *provisionConfig) error {
	ports := map[int]string{}
	for _, id := range ids {
		r, ok := profiles.get(id)
		if !ok {
			return fmt.Errorf("profile %s does not exist", id)
		}
		cfg := r.Config
		if replacement != nil && id == replacementID {
			cfg = *replacement
		}
		if mode == "parallel" {
			if other, exists := ports[cfg.ListenPort]; exists {
				return fmt.Errorf("parallel bundle listen_port conflict: profiles %s and %s both use %d", other, id, cfg.ListenPort)
			}
			ports[cfg.ListenPort] = id
		}
	}
	return nil
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
	store        *store
	bundles      *bundleStore
	devices      *deviceStore
	mutationMu   sync.Mutex
	adminUser    string
	authMu       sync.RWMutex
	adminPass    string
	passwordFile string
	publicBase   string
}

func validAdminPassword(pass string) error {
	if len(pass) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if len(pass) > 512 {
		return errors.New("password must be at most 512 characters")
	}
	if strings.ContainsAny(pass, "\x00\r\n") {
		return errors.New("password must not contain NUL or line breaks")
	}
	return nil
}

func loadAdminPassword(path, fallback string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := validAdminPassword(fallback); err != nil {
			return "", err
		}
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("secure admin password file permissions: %w", err)
	}
	pass := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if err := validAdminPassword(pass); err != nil {
		return "", fmt.Errorf("invalid admin password file: %w", err)
	}
	return pass, nil
}

func writeAdminPassword(path, pass string) error {
	if err := validAdminPassword(pass); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(pass+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(path, 0o600)
}

func (a *app) passwordMatches(pass string) bool {
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	return subtle.ConstantTimeCompare([]byte(pass), []byte(a.adminPass)) == 1
}

func (a *app) authOK(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	return subtle.ConstantTimeCompare([]byte(user), []byte(a.adminUser)) == 1 && subtle.ConstantTimeCompare([]byte(pass), []byte(a.adminPass)) == 1
}

func (a *app) changePassword(current, next string) error {
	if err := validAdminPassword(next); err != nil {
		return err
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if subtle.ConstantTimeCompare([]byte(current), []byte(a.adminPass)) != 1 {
		return errors.New("current password is incorrect")
	}
	if subtle.ConstantTimeCompare([]byte(next), []byte(a.adminPass)) == 1 {
		return errors.New("new password must be different")
	}
	if a.passwordFile == "" {
		return errors.New("password persistence is not configured")
	}
	if err := writeAdminPassword(a.passwordFile, next); err != nil {
		return fmt.Errorf("persist admin password: %w", err)
	}
	a.adminPass = next
	return nil
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
	path := "/v1/config/" + rec.Token
	if rec.APIAlias != "" {
		path = "/v1/config/" + rec.APIAlias + "/" + rec.Token
	}
	return adminRecord{ID: rec.ID, Name: rec.Name, Revision: rec.Revision, UpdatedAt: rec.UpdatedAt, APIURL: a.baseURL(r) + path, APIAlias: rec.APIAlias, Config: rec.Config}
}

func (a *app) adminBundleView(r *http.Request, rec bundleRecord) adminBundle {
	path := "/v1/bundle/" + rec.Token
	if rec.APIAlias != "" {
		path = "/v1/bundle/" + rec.APIAlias + "/" + rec.Token
	}
	return adminBundle{ID: rec.ID, Name: rec.Name, Revision: rec.Revision, UpdatedAt: rec.UpdatedAt, APIURL: a.baseURL(r) + path, APIAlias: rec.APIAlias, Mode: rec.Mode, ProfileIDs: append([]string(nil), rec.ProfileIDs...)}
}

func profilePublic(rec record) bundlePublicProfile {
	return bundlePublicProfile{SchemaVersion: 1, ProfileID: rec.ID, Revision: fmt.Sprintf("r%d-%s", rec.Revision, rec.UpdatedAt.Format("20060102T150405Z")), DisplayName: rec.Name, Mode: rec.Config.Mode, ListenPort: rec.Config.ListenPort, SchedulerMode: rec.Config.SchedulerMode, TCPEnabled: rec.Config.TCPEnabled, UDPEnabled: rec.Config.UDPEnabled, BackgroundResident: rec.Config.BackgroundResident, TransportKey: rec.Config.TransportKey, Relays: rec.Config.Relays}
}

func (a *app) publicBundle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/bundle/"), "/")
	parts := strings.Split(rest, "/")
	alias, token := "", ""
	switch len(parts) {
	case 1:
		token = parts[0]
	case 2:
		alias, token = strings.ToLower(parts[0]), parts[1]
	default:
		http.NotFound(w, r)
		return
	}
	if len(token) != 64 {
		http.NotFound(w, r)
		return
	}
	rec, ok := a.bundles.byAccess(alias, token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := validateBundleProfiles(rec.Mode, rec.ProfileIDs, a.store, "", nil); err != nil {
		http.Error(w, "bundle configuration invalid", http.StatusConflict)
		return
	}
	profiles := make([]bundlePublicProfile, 0, len(rec.ProfileIDs))
	for _, id := range rec.ProfileIDs {
		p, _ := a.store.get(id)
		profiles = append(profiles, profilePublic(p))
	}
	payload := bundlePublicPayload{SchemaVersion: 2, Kind: "bundle", BundleID: rec.ID, Revision: fmt.Sprintf("r%d-%s", rec.Revision, rec.UpdatedAt.Format("20060102T150405Z")), DisplayName: rec.Name, Mode: rec.Mode, Profiles: profiles}
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := writeEncryptedJSON(w, http.StatusOK, token, payload); err != nil {
		http.Error(w, "encrypt configuration", http.StatusInternalServerError)
	}
}

func envelopeKey(token string) ([]byte, error) {
	raw, err := hex.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("invalid provisioning token")
	}
	mac := hmac.New(sha256.New, raw)
	_, _ = mac.Write([]byte("mpx-provision-config-envelope-v1"))
	return mac.Sum(nil), nil
}

func encryptEnvelope(token string, v any) (encryptedEnvelope, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return encryptedEnvelope{}, err
	}
	key, err := envelopeKey(token)
	if err != nil {
		return encryptedEnvelope{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return encryptedEnvelope{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return encryptedEnvelope{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return encryptedEnvelope{}, err
	}
	sealed := gcm.Seal(nil, nonce, plain, []byte("mpx-provision-envelope-v1"))
	return encryptedEnvelope{Version: 1, Nonce: base64.RawURLEncoding.EncodeToString(nonce), Data: base64.RawURLEncoding.EncodeToString(sealed)}, nil
}

func writeEncryptedJSON(w http.ResponseWriter, status int, token string, v any) error {
	envelope, err := encryptEnvelope(token, v)
	if err != nil {
		return err
	}
	writeJSON(w, status, envelope)
	return nil
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
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/config/"), "/")
	parts := strings.Split(rest, "/")
	alias, token := "", ""
	switch len(parts) {
	case 1:
		token = parts[0]
	case 2:
		alias, token = strings.ToLower(parts[0]), parts[1]
	default:
		http.NotFound(w, r)
		return
	}
	if token == "" || len(token) != 64 {
		http.NotFound(w, r)
		return
	}
	rec, ok := a.store.byAccess(alias, token)
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
	if err := writeEncryptedJSON(w, http.StatusOK, token, p); err != nil {
		http.Error(w, "encrypt configuration", http.StatusInternalServerError)
	}
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
		alias := ""
		if in.APIAlias != nil {
			alias = *in.APIAlias
		}
		rec, err := a.store.createWithAlias(in.Name, in.Config, alias)
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
		a.mutationMu.Lock()
		defer a.mutationMu.Unlock()
		if err := a.validateProfileMutation(id, normalizeConfig(in.Config)); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		rec, err := a.store.updateWithAlias(id, in.Name, in.Config, in.APIAlias)
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
		a.mutationMu.Lock()
		defer a.mutationMu.Unlock()
		if name, used := a.profileReferenced(id); used {
			http.Error(w, "profile is used by bundle "+name, http.StatusConflict)
			return
		}
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

func (a *app) adminBundles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows := a.bundles.list()
		out := make([]adminBundle, 0, len(rows))
		for _, rec := range rows {
			out = append(out, a.adminBundleView(r, rec))
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var in bundleInput
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		alias := ""
		if in.APIAlias != nil {
			alias = *in.APIAlias
		}
		a.mutationMu.Lock()
		defer a.mutationMu.Unlock()
		if err := validateBundleProfiles(in.Mode, in.ProfileIDs, a.store, "", nil); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		rec, err := a.bundles.create(in.Name, in.Mode, in.ProfileIDs, alias)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, http.StatusCreated, a.adminBundleView(r, rec))
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (a *app) adminBundleByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/api/bundles/")
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
		rec, err := a.bundles.rotate(id)
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, 200, a.adminBundleView(r, rec))
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var in bundleInput
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		a.mutationMu.Lock()
		defer a.mutationMu.Unlock()
		if err := validateBundleProfiles(in.Mode, in.ProfileIDs, a.store, "", nil); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		rec, err := a.bundles.update(id, in.Name, in.Mode, in.ProfileIDs, in.APIAlias)
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, 200, a.adminBundleView(r, rec))
	case http.MethodDelete:
		if err := a.bundles.delete(id); errors.Is(err, os.ErrNotExist) {
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

func (a *app) validateProfileMutation(id string, cfg provisionConfig) error {
	if a.bundles == nil {
		return nil
	}
	for _, b := range a.bundles.list() {
		if b.Mode != "parallel" {
			continue
		}
		contains := false
		for _, pid := range b.ProfileIDs {
			if pid == id {
				contains = true
				break
			}
		}
		if contains {
			if err := validateBundleProfiles(b.Mode, b.ProfileIDs, a.store, id, &cfg); err != nil {
				return fmt.Errorf("bundle %q: %w", b.Name, err)
			}
		}
	}
	return nil
}
func (a *app) profileReferenced(id string) (string, bool) {
	if a.bundles == nil {
		return "", false
	}
	for _, b := range a.bundles.list() {
		for _, pid := range b.ProfileIDs {
			if pid == id {
				return b.Name, true
			}
		}
	}
	return "", false
}

func (a *app) adminSystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, systemInfo{Component: "mpx-provision", Version: Version, SourceID: SourceID, APISchema: 1, PasswordChange: a.passwordFile != ""})
}

func (a *app) adminPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in passwordInput
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !a.passwordMatches(in.CurrentPassword) {
		http.Error(w, "current password is incorrect", http.StatusForbidden)
		return
	}
	if err := a.changePassword(in.CurrentPassword, in.NewPassword); err != nil {
		if strings.Contains(err.Error(), "current password") {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
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
	mux.HandleFunc("/v1/bundle/", a.publicBundle)
	mux.HandleFunc("/v1/device/pair", a.devicePair)
	mux.HandleFunc("/v1/device/poll", a.devicePoll)
	mux.HandleFunc("/v1/device/report", a.deviceReport)
	mux.HandleFunc("/v1/device/unpair", a.deviceUnpair)
	mux.HandleFunc("/admin", a.requireAdmin(a.adminPage))
	mux.HandleFunc("/admin/", a.requireAdmin(a.adminPage))
	mux.HandleFunc("/admin/api/system", a.requireAdmin(a.adminSystem))
	mux.HandleFunc("/admin/api/password", a.requireAdmin(a.adminPassword))
	mux.HandleFunc("/admin/api/profiles", a.requireAdmin(a.adminProfiles))
	mux.HandleFunc("/admin/api/profiles/", a.requireAdmin(a.adminProfileByID))
	mux.HandleFunc("/admin/api/bundles", a.requireAdmin(a.adminBundles))
	mux.HandleFunc("/admin/api/bundles/", a.requireAdmin(a.adminBundleByID))
	mux.HandleFunc("/admin/api/devices", a.requireAdmin(a.adminDevices))
	mux.HandleFunc("/admin/api/devices/", a.requireAdmin(a.adminDeviceByID))
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
	bootstrapPass := os.Getenv("MPX_PROVISION_ADMIN_PASSWORD")
	publicBase := strings.TrimRight(strings.TrimSpace(os.Getenv("MPX_PROVISION_PUBLIC_BASE_URL")), "/")
	if err := validatePublicBase(publicBase); err != nil {
		log.Fatal(err)
	}
	dataPath := getenv("MPX_PROVISION_DATA", "./provisioning-data/profiles.json")
	passwordFile := getenv("MPX_PROVISION_ADMIN_PASSWORD_FILE", filepath.Join(filepath.Dir(dataPath), "admin-password"))
	pass, err := loadAdminPassword(passwordFile, bootstrapPass)
	if err != nil {
		log.Fatal(err)
	}
	st, err := newStore(dataPath)
	if err != nil {
		log.Fatal(err)
	}
	bundlePath := getenv("MPX_PROVISION_BUNDLES", filepath.Join(filepath.Dir(dataPath), "bundles.json"))
	bs, err := newBundleStore(bundlePath)
	if err != nil {
		log.Fatal(err)
	}
	devicePath := getenv("MPX_PROVISION_DEVICES", filepath.Join(filepath.Dir(dataPath), "devices.json"))
	ds, err := newDeviceStore(devicePath)
	if err != nil {
		log.Fatal(err)
	}
	for _, b := range bs.list() {
		if err := validateBundleProfiles(b.Mode, b.ProfileIDs, st, "", nil); err != nil {
			log.Fatalf("bundle %s invalid: %v", b.ID, err)
		}
	}
	a := &app{store: st, bundles: bs, devices: ds, adminUser: user, adminPass: pass, passwordFile: passwordFile, publicBase: publicBase}
	srv := &http.Server{Addr: listen, Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	log.Printf("MPX Provisioning %s (%s) listening on %s (admin /admin)", Version, SourceID, listen)
	log.Fatal(srv.ListenAndServe())
}
