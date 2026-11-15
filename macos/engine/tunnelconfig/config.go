// Package tunnelconfig is the shared v1.2 configuration contract used by the
// Engine, provisioning/controller service and Windows UI. It has no transport
// runtime dependencies. The macOS Codable model is checked against the same
// golden JSON fixtures during suite validation.
package tunnelconfig

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
)

const (
	Kind             = "shared_mpx4_tunnel"
	ManagedKind      = "tunnel_profile"
	ManagedSchema    = 4
	MaxDocumentBytes = 64 << 10
	MaxInlets        = 64
)

type Relay struct {
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	DownloadMbps *float64 `json:"download_mbps,omitempty"`
	UploadMbps   *float64 `json:"upload_mbps,omitempty"`
}
type Transport struct {
	TransportKey  string  `json:"transport_key"`
	Relays        []Relay `json:"relays"`
	SchedulerMode string  `json:"scheduler_mode,omitempty"`
}
type Inlet struct {
	ID             string `json:"id"`
	Name           string `json:"name,omitempty"`
	ListenPort     int    `json:"listen_port"`
	RouteID        uint32 `json:"route_id"`
	Weight         int    `json:"weight,omitempty"`
	MaxConnections int    `json:"max_connections,omitempty"`
	TCPEnabled     *bool  `json:"tcp_enabled,omitempty"`
	UOTEnabled     bool   `json:"uot_enabled,omitempty"`
}

func (i Inlet) TCP() bool { return i.TCPEnabled == nil || *i.TCPEnabled }

type Config struct {
	SchemaVersion int       `json:"schema_version"`
	Kind          string    `json:"kind"`
	Tunnel        Transport `json:"tunnel"`
	Inlets        []Inlet   `json:"inlets"`
}

type ManagedProfile struct {
	SchemaVersion      int    `json:"schema_version"`
	Kind               string `json:"kind"`
	ProfileID          string `json:"profile_id"`
	Revision           string `json:"revision"`
	DisplayName        string `json:"display_name"`
	BackgroundResident bool   `json:"background_resident"`
	Config             Config `json:"config"`
}

func validID(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func validHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsUnspecified() && !ip.IsMulticast() && !ip.Equal(net.IPv4bcast)
	}
	if len(host) < 1 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func validCapacity(p *float64) bool {
	return p == nil || !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0.1 && *p <= 6553.5 && math.Abs(math.Round(*p*10)-*p*10) < 0.000001
}

func (c Config) Validate() error {
	if c.SchemaVersion != 1 || c.Kind != Kind {
		return errors.New("1.2.0 requires a shared_mpx4_tunnel configuration; legacy independent profiles/bundles are not supported")
	}
	key, err := hex.DecodeString(c.Tunnel.TransportKey)
	if err != nil || len(key) != 32 {
		return errors.New("tunnel transport_key must be 64 hexadecimal characters")
	}
	switch c.Tunnel.SchedulerMode {
	case "", "auto", "aggregate", "protect", "weighted":
	default:
		return errors.New("invalid tunnel scheduler_mode")
	}
	if len(c.Tunnel.Relays) < 2 || len(c.Tunnel.Relays) > 8 {
		return errors.New("configure 2..8 shared Relay paths")
	}
	if len(c.Inlets) < 1 || len(c.Inlets) > MaxInlets {
		return errors.New("configure 1..64 tunnel inlets")
	}
	ports, ids, routes := map[int]bool{}, map[string]bool{}, map[uint32]bool{}
	for _, in := range c.Inlets {
		if !validID(in.ID) || ids[in.ID] {
			return errors.New("inlet id must be unique and contain 1..64 ASCII letters, digits, hyphen or underscore")
		}
		if len(in.Name) > 128 {
			return errors.New("inlet name exceeds 128 UTF-8 bytes")
		}
		if in.ListenPort < 1024 || in.ListenPort > 65535 || ports[in.ListenPort] {
			return errors.New("inlet listen_port must be unique and in 1024..65535")
		}
		if in.RouteID == 0 || routes[in.RouteID] {
			return errors.New("inlet route_id must be nonzero and unique")
		}
		if in.Weight < 0 || in.Weight > 16 {
			return errors.New("inlet weight must be 1..16, or omitted for 1")
		}
		if in.MaxConnections < 0 || in.MaxConnections > 2048 {
			return errors.New("inlet max_connections must be 1..2048, or omitted for 512")
		}
		if !in.TCP() && !in.UOTEnabled {
			return errors.New("an inlet must enable TCP or UoT")
		}
		ports[in.ListenPort], ids[in.ID], routes[in.RouteID] = true, true, true
	}
	addresses := map[string]bool{}
	for _, r := range c.Tunnel.Relays {
		identity := strings.ToLower(net.JoinHostPort(r.Host, strconv.Itoa(r.Port)))
		if !validHost(r.Host) || r.Port < 1 || r.Port > 65535 || addresses[identity] {
			return errors.New("Relay requires a unique unicast IP/DNS host and valid port")
		}
		if (strings.EqualFold(strings.TrimSuffix(r.Host, "."), "localhost") || net.ParseIP(r.Host) != nil && net.ParseIP(r.Host).IsLoopback()) && ports[r.Port] {
			return errors.New("Relay cannot point at a local inlet")
		}
		if !validCapacity(r.DownloadMbps) || !validCapacity(r.UploadMbps) {
			return errors.New("Relay capacity must be 0.1..6553.5 Mbps, at most one decimal place")
		}
		if c.Tunnel.SchedulerMode == "weighted" && r.DownloadMbps == nil {
			return errors.New("weighted requires download_mbps for every Relay")
		}
		addresses[identity] = true
	}
	return nil
}

func (c Config) Select(ids []string) (Config, error) {
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	if len(ids) == 0 {
		return c, nil
	}
	selected := map[string]bool{}
	for _, id := range ids {
		if selected[id] {
			return Config{}, errors.New("duplicate selected inlet")
		}
		selected[id] = true
	}
	out := c
	out.Inlets = nil
	for _, in := range c.Inlets {
		if selected[in.ID] {
			out.Inlets = append(out.Inlets, in)
			delete(selected, in.ID)
		}
	}
	if len(selected) != 0 {
		return Config{}, errors.New("selected inlet does not exist")
	}
	return out, out.Validate()
}
func (p ManagedProfile) Validate() error {
	if p.SchemaVersion != ManagedSchema || p.Kind != ManagedKind || !validID(p.ProfileID) || p.Revision == "" || len(p.Revision) > 128 || len(p.DisplayName) > 256 {
		return errors.New("1.2.0 requires schema_version=4, kind=tunnel_profile and a valid profile identity")
	}
	return p.Config.Validate()
}
func decode(r io.Reader, out any) error {
	b, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return err
	}
	if len(b) > MaxDocumentBytes {
		return errors.New("tunnel document exceeds 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return fmt.Errorf("invalid tunnel document: %w", err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return errors.New("tunnel document must contain exactly one JSON object")
	}
	return nil
}
func Decode(r io.Reader) (Config, error) {
	var c Config
	if err := decode(r, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}
func DecodeManaged(r io.Reader) (ManagedProfile, error) {
	var p ManagedProfile
	if err := decode(r, &p); err != nil {
		return p, err
	}
	return p, p.Validate()
}
