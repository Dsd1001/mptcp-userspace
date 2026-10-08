package main

import "time"

type Relay struct {
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	DownloadMbps *float64 `json:"download_mbps,omitempty"`
	UploadMbps   *float64 `json:"upload_mbps,omitempty"`
}

type LocalProfile struct {
	ListenPort    int     `json:"listen_port"`
	TCPEnabled    bool    `json:"tcp_enabled"`
	UDPEnabled    bool    `json:"udp_enabled"`
	UOTEnabled    bool    `json:"uot_enabled"`
	SchedulerMode string  `json:"scheduler_mode"`
	Relays        []Relay `json:"relays"`
	TransportKey  string  `json:"transport_key,omitempty"`
}

type engineConfig struct {
	SchemaVersion int     `json:"schema_version"`
	Mode          string  `json:"mode"`
	ListenPort    int     `json:"listen_port"`
	TCPEnabled    bool    `json:"tcp_enabled"`
	UDPEnabled    bool    `json:"udp_enabled"`
	UOTEnabled    bool    `json:"uot_enabled,omitempty"`
	SchedulerMode string  `json:"scheduler_mode"`
	Relays        []Relay `json:"relays"`
	TransportKey  string  `json:"transport_key"`
}

type managedProfile struct {
	SchemaVersion      int     `json:"schema_version"`
	ProfileID          string  `json:"profile_id"`
	Revision           string  `json:"revision"`
	DisplayName        string  `json:"display_name"`
	Mode               string  `json:"mode"`
	ListenPort         int     `json:"listen_port"`
	SchedulerMode      string  `json:"scheduler_mode,omitempty"`
	TCPEnabled         bool    `json:"tcp_enabled"`
	UDPEnabled         bool    `json:"udp_enabled"`
	UOTEnabled         bool    `json:"uot_enabled,omitempty"`
	BackgroundResident bool    `json:"background_resident"`
	TransportKey       string  `json:"transport_key"`
	Relays             []Relay `json:"relays"`
}

type managedBundle struct {
	SchemaVersion int              `json:"schema_version"`
	Kind          string           `json:"kind"`
	BundleID      string           `json:"bundle_id"`
	Revision      string           `json:"revision"`
	DisplayName   string           `json:"display_name"`
	Mode          string           `json:"mode"`
	Profiles      []managedProfile `json:"profiles"`
}

type managedDocument struct {
	Kind      string          `json:"kind"`
	Profile   *managedProfile `json:"profile,omitempty"`
	Bundle    *managedBundle  `json:"bundle,omitempty"`
	FetchedAt time.Time       `json:"fetched_at"`
	FromCache bool            `json:"from_cache"`
}

type ManagedProfileChoice struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	ListenPort         int    `json:"listen_port"`
	SchedulerMode      string `json:"scheduler_mode"`
	TCPEnabled         bool   `json:"tcp_enabled"`
	UDPEnabled         bool   `json:"udp_enabled"`
	UOTEnabled         bool   `json:"uot_enabled"`
	BackgroundResident bool   `json:"background_resident"`
}

type ManagedSummary struct {
	Configured      bool                   `json:"configured"`
	DisplayEndpoint string                 `json:"display_endpoint"`
	Kind            string                 `json:"kind"`
	DisplayName     string                 `json:"display_name"`
	Revision        string                 `json:"revision"`
	BundleMode      string                 `json:"bundle_mode"`
	Profiles        []ManagedProfileChoice `json:"profiles"`
	SelectedIDs     []string               `json:"selected_ids"`
	FetchedAt       string                 `json:"fetched_at"`
	FromCache       bool                   `json:"from_cache"`
	Status          string                 `json:"status"`
}

type SettingsView struct {
	ConfigurationSource string       `json:"configuration_source"`
	Profile             LocalProfile `json:"profile"`
	TransportKeySet     bool         `json:"transport_key_set"`
	BackgroundResident  bool         `json:"background_resident"`
	AutomaticUpdates    bool         `json:"automatic_updates"`
	RemoteEnabled       bool         `json:"remote_enabled"`
	RemoteServer        string       `json:"remote_server"`
	RemoteDeviceID      string       `json:"remote_device_id"`
}

type EngineEvent struct {
	Kind                    string           `json:"kind"`
	Message                 string           `json:"message,omitempty"`
	ProfileID               string           `json:"profile_id,omitempty"`
	ProfileName             string           `json:"profile_name,omitempty"`
	BundleID                string           `json:"bundle_id,omitempty"`
	BundleName              string           `json:"bundle_name,omitempty"`
	ListenPort              int              `json:"listen_port,omitempty"`
	TotalProfiles           int              `json:"total_profiles,omitempty"`
	ActiveProfiles          int              `json:"active_profiles,omitempty"`
	ConnectingProfiles      int              `json:"connecting_profiles,omitempty"`
	ReconnectingProfiles    int              `json:"reconnecting_profiles,omitempty"`
	FailedProfiles          int              `json:"failed_profiles,omitempty"`
	RetryAfterSeconds       int              `json:"retry_after_seconds,omitempty"`
	RetryAttempt            int              `json:"retry_attempt,omitempty"`
	ConfiguredSchedulerMode string           `json:"configured_scheduler_mode,omitempty"`
	EffectiveSchedulerMode  string           `json:"effective_scheduler_mode,omitempty"`
	SchedulerSwitches       uint64           `json:"scheduler_switches,omitempty"`
	Paths                   int              `json:"paths,omitempty"`
	Connections             int64            `json:"connections,omitempty"`
	Sent                    int64            `json:"sent,omitempty"`
	Received                int64            `json:"received,omitempty"`
	UDPConnections          int64            `json:"udp_connections,omitempty"`
	Retransmits             uint64           `json:"retransmits,omitempty"`
	Dropped                 uint64           `json:"dropped,omitempty"`
	WindowWaits             uint64           `json:"window_waits,omitempty"`
	PathStats               []PathMetric     `json:"path_stats,omitempty"`
	Resources               *ResourceMetric  `json:"resources,omitempty"`
	Lifecycle               *LifecycleMetric `json:"lifecycle,omitempty"`
}

type PathMetric struct {
	ID                  int     `json:"id"`
	Address             string  `json:"address"`
	Connected           bool    `json:"connected"`
	Role                string  `json:"role,omitempty"`
	RoleReason          string  `json:"role_reason,omitempty"`
	RTTMS               float64 `json:"rtt_ms"`
	GoodputBPS          float64 `json:"goodput_bps"`
	MeasuredDeliveryBPS float64 `json:"measured_delivery_bps,omitempty"`
	DeliverySamples     uint64  `json:"delivery_samples,omitempty"`
	QueueBytes          int64   `json:"queue_bytes"`
	OutstandingBytes    int64   `json:"outstanding_bytes"`
	BudgetBytes         int64   `json:"budget_bytes,omitempty"`
	DialAttempts        uint64  `json:"dial_attempts,omitempty"`
	Errors              uint64  `json:"errors"`
	LastError           string  `json:"last_error,omitempty"`
}

type ResourceMetric struct {
	ActiveStreams              int   `json:"active_streams"`
	ClosingStreams             int   `json:"closing_streams,omitempty"`
	LocalConnections           int   `json:"local_connections,omitempty"`
	LifecycleOpening           int   `json:"lifecycle_opening,omitempty"`
	LifecycleOpenBidirectional int   `json:"lifecycle_open_bidirectional,omitempty"`
	LifecycleHalfClosed        int   `json:"lifecycle_half_closed,omitempty"`
	DataIdleOver30s            int   `json:"data_idle_over_30s,omitempty"`
	DataIdleOver1m             int   `json:"data_idle_over_1m,omitempty"`
	DataIdleOver5m             int   `json:"data_idle_over_5m,omitempty"`
	PendingFrames              int   `json:"pending_frames"`
	PendingFrameLimit          int   `json:"pending_frame_limit"`
	ReceiveCreditBytes         int64 `json:"receive_credit_bytes"`
	ReceiveCreditLimitBytes    int64 `json:"receive_credit_limit_bytes"`
	ReceiveAllocatedBytes      int64 `json:"receive_allocated_bytes"`
	ReceiveAllocatedLimitBytes int64 `json:"receive_allocated_limit_bytes"`
	DataPendingFrames          int   `json:"data_pending_frames,omitempty"`
	ControlPendingFrames       int   `json:"control_pending_frames,omitempty"`
	WindowBlockedWriters       int   `json:"window_blocked_writers,omitempty"`
}

type LifecycleMetric struct {
	StreamsOpened uint64 `json:"streams_opened,omitempty"`
	StreamsClosed uint64 `json:"streams_closed,omitempty"`
	CarrierJoins  uint64 `json:"carrier_joins,omitempty"`
	CarrierDrops  uint64 `json:"carrier_drops,omitempty"`
}

type RemoteView struct {
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	Status    string `json:"status"`
	DeviceID  string `json:"device_id"`
	Server    string `json:"server"`
}

type UpdateView struct {
	Automatic bool   `json:"automatic"`
	Status    string `json:"status"`
	Available bool   `json:"available"`
	Version   string `json:"version"`
}

type AppState struct {
	Version        string                 `json:"version"`
	Platform       string                 `json:"platform"`
	Running        bool                   `json:"running"`
	Busy           bool                   `json:"busy"`
	DesiredRunning bool                   `json:"desired_running"`
	Status         string                 `json:"status"`
	Problem        string                 `json:"problem"`
	Settings       SettingsView           `json:"settings"`
	Managed        ManagedSummary         `json:"managed"`
	Remote         RemoteView             `json:"remote"`
	Update         UpdateView             `json:"update"`
	Event          EngineEvent            `json:"event"`
	ProfileStatus  map[string]string      `json:"profile_status"`
	ProfileErrors  map[string]string      `json:"profile_errors"`
	ProfileEvents  map[string]EngineEvent `json:"profile_events"`
	Logs           []string               `json:"logs"`
}
