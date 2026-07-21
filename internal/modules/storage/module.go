package storage

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	snapshot, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{Component: SummaryCard(snapshot), Order: 25, Span: platform.SpanHalf}}, nil
}

func (*Module) Health(ctx context.Context, host platform.Host) ([]platform.Finding, error) {
	snapshot, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	findings := make([]platform.Finding, 0, len(snapshot.Findings))
	for _, finding := range snapshot.Findings {
		findings = append(findings, platform.Finding{
			Detail: finding.Detail, ID: "storage." + finding.ResourceID,
			Path: "/storage#" + storageAnchor(finding.ResourceID), Severity: storageSeverity(finding.Severity),
			Source: "Storage", Title: finding.Title,
		})
	}
	return findings, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{ID: "storage", Name: "Storage", Path: "/storage", Icon: "disk", Order: 25}
}

func (*Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /storage", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		snapshot, err := queryState(ctx, host)
		_ = host.Render(w, r, platform.Page{Active: "storage", Body: Page(snapshot, err != nil), Eyebrow: "Storage capacity", Title: "Storage"})
	})
}

func queryState(ctx context.Context, host platform.Host) (Snapshot, error) {
	var snapshot Snapshot
	err := host.Query(ctx, broker.QueryStorageState, nil, &snapshot)
	return snapshot, err
}

func storageSeverity(health Health) platform.Severity {
	switch health {
	case HealthCritical:
		return platform.SeverityCritical
	case HealthWarning:
		return platform.SeverityWarning
	case HealthUnknown:
		return platform.SeverityUnknown
	default:
		return platform.SeverityInfo
	}
}

func storageAnchor(resourceID string) string {
	return strings.Map(func(character rune) rune {
		if character <= unicode.MaxASCII && ((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9')) {
			return character
		}
		return '-'
	}, resourceID)
}
