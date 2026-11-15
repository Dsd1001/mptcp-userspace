package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mptcp-desktop/engine/tunnelconfig"
)

const maxTunnelRecords = 128

var errTunnelConflict = errors.New("tunnel revision changed; reload before editing")

type tunnelRecord struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Token              string              `json:"token"`
	APIAlias           string              `json:"api_alias,omitempty"`
	Revision           uint64              `json:"revision"`
	UpdatedAt          time.Time           `json:"updated_at"`
	BackgroundResident bool                `json:"background_resident"`
	Config             tunnelconfig.Config `json:"config"`
}
type tunnelInput struct {
	Name               string              `json:"name"`
	APIAlias           string              `json:"api_alias,omitempty"`
	BackgroundResident bool                `json:"background_resident"`
	ExpectedRevision   *uint64             `json:"expected_revision,omitempty"`
	Config             tunnelconfig.Config `json:"config"`
}
type adminTunnel struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	APIAlias           string              `json:"api_alias,omitempty"`
	APIURL             string              `json:"api_url"`
	Revision           uint64              `json:"revision"`
	UpdatedAt          time.Time           `json:"updated_at"`
	BackgroundResident bool                `json:"background_resident"`
	Config             tunnelconfig.Config `json:"config"`
}
type tunnelStore struct {
	mu      sync.RWMutex
	path    string
	records map[string]tunnelRecord
}
type tunnelStoreFile struct {
	SchemaVersion int            `json:"schema_version"`
	Kind          string         `json:"kind"`
	Tunnels       []tunnelRecord `json:"tunnels"`
}

func cloneTunnel(rec tunnelRecord) tunnelRecord {
	// Control-plane snapshots must not share mutable relay capacity pointers
	// or inlet slices with the store after releasing its lock.
	b, _ := json.Marshal(rec)
	var copy tunnelRecord
	_ = json.Unmarshal(b, &copy)
	return copy
}
func validateTunnelInput(in tunnelInput) error {
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 128 {
		return errors.New("tunnel name must be 1..128 UTF-8 bytes")
	}
	if _, err := normalizeAPIAlias(in.APIAlias); err != nil {
		return err
	}
	return in.Config.Validate()
}
func newTunnelStore(path string) (*tunnelStore, error) {
	s := &tunnelStore{path: path, records: map[string]tunnelRecord{}}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, errors.New("tunnel store must be a regular file no larger than 16 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc tunnelStoreFile
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil || d.Decode(new(any)) != io.EOF || doc.SchemaVersion != 1 || doc.Kind != "mpx_tunnel_store" {
		return nil, errors.New("1.2.0 tunnel store format required; legacy profile/bundle data is not converted or overwritten")
	}
	if len(doc.Tunnels) > maxTunnelRecords {
		return nil, errors.New("too many tunnel records")
	}
	aliases, tokens := map[string]bool{}, map[string]bool{}
	for _, rec := range doc.Tunnels {
		if rec.ID == "" || s.records[rec.ID].ID != "" || rec.Revision == 0 || rec.UpdatedAt.IsZero() {
			return nil, errors.New("invalid tunnel record identity")
		}
		token, err := hex.DecodeString(rec.Token)
		if err != nil || len(token) != 32 || tokens[rec.Token] {
			return nil, errors.New("invalid or duplicated tunnel access token")
		}
		if err := validateTunnelInput(tunnelInput{Name: rec.Name, APIAlias: rec.APIAlias, Config: rec.Config}); err != nil {
			return nil, fmt.Errorf("tunnel %s: %w", rec.ID, err)
		}
		alias, _ := normalizeAPIAlias(rec.APIAlias)
		if alias != "" && aliases[alias] {
			return nil, errors.New("duplicate tunnel API alias")
		}
		rec.APIAlias = alias
		aliases[alias] = alias != ""
		tokens[rec.Token] = true
		s.records[rec.ID] = rec
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *tunnelStore) persistLocked(next map[string]tunnelRecord) error {
	rows := make([]tunnelRecord, 0, len(next))
	for _, rec := range next {
		rows = append(rows, rec)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	b, err := json.MarshalIndent(tunnelStoreFile{SchemaVersion: 1, Kind: "mpx_tunnel_store", Tunnels: rows}, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 16<<20 {
		return errors.New("tunnel store exceeds 16 MiB")
	}
	dir := filepath.Dir(s.path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	if info, e := os.Lstat(s.path); e == nil && !info.Mode().IsRegular() {
		return errors.New("refusing non-regular tunnel store")
	}
	file, err := os.CreateTemp(dir, ".tunnels-*.tmp")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(append(b, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		return err
	}
	if dirFile, e := os.Open(dir); e == nil {
		_ = dirFile.Sync()
		dirFile.Close()
	}
	return nil
}
func (s *tunnelStore) candidateLocked() map[string]tunnelRecord {
	out := make(map[string]tunnelRecord, len(s.records)+1)
	for id, rec := range s.records {
		out[id] = rec
	}
	return out
}
func (s *tunnelStore) get(id string) (tunnelRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[id]
	return cloneTunnel(r), ok
}
func (s *tunnelStore) list() []tunnelRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]tunnelRecord, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, cloneTunnel(rec))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out
}
func (s *tunnelStore) byAccess(alias, token string) (tunnelRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rec := range s.records {
		if rec.APIAlias == alias && subtle.ConstantTimeCompare([]byte(rec.Token), []byte(token)) == 1 {
			return cloneTunnel(rec), true
		}
	}
	return tunnelRecord{}, false
}
func (s *tunnelStore) upsert(id string, in tunnelInput) (tunnelRecord, error) {
	if err := validateTunnelInput(in); err != nil {
		return tunnelRecord{}, err
	}
	alias, _ := normalizeAPIAlias(in.APIAlias)
	s.mu.Lock()
	defer s.mu.Unlock()
	var rec tunnelRecord
	if id == "" {
		if len(s.records) >= maxTunnelRecords {
			return rec, errors.New("tunnel record limit reached")
		}
		if in.ExpectedRevision != nil {
			return rec, errors.New("creation does not accept expected_revision")
		}
		var err error
		rec.ID, err = randomHex(16)
		if err != nil {
			return rec, err
		}
		if _, exists := s.records[rec.ID]; exists {
			return rec, errors.New("random identity collision; retry creation")
		}
		rec.Token, err = randomHex(32)
		if err != nil {
			return rec, err
		}
		rec.Revision = 1
	} else {
		var ok bool
		rec, ok = s.records[id]
		if !ok {
			return rec, os.ErrNotExist
		}
		if in.ExpectedRevision == nil || *in.ExpectedRevision != rec.Revision {
			return rec, errTunnelConflict
		}
		if rec.Revision == ^uint64(0) {
			return rec, errors.New("revision exhausted")
		}
		rec.Revision++
	}
	for otherID, r := range s.records {
		if alias != "" && otherID != rec.ID && r.APIAlias == alias {
			return rec, errors.New("API alias already in use")
		}
	}
	rec.Name = strings.TrimSpace(in.Name)
	rec.APIAlias = alias
	rec.Config = in.Config
	rec.BackgroundResident = in.BackgroundResident
	rec.UpdatedAt = time.Now().UTC()
	rec = cloneTunnel(rec)
	next := s.candidateLocked()
	next[rec.ID] = rec
	if err := s.persistLocked(next); err != nil {
		return tunnelRecord{}, err
	}
	s.records = next
	return cloneTunnel(rec), nil
}
func (s *tunnelStore) mutate(id string, expected uint64, rotate bool) (tunnelRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return rec, os.ErrNotExist
	}
	if expected != rec.Revision {
		return rec, errTunnelConflict
	}
	next := s.candidateLocked()
	if rotate {
		token, err := randomHex(32)
		if err != nil {
			return rec, err
		}
		if rec.Revision == ^uint64(0) {
			return rec, errors.New("revision exhausted")
		}
		rec.Token = token
		rec.Revision++
		rec.UpdatedAt = time.Now().UTC()
		next[id] = rec
	} else {
		delete(next, id)
	}
	if err := s.persistLocked(next); err != nil {
		return tunnelRecord{}, err
	}
	s.records = next
	return cloneTunnel(rec), nil
}
func (a *app) tunnelView(r *http.Request, rec tunnelRecord) adminTunnel {
	path := "/v1/tunnel/" + rec.Token
	if rec.APIAlias != "" {
		path = "/v1/tunnel/" + rec.APIAlias + "/" + rec.Token
	}
	return adminTunnel{ID: rec.ID, Name: rec.Name, APIAlias: rec.APIAlias, APIURL: a.baseURL(r) + path, Revision: rec.Revision, UpdatedAt: rec.UpdatedAt, BackgroundResident: rec.BackgroundResident, Config: rec.Config}
}
func tunnelPayload(rec tunnelRecord) tunnelconfig.ManagedProfile {
	return tunnelconfig.ManagedProfile{SchemaVersion: 4, Kind: tunnelconfig.ManagedKind, ProfileID: rec.ID, Revision: fmt.Sprintf("r%d-%s", rec.Revision, rec.UpdatedAt.Format("20060102T150405Z")), DisplayName: rec.Name, BackgroundResident: rec.BackgroundResident, Config: rec.Config}
}
func (a *app) publicTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/tunnel/"), "/"), "/")
	alias, token := "", ""
	if len(parts) == 1 {
		token = parts[0]
	} else if len(parts) == 2 {
		alias = strings.ToLower(parts[0])
		token = parts[1]
	} else {
		http.NotFound(w, r)
		return
	}
	raw, err := hex.DecodeString(token)
	if err != nil || len(raw) != 32 || a.tunnels == nil {
		http.NotFound(w, r)
		return
	}
	rec, ok := a.tunnels.byAccess(alias, token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Pragma", "no-cache")
	if err := writeEncryptedJSON(w, 200, token, tunnelPayload(rec)); err != nil {
		http.Error(w, "configuration encryption failed", 500)
	}
}
func (a *app) adminTunnels(w http.ResponseWriter, r *http.Request) {
	if a.tunnels == nil {
		http.Error(w, "tunnel store unavailable", 503)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out := make([]adminTunnel, 0)
		for _, rec := range a.tunnels.list() {
			out = append(out, a.tunnelView(r, rec))
		}
		writeJSON(w, 200, out)
	case http.MethodPost:
		var in tunnelInput
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		a.mutationMu.Lock()
		rec, err := a.tunnels.upsert("", in)
		a.mutationMu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, 201, a.tunnelView(r, rec))
	default:
		http.Error(w, "method not allowed", 405)
	}
}
func (a *app) adminTunnelByID(w http.ResponseWriter, r *http.Request) {
	if a.tunnels == nil {
		http.Error(w, "tunnel store unavailable", 503)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/api/tunnels/"), "/"), "/")
	if len(parts) > 2 || parts[0] == "" || len(parts) == 2 && parts[1] != "rotate" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if r.Method == http.MethodGet && len(parts) == 1 {
		rec, ok := a.tunnels.get(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, a.tunnelView(r, rec))
		return
	}
	var rec tunnelRecord
	var err error
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	switch {
	case r.Method == http.MethodPut && len(parts) == 1:
		var in tunnelInput
		if err = decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		rec, err = a.tunnels.upsert(id, in)
	case r.Method == http.MethodDelete && len(parts) == 1 || r.Method == http.MethodPost && len(parts) == 2:
		var in struct {
			ExpectedRevision uint64 `json:"expected_revision"`
		}
		if err = decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if r.Method == http.MethodDelete && a.devices != nil {
			for _, dev := range a.devices.list() {
				if dev.AssignmentType == "tunnel" && dev.AssignmentID == id {
					http.Error(w, "tunnel is assigned to a device; unassign it first", 409)
					return
				}
			}
		}
		rec, err = a.tunnels.mutate(id, in.ExpectedRevision, r.Method == http.MethodPost)
	default:
		http.Error(w, "method not allowed", 405)
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, errTunnelConflict) {
		http.Error(w, err.Error(), 409)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if a.devices != nil {
		a.devices.mu.Lock()
		a.devices.signalLocked()
		a.devices.mu.Unlock()
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(204)
	} else {
		writeJSON(w, 200, a.tunnelView(r, rec))
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
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/v1/tunnel/", a.publicTunnel)
	mux.HandleFunc("/v1/device/pair", a.devicePair)
	mux.HandleFunc("/v1/device/poll", a.devicePoll)
	mux.HandleFunc("/v1/device/report", a.deviceReport)
	mux.HandleFunc("/v1/device/unpair", a.deviceUnpair)
	mux.HandleFunc("/admin", a.requireAdmin(a.adminPage))
	mux.HandleFunc("/admin/", a.requireAdmin(a.adminPage))
	mux.HandleFunc("/admin/api/system", a.requireAdmin(a.adminSystem))
	mux.HandleFunc("/admin/api/password", a.requireAdmin(a.adminPassword))
	mux.HandleFunc("/admin/api/tunnels", a.requireAdmin(a.adminTunnels))
	mux.HandleFunc("/admin/api/tunnels/", a.requireAdmin(a.adminTunnelByID))
	mux.HandleFunc("/admin/api/devices", a.requireAdmin(a.adminDevices))
	mux.HandleFunc("/admin/api/devices/", a.requireAdmin(a.adminDeviceByID))
	gone := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "1.2.0 is tunnel-only; legacy profiles and bundles are not supported", http.StatusGone)
	}
	mux.HandleFunc("/v1/config/", gone)
	mux.HandleFunc("/v1/bundle/", gone)
	for _, p := range []string{"/admin/api/profiles", "/admin/api/profiles/", "/admin/api/bundles", "/admin/api/bundles/"} {
		mux.HandleFunc(p, a.requireAdmin(gone))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/admin/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			http.Error(w, "admin mutations require application/json", http.StatusUnsupportedMediaType)
			return
		}
		// All mutation APIs require JSON; simple cross-site form submissions
		// cannot modify a controller authenticated with browser Basic Auth.
		mux.ServeHTTP(w, r)
	})
}
