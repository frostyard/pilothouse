package attention

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct {
	providers []platform.HealthProvider
}

func New(providers ...platform.HealthProvider) *Module {
	return &Module{providers: slices.Clone(providers)}
}

func (m *Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	findings := m.findings(ctx, host)
	return []platform.DashboardCard{{Component: Summary(findings), Order: 5, Span: platform.SpanFull}}, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{ID: "attention", Name: "Attention", Description: "Current host and service issues", Icon: "alert", Order: 5, Path: "/attention"}
}

func (m *Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /attention", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		_ = host.Render(w, r, platform.Page{Active: "attention", Body: Page(m.findings(ctx, host)), Eyebrow: "Operational status", Title: "Attention required"})
	})
}

// findings collects Health results from every registered provider, skipping
// any provider whose module is unavailable on this host. A provider that
// implements platform.CapabilityGate is treated the same way its module's
// own routes (platform.Gate) and nav/dashboard entry (platform.Available)
// are: when host's current capability.Set doesn't satisfy the provider's
// RequiredCapabilities, its Health method is never called and it
// contributes nothing — not even an "unavailable" placeholder — since an
// absent module is not the same as one whose status collection failed.
func (m *Module) findings(ctx context.Context, host platform.Host) []platform.Finding {
	findings := make([]platform.Finding, 0)
	var caps capability.Set
	capsLoaded := false
	for _, provider := range m.providers {
		if gate, ok := provider.(platform.CapabilityGate); ok {
			if !capsLoaded {
				caps = host.Capabilities(ctx)
				capsLoaded = true
			}
			if !caps.HasAll(gate.RequiredCapabilities()...) {
				// The provider's module is absent on this host (the same
				// capability check platform.Gate applies to its routes and
				// platform.Available applies to its nav/dashboard entry).
				// Skip it entirely: no Health call, and no "unavailable"
				// finding either, since an absent module is not the same
				// as one whose status collection failed.
				continue
			}
		}
		providerCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		provided, err := provider.Health(providerCtx, host)
		cancel()
		if err != nil {
			manifest := provider.Manifest()
			findings = append(findings, platform.Finding{ID: manifest.ID + ".unavailable", Source: manifest.Name, Severity: platform.SeverityUnknown, Title: manifest.Name + " status is unavailable", Detail: "Pilothouse could not collect current status.", Path: manifest.Path})
			continue
		}
		findings = append(findings, provided...)
	}
	slices.SortStableFunc(findings, func(a, b platform.Finding) int {
		if severityOrder(a.Severity) != severityOrder(b.Severity) {
			return severityOrder(a.Severity) - severityOrder(b.Severity)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return findings
}

func severityOrder(severity platform.Severity) int {
	switch severity {
	case platform.SeverityCritical:
		return 0
	case platform.SeverityUnknown:
		return 1
	case platform.SeverityWarning:
		return 2
	default:
		return 3
	}
}
