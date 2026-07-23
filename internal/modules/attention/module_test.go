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

// gatedAnyProvider is the any-of counterpart of gatedProvider: it implements
// platform.CapabilityGateAny (and, when requiredAll is non-nil, also
// platform.CapabilityGate) and counts Health calls the same way, so tests
// can prove findings() honors the any-of whole-module gate on this direct
// call path too — not just platform.GateAny-wrapped routes and the web
// shell's nav/dashboard.
type gatedAnyProvider struct {
	fakeProvider
	requiredAny []capability.ID
	healthHits  *int
}

func (p gatedAnyProvider) Health(ctx context.Context, host platform.Host) ([]platform.Finding, error) {
	*p.healthHits++
	return p.fakeProvider.Health(ctx, host)
}

func (p gatedAnyProvider) RequiredAnyCapabilities() []capability.ID { return p.requiredAny }

// bothGatedProvider implements both whole-module gate interfaces at once, so
// tests can prove findings() applies them as an AND of two defaults, exactly
// as internal/web's moduleAvailable does.
type bothGatedProvider struct {
	gatedAnyProvider
	required []capability.ID
}

func (p bothGatedProvider) RequiredCapabilities() []capability.ID { return p.required }

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

// TestFindingsSkipsProviderWhenNoneOfRequiredAnyCapabilitiesPresent extends
// the "gate every call path" fix to platform.CapabilityGateAny: a provider
// whose module is absent because none of its any-of capabilities are present
// must have Health left uncalled here too, exactly as a CapabilityGate
// provider does. Without this, a CapabilityGateAny module would be hidden
// from nav/dashboard and 404 on its own routes while /attention still called
// its Health and rendered findings (or an "unavailable" placeholder) for it.
func TestFindingsSkipsProviderWhenNoneOfRequiredAnyCapabilitiesPresent(t *testing.T) {
	hits := 0
	module := New(
		fakeProvider{manifest: platform.Manifest{ID: "system", Name: "System", Path: "/system"}, findings: []platform.Finding{{ID: "system.disk", Severity: platform.SeverityWarning}}},
		gatedAnyProvider{
			fakeProvider: fakeProvider{manifest: platform.Manifest{ID: "workloads", Name: "Workloads", Path: "/workloads"}, findings: []platform.Finding{{ID: "workloads.api", Severity: platform.SeverityCritical}}},
			requiredAny:  []capability.ID{capability.Docker, capability.Podman},
			healthHits:   &hits,
		},
	)
	host := capsHost{caps: capability.New(capability.Systemd)} // neither docker nor podman

	findings := module.findings(context.Background(), host)

	assert.Equal(t, 0, hits, "any-of gated provider's Health must never be called when none of its capabilities are present")
	require.Len(t, findings, 1)
	assert.Equal(t, "system.disk", findings[0].ID)
}

// TestFindingsCallsAnyGatedProviderWhenOneRequiredAnyCapabilityPresent proves
// the any-of gate is additive: one of the alternatives being present is
// enough for the provider to contribute its findings normally.
func TestFindingsCallsAnyGatedProviderWhenOneRequiredAnyCapabilityPresent(t *testing.T) {
	hits := 0
	module := New(
		gatedAnyProvider{
			fakeProvider: fakeProvider{manifest: platform.Manifest{ID: "workloads", Name: "Workloads", Path: "/workloads"}, findings: []platform.Finding{{ID: "workloads.api", Severity: platform.SeverityCritical}}},
			requiredAny:  []capability.ID{capability.Docker, capability.Podman},
			healthHits:   &hits,
		},
	)
	host := capsHost{caps: capability.New(capability.Podman)} // only one of the two

	findings := module.findings(context.Background(), host)

	assert.Equal(t, 1, hits)
	require.Len(t, findings, 1)
	assert.Equal(t, "workloads.api", findings[0].ID)
}

// TestFindingsAppliesBothGatesAsAnAnd covers the remaining cells of the
// gate matrix for a provider declaring both interfaces: it is collected only
// when the HasAll gate and the HasAny gate are both satisfied, mirroring
// internal/web's moduleAvailable AND-of-two-defaults composition.
func TestFindingsAppliesBothGatesAsAnAnd(t *testing.T) {
	newModule := func(hits *int) *Module {
		return New(bothGatedProvider{
			gatedAnyProvider: gatedAnyProvider{
				fakeProvider: fakeProvider{manifest: platform.Manifest{ID: "workloads", Name: "Workloads", Path: "/workloads"}, findings: []platform.Finding{{ID: "workloads.api", Severity: platform.SeverityCritical}}},
				requiredAny:  []capability.ID{capability.Docker, capability.Podman},
				healthHits:   hits,
			},
			required: []capability.ID{capability.Systemd},
		})
	}

	for _, tc := range []struct {
		name      string
		caps      capability.Set
		wantHits  int
		wantCount int
	}{
		{name: "neither gate satisfied", caps: capability.New(capability.Journald), wantHits: 0, wantCount: 0},
		{name: "only HasAll gate satisfied", caps: capability.New(capability.Systemd), wantHits: 0, wantCount: 0},
		{name: "only HasAny gate satisfied", caps: capability.New(capability.Docker), wantHits: 0, wantCount: 0},
		{name: "both gates satisfied", caps: capability.New(capability.Systemd, capability.Docker), wantHits: 1, wantCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := 0
			findings := newModule(&hits).findings(context.Background(), capsHost{caps: tc.caps})

			assert.Equal(t, tc.wantHits, hits)
			assert.Len(t, findings, tc.wantCount)
		})
	}
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
