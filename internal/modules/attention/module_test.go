package attention

import (
	"context"
	"errors"
	"testing"

	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
)

type fakeProvider struct {
	err      error
	findings []platform.Finding
	manifest platform.Manifest
}

func (p fakeProvider) Health(context.Context, platform.Host) ([]platform.Finding, error) {
	return p.findings, p.err
}

func (p fakeProvider) Manifest() platform.Manifest { return p.manifest }

func TestFindingsSortsAndReportsUnavailableProvider(t *testing.T) {
	module := New(
		fakeProvider{manifest: platform.Manifest{ID: "system", Name: "System", Path: "/system"}, findings: []platform.Finding{{ID: "system.disk", Severity: platform.SeverityWarning}}},
		fakeProvider{manifest: platform.Manifest{ID: "services", Name: "Services", Path: "/services"}, err: errors.New("unavailable")},
		fakeProvider{manifest: platform.Manifest{ID: "workloads", Name: "Workloads", Path: "/workloads"}, findings: []platform.Finding{{ID: "workloads.api", Severity: platform.SeverityCritical}}},
	)

	findings := module.findings(context.Background(), nil)
	assert.Equal(t, []string{"workloads.api", "services.unavailable", "system.disk"}, []string{findings[0].ID, findings[1].ID, findings[2].ID})
	assert.Equal(t, platform.SeverityUnknown, findings[1].Severity)
	assert.Equal(t, "/services", findings[1].Path)
}
