package platform

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHost is a minimal platform.Host stub whose only interesting behavior
// is Capabilities; every other method is a no-op sufficient to satisfy the
// interface.
type fakeHost struct {
	caps capability.Set
}

func (h fakeHost) Capabilities(context.Context) capability.Set { return h.caps }
func (h fakeHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool {
	return true
}
func (h fakeHost) CSRFToken(*http.Request) string { return "" }
func (h fakeHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (h fakeHost) Identity(*http.Request) auth.Identity                        { return auth.Identity{} }
func (h fakeHost) Query(context.Context, string, map[string]string, any) error { return nil }
func (h fakeHost) Render(http.ResponseWriter, *http.Request, Page) error       { return nil }
func (h fakeHost) ValidateAction(http.ResponseWriter, *http.Request) bool      { return true }
func (h fakeHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool {
	return true
}
func (h fakeHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}
func (h fakeHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

// gatedStubModule embeds stubModule (declared in registry_test.go, same
// package) and additionally implements CapabilityGate.
type gatedStubModule struct {
	stubModule
	required []capability.ID
}

func (m gatedStubModule) RequiredCapabilities() []capability.ID { return m.required }

func TestGateDelegatesWhenAllRequiredCapabilitiesPresent(t *testing.T) {
	host := fakeHost{caps: capability.New(capability.Systemd, capability.Journald)}
	called := false
	next := func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}

	handler := Gate(host, []capability.ID{capability.Systemd, capability.Journald}, next)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/logs", nil))

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, response.Code)
}

func TestGate404sWhenARequiredCapabilityIsMissing(t *testing.T) {
	host := fakeHost{caps: capability.New(capability.Systemd)}
	called := false
	next := func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}

	// Systemd is present but Journald is not: HasAll must fail and the
	// request must 404 without ever reaching next.
	handler := Gate(host, []capability.ID{capability.Systemd, capability.Journald}, next)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/logs", nil))

	assert.False(t, called)
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestGateWithZeroRequiredCapabilitiesIsAlwaysAvailable(t *testing.T) {
	host := fakeHost{} // zero Set: no capabilities present at all
	called := false
	next := func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}

	handler := Gate(host, nil, next)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/system", nil))

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, response.Code)
}

// availableModuleIDs filters registry's modules exactly the way a real
// consumer (internal/web's nav and dashboard filtering) is expected to: it
// reads caps from host.Capabilities — the same production Host method a
// live server calls on every request — and applies each module against the
// production Available predicate. Nothing here reimplements the gating
// decision; both the capability source and the gating logic are the actual
// production code under test.
func availableModuleIDs(t *testing.T, registry *Registry, host Host) []string {
	t.Helper()
	caps := host.Capabilities(context.Background())
	ids := make([]string, 0, len(registry.Modules()))
	for _, module := range registry.Modules() {
		if !Available(module, caps) {
			continue
		}
		ids = append(ids, module.Manifest().ID)
	}
	return ids
}

func TestCapabilityGateModuleExcludedFromRegistryAvailableSetUntilCapabilityPresent(t *testing.T) {
	registry, err := NewRegistry(
		stubModule{manifest: Manifest{ID: "always", Name: "Always", Order: 1, Path: "/always"}},
		gatedStubModule{
			stubModule: stubModule{manifest: Manifest{ID: "gated", Name: "Gated", Order: 2, Path: "/gated"}},
			required:   []capability.ID{capability.Docker},
		},
	)
	require.NoError(t, err)

	// Docker absent: the CapabilityGate module is excluded; the plain
	// module is always included.
	assert.Equal(t, []string{"always"}, availableModuleIDs(t, registry, fakeHost{caps: capability.New(capability.Systemd)}))

	// Docker present: both are included.
	assert.Equal(t, []string{"always", "gated"}, availableModuleIDs(t, registry, fakeHost{caps: capability.New(capability.Docker)}))
}
