package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

//nolint:unused // Limits are consumed by subsequent storage module tasks.
const (
	maxResources     = 4096
	maxRelations     = 8192
	maxMounts        = 1024
	maxFindings      = 512
	maxDetails       = 32
	maxFieldBytes    = 4 * 1024
	maxAdapterBytes  = 4 * 1024 * 1024
	maxSnapshotBytes = 2 * 1024 * 1024
	maxGraphDepth    = 32
)

type Health string

const (
	HealthHealthy  Health = "healthy"
	HealthUnknown  Health = "unknown"
	HealthWarning  Health = "warning"
	HealthCritical Health = "critical"
)

type Availability string

const (
	BackendAvailable   Availability = "available"
	BackendUnsupported Availability = "unsupported"
	BackendUnavailable Availability = "unavailable"
	BackendTimedOut    Availability = "timed-out"
	BackendTruncated   Availability = "truncated"
)

type Manager interface {
	State(context.Context) (Snapshot, error)
}

type Snapshot struct {
	Backends    []BackendStatus `json:"backends"`
	CollectedAt time.Time       `json:"collected_at"`
	Findings    []Finding       `json:"findings"`
	Mounts      []Mount         `json:"mounts"`
	Relations   []Relation      `json:"relations"`
	Resources   []Resource      `json:"resources"`
	Summary     Summary         `json:"summary"`
	Truncated   bool            `json:"truncated"`
}

type Summary struct {
	ActiveMounts        int    `json:"active_mounts"`
	FreeBytes           uint64 `json:"free_bytes"`
	HighestHealth       Health `json:"highest_health"`
	UnavailableBackends int    `json:"unavailable_backends"`
	UnhealthyResources  int    `json:"unhealthy_resources"`
	UsableBytes         uint64 `json:"usable_bytes"`
	UsedBytes           uint64 `json:"used_bytes"`
}

type Resource struct {
	Details   []Detail `json:"details"`
	Health    Health   `json:"health"`
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Path      string   `json:"path,omitempty"`
	SizeBytes uint64   `json:"size_bytes,omitempty"`
	State     string   `json:"state,omitempty"`
}

type Detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type Relation struct {
	From string `json:"from"`
	Kind string `json:"kind"`
	To   string `json:"to"`
}

type Mount struct {
	AvailableBytes uint64   `json:"available_bytes"`
	Filesystem     string   `json:"filesystem"`
	Health         Health   `json:"health"`
	ID             string   `json:"id"`
	Managed        bool     `json:"managed"`
	Options        []string `json:"options"`
	ReadOnly       bool     `json:"read_only"`
	ResourceID     string   `json:"resource_id,omitempty"`
	Source         string   `json:"source"`
	State          string   `json:"state"`
	Target         string   `json:"target"`
	TotalBytes     uint64   `json:"total_bytes"`
	UsedBytes      uint64   `json:"used_bytes"`
	UsedPercent    float64  `json:"used_percent"`
}

type BackendStatus struct {
	Availability Availability `json:"availability"`
	CollectedAt  time.Time    `json:"collected_at"`
	Name         string       `json:"name"`
}

type Finding struct {
	Detail     string `json:"detail"`
	ResourceID string `json:"resource_id"`
	Severity   Health `json:"severity"`
	Title      string `json:"title"`
}

func stableID(kind, identity string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + identity))
	return kind + ":" + hex.EncodeToString(sum[:8])
}

func healthRank(value Health) int {
	switch value {
	case HealthCritical:
		return 3
	case HealthWarning:
		return 2
	case HealthUnknown:
		return 1
	default:
		return 0
	}
}
