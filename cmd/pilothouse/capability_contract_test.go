package main

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/frostyard/pilothouse/internal/web"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the contract-test harness described in the mill plan for
// issue #54: it builds the *real* module registry via newRegistry(...) (the
// same function run() calls) and a real web.NewServer, then drives the
// assembled app entirely through Server.Handler() HTTP round trips against
// a fake broker. c10 established the full-capability fixture; this chunk
// (c11) extends the same harness with three degraded fixtures (no-journald,
// no-systemd, no-engines) and generalizes the assertions into a single
// runner (runCapabilityContractFixture) driven purely by the fixture's
// capability.Set — the full-capability case is just that same runner called
// with every capability present (an "empty exclusion set"), so nothing here
// is a second, parallel implementation of the full-capability assertions.

// contractIdentity is the authenticated identity used by every contract
// test: an administrator, so every module's admin-gated view (activity,
// logs, files) actually queries the broker and renders its real content
// rather than an early "access denied" page that would never exercise the
// module's Query call.
var contractIdentity = auth.Identity{Admin: true, Groups: []string{"wheel"}, UID: 1000, Username: "operator"}

// contractCSRF is the fixed CSRF token fakeCapabilityBroker returns from
// both Login and Session. Contract tests that need a POST to pass
// ValidateAction (rather than short-circuit on a CSRF mismatch before ever
// reaching a module's capability-gated logic) send this value back as the
// "csrf" form field.
const contractCSRF = "contract-csrf"

// fullCapabilitySet returns every capability.ID the vocabulary defines,
// matching a host with every optional dependency present — today's
// unchanged, pre-capability-gating behavior.
func fullCapabilitySet() capability.Set {
	return capability.New(
		capability.Systemd,
		capability.Journald,
		capability.Updex,
		capability.Sysext,
		capability.Bootc,
		capability.RPMOStree,
		capability.AutoupdateRPMOStree,
		capability.AutoupdateBootc,
		capability.Podman,
		capability.Docker,
		capability.Incus,
	)
}

// noJournaldCapabilitySet matches a host with every capability present
// except journald: systemd itself is present (services state/actions,
// storage remote-mount actions, backups, and maintenance all keep
// working), but the journal-dependent surfaces (the services journal tab
// and the whole logs module) require Systemd AND Journald and go absent.
func noJournaldCapabilitySet() capability.Set {
	return withoutCapabilities(fullCapabilitySet(), capability.Journald)
}

// noSystemdCapabilitySet matches a host with every capability present
// except systemd: services, the storage remote-mount routes, backups,
// maintenance, and logs (which also needs systemd) all go absent, while
// storage's own inventory (QueryStorageState has no capability requirement
// per docs/capabilities.md) stays available.
func noSystemdCapabilitySet() capability.Set {
	return withoutCapabilities(fullCapabilitySet(), capability.Systemd)
}

// noEnginesCapabilitySet matches a host with every capability present
// except the three container engines: podman, docker, and incus all go
// absent as whole modules, and nothing else is affected.
func noEnginesCapabilitySet() capability.Set {
	return withoutCapabilities(fullCapabilitySet(), capability.Podman, capability.Docker, capability.Incus)
}

// withoutCapabilities returns fullCapabilitySet() minus the given IDs, by
// rebuilding a Set from capability.Set.List() (the only way to enumerate
// a Set's members) filtered against excluded.
func withoutCapabilities(full capability.Set, excluded ...capability.ID) capability.Set {
	remaining := make([]capability.ID, 0, len(full.List()))
	for _, id := range full.List() {
		keep := true
		for _, excludedID := range excluded {
			if id == excludedID {
				keep = false
				break
			}
		}
		if keep {
			remaining = append(remaining, id)
		}
	}
	return capability.New(remaining...)
}

// capabilityRequirements is the binding broker-ID → required-capabilities
// table transcribed from docs/capabilities.md's "Handler capability table"
// (the AND-semantics capability list each ID needs present, per its
// HasAll-checked module/route gate). Per the mill plan's "Why the contract
// test is grounded, not a second hardcoded copy" section, this is the one
// deliberately hand-maintained list in this phase: docs/capabilities.md is
// itself derived from cmd/pilothoused/main.go's actual registration guards,
// and c12 (the closing chunk) re-confirms this table stays in sync with
// that document. A nil/empty value means the ID has no capability
// requirement (callable regardless of the active fixture).
var capabilityRequirements = map[string][]capability.ID{
	// Actions (35)
	broker.ActionFilesUpload:                      nil,
	broker.ActionDockerRemove:                     {capability.Docker},
	broker.ActionDockerRemoveImage:                {capability.Docker},
	broker.ActionDockerRestart:                    {capability.Docker},
	broker.ActionDockerStart:                      {capability.Docker},
	broker.ActionDockerStop:                       {capability.Docker},
	broker.ActionIncusRemove:                      {capability.Incus},
	broker.ActionIncusRemoveImage:                 {capability.Incus},
	broker.ActionIncusRestart:                     {capability.Incus},
	broker.ActionIncusStart:                       {capability.Incus},
	broker.ActionIncusStop:                        {capability.Incus},
	broker.ActionMaintenanceReboot:                {capability.Systemd},
	broker.ActionPodmanRemove:                     {capability.Podman},
	broker.ActionPodmanRemoveImage:                {capability.Podman},
	broker.ActionPodmanRestart:                    {capability.Podman},
	broker.ActionPodmanStart:                      {capability.Podman},
	broker.ActionPodmanStop:                       {capability.Podman},
	broker.ActionSysextDisable:                    {capability.Updex, capability.Sysext},
	broker.ActionSysextEnable:                     {capability.Updex, capability.Sysext},
	broker.ActionSysextRefresh:                    {capability.Sysext},
	broker.ActionSysextUpdate:                     {capability.Updex},
	broker.ActionServicesDisable:                  {capability.Systemd},
	broker.ActionServicesEnable:                   {capability.Systemd},
	broker.ActionServicesResetFailed:              {capability.Systemd},
	broker.ActionServicesRestart:                  {capability.Systemd},
	broker.ActionServicesStart:                    {capability.Systemd},
	broker.ActionServicesStop:                     {capability.Systemd},
	broker.ActionStorageCreateNFS:                 {capability.Systemd},
	broker.ActionStorageCreateSMBGuest:            {capability.Systemd},
	broker.ActionStorageCreateSMBCredentials:      {capability.Systemd},
	broker.ActionStorageCreateSMBGuestOwned:       {capability.Systemd},
	broker.ActionStorageCreateSMBCredentialsOwned: {capability.Systemd},
	broker.ActionStorageMount:                     {capability.Systemd},
	broker.ActionStorageUnmount:                   {capability.Systemd},
	broker.ActionStorageDelete:                    {capability.Systemd},
	// Queries (16)
	broker.QueryActivity:         nil,
	broker.QueryBackupsState:     {capability.Systemd},
	broker.QueryCapabilities:     nil,
	broker.QueryDockerLogs:       {capability.Docker},
	broker.QueryDockerState:      {capability.Docker},
	broker.QueryIncusState:       {capability.Incus},
	broker.QueryJobs:             nil,
	broker.QueryLogs:             {capability.Systemd, capability.Journald},
	broker.QueryMaintenanceState: {capability.Systemd},
	broker.QueryPodmanLogs:       {capability.Podman},
	broker.QueryPodmanState:      {capability.Podman},
	broker.QueryServicesJournal:  {capability.Systemd, capability.Journald},
	broker.QueryServicesState:    {capability.Systemd},
	broker.QueryStorageState:     nil,
	broker.QueryFilesDownload:    nil,
	broker.QueryFilesList:        nil,
}

// fakeCapabilityBroker implements web.BrokerClient. Session always succeeds
// for any token; Login always succeeds and returns contractIdentity; Query
// answers broker.QueryCapabilities with the configured capability.Set and
// otherwise fills the caller's target with a minimal valid canned
// zero-value-shaped response (an empty JSON object for struct targets, an
// empty JSON array for slice/array targets) so every module page can query
// its state and render without erroring, regardless of which broker ID it
// calls. Action/StreamAction/StreamQuery all succeed trivially. Every entry
// point that carries a broker ID (Query, Action, StreamAction, StreamQuery)
// first checks that ID's required capabilities (per capabilityRequirements)
// against the fixture's capability.Set and calls t.Fatal if the web side
// ever invokes a broker ID whose required capability is absent from the
// active fixture — proving the web side never attempts a gated-off broker
// call, not merely that the page around it 404s.
type fakeCapabilityBroker struct {
	t            *testing.T
	capabilities capability.Set
}

func newFakeCapabilityBroker(t *testing.T, caps capability.Set) *fakeCapabilityBroker {
	return &fakeCapabilityBroker{t: t, capabilities: caps}
}

// requireAvailable fails the test immediately if id's required capabilities
// (per capabilityRequirements) are not all present in the fixture's
// capability.Set. An id missing from the table also fails the test, since
// docs/capabilities.md's table is supposed to cover every registered broker
// ID (c12 confirms this against the live source; here an unlisted ID most
// likely means this table fell out of sync while a new ID was added).
func (b *fakeCapabilityBroker) requireAvailable(id string) {
	b.t.Helper()
	required, known := capabilityRequirements[id]
	if !known {
		b.t.Fatalf("fake broker received call for broker ID %q, which is not present in capabilityRequirements; add it (see docs/capabilities.md)", id)
		return
	}
	if !b.capabilities.HasAll(required...) {
		b.t.Fatalf("fake broker received call for broker ID %q whose required capability %v is absent from the active fixture; the web side must never invoke a gated-off broker call", id, required)
	}
}

func (b *fakeCapabilityBroker) Action(_ context.Context, _ string, id string, _ map[string]string, _ string) error {
	b.requireAvailable(id)
	return nil
}

func (b *fakeCapabilityBroker) Health(context.Context) error { return nil }

func (b *fakeCapabilityBroker) Login(context.Context, string, string, string) (broker.LoginResponse, error) {
	return broker.LoginResponse{
		Session: broker.SessionResponse{CSRF: contractCSRF, Identity: contractIdentity},
		Token:   "contract-token",
	}, nil
}

func (b *fakeCapabilityBroker) Logout(context.Context, string) error { return nil }

func (b *fakeCapabilityBroker) Query(_ context.Context, _, id string, _ map[string]string, target any) error {
	b.requireAvailable(id)
	if id == broker.QueryCapabilities {
		encoded, err := json.Marshal(b.capabilities)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	}
	return cannedQueryResponse(target)
}

func (b *fakeCapabilityBroker) Session(context.Context, string) (broker.SessionResponse, error) {
	return broker.SessionResponse{CSRF: contractCSRF, Identity: contractIdentity}, nil
}

func (b *fakeCapabilityBroker) StreamAction(_ context.Context, _ string, id string, _ map[string]string, _ io.Reader) error {
	b.requireAvailable(id)
	return nil
}

func (b *fakeCapabilityBroker) StreamQuery(_ context.Context, _ string, id string, _ map[string]string) (broker.StreamResult, error) {
	b.requireAvailable(id)
	return broker.StreamResult{}, nil
}

// cannedQueryResponse fills target (always a pointer, as every host.Query
// caller in this codebase passes one) with a minimal valid zero-value JSON
// response shaped to target's underlying kind: an empty array for a
// slice/array-typed target (e.g. []audit.Record, []jobs.Job), an empty
// object otherwise (every module State/Snapshot/Logs/Journal struct).
// Deriving the shape from target's real type, rather than hand-listing a
// response per broker.Query* ID, means this keeps working as modules and
// their state types change without needing per-ID maintenance here.
func cannedQueryResponse(target any) error {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	switch value.Elem().Kind() {
	case reflect.Slice, reflect.Array:
		return json.Unmarshal([]byte("[]"), target)
	default:
		return json.Unmarshal([]byte("{}"), target)
	}
}

// newCapabilityContractServer builds the production registry via
// newRegistry("", "") and wires it into a real web.NewServer backed by
// brokerClient, returning both the registry (so tests can enumerate the
// real module list) and the assembled HTTP handler. Using newRegistry(...)
// rather than a hand-built module list is the whole point of this harness:
// per docs/agents/skills/completeness-tests-need-live-source-of-truth.md,
// a completeness assertion is only meaningful when it is checked against
// the live production wiring, not a second copy of the module list that
// could silently drift from it.
func newCapabilityContractServer(t *testing.T, brokerClient web.BrokerClient) (*platform.Registry, http.Handler) {
	t.Helper()
	registry, err := newRegistry("", "")
	require.NoError(t, err)
	server, err := web.NewServer(registry, brokerClient, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)
	return registry, server.Handler()
}

var loginCSRFPattern = regexp.MustCompile(`name="csrf" value="([^"]*)"`)

// loginSession drives the real POST /login flow — GET /login to recover the
// server's per-instance login CSRF token from the rendered form, then POST
// credentials — and returns the resulting session cookie. This is the only
// way to populate internal/web.Server's capability cache from outside
// package web (login is what triggers refreshCapabilities), so every
// contract test needs it before asserting on capability-gated nav/routes.
func loginSession(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	getRequest := httptest.NewRequest(http.MethodGet, "/login", nil)
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, getRequest)
	require.Equal(t, http.StatusOK, getRecorder.Code)
	match := loginCSRFPattern.FindStringSubmatch(getRecorder.Body.String())
	require.Lenf(t, match, 2, "login csrf token not found in rendered login page: %s", getRecorder.Body.String())

	form := url.Values{"csrf": {match[1]}, "username": {"operator"}, "password": {"password"}}
	postRequest := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	postRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRecorder := httptest.NewRecorder()
	handler.ServeHTTP(postRecorder, postRequest)
	require.Equal(t, http.StatusSeeOther, postRecorder.Code, "login did not redirect: %s", postRecorder.Body.String())

	for _, cookie := range postRecorder.Result().Cookies() {
		if cookie.Name == "pilothouse_session" {
			return cookie
		}
	}
	t.Fatal("login did not set a session cookie")
	return nil
}

// --- link crawling -----------------------------------------------------
//
// crawlLinks scans a page of rendered HTML for every <a href="..."> and
// <form ...action="...">, so a fixture's contract test can prove "no
// rendered page links to a 404ing route" by actually requesting what a
// user's browser would request, rather than guessing at route names.

var (
	anchorHrefPattern = regexp.MustCompile(`<a\b[^>]*\bhref="([^"]*)"`)
	formTagPattern    = regexp.MustCompile(`<form\b[^>]*>`)
	formActionPattern = regexp.MustCompile(`\baction="([^"]*)"`)
	formMethodPattern = regexp.MustCompile(`\bmethod="([^"]*)"`)
)

type crawledLink struct {
	method string
	target string
}

// crawlLinks extracts every same-origin anchor href and form action found
// in body. Anchor targets are always requested with GET; form targets use
// the form's declared method (defaulting to GET, matching HTML's own
// default), uppercased to match http.MethodGet/http.MethodPost. Duplicate
// (method, target) pairs collapse to a single entry.
func crawlLinks(body string) []crawledLink {
	seen := map[string]bool{}
	var links []crawledLink
	addLink := func(method, target string) {
		target = html.UnescapeString(target)
		if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
			return
		}
		key := method + " " + target
		if seen[key] {
			return
		}
		seen[key] = true
		links = append(links, crawledLink{method: method, target: target})
	}
	for _, match := range anchorHrefPattern.FindAllStringSubmatch(body, -1) {
		addLink(http.MethodGet, match[1])
	}
	for _, tag := range formTagPattern.FindAllString(body, -1) {
		actionMatch := formActionPattern.FindStringSubmatch(tag)
		if actionMatch == nil {
			continue
		}
		method := http.MethodGet
		if methodMatch := formMethodPattern.FindStringSubmatch(tag); methodMatch != nil {
			method = strings.ToUpper(methodMatch[1])
		}
		addLink(method, actionMatch[1])
	}
	return links
}

// assertNoDeadLinks crawls body (rendered from source, e.g. "GET /" or
// "GET /storage") and asserts that none of its links/form actions resolve
// to a 404 through handler, using cookie for authentication.
func assertNoDeadLinks(t *testing.T, handler http.Handler, cookie *http.Cookie, source, body string) {
	t.Helper()
	for _, link := range crawlLinks(body) {
		var request *http.Request
		if link.method == http.MethodGet {
			request = httptest.NewRequest(link.method, link.target, nil)
		} else {
			request = httptest.NewRequest(link.method, link.target, strings.NewReader(url.Values{"csrf": {contractCSRF}}.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assert.NotEqualf(t, http.StatusNotFound, recorder.Code,
			"%s: rendered link/form %s %s (found on %s) leads to a 404", t.Name(), link.method, link.target, source)
	}
}

// --- sub-routes not covered by any module's Manifest().Path ------------
//
// Every module's primary route is checked generically via
// platform.Available(module, caps) against manifest.Path. Several modules
// also mount secondary routes gated at a finer grain (route-level, or with
// a stricter capability requirement than the module's own
// RequiredCapabilities — the services journal tab and the whole logs
// module both need systemd AND journald). contractSubRoutes enumerates
// every one of those secondary routes so the degraded fixtures exercise
// them explicitly, per docs/agents/skills/gate-every-call-path-not-just-
// routes-and-nav.md and partial-gate-modules-need-full-view-element-audit.md.
var sampleUnit = "sample.service"
var sampleDefinitionID = strings.Repeat("0123456789abcdef", 2) // 32 hex chars
var sampleContainerID = strings.Repeat("a1b2c3d4e5f60789", 4)  // 64 hex chars

var contractSubRoutes = []struct {
	method       string
	path         string
	requirements []capability.ID
}{
	{http.MethodGet, "/services/" + sampleUnit + "/logs", []capability.ID{capability.Systemd, capability.Journald}},
	{http.MethodPost, "/services/" + sampleUnit + "/start", []capability.ID{capability.Systemd}},
	{http.MethodGet, "/logs", []capability.ID{capability.Systemd, capability.Journald}},
	{http.MethodGet, "/storage/mounts/new", []capability.ID{capability.Systemd}},
	{http.MethodPost, "/storage/mounts", []capability.ID{capability.Systemd}},
	{http.MethodPost, "/storage/mounts/" + sampleDefinitionID + "/mount", []capability.ID{capability.Systemd}},
	{http.MethodPost, "/maintenance/reboot", []capability.ID{capability.Systemd}},
	{http.MethodGet, "/podman/containers/" + sampleContainerID + "/logs", []capability.ID{capability.Podman}},
	{http.MethodPost, "/podman/containers/" + sampleContainerID + "/start", []capability.ID{capability.Podman}},
	{http.MethodPost, "/podman/images/" + sampleContainerID + "/remove", []capability.ID{capability.Podman}},
	{http.MethodGet, "/docker/containers/" + sampleContainerID + "/logs", []capability.ID{capability.Docker}},
	{http.MethodPost, "/docker/containers/" + sampleContainerID + "/start", []capability.ID{capability.Docker}},
	{http.MethodPost, "/docker/images/" + sampleContainerID + "/remove", []capability.ID{capability.Docker}},
	{http.MethodPost, "/incus/instances/sample/start", []capability.ID{capability.Incus}},
	{http.MethodPost, "/incus/images/sample-fingerprint/remove", []capability.ID{capability.Incus}},
}

// runCapabilityContractFixture drives the full contract-test assertion
// suite against a single fixture identified by caps: it builds a real
// registry + web.NewServer over a fake broker configured with caps, logs
// in, then asserts — across all four registries the spec calls out —
// that no route, navigation entry, dashboard card, query, action, or
// stream reference exists for a capability caps does not have, while
// everything whose capability caps does have keeps working. Called with
// fullCapabilitySet() (no exclusions), this reduces exactly to c10's
// full-capability assertions; called with a degraded fixture, the same
// code exercises the "gated absent" side without being a second,
// hand-duplicated implementation of either case.
func runCapabilityContractFixture(t *testing.T, caps capability.Set) {
	t.Helper()
	registry, handler := newCapabilityContractServer(t, newFakeCapabilityBroker(t, caps))
	cookie := loginSession(t, handler)

	modules := registry.Modules()
	require.NotEmpty(t, modules)

	dashboardRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	dashboardRequest.AddCookie(cookie)
	dashboardRecorder := httptest.NewRecorder()
	handler.ServeHTTP(dashboardRecorder, dashboardRequest)
	require.Equal(t, http.StatusOK, dashboardRecorder.Code)
	dashboardBody := dashboardRecorder.Body.String()

	for _, module := range modules {
		manifest := module.Manifest()
		available := platform.Available(module, caps)

		if available {
			assert.Containsf(t, dashboardBody, manifest.Name,
				"fixture: dashboard is missing a nav/dashboard reference for available module %q", manifest.ID)
		} else {
			assert.NotContainsf(t, dashboardBody, manifest.Name,
				"fixture: dashboard unexpectedly references gated-absent module %q", manifest.ID)
		}

		routeRequest := httptest.NewRequest(http.MethodGet, manifest.Path, nil)
		routeRequest.AddCookie(cookie)
		routeRecorder := httptest.NewRecorder()
		handler.ServeHTTP(routeRecorder, routeRequest)
		if available {
			assert.NotEqualf(t, http.StatusNotFound, routeRecorder.Code,
				"fixture: available module %q primary route %s returned 404", manifest.ID, manifest.Path)
			assertNoDeadLinks(t, handler, cookie, manifest.Path, routeRecorder.Body.String())
		} else {
			assert.Equalf(t, http.StatusNotFound, routeRecorder.Code,
				"fixture: gated-absent module %q primary route %s did not return 404", manifest.ID, manifest.Path)
		}
	}

	assertNoDeadLinks(t, handler, cookie, "/", dashboardBody)

	// Storage is the plan's one partial-gate module (docs/agents/skills/
	// partial-gate-modules-need-full-view-element-audit.md): its inventory
	// page (GET /storage) has no capability requirement and must stay
	// available in every fixture, but its remote-mount controls (the "Add
	// remote mount" link, gated together with the Mount/Unmount/Delete
	// forms per storage.Module.Mount's remoteMountCapabilities) require
	// systemd. This is checked explicitly, not just inferred from the
	// dead-link crawl, because the acceptance criteria call it out by name.
	storageRequest := httptest.NewRequest(http.MethodGet, "/storage", nil)
	storageRequest.AddCookie(cookie)
	storageRecorder := httptest.NewRecorder()
	handler.ServeHTTP(storageRecorder, storageRequest)
	require.Equal(t, http.StatusOK, storageRecorder.Code,
		"fixture: storage inventory (GET /storage) must stay available regardless of capabilities")
	if caps.Has(capability.Systemd) {
		assert.Contains(t, storageRecorder.Body.String(), "Add remote mount",
			"fixture: storage page should render the remote-mount control when systemd is present")
	} else {
		assert.NotContains(t, storageRecorder.Body.String(), "Add remote mount",
			"fixture: storage page rendered a remote-mount control despite systemd being absent")
	}

	for _, route := range contractSubRoutes {
		expectAvailable := caps.HasAll(route.requirements...)
		var request *http.Request
		if route.method == http.MethodGet {
			request = httptest.NewRequest(route.method, route.path, nil)
		} else {
			request = httptest.NewRequest(route.method, route.path, strings.NewReader(url.Values{"csrf": {contractCSRF}}.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if expectAvailable {
			assert.NotEqualf(t, http.StatusNotFound, recorder.Code,
				"fixture: sub-route %s %s should be available (requires %v) but returned 404", route.method, route.path, route.requirements)
		} else {
			assert.Equalf(t, http.StatusNotFound, recorder.Code,
				"fixture: sub-route %s %s should be gated absent (requires %v) but did not return 404", route.method, route.path, route.requirements)
		}
	}
}

// TestCapabilityContractFullCapabilityFixture proves the trivial,
// highest-value property first: under a fixture with every capability
// present (today's behavior, unchanged by the whole capability-gating
// phase), nothing regresses. This is runCapabilityContractFixture called
// with an empty exclusion set — the same runner c11's degraded fixtures
// use below — so the full-capability behavior established in c10 is
// re-asserted unchanged by construction, not by a parallel copy of the
// assertions.
func TestCapabilityContractFullCapabilityFixture(t *testing.T) {
	runCapabilityContractFixture(t, fullCapabilitySet())
}

// TestCapabilityContractDegradedFixtures exercises the three degraded
// fixtures named in the mill plan for issue #54, chunk c11: no-journald
// (services keeps working, journal/logs go absent), no-systemd (services,
// storage's remote-mount routes, backups, maintenance, and logs all go
// absent, storage inventory stays), and no-engines (podman/docker/incus
// all go absent together). Each subtest reuses the exact same
// runCapabilityContractFixture assertions the full-capability fixture
// above uses, driven purely by the fixture's capability.Set.
func TestCapabilityContractDegradedFixtures(t *testing.T) {
	t.Run("no-journald", func(t *testing.T) {
		runCapabilityContractFixture(t, noJournaldCapabilitySet())
	})
	t.Run("no-systemd", func(t *testing.T) {
		runCapabilityContractFixture(t, noSystemdCapabilitySet())
	})
	t.Run("no-engines", func(t *testing.T) {
		runCapabilityContractFixture(t, noEnginesCapabilitySet())
	})
}
