package attention

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// gatedProvider is a fakeProvider that also implements platform.CapabilityGate
// and counts every Health call, so tests can prove findings() never invokes
// Health on a provider whose module is unavailable — not merely that its own
// routes 404 — the same gap the "gate every call path" skill documents.
type gatedProvider struct {
	fakeProvider
	required   []capability.ID
	healthHits *int
}

func (p gatedProvider) Health(ctx context.Context, host platform.Host) ([]platform.Finding, error) {
	*p.healthHits++
	return p.fakeProvider.Health(ctx, host)
}

func (p gatedProvider) RequiredCapabilities() []capability.ID { return p.required }

type capsHost struct {
	caps capability.Set
}

func (h capsHost) Capabilities(context.Context) capability.Set { return h.caps }
func (capsHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool {
	return true
}
func (capsHost) CSRFToken(*http.Request) string { return "" }
func (capsHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (capsHost) Identity(*http.Request) auth.Identity                           { return auth.Identity{} }
func (capsHost) Query(context.Context, string, map[string]string, any) error    { return nil }
func (capsHost) Render(http.ResponseWriter, *http.Request, platform.Page) error { return nil }
func (capsHost) ValidateAction(http.ResponseWriter, *http.Request) bool         { return true }
func (capsHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool {
	return true
}
func (capsHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}
func (capsHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

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

// TestFindingsSkipsProviderWhenRequiredCapabilityAbsent proves the fix for
// the defect the "gate every call path, not just routes and nav" skill
// documents: findings() must not call Health at all on a provider whose
// module's RequiredCapabilities aren't satisfied — not merely omit its
// result, and not report it as "unavailable" either, since an absent module
// is not a collection failure. Health-call counting (not just the returned
// findings) proves the method itself is never invoked.
func TestFindingsSkipsProviderWhenRequiredCapabilityAbsent(t *testing.T) {
	hits := 0
	module := New(
		fakeProvider{manifest: platform.Manifest{ID: "system", Name: "System", Path: "/system"}, findings: []platform.Finding{{ID: "system.disk", Severity: platform.SeverityWarning}}},
		gatedProvider{
			fakeProvider: fakeProvider{manifest: platform.Manifest{ID: "backups", Name: "Backups", Path: "/backups"}, findings: []platform.Finding{{ID: "backups.nightly", Severity: platform.SeverityCritical}}},
			required:     []capability.ID{capability.Systemd},
			healthHits:   &hits,
		},
	)
	host := capsHost{caps: capability.New(capability.Journald)} // systemd absent

	findings := module.findings(context.Background(), host)

	assert.Equal(t, 0, hits, "gated provider's Health must never be called when its required capability is absent")
	require.Len(t, findings, 1)
	assert.Equal(t, "system.disk", findings[0].ID)
}

// TestFindingsCallsGatedProviderWhenRequiredCapabilityPresent proves the
// gate is additive: a provider implementing platform.CapabilityGate still
// contributes its findings, exactly as before, once its required
// capability is present.
func TestFindingsCallsGatedProviderWhenRequiredCapabilityPresent(t *testing.T) {
	hits := 0
	module := New(
		gatedProvider{
			fakeProvider: fakeProvider{manifest: platform.Manifest{ID: "backups", Name: "Backups", Path: "/backups"}, findings: []platform.Finding{{ID: "backups.nightly", Severity: platform.SeverityCritical}}},
			required:     []capability.ID{capability.Systemd},
			healthHits:   &hits,
		},
	)
	host := capsHost{caps: capability.New(capability.Systemd)}

	findings := module.findings(context.Background(), host)

	assert.Equal(t, 1, hits)
	require.Len(t, findings, 1)
	assert.Equal(t, "backups.nightly", findings[0].ID)
}

func TestSummaryAndPageRenderChevronIcons(t *testing.T) {
	findings := []platform.Finding{{
		ID:       "attention.disk",
		Severity: platform.SeverityWarning,
		Source:   "System",
		Title:    "Disk space is low",
		Detail:   "Only 10% remains.",
		Path:     "/attention/disk",
	}}

	var summaryOutput strings.Builder
	require.NoError(t, Summary(findings).Render(context.Background(), &summaryOutput))
	summaryHTML := summaryOutput.String()
	assert.Contains(t, summaryHTML, "View all")
	assert.Contains(t, summaryHTML, "m9 18 6-6-6-6")
	assert.NotContains(t, summaryHTML, "@web.Icon")

	var pageOutput strings.Builder
	require.NoError(t, Page(findings).Render(context.Background(), &pageOutput))
	pageHTML := pageOutput.String()
	assert.Contains(t, pageHTML, "Review")
	assert.Contains(t, pageHTML, "m9 18 6-6-6-6")
	assert.NotContains(t, pageHTML, "@web.Icon")
}
