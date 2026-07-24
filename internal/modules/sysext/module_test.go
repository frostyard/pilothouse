package sysext

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The four capability fixtures this module's gates distinguish. sysext's
// whole-module gate is HasAny(Updex, Sysext), but its three mutating
// sub-routes split across three *different* requirements, so "both present"
// and "neither present" alone cannot tell an OR gate from an AND gate or
// prove the per-action split — the two single-tool fixtures are what make
// each row of the matrix in TestRouteCapabilityMatrix discriminating.
var (
	bothTools   = capability.New(capability.Updex, capability.Sysext)
	updexOnly   = capability.New(capability.Updex)
	sysextOnly  = capability.New(capability.Sysext)
	neitherTool = capability.New(capability.Bootc)
)

type fakeHost struct {
	actions    []string
	caps       capability.Set
	queryError error
	queryIDs   []string
	state      ExtensionsState
}

func (h *fakeHost) Capabilities(context.Context) capability.Set { return h.caps }

func (*fakeHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool { return true }

func (*fakeHost) CSRFToken(*http.Request) string { return "token" }

func (h *fakeHost) Execute(_ context.Context, _ *http.Request, action string, _ map[string]string) error {
	h.actions = append(h.actions, action)
	return nil
}

func (*fakeHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: true} }

func (h *fakeHost) Query(_ context.Context, id string, _ map[string]string, target any) error {
	h.queryIDs = append(h.queryIDs, id)
	if h.queryError != nil {
		return h.queryError
	}
	if id == broker.QueryExtensionsState {
		*target.(*ExtensionsState) = h.state
	}
	return nil
}

func (*fakeHost) Render(w http.ResponseWriter, _ *http.Request, page platform.Page) error {
	return page.Body.Render(context.Background(), w)
}

func (*fakeHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }

func (*fakeHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool { return true }

func (*fakeHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}

func (*fakeHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

// contractState is a representative aggregate: one managed extension (which
// may carry an enable/disable control) and one installed-but-unmanaged one
// (which never may). Populated rather than empty so per-row assertions are
// not vacuously true.
func contractState() ExtensionsState {
	return ExtensionsState{
		SysextAvailable: true,
		UpdexAvailable:  true,
		Extensions: []Extension{
			{Enabled: true, Installed: true, Managed: true, Name: "managed-ext", Version: "1.0.0"},
			{Installed: true, Name: "unmanaged-ext", Version: "2.0.0"},
		},
	}
}

func newTestHost(caps capability.Set) *fakeHost {
	return &fakeHost{caps: caps, state: contractState()}
}

func mount(host platform.Host) *http.ServeMux {
	mux := http.NewServeMux()
	New().Mount(mux, host)
	return mux
}

func post(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"csrf": {"token"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

// TestModuleImplementsAnyOfGateOnly pins the module's gate shape. The
// expected value is written out by hand from the spec (updex OR sysext),
// never read back from platform.AvailableAny, per
// docs/agents/skills/dont-use-the-gate-under-test-as-the-test-oracle.md.
func TestModuleImplementsAnyOfGateOnly(t *testing.T) {
	module := New()
	var gateAny platform.CapabilityGateAny = module
	assert.Equal(t, []capability.ID{capability.Updex, capability.Sysext}, gateAny.RequiredAnyCapabilities(),
		"Extensions must be gated on updex OR sysext, matching the daemon's registerExtensions guard")

	_, isAllOf := any(module).(platform.CapabilityGate)
	assert.False(t, isAllOf,
		"Extensions must not also implement platform.CapabilityGate; the two whole-module gates are alternatives, not layers")
}

// TestRouteCapabilityMatrix drives real ServeMux round trips across every
// route x capability-fixture cell. The single-tool fixtures are the point:
// they prove the module gate is a genuine OR (GET /sysext survives either
// tool alone) while the mutating routes keep the daemon's narrower
// requirements (enable/disable need both; refresh needs only sysext; update
// needs only updex), exactly as cmd/pilothoused's registerSysextActions
// splits them.
func TestRouteCapabilityMatrix(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		caps    capability.Set
		get     bool // GET /sysext served
		enable  bool // POST /sysext/{name}/{enable,disable} served
		refresh bool // POST /sysext/actions/refresh served
		update  bool // POST /sysext/actions/update served
	}{
		{name: "both tools", caps: bothTools, get: true, enable: true, refresh: true, update: true},
		{name: "updex only", caps: updexOnly, get: true, enable: false, refresh: false, update: true},
		{name: "sysext only", caps: sysextOnly, get: true, enable: false, refresh: true, update: false},
		{name: "neither tool", caps: neitherTool, get: false, enable: false, refresh: false, update: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			host := newTestHost(testCase.caps)
			mux := mount(host)

			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/sysext", nil))
			if testCase.get {
				assert.Equal(t, http.StatusOK, recorder.Code, "GET /sysext should be served")
				assert.Equal(t, []string{broker.QueryExtensionsState}, host.queryIDs,
					"GET /sysext must read through the fixed broker query, and only that query")
			} else {
				assert.Equal(t, http.StatusNotFound, recorder.Code, "GET /sysext should 404")
				assert.Empty(t, host.queryIDs,
					"a host advertising neither tool must never invoke QueryExtensionsState")
			}

			for _, route := range []struct {
				path   string
				served bool
				action string
			}{
				{"/sysext/managed-ext/enable", testCase.enable, broker.ActionSysextEnable},
				{"/sysext/managed-ext/disable", testCase.enable, broker.ActionSysextDisable},
				{"/sysext/actions/refresh", testCase.refresh, broker.ActionSysextRefresh},
				{"/sysext/actions/update", testCase.update, broker.ActionSysextUpdate},
			} {
				host.actions = nil
				recorder := post(t, mux, route.path)
				if route.served {
					assert.NotEqual(t, http.StatusNotFound, recorder.Code, "POST %s should be served", route.path)
					assert.Equal(t, []string{route.action}, host.actions,
						"POST %s must invoke exactly its own broker action", route.path)
				} else {
					assert.Equal(t, http.StatusNotFound, recorder.Code, "POST %s should 404", route.path)
					assert.Empty(t, host.actions,
						"POST %s must not reach a broker action the daemon never registered", route.path)
				}
			}
		})
	}
}

// TestUnknownActionsAre404 keeps the per-action capability branch from
// swallowing the pre-existing unknown-action behavior.
func TestUnknownActionsAre404(t *testing.T) {
	host := newTestHost(bothTools)
	mux := mount(host)
	assert.Equal(t, http.StatusNotFound, post(t, mux, "/sysext/managed-ext/frobnicate").Code)
	assert.Equal(t, http.StatusNotFound, post(t, mux, "/sysext/actions/frobnicate").Code)
	assert.Empty(t, host.actions, "an unknown action must never reach the broker")
}

// TestDashboardReadsThroughTheBrokerQuery proves the dashboard card path —
// which internal/web/server.go calls directly, outside any route — is also
// query-driven rather than backed by a local manager.
func TestDashboardReadsThroughTheBrokerQuery(t *testing.T) {
	host := newTestHost(bothTools)
	cards, err := New().Dashboard(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Equal(t, []string{broker.QueryExtensionsState}, host.queryIDs)

	builder := &strings.Builder{}
	require.NoError(t, cards[0].Component.Render(context.Background(), builder))
	assert.Contains(t, builder.String(), "System extensions")
}

// TestGetPageReportsQueryFailure keeps a broker-side failure a 503 rather
// than a panic or a silently empty page.
func TestGetPageReportsQueryFailure(t *testing.T) {
	host := newTestHost(bothTools)
	host.queryError = assert.AnError
	recorder := httptest.NewRecorder()
	mount(host).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/sysext", nil))
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

// TestGatedPageOmitsControlsForAbsentTools is the view-element half of the
// gate: it drives the real GET /sysext handler (not Page directly) so the
// flags under test are the ones the production handler computes from
// host.Capabilities, per docs/agents/skills/partial-gate-modules-need-full-
// view-element-audit.md.
func TestGatedPageOmitsControlsForAbsentTools(t *testing.T) {
	render := func(caps capability.Set) string {
		host := newTestHost(caps)
		recorder := httptest.NewRecorder()
		mount(host).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/sysext", nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		return recorder.Body.String()
	}

	both := render(bothTools)
	assert.Contains(t, both, `action="/sysext/actions/refresh"`)
	assert.Contains(t, both, `action="/sysext/actions/update"`)
	assert.Contains(t, both, "/sysext/managed-ext/disable")

	updex := render(updexOnly)
	assert.NotContains(t, updex, `action="/sysext/actions/refresh"`,
		"refresh needs systemd-sysext; its form must not render without it")
	assert.Contains(t, updex, `action="/sysext/actions/update"`)
	assert.NotContains(t, updex, "/sysext/managed-ext/disable",
		"enable/disable need both tools; the per-row form must not render with only updex")

	sysextTool := render(sysextOnly)
	assert.Contains(t, sysextTool, `action="/sysext/actions/refresh"`)
	assert.NotContains(t, sysextTool, `action="/sysext/actions/update"`,
		"update needs updex; its form must not render without it")
	assert.NotContains(t, sysextTool, "/sysext/managed-ext/disable",
		"enable/disable need both tools; the per-row form must not render with only systemd-sysext")

	// The unmanaged row has no updex definition, so no capability set may
	// give it an enable/disable control.
	for _, body := range []string{both, updex, sysextTool} {
		assert.NotContains(t, body, "/sysext/unmanaged-ext/enable")
		assert.NotContains(t, body, "/sysext/unmanaged-ext/disable")
	}
}

// TestPackageNeverReachesAHostTool is the structural half of "the web process
// performs no local sysext reads". The gates and the query-based reads above
// prove the runtime behavior; this proves the property that makes it hold by
// construction, so a future edit cannot quietly reintroduce a local
// updex/systemd-sysext invocation into the package the web binary links.
//
// Every non-test file in this package must be free of os/exec, of any
// CommandRunner, and of an import of the extctl subpackage that owns them.
// The check reads the directory itself rather than a hand-listed set of
// files, so a newly added file is covered the moment it lands.
func TestPackageNeverReachesAHostTool(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		file, parseErr := parser.ParseFile(token.NewFileSet(), name, nil, parser.ParseComments)
		require.NoError(t, parseErr)
		for _, imported := range file.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			assert.NotEqual(t, "os/exec", path, "%s must not import os/exec", name)
			assert.NotEqual(t, "github.com/frostyard/pilothouse/internal/modules/sysext/extctl", path,
				"%s must not import the exec-backed subpackage; the dependency runs the other way", name)
		}

		// Identifiers only, never raw source text: the doc comments that
		// *describe* this invariant necessarily name CommandRunner, and a
		// substring check would trip over them.
		ast.Inspect(file, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if ok {
				assert.NotEqual(t, "CommandRunner", identifier.Name,
					"%s must not name a CommandRunner; command execution belongs to extctl", name)
			}
			return true
		})
	}
	assert.GreaterOrEqual(t, checked, 4, "expected at least module.go, state.go, manager.go, and helpers.go to be checked")
}
