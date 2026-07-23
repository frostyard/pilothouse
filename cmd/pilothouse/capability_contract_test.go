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
	"github.com/frostyard/pilothouse/internal/modules/storage"
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
	switch id {
	case broker.QueryCapabilities:
		encoded, err := json.Marshal(b.capabilities)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	case broker.QueryStorageState:
		encoded, err := json.Marshal(cannedStorageSnapshot())
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	default:
		return cannedQueryResponse(target)
	}
}

// cannedStorageSnapshot returns a storage.Snapshot carrying one managed,
// mounted remote mount (ID "remote:"+sampleDefinitionID, matching the
// definition ID contractSubRoutes exercises against the mount/delete
// sub-routes). Per docs/agents/skills/canned-fixtures-need-populated-data-
// for-what-they-assert.md, an empty Snapshot can never render
// ManagedMountTable's per-mount Unmount/Delete forms under any fixture, so
// an assertion that those forms are absent under a gated fixture would be
// vacuously true — it would pass identically whether the gating logic
// correctly hid the forms or whether the forms were deleted outright. A
// populated managed mount makes the "forms present when available, absent
// when gated" assertion actually exercise storage's per-mount conditional
// rendering (internal/modules/storage/views.templ's ManagedMountTable),
// not just the top-level "Add remote mount" link.
func cannedStorageSnapshot() storage.Snapshot {
	return storage.Snapshot{
		Mounts: []storage.Mount{
			{
				ID:      "remote:" + sampleDefinitionID,
				Managed: true,
				State:   "mounted",
				Health:  storage.HealthHealthy,
				Source:  "nfs.example.com:/export/contract",
				Target:  "/mnt/contract",
			},
		},
	}
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

// --- scoped nav/dashboard region assertions -----------------------------
//
// The nav (primary navigation, rendered by internal/web's Layout) and the
// dashboard cards (rendered by internal/web's Dashboard, inside
// <section id="dashboard">) are two distinct web-side registries per the
// spec's contract-test requirement. Checking manifest.Name anywhere in the
// whole page conflates them — the sidebar nav link and a dashboard card
// heading both happen to contain the module's Name, so a regression that
// dropped only one of the two would still pass a whole-page Contains check.
// These helpers scope each assertion to its own region so nav and dashboard
// are proven independently, and are reused both on GET / and on every other
// authenticated module page (nav is rendered identically everywhere).

var (
	navSectionPattern       = regexp.MustCompile(`(?s)<nav\b[^>]*aria-label="Primary navigation"[^>]*>(.*?)</nav>`)
	dashboardSectionPattern = regexp.MustCompile(`(?s)<section\b[^>]*\bid="dashboard"[^>]*>(.*)</main>`)
)

// extractRequiredSection returns the first submatch of pattern in body, or
// fails the test if pattern does not match — a nav/dashboard region that
// can't be located means the page's markup shape changed underneath this
// harness, which is itself worth failing loudly on rather than silently
// asserting against an empty string.
func extractRequiredSection(t *testing.T, pattern *regexp.Regexp, body, source, label string) string {
	t.Helper()
	match := pattern.FindStringSubmatch(body)
	require.NotNilf(t, match, "%s: could not locate the %s region in rendered HTML", source, label)
	return match[1]
}

// assertNavigation scopes to the primary navigation region of body (as
// rendered on source, e.g. "GET /" or "GET /services") and asserts, for
// every module in modules, that its nav link (an anchor whose href is its
// manifest Path) is present when the module is available under caps and
// absent when it is gated off — proving the navigation registry
// independently of the dashboard registry.
func assertNavigation(t *testing.T, source, body string, modules []platform.Module, caps capability.Set) {
	t.Helper()
	navSection := extractRequiredSection(t, navSectionPattern, body, source, "primary navigation")
	for _, module := range modules {
		manifest := module.Manifest()
		href := `href="` + manifest.Path + `"`
		if platform.Available(module, caps) {
			assert.Containsf(t, navSection, href,
				"%s: primary navigation is missing a link for available module %q", source, manifest.ID)
		} else {
			assert.NotContainsf(t, navSection, href,
				"%s: primary navigation unexpectedly links to gated-absent module %q", source, manifest.ID)
		}
	}
}

// assertDashboardCards scopes to the <section id="dashboard"> region of
// dashboardBody and asserts, for every module in modules, that its
// dashboard card is absent when the module is gated off under caps. When
// the module is available, it asserts the card is present only if
// cardModules says that module actually renders one when available — some
// modules (activity, fleet, files, logs) always return no dashboard cards
// by design, so their absence from this section is not a regression to
// flag. cardModules is derived from the real Dashboard() output directly
// (see dashboardCardModuleIDs), not hand-listed here, so it can't drift
// from which modules actually render cards.
//
// Presence is checked by href="<manifest.Path>" (every card-producing
// module's Summary/Hero component links back to its own module, e.g.
// internal/modules/podman/views.templ's `<a class="card-link"
// href="/podman">`) rather than by manifest.Name: several modules' card
// headings are a different phrase than their nav Name (podman's Manifest
// Name is "Containers" but its card heading is "Podman"; sysext's Name is
// "Extensions" but its card heading is "System extensions"), so Name is not
// a reliable in-card marker. manifest.Name is checked too, as an
// alternative match, because internal/web.ModuleErrorCard (rendered when a
// module's Dashboard() call errors) shows only the module's Name with no
// href.
func assertDashboardCards(t *testing.T, dashboardBody string, modules []platform.Module, caps capability.Set, cardModules map[string]bool) {
	t.Helper()
	dashboardSection := extractRequiredSection(t, dashboardSectionPattern, dashboardBody, "GET /", "dashboard cards")
	for _, module := range modules {
		manifest := module.Manifest()
		href := `href="` + manifest.Path + `"`
		present := strings.Contains(dashboardSection, href) || strings.Contains(dashboardSection, manifest.Name)
		if !platform.Available(module, caps) {
			assert.Falsef(t, present,
				"GET /: dashboard unexpectedly renders a card for gated-absent module %q", manifest.ID)
			continue
		}
		if cardModules[manifest.ID] {
			assert.Truef(t, present,
				"GET /: dashboard is missing a card for available module %q", manifest.ID)
		}
	}
}

// dashboardProbeHost is a minimal platform.Host used only to call a
// module's Dashboard(ctx, host) directly, bypassing internal/web.Server's
// dashboard() HTTP handler entirely. Capabilities always reports every
// capability present (dashboardCardModuleIDs only wants to know what a
// module renders when available, not whether it currently is), and Query
// answers with the same canned zero-value response fakeCapabilityBroker
// uses. Every other Host method is unused by any module's Dashboard()
// implementation (which takes only a context and a Host, never an
// http.ResponseWriter/*http.Request) and returns an inert zero value.
type dashboardProbeHost struct{}

func (dashboardProbeHost) Capabilities(context.Context) capability.Set { return fullCapabilitySet() }
func (dashboardProbeHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool {
	return false
}
func (dashboardProbeHost) CSRFToken(*http.Request) string { return "" }
func (dashboardProbeHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (dashboardProbeHost) Identity(*http.Request) auth.Identity { return auth.Identity{} }
func (dashboardProbeHost) Query(_ context.Context, _ string, _ map[string]string, target any) error {
	return cannedQueryResponse(target)
}
func (dashboardProbeHost) Render(http.ResponseWriter, *http.Request, platform.Page) error { return nil }
func (dashboardProbeHost) ValidateAction(http.ResponseWriter, *http.Request) bool         { return false }
func (dashboardProbeHost) ValidateActionToken(http.ResponseWriter, *http.Request, string) bool {
	return false
}
func (dashboardProbeHost) StreamAction(context.Context, *http.Request, string, map[string]string, io.Reader) error {
	return nil
}
func (dashboardProbeHost) StreamQuery(context.Context, string, map[string]string) (broker.StreamResult, error) {
	return broker.StreamResult{}, nil
}

// dashboardCardModuleIDs determines, for each module in registry, whether
// that module renders a dashboard card at all when available — a static
// property of the module's own Dashboard() implementation
// (activity/fleet/files/logs always return nil cards by design; the rest
// always return at least one card, or a platform.ModuleErrorCard carrying
// the module's Name on error — see internal/web.Server.dashboard), wholly
// independent of which capability fixture is active or of the server's own
// dashboard-assembly loop. Calling module.Dashboard() directly here (rather
// than deriving this from a GET / round trip through that same assembly
// loop) matters: if the loop itself regressed and silently dropped an
// available module's card, a derivation sourced from that loop's own output
// would "learn" the card is expectedly absent and hide the very regression
// assertDashboardCards exists to catch.
func dashboardCardModuleIDs(t *testing.T, registry *platform.Registry) map[string]bool {
	t.Helper()
	host := dashboardProbeHost{}
	cardModules := make(map[string]bool, len(registry.Modules()))
	for _, module := range registry.Modules() {
		manifest := module.Manifest()
		cards, err := module.Dashboard(context.Background(), host)
		cardModules[manifest.ID] = err != nil || len(cards) > 0
	}
	return cardModules
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
	cardModules := dashboardCardModuleIDs(t, registry)

	dashboardRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	dashboardRequest.AddCookie(cookie)
	dashboardRecorder := httptest.NewRecorder()
	handler.ServeHTTP(dashboardRecorder, dashboardRequest)
	require.Equal(t, http.StatusOK, dashboardRecorder.Code)
	dashboardBody := dashboardRecorder.Body.String()

	// Navigation and dashboard cards are two distinct web-side registries;
	// each is asserted in its own scoped region so a regression in one
	// can't hide behind the other still containing the module's Name.
	assertNavigation(t, "/", dashboardBody, modules, caps)
	assertDashboardCards(t, dashboardBody, modules, caps, cardModules)

	for _, module := range modules {
		manifest := module.Manifest()
		available := platform.Available(module, caps)

		routeRequest := httptest.NewRequest(http.MethodGet, manifest.Path, nil)
		routeRequest.AddCookie(cookie)
		routeRecorder := httptest.NewRecorder()
		handler.ServeHTTP(routeRecorder, routeRequest)
		if available {
			assert.NotEqualf(t, http.StatusNotFound, routeRecorder.Code,
				"fixture: available module %q primary route %s returned 404", manifest.ID, manifest.Path)
			routeBody := routeRecorder.Body.String()
			// Every other authenticated page shares the same Layout nav, so
			// a gated-absent module's link must stay gone (and every
			// available module's link must stay present) here too, not
			// only on GET /. This is scoped to a normal (200) render: a
			// module whose local unprivileged read depends on a host tool
			// that isn't installed in this environment (e.g. sysext's page
			// shells out to updex directly, not through the broker) can
			// legitimately answer with a non-Layout error body instead —
			// that's an environment/tooling concern the capability-gating
			// contract this fixture proves has nothing to do with.
			if routeRecorder.Code == http.StatusOK {
				assertNavigation(t, manifest.Path, routeBody, modules, caps)
			}
			assertNoDeadLinks(t, handler, cookie, manifest.Path, routeBody)
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
	// cannedStorageSnapshot() (returned by the fake broker for every
	// fixture) carries one managed, mounted remote mount so the per-mount
	// Unmount/Delete forms actually render when available — proving they
	// are hidden when gated, not just vacuously absent from an empty
	// mount table (docs/agents/skills/canned-fixtures-need-populated-data-
	// for-what-they-assert.md).
	storageRequest := httptest.NewRequest(http.MethodGet, "/storage", nil)
	storageRequest.AddCookie(cookie)
	storageRecorder := httptest.NewRecorder()
	handler.ServeHTTP(storageRecorder, storageRequest)
	require.Equal(t, http.StatusOK, storageRecorder.Code,
		"fixture: storage inventory (GET /storage) must stay available regardless of capabilities")
	storageBody := storageRecorder.Body.String()
	unmountFormAction := `action="/storage/mounts/` + sampleDefinitionID + `/unmount"`
	deleteFormAction := `action="/storage/mounts/` + sampleDefinitionID + `/delete"`
	if caps.Has(capability.Systemd) {
		assert.Contains(t, storageBody, "Add remote mount",
			"fixture: storage page should render the remote-mount control when systemd is present")
		assert.Contains(t, storageBody, unmountFormAction,
			"fixture: storage page should render the per-mount Unmount form when systemd is present")
		assert.Contains(t, storageBody, deleteFormAction,
			"fixture: storage page should render the per-mount Delete form when systemd is present")
	} else {
		assert.NotContains(t, storageBody, "Add remote mount",
			"fixture: storage page rendered a remote-mount control despite systemd being absent")
		assert.NotContains(t, storageBody, unmountFormAction,
			"fixture: storage page rendered a per-mount Unmount form despite systemd being absent")
		assert.NotContains(t, storageBody, deleteFormAction,
			"fixture: storage page rendered a per-mount Delete form despite systemd being absent")
	}
	assertNoDeadLinks(t, handler, cookie, "/storage", storageBody)

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
