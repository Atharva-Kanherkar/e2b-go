package e2b

import "time"

// SandboxState is the control-plane lifecycle state of a sandbox.
type SandboxState string

const (
	// SandboxStateRunning indicates the sandbox is running.
	SandboxStateRunning SandboxState = "running"
	// SandboxStatePaused indicates the sandbox is paused.
	SandboxStatePaused SandboxState = "paused"
)

// SandboxNetwork describes control-plane network configuration.
type SandboxNetwork struct {
	AllowPublicTraffic *bool
	AllowOut           []string
	DenyOut            []string
	MaskRequestHost    string
}

// SandboxLifecycle describes timeout and auto-resume behavior.
type SandboxLifecycle struct {
	AutoResume bool
	OnTimeout  string
}

// SandboxVolumeMount describes a volume mounted into a sandbox.
type SandboxVolumeMount struct {
	Name string
	Path string
}

// SandboxInfo is returned by sandbox list/info APIs.
type SandboxInfo struct {
	SandboxID           string
	TemplateID          string
	Name                string
	Metadata            map[string]string
	StartedAt           time.Time
	EndAt               time.Time
	State               SandboxState
	CPUCount            int
	MemoryMB            int
	DiskSizeMB          int
	EnvdVersion         string
	AllowInternetAccess *bool
	Domain              string
	Network             *SandboxNetwork
	Lifecycle           *SandboxLifecycle
	VolumeMounts        []SandboxVolumeMount
}

// ListSandboxesRequest filters and paginates sandbox listing.
type ListSandboxesRequest struct {
	Metadata  map[string]string
	States    []SandboxState
	Limit     int
	NextToken string
}

// ListSandboxesResponse contains one page of sandboxes and the next token.
type ListSandboxesResponse struct {
	Sandboxes []SandboxInfo
	NextToken string
}

// ConnectSandboxRequest reconnects to an existing sandbox, resuming it if
// needed.
type ConnectSandboxRequest struct {
	SandboxID          string
	Timeout            time.Duration
	AllowShellFallback bool
}

// SandboxMetricsRequest constrains the requested metric interval.
type SandboxMetricsRequest struct {
	Start time.Time
	End   time.Time
}

// SandboxMetric is a single resource-usage sample from the control plane.
type SandboxMetric struct {
	Timestamp     time.Time
	TimestampUnix int64
	CPUCount      int
	CPUUsedPct    float64
	MemUsed       int64
	MemTotal      int64
	DiskUsed      int64
	DiskTotal     int64
}

// SnapshotInfo describes a persistent sandbox snapshot.
type SnapshotInfo struct {
	SnapshotID string
	Names      []string
}

// CreateSnapshotRequest controls snapshot creation.
type CreateSnapshotRequest struct {
	Name string
}

// ListSnapshotsRequest filters and paginates snapshot listing.
type ListSnapshotsRequest struct {
	SandboxID string
	Limit     int
	NextToken string
}

// ListSnapshotsResponse contains one page of snapshots and the next token.
type ListSnapshotsResponse struct {
	Snapshots []SnapshotInfo
	NextToken string
}
