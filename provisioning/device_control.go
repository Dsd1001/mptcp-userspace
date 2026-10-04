package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const devicePairingTTL = 10 * time.Minute
const devicePollTimeout = 25 * time.Second

type deviceObserved struct {
	AppVersion        string            `json:"app_version,omitempty"`
	Running           bool              `json:"running"`
	Status            string            `json:"status,omitempty"`
	ConfigRevision    string            `json:"config_revision,omitempty"`
	BundleID          string            `json:"bundle_id,omitempty"`
	ProfileStatus     map[string]string `json:"profile_status,omitempty"`
	UpdateStatus      string            `json:"update_status,omitempty"`
	LastError         string            `json:"last_error,omitempty"`
	RestartGeneration uint64            `json:"restart_generation,omitempty"`
	SyncGeneration    uint64            `json:"sync_generation,omitempty"`
	UpdateGeneration  uint64            `json:"update_generation,omitempty"`
}

type deviceAudit struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Detail string    `json:"detail,omitempty"`
}

type deviceRecord struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	SecretHash        string         `json:"secret_hash,omitempty"`
	PairingHash       string         `json:"pairing_hash,omitempty"`
	PairingExpiresAt  *time.Time     `json:"pairing_expires_at,omitempty"`
	Revoked           bool           `json:"revoked,omitempty"`
	Revision          uint64         `json:"revision"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	LastSeen          *time.Time     `json:"last_seen,omitempty"`
	AssignmentType    string         `json:"assignment_type,omitempty"`
	AssignmentID      string         `json:"assignment_id,omitempty"`
	DesiredState      string         `json:"desired_state"`
	RestartGeneration uint64         `json:"restart_generation,omitempty"`
	SyncGeneration    uint64         `json:"sync_generation,omitempty"`
	UpdateGeneration  uint64         `json:"update_generation,omitempty"`
	DesiredVersion    string         `json:"desired_version,omitempty"`
	Observed          deviceObserved `json:"observed"`
	Audit             []deviceAudit  `json:"audit,omitempty"`
}

type adminDevice struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Revoked           bool           `json:"revoked"`
	Paired            bool           `json:"paired"`
	Revision          uint64         `json:"revision"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	LastSeen          *time.Time     `json:"last_seen,omitempty"`
	AssignmentType    string         `json:"assignment_type,omitempty"`
	AssignmentID      string         `json:"assignment_id,omitempty"`
	DesiredState      string         `json:"desired_state"`
	RestartGeneration uint64         `json:"restart_generation,omitempty"`
	SyncGeneration    uint64         `json:"sync_generation,omitempty"`
	UpdateGeneration  uint64         `json:"update_generation,omitempty"`
	DesiredVersion    string         `json:"desired_version,omitempty"`
	Observed          deviceObserved `json:"observed"`
	Audit             []deviceAudit  `json:"audit,omitempty"`
	PairingCode       string         `json:"pairing_code,omitempty"`
	PairingExpiresAt  *time.Time     `json:"pairing_expires_at,omitempty"`
}

type adminDeviceInput struct {
	Name           string `json:"name"`
	AssignmentType string `json:"assignment_type,omitempty"`
	AssignmentID   string `json:"assignment_id,omitempty"`
	DesiredState   string `json:"desired_state"`
}

type devicePairInput struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
}

type devicePairResponse struct {
	DeviceID        string    `json:"device_id"`
	DeviceSecret    string    `json:"device_secret"`
	ControlRevision uint64    `json:"control_revision"`
	ServerTime      time.Time `json:"server_time"`
}

type deviceControl struct {
	Revision          uint64    `json:"revision"`
	DesiredState      string    `json:"desired_state"`
	AssignmentType    string    `json:"assignment_type,omitempty"`
	AssignmentID      string    `json:"assignment_id,omitempty"`
	ProvisioningURL   string    `json:"provisioning_url,omitempty"`
	RestartGeneration uint64    `json:"restart_generation,omitempty"`
	SyncGeneration    uint64    `json:"sync_generation,omitempty"`
	UpdateGeneration  uint64    `json:"update_generation,omitempty"`
	DesiredVersion    string    `json:"desired_version,omitempty"`
	ServerTime        time.Time `json:"server_time"`
}

type deviceStore struct {
	mu      sync.RWMutex
	path    string
	records map[string]deviceRecord
	notify  chan struct{}
}

func newDeviceStore(path string) (*deviceStore, error) {
	s := &deviceStore{path: path, records: map[string]deviceRecord{}, notify: make(chan struct{})}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure device data file permissions: %w", err)
	}
	var rows []deviceRecord
	if err := jsonUnmarshalStrict(data, &rows); err != nil {
		return nil, fmt.Errorf("decode device data file: %w", err)
	}
	for _, rec := range rows {
		if rec.ID == "" || rec.Revision == 0 {
			return nil, errors.New("device data contains invalid record")
		}
		if rec.DesiredState == "" {
			rec.DesiredState = "stopped"
		}
		s.records[rec.ID] = rec
	}
	return s, nil
}

func jsonUnmarshalStrict(data []byte, v any) error {
	dec := jsonNewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func jsonNewDecoder(r *strings.Reader) *json.Decoder { return json.NewDecoder(r) }

func (s *deviceStore) saveLocked() error {
	rows := make([]deviceRecord, 0, len(s.records))
	for _, rec := range s.records {
		rows = append(rows, rec)
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

func (s *deviceStore) signalLocked() {
	close(s.notify)
	s.notify = make(chan struct{})
}

func pairingHash(code string) string {
	cleaned := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	sum := sha256.Sum256([]byte(cleaned))
	return hex.EncodeToString(sum[:])
}

func secretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func randomPairingCode() (string, error) {
	raw, err := randomHex(8)
	if err != nil {
		return "", err
	}
	value := strings.ToUpper(raw[:12])
	return value[:4] + "-" + value[4:8] + "-" + value[8:12], nil
}

func appendDeviceAudit(rec *deviceRecord, actor, action, detail string) {
	rec.Audit = append(rec.Audit, deviceAudit{At: time.Now().UTC(), Actor: actor, Action: action, Detail: detail})
	if len(rec.Audit) > 80 {
		rec.Audit = append([]deviceAudit(nil), rec.Audit[len(rec.Audit)-80:]...)
	}
}

func validateDesiredState(value string) error {
	if value != "running" && value != "stopped" {
		return errors.New("desired_state must be running or stopped")
	}
	return nil
}

func (a *app) validateDeviceAssignment(kind, id string) error {
	kind, id = strings.TrimSpace(kind), strings.TrimSpace(id)
	if kind == "" && id == "" {
		return nil
	}
	if kind == "profile" {
		if _, ok := a.store.get(id); !ok {
			return errors.New("assigned Profile does not exist")
		}
		return nil
	}
	if kind == "bundle" {
		if _, ok := a.bundles.get(id); !ok {
			return errors.New("assigned Bundle does not exist")
		}
		return nil
	}
	return errors.New("assignment_type must be profile, bundle or empty")
}

func (s *deviceStore) create(name, assignmentType, assignmentID, desiredState string) (adminDevice, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return adminDevice{}, errors.New("device name must be 1-120 characters")
	}
	if err := validateDesiredState(desiredState); err != nil {
		return adminDevice{}, err
	}
	id, err := randomHex(8)
	if err != nil {
		return adminDevice{}, err
	}
	code, err := randomPairingCode()
	if err != nil {
		return adminDevice{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(devicePairingTTL)
	rec := deviceRecord{
		ID: id, Name: name, PairingHash: pairingHash(code), PairingExpiresAt: &expires,
		Revision: 1, CreatedAt: now, UpdatedAt: now, AssignmentType: assignmentType,
		AssignmentID: assignmentID, DesiredState: desiredState,
	}
	appendDeviceAudit(&rec, "admin", "device_created", "")
	s.mu.Lock()
	s.records[id] = rec
	if err := s.saveLocked(); err != nil {
		delete(s.records, id)
		s.mu.Unlock()
		return adminDevice{}, err
	}
	s.signalLocked()
	s.mu.Unlock()
	view := deviceAdminView(rec)
	view.PairingCode = code
	return view, nil
}

func deviceAdminView(rec deviceRecord) adminDevice {
	audit := append([]deviceAudit(nil), rec.Audit...)
	return adminDevice{
		ID: rec.ID, Name: rec.Name, Revoked: rec.Revoked, Paired: rec.SecretHash != "" && !rec.Revoked,
		Revision: rec.Revision, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, LastSeen: rec.LastSeen,
		AssignmentType: rec.AssignmentType, AssignmentID: rec.AssignmentID, DesiredState: rec.DesiredState,
		RestartGeneration: rec.RestartGeneration, SyncGeneration: rec.SyncGeneration, UpdateGeneration: rec.UpdateGeneration,
		DesiredVersion: rec.DesiredVersion, Observed: rec.Observed, Audit: audit,
		PairingExpiresAt: rec.PairingExpiresAt,
	}
}

func (s *deviceStore) list() []adminDevice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]adminDevice, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, deviceAdminView(rec))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (s *deviceStore) get(id string) (deviceRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[id]
	return rec, ok
}

func (s *deviceStore) update(id string, in adminDeviceInput) (adminDevice, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 120 {
		return adminDevice{}, errors.New("device name must be 1-120 characters")
	}
	if err := validateDesiredState(in.DesiredState); err != nil {
		return adminDevice{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return adminDevice{}, os.ErrNotExist
	}
	changed := rec.Name != name || rec.AssignmentType != in.AssignmentType || rec.AssignmentID != in.AssignmentID || rec.DesiredState != in.DesiredState
	rec.Name = name
	rec.AssignmentType = in.AssignmentType
	rec.AssignmentID = in.AssignmentID
	rec.DesiredState = in.DesiredState
	if changed {
		rec.Revision++
		rec.UpdatedAt = time.Now().UTC()
		appendDeviceAudit(&rec, "admin", "desired_state_updated", fmt.Sprintf("state=%s assignment=%s:%s", rec.DesiredState, rec.AssignmentType, rec.AssignmentID))
	}
	s.records[id] = rec
	if err := s.saveLocked(); err != nil {
		return adminDevice{}, err
	}
	if changed {
		s.signalLocked()
	}
	return deviceAdminView(rec), nil
}

func (s *deviceStore) newPairing(id string) (adminDevice, error) {
	code, err := randomPairingCode()
	if err != nil {
		return adminDevice{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(devicePairingTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return adminDevice{}, os.ErrNotExist
	}
	rec.PairingHash = pairingHash(code)
	rec.PairingExpiresAt = &expires
	rec.SecretHash = ""
	rec.Revoked = false
	rec.Revision++
	rec.UpdatedAt = now
	appendDeviceAudit(&rec, "admin", "pairing_rotated", "")
	s.records[id] = rec
	if err := s.saveLocked(); err != nil {
		return adminDevice{}, err
	}
	s.signalLocked()
	view := deviceAdminView(rec)
	view.PairingCode = code
	return view, nil
}

func (s *deviceStore) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[id]; !ok {
		return os.ErrNotExist
	}
	delete(s.records, id)
	if err := s.saveLocked(); err != nil {
		return err
	}
	s.signalLocked()
	return nil
}

func (s *deviceStore) bumpRestart(id string) (adminDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return adminDevice{}, os.ErrNotExist
	}
	rec.RestartGeneration++
	rec.Revision++
	rec.UpdatedAt = time.Now().UTC()
	appendDeviceAudit(&rec, "admin", "restart_requested", fmt.Sprintf("generation=%d", rec.RestartGeneration))
	s.records[id] = rec
	if err := s.saveLocked(); err != nil {
		return adminDevice{}, err
	}
	s.signalLocked()
	return deviceAdminView(rec), nil
}

func (s *deviceStore) bumpSync(id string) (adminDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return adminDevice{}, os.ErrNotExist
	}
	rec.SyncGeneration++
	rec.Revision++
	rec.UpdatedAt = time.Now().UTC()
	appendDeviceAudit(&rec, "admin", "config_sync_requested", fmt.Sprintf("generation=%d", rec.SyncGeneration))
	s.records[id] = rec
	if err := s.saveLocked(); err != nil {
		return adminDevice{}, err
	}
	s.signalLocked()
	return deviceAdminView(rec), nil
}

func (s *deviceStore) bumpUpdate(id, version string) (adminDevice, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "latest"
	}
	if version != "latest" {
		return adminDevice{}, errors.New("desired_version currently supports latest only")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return adminDevice{}, os.ErrNotExist
	}
	rec.UpdateGeneration++
	rec.DesiredVersion = version
	rec.Revision++
	rec.UpdatedAt = time.Now().UTC()
	appendDeviceAudit(&rec, "admin", "update_requested", fmt.Sprintf("generation=%d version=%s", rec.UpdateGeneration, version))
	s.records[id] = rec
	if err := s.saveLocked(); err != nil {
		return adminDevice{}, err
	}
	s.signalLocked()
	return deviceAdminView(rec), nil
}

func (s *deviceStore) pair(code, deviceName, appVersion string) (devicePairResponse, error) {
	hash := pairingHash(code)
	now := time.Now().UTC()
	secret, err := randomHex(32)
	if err != nil {
		return devicePairResponse{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, rec := range s.records {
		if rec.PairingHash == "" || rec.PairingExpiresAt == nil || now.After(*rec.PairingExpiresAt) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(rec.PairingHash), []byte(hash)) != 1 {
			continue
		}
		if name := strings.TrimSpace(deviceName); name != "" && len(name) <= 120 {
			rec.Name = name
		}
		rec.SecretHash = secretHash(secret)
		rec.PairingHash = ""
		rec.PairingExpiresAt = nil
		rec.Revoked = false
		rec.Revision++
		rec.UpdatedAt = now
		rec.LastSeen = &now
		rec.Observed.AppVersion = strings.TrimSpace(appVersion)
		appendDeviceAudit(&rec, "device", "paired", "")
		s.records[id] = rec
		if err := s.saveLocked(); err != nil {
			return devicePairResponse{}, err
		}
		s.signalLocked()
		return devicePairResponse{DeviceID: id, DeviceSecret: secret, ControlRevision: rec.Revision, ServerTime: now}, nil
	}
	return devicePairResponse{}, errors.New("pairing code invalid or expired")
}

func (s *deviceStore) authenticate(id, secret string) (deviceRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[id]
	if !ok || rec.Revoked || rec.SecretHash == "" || secret == "" {
		return deviceRecord{}, false
	}
	got := secretHash(secret)
	if subtle.ConstantTimeCompare([]byte(rec.SecretHash), []byte(got)) != 1 {
		return deviceRecord{}, false
	}
	return rec, true
}

func (s *deviceStore) waitChannel() <-chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.notify
}

func (s *deviceStore) touch(id string, observed *deviceObserved) (deviceRecord, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok || rec.Revoked {
		return deviceRecord{}, os.ErrNotExist
	}
	needSave := rec.LastSeen == nil || now.Sub(*rec.LastSeen) >= 15*time.Second
	rec.LastSeen = &now
	if observed != nil {
		rec.Observed = *observed
		needSave = true
	}
	if needSave {
		s.records[id] = rec
		if err := s.saveLocked(); err != nil {
			return deviceRecord{}, err
		}
	} else {
		s.records[id] = rec
	}
	return rec, nil
}

func (s *deviceStore) revokeFromDevice(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return os.ErrNotExist
	}
	rec.Revoked = true
	rec.SecretHash = ""
	rec.PairingHash = ""
	rec.PairingExpiresAt = nil
	rec.Revision++
	rec.UpdatedAt = time.Now().UTC()
	appendDeviceAudit(&rec, "device", "unpaired", "")
	s.records[id] = rec
	if err := s.saveLocked(); err != nil {
		return err
	}
	s.signalLocked()
	return nil
}

func deviceCredentials(r *http.Request) (string, string) {
	id := strings.TrimSpace(r.Header.Get("X-MPX-Device-ID"))
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") {
		return id, ""
	}
	return id, strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
}

func (a *app) assignmentURL(r *http.Request, rec deviceRecord) string {
	switch rec.AssignmentType {
	case "profile":
		if p, ok := a.store.get(rec.AssignmentID); ok {
			path := "/v1/config/" + p.Token
			if p.APIAlias != "" {
				path = "/v1/config/" + p.APIAlias + "/" + p.Token
			}
			return a.baseURL(r) + path
		}
	case "bundle":
		if b, ok := a.bundles.get(rec.AssignmentID); ok {
			path := "/v1/bundle/" + b.Token
			if b.APIAlias != "" {
				path = "/v1/bundle/" + b.APIAlias + "/" + b.Token
			}
			return a.baseURL(r) + path
		}
	}
	return ""
}

func (a *app) deviceControlView(r *http.Request, rec deviceRecord) deviceControl {
	return deviceControl{
		Revision: rec.Revision, DesiredState: rec.DesiredState,
		AssignmentType: rec.AssignmentType, AssignmentID: rec.AssignmentID,
		ProvisioningURL:   a.assignmentURL(r, rec),
		RestartGeneration: rec.RestartGeneration, SyncGeneration: rec.SyncGeneration, UpdateGeneration: rec.UpdateGeneration,
		DesiredVersion: rec.DesiredVersion, ServerTime: time.Now().UTC(),
	}
}

func (a *app) adminDevices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.devices.list())
	case http.MethodPost:
		var in adminDeviceInput
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if in.DesiredState == "" {
			in.DesiredState = "stopped"
		}
		if err := a.validateDeviceAssignment(in.AssignmentType, in.AssignmentID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := a.devices.create(in.Name, in.AssignmentType, in.AssignmentID, in.DesiredState)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *app) adminDeviceByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/api/devices/"), "/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]
	if len(parts) == 2 {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var out adminDevice
		var err error
		switch parts[1] {
		case "pairing":
			out, err = a.devices.newPairing(id)
		case "restart":
			out, err = a.devices.bumpRestart(id)
		case "sync":
			out, err = a.devices.bumpSync(id)
		case "update":
			var in struct {
				Version string `json:"version,omitempty"`
			}
			if err = decodeJSON(r, &in); err == nil {
				out, err = a.devices.bumpUpdate(id, in.Version)
			}
		default:
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rec, ok := a.devices.get(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, deviceAdminView(rec))
	case http.MethodPut:
		var in adminDeviceInput
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := a.validateDeviceAssignment(in.AssignmentType, in.AssignmentID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := a.devices.update(id, in)
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodDelete:
		if err := a.devices.delete(id); errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *app) devicePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in devicePairInput
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out, err := a.devices.pair(in.Code, in.DeviceName, in.AppVersion)
	if err != nil {
		http.Error(w, "pairing code invalid or expired", http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *app) authenticateDevice(w http.ResponseWriter, r *http.Request) (deviceRecord, bool) {
	id, secret := deviceCredentials(r)
	rec, ok := a.devices.authenticate(id, secret)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "device authentication required", http.StatusUnauthorized)
		return deviceRecord{}, false
	}
	return rec, true
}

func (a *app) devicePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ch := a.devices.waitChannel()
	rec, ok := a.authenticateDevice(w, r)
	if !ok {
		return
	}
	since, _ := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("since")), 10, 64)
	rec, _ = a.devices.touch(rec.ID, nil)
	if rec.Revision <= since {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
		case <-time.After(devicePollTimeout):
		}
		fresh, authOK := a.authenticateDevice(w, r)
		if !authOK {
			return
		}
		rec, _ = a.devices.touch(fresh.ID, nil)
	}
	writeJSON(w, http.StatusOK, a.deviceControlView(r, rec))
}

func (a *app) deviceReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rec, ok := a.authenticateDevice(w, r)
	if !ok {
		return
	}
	var observed deviceObserved
	if err := decodeJSON(r, &observed); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(observed.AppVersion) > 64 || len(observed.Status) > 200 || len(observed.ConfigRevision) > 160 || len(observed.BundleID) > 128 || len(observed.UpdateStatus) > 200 || len(observed.LastError) > 500 || len(observed.ProfileStatus) > 64 {
		http.Error(w, "device report too large", http.StatusBadRequest)
		return
	}
	if _, err := a.devices.touch(rec.ID, &observed); err != nil {
		http.Error(w, "device unavailable", http.StatusGone)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) deviceUnpair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rec, ok := a.authenticateDevice(w, r)
	if !ok {
		return
	}
	if err := a.devices.revokeFromDevice(rec.ID); err != nil {
		http.Error(w, "device unavailable", http.StatusGone)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
