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
	"github.com/frostyard/pilothouse/internal/modules/maintenance"
	"github.com/frostyard/pilothouse/internal/modules/storage"
	"github.com/frostyard/pilothouse/internal/modules/sysext"
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
//
// Issue #51's closing chunk extends the same runner with this phase's
// host-image surfaces: the any-of oracle tables for maintenance's
// whole-module gate and QueryHostImageStatus, the spec-named `ucore` and
// `snosi-without-bootc` fixtures plus a `bootc-only` one, populated canned
// responses for QueryMaintenanceState and QueryHostImageStatus, and a
// per-element audit of the Maintenance page's two independently gated
// halves (assertMaintenanceSurfaces). It also records every broker ID the
// web side invokes, so a fixture can assert a call was never made rather
// than only that the page around it 404s.

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
//
// (maintenance keeps working here for two independent reasons — it needs
// only any one of systemd/bootc/rpm-ostree, and all three are present.)
func noJournaldCapabilitySet() capability.Set {
	return withoutCapabilities(fullCapabilitySet(), capability.Journald)
}

// noSystemdCapabilitySet matches a host with every capability present
// except systemd: services, the storage remote-mount routes, backups, and
// logs (which also needs systemd) all go absent, while storage's own
// inventory (QueryStorageState has no capability requirement per
// docs/capabilities.md) stays available. maintenance stays available too:
// its whole-module gate is HasAny(Systemd, Bootc, RPMOStree) and this
// fixture still has bootc and rpm-ostree, so its nav entry, dashboard card,
// and GET /maintenance remain while only its POST /maintenance/reboot
// sub-route goes absent — and its systemd-gated QueryMaintenanceState is
// never called, which the fake broker's requireAvailable guard enforces.
func noSystemdCapabilitySet() capability.Set {
	return withoutCapabilities(fullCapabilitySet(), capability.Systemd)
}

// noEnginesCapabilitySet matches a host with every capability present
// except the three container engines: podman, docker, and incus all go
// absent as whole modules, and nothing else is affected.
func noEnginesCapabilitySet() capability.Set {
	return withoutCapabilities(fullCapabilitySet(), capability.Podman, capability.Docker, capability.Incus)
}

// ucoreCapabilitySet is the spec's "uCore fixture": an image-based host with
// systemd, journald, both host-image sources, and every container engine,
// but no system-extension tooling. It is the fixture the acceptance
// criterion "uCore fixture reports read-only bootc state with supplementary
// rpm-ostree detail" names — the one host shape where the Maintenance page's
// systemd-gated reboot posture and its bootc/rpm-ostree-gated host-image
// section are rendered together.
func ucoreCapabilitySet() capability.Set {
	return capability.New(
		capability.Systemd,
		capability.Journald,
		capability.Bootc,
		capability.RPMOStree,
		capability.Podman,
		capability.Docker,
		capability.Incus,
	)
}

// snosiWithoutBootcCapabilitySet is the spec's "Snosi without bootc"
// fixture: a systemd host with sysext/updex tooling and every container
// engine, but neither bootc nor rpm-ostree. The acceptance criterion is that
// it "remains supported; host-image state is omitted rather than failing" —
// so GET /maintenance still renders (via Systemd), the reboot form and route
// still work, the "Host image" section is absent entirely, and the web side
// never calls QueryHostImageStatus (enforced by the fake broker's
// requireAvailable, and asserted explicitly from its recorded call log).
func snosiWithoutBootcCapabilitySet() capability.Set {
	return capability.New(
		capability.Systemd,
		capability.Journald,
		capability.Updex,
		capability.Sysext,
		capability.Podman,
		capability.Docker,
		capability.Incus,
	)
}

// bootcOnlyCapabilitySet is the inverse extreme the plan calls out: a host
// that advertises bootc and nothing else — no systemd, no rpm-ostree, no
// engines. It is the fixture that proves maintenance's whole-module any-of
// gate is genuinely an OR rather than a disguised systemd gate: the nav
// entry and GET /maintenance must be present, POST /maintenance/reboot must
// 404, and QueryMaintenanceState must never be called.
func bootcOnlyCapabilitySet() capability.Set {
	return capability.New(capability.Bootc)
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
// itself derived from cmd/pilothoused/main.go's actual registration guards.
// Its counterpart on the daemon side, cmd/pilothoused's capabilityTable, is
// diffed against internal/broker/api.go's live constant declarations by
// TestCapabilityTableMirrorsBrokerAPIConstants, so a new broker ID cannot be
// added without that document and that table being updated together — which
// is what keeps this hand-transcribed web-side copy anchored. A nil/empty
// value means the ID has no capability requirement (callable regardless of
// the active fixture).
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
	// Queries (16 of the 17 declared; QueryHostImageStatus is the 17th and
	// lives in capabilityAnyRequirements below, being the API's one any-of ID)
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

// capabilityAnyRequirements is the any-of counterpart of
// capabilityRequirements, for broker IDs whose daemon-side registration guard
// is HasAny rather than HasAll. It is likewise transcribed by hand from
// docs/capabilities.md, which documents QueryHostImageStatus as the table's
// one any-of row (`bootc OR rpm-ostree`) — a call the web side may make
// whenever *either* source is advertised, so checking it with HasAll would
// wrongly demand both.
//
// A broker ID must appear in exactly one of the two maps; appearing in both
// fails the test, as does appearing in neither (see requireAvailable). This
// mirrors the moduleRequiredCapabilities / moduleRequiredAnyCapabilities split
// below, for the same reason.
var capabilityAnyRequirements = map[string][]capability.ID{
	broker.QueryHostImageStatus: {capability.Bootc, capability.RPMOStree},
}

// webSideUngatedBrokerIDs is the exact, closed set of broker IDs whose
// *web-side* capability gate does not exist yet by explicit, documented
// design decision, so a rendered sysext control can still lead to a broker
// call the fixture's host does not advertise. docs/capabilities.md's "Phase
// 1b (#54) — web-side gating complete" section states this outright: "The
// sysext web surface is unchanged and out of scope for #54. The web process
// still constructs sysext.NewSystemManager directly from its own --updex
// config, and no platform.CapabilityGate or platform.Gate is applied to any
// sysext route, navigation entry, dashboard card, or action ... deferred to
// #52." The spec's round-2 clarification 3 repeats it for this phase: #51
// "leaves the existing sysext update reporting and the /sysext link exactly
// as they are on main".
//
// This is a UX gap, not a hole in the privilege boundary: the *daemon* still
// withholds these actions entirely when updex/sysext are absent, which
// cmd/pilothoused/capability_contract_test.go proves for every fixture in
// its own matrix, so such a call fails at the broker rather than executing.
// The exemption is deliberately keyed to individual IDs (not to a module or
// a prefix) so that when #52 lands the web-side gate, deleting these four
// entries is what re-arms the check — and any *other* ID that starts leaking
// through an ungated web control fails immediately, including every ID this
// phase adds.
var webSideUngatedBrokerIDs = map[string]bool{
	broker.ActionSysextDisable: true,
	broker.ActionSysextEnable:  true,
	broker.ActionSysextRefresh: true,
	broker.ActionSysextUpdate:  true,
}

// moduleRequiredCapabilities is the independent, hand-maintained oracle for
// which whole-module capability gate each web module carries, transcribed
// from docs/capabilities.md's "Module-level defaults applied" section — NOT
// derived by calling platform.Available (the production gating predicate
// this harness exists to verify). Per docs/agents/skills/dont-use-the-gate-
// under-test-as-the-test-oracle.md, computing the expected availability by
// calling that same predicate would be tautological: a regression that made
// an "unaffected" module (e.g. system, files, activity) accidentally pick up
// a Systemd gate — or drop one it should keep — would shift both the
// expected and the actual side together, so the degraded fixture would keep
// passing while the "every other module is unaffected" acceptance criterion
// was silently violated. By stating the expected gate here by hand and
// asserting the real route/nav/dashboard behavior against it, that class of
// regression fails the test.
//
// Whole-module gates only. storage is deliberately mapped to `nil` (always
// available) because it is a partial-gate module: its inventory page is
// always present, and its remote-mount sub-routes are gated separately via
// contractSubRoutes and the explicit storage assertions in
// runCapabilityContractFixture. A nil/empty value means the module has no
// whole-module capability requirement. A module ID missing from this map
// fails the test (see expectModuleAvailable), so adding a module to the
// registry forces a deliberate decision here rather than silently defaulting
// to "always available".
var moduleRequiredCapabilities = map[string][]capability.ID{
	"activity":  nil,
	"attention": nil,
	"fleet":     nil,
	"system":    nil,
	"storage":   nil, // partial-gate: inventory always present; remote-mount routes gated (see contractSubRoutes)
	"sysext":    nil,
	"files":     nil,
	"services":  {capability.Systemd},
	"backups":   {capability.Systemd},
	"logs":      {capability.Systemd, capability.Journald},
	"podman":    {capability.Podman},
	"docker":    {capability.Docker},
	"incus":     {capability.Incus},
}

// moduleRequiredAnyCapabilities is the any-of counterpart of
// moduleRequiredCapabilities, for modules whose whole-module gate is
// platform.CapabilityGateAny (HasAny semantics) rather than
// platform.CapabilityGate (HasAll). It is likewise transcribed by hand from
// docs/capabilities.md and docs/modules.md, never derived from
// platform.AvailableAny. maintenance is the only entry: per #51 it reports
// on two independently gated sources — systemd-gated reboot posture, update
// availability, and jobs (QueryMaintenanceState), and bootc-or-rpm-ostree-
// gated host-image status (QueryHostImageStatus) — so the module is present
// whenever any one of the three is, and only its POST /maintenance/reboot
// sub-route stays systemd-only (see contractSubRoutes).
//
// A module ID must appear in exactly one of the two maps; appearing in both
// fails the test, as does appearing in neither.
var moduleRequiredAnyCapabilities = map[string][]capability.ID{
	"maintenance": {capability.Systemd, capability.Bootc, capability.RPMOStree},
}

// allOfPresent reports whether every id is in caps, and anyOfPresent whether
// at least one is. Both are built only from capability.Set.Has, deliberately
// re-deriving all-of / any-of semantics here instead of calling
// capability.Set.HasAll / capability.Set.HasAny.
//
// That indirection is the whole point. This phase's production gates are
// exactly those two aggregation predicates — maintenance's whole-module gate
// is platform.CapabilityGateAny → platform.AvailableAny → caps.HasAny(Systemd,
// Bootc, RPMOStree), and queryHostImage's guard is caps.HasAny(Bootc,
// RPMOStree). Per docs/agents/skills/dont-use-the-gate-under-test-as-the-test-
// oracle.md, computing the expected side with the same predicate would be
// tautological: if HasAny silently degraded into HasAll (or vice versa), the
// expectation and the observed behavior would move together and every any-of
// fixture below would keep passing while the acceptance criterion was
// violated. Combining per-capability Has checks with Go's own control flow
// keeps the oracle independent of the aggregation logic under test.
//
// The zero-ids cases match the documented semantics they stand in for:
// allOfPresent(nil) is true (that is how a "no capability requirement" row
// spells "always available"), anyOfPresent(nil) is false ("any of nothing" has
// nothing to satisfy it).
func allOfPresent(caps capability.Set, ids []capability.ID) bool {
	for _, id := range ids {
		if !caps.Has(id) {
			return false
		}
	}
	return true
}

func anyOfPresent(caps capability.Set, ids []capability.ID) bool {
	for _, id := range ids {
		if caps.Has(id) {
			return true
		}
	}
	return false
}

// expectModuleAvailable is the independent oracle for whether the module with
// the given manifest ID should be available (nav link present, primary route
// non-404, dashboard card allowed) under caps. It consults
// moduleRequiredCapabilities and moduleRequiredAnyCapabilities — the
// hand-maintained, spec-derived tables — and never calls
// platform.Available/platform.AvailableAny (nor the HasAll/HasAny predicates
// those delegate to; see allOfPresent/anyOfPresent above), so the real
// production predicates' actual behavior can be asserted against this
// independent expectation. An unknown module ID fails the test loudly:
// docs/capabilities.md's module table is meant to cover every registered
// module, so an unlisted one most likely means a module was added to the
// registry without recording its gate here.
func expectModuleAvailable(t *testing.T, manifestID string, caps capability.Set) bool {
	t.Helper()
	required, known := moduleRequiredCapabilities[manifestID]
	requiredAny, knownAny := moduleRequiredAnyCapabilities[manifestID]
	switch {
	case known && knownAny:
		t.Fatalf("module %q appears in both moduleRequiredCapabilities and moduleRequiredAnyCapabilities; a module declares at most one whole-module gate", manifestID)
		return false
	case knownAny:
		return anyOfPresent(caps, requiredAny)
	case known:
		return allOfPresent(caps, required)
	default:
		t.Fatalf("module %q is not present in moduleRequiredCapabilities or moduleRequiredAnyCapabilities; record its spec-derived capability gate (see docs/capabilities.md's Module-level defaults)", manifestID)
		return false
	}
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
//
// Two broker IDs answer with representative, populated fixture data rather
// than the generic zero-value canned response: QueryMaintenanceState (see
// cannedMaintenanceState) and QueryHostImageStatus (see hostImage below).
// Both are conditionally rendered surfaces whose per-field markup this
// chunk asserts present-or-absent per fixture, and an empty response would
// make every one of those "absent" assertions vacuously true — the failure
// mode docs/agents/skills/canned-fixtures-need-populated-data-for-what-they-
// assert.md records.
//
// calls records every broker ID the web side actually invoked, so a fixture
// can assert a *negative* directly ("QueryMaintenanceState was never called
// on a bootc-only host") instead of relying only on requireAvailable's
// t.Fatalf, which proves the same thing but leaves no positive record that
// the check ran at all.
type fakeCapabilityBroker struct {
	t            *testing.T
	capabilities capability.Set
	hostImage    maintenance.HostImageStatus
	calls        map[string]int
}

// newFakeCapabilityBroker builds the fake for a fixture. hostImage is passed
// in explicitly rather than defaulted so a fixture can exercise the
// rpm-ostree read-failure shape (RPMOStreeAvailable=false + RPMOStreeError
// set) as well as the fully-successful one; runCapabilityContractFixture
// supplies cannedHostImageStatus() for the ordinary case.
func newFakeCapabilityBroker(t *testing.T, caps capability.Set, hostImage maintenance.HostImageStatus) *fakeCapabilityBroker {
	return &fakeCapabilityBroker{t: t, capabilities: caps, hostImage: hostImage, calls: map[string]int{}}
}

// called reports how many times the web side invoked the given broker ID
// through any of the four entry points.
func (b *fakeCapabilityBroker) called(id string) int { return b.calls[id] }

// requireAvailable fails the test immediately if id's required capabilities
// are not satisfied by the fixture's capability.Set — all of them for an
// all-of ID (capabilityRequirements), at least one of them for an any-of ID
// (capabilityAnyRequirements). An id missing from both tables also fails the
// test, since docs/capabilities.md's table is supposed to cover every
// registered broker ID (cmd/pilothoused's
// TestCapabilityTableMirrorsBrokerAPIConstants confirms that against the live
// internal/broker/api.go declarations; here an unlisted ID most likely means
// these tables fell out of sync while a new ID was added).
//
// The one documented relaxation is webSideUngatedBrokerIDs above.
func (b *fakeCapabilityBroker) requireAvailable(id string) {
	b.t.Helper()
	b.calls[id]++
	required, known := capabilityRequirements[id]
	requiredAny, knownAny := capabilityAnyRequirements[id]
	if webSideUngatedBrokerIDs[id] {
		// Still required to be documented in exactly one table (below's
		// completeness rule is not relaxed) — only the capability check is
		// skipped, for the IDs docs/capabilities.md explicitly records as
		// having no web-side gate yet.
		if !known && !knownAny {
			b.t.Fatalf("broker ID %q is listed in webSideUngatedBrokerIDs but not in capabilityRequirements or capabilityAnyRequirements; add it (see docs/capabilities.md)", id)
		}
		return
	}
	switch {
	case known && knownAny:
		b.t.Fatalf("broker ID %q appears in both capabilityRequirements and capabilityAnyRequirements; an ID carries at most one registration guard", id)
	case knownAny:
		if !anyOfPresent(b.capabilities, requiredAny) {
			b.t.Fatalf("fake broker received call for broker ID %q whose required capabilities %v are all absent from the active fixture; the web side must never invoke a gated-off broker call", id, requiredAny)
		}
	case known:
		if !allOfPresent(b.capabilities, required) {
			b.t.Fatalf("fake broker received call for broker ID %q whose required capability %v is absent from the active fixture; the web side must never invoke a gated-off broker call", id, required)
		}
	default:
		b.t.Fatalf("fake broker received call for broker ID %q, which is not present in capabilityRequirements or capabilityAnyRequirements; add it (see docs/capabilities.md)", id)
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
	case broker.QueryMaintenanceState:
		encoded, err := json.Marshal(cannedMaintenanceState())
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	case broker.QueryHostImageStatus:
		encoded, err := json.Marshal(b.hostImage)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	default:
		return cannedQueryResponse(target)
	}
}

// The values below are the representative host-image fixture data every
// per-field assertion in this harness is checked against. They are shared
// between cannedHostImageStatus and its rpm-ostree-failure variant so the
// two differ in exactly the one dimension under test.
const (
	// bootc-authoritative deployment identity (image reference + digest).
	contractBootedImage    = "quay.io/fedora/fedora-bootc:41"
	contractBootedDigest   = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	contractStagedImage    = "quay.io/fedora/fedora-bootc:42"
	contractStagedDigest   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	contractRollbackImage  = "quay.io/fedora/fedora-bootc:40"
	contractRollbackDigest = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	// rpm-ostree-supplementary detail (version + checksum), which bootc does
	// not provide and which MergeHostImage folds in without ever overriding
	// a bootc-provided field.
	contractBootedVersion  = "41.20260701.0"
	contractBootedChecksum = "aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa7777bbbb8888"
	// the two representative per-source read failures, one per source, so the
	// symmetric BootcAvailable/BootcError and RPMOStreeAvailable/RPMOStreeError
	// pairs are each exercised in both directions.
	contractRPMOStreeError = "run rpm-ostree status: exit status 1"
	contractBootcError     = "run bootc status: exit status 1"
)

// cannedHostImageStatus is the QueryHostImageStatus response the contract
// fixtures use by default: bootc answered, rpm-ostree answered, and every
// conditionally-rendered piece of the Maintenance page's "Host image"
// section has data behind it.
//
// It deliberately carries all three deployment slots — booted, *staged*, and
// *rollback* — plus rpm-ostree's supplementary Version/Checksum on the
// booted deployment, and a non-nil SoftRebootCapable. Per docs/agents/
// skills/canned-fixtures-need-populated-data-for-what-they-assert.md, an
// empty or booted-only response would make this chunk's "no host-image
// detail is rendered under a fixture with no host-image source" assertions
// vacuously true: internal/modules/maintenance/views.templ renders the
// staged and rollback rows only when those slots are non-nil and the
// Version/Checksum lines only when those strings are non-empty, so with
// empty data those markers could never appear under *any* fixture and the
// absence assertions would pass identically if the rendering code were
// deleted outright.
func cannedHostImageStatus() maintenance.HostImageStatus {
	softRebootCapable := true
	return maintenance.HostImageStatus{
		BootcAvailable: true,
		Booted: &maintenance.Deployment{
			Image:    contractBootedImage,
			Digest:   contractBootedDigest,
			Version:  contractBootedVersion,
			Checksum: contractBootedChecksum,
		},
		Staged: &maintenance.Deployment{
			Image:  contractStagedImage,
			Digest: contractStagedDigest,
		},
		Rollback: &maintenance.Deployment{
			Image:  contractRollbackImage,
			Digest: contractRollbackDigest,
		},
		RPMOStreeAvailable: true,
		SoftRebootCapable:  &softRebootCapable,
	}
}

// cannedHostImageStatusRPMOStreeFailed is the symmetric failure fixture: the
// same bootc-authoritative deployments, but rpm-ostree was advertised and
// did not answer, so its own availability/error pair carries the failure and
// its supplementary Version/Checksum detail is simply missing. This is the
// case the acceptance criterion asks for so that "host-image detail is
// present/absent" is proven for rpm-ostree in *both* directions, success and
// failure, rather than only where the source works.
func cannedHostImageStatusRPMOStreeFailed() maintenance.HostImageStatus {
	status := cannedHostImageStatus()
	status.RPMOStreeAvailable = false
	status.RPMOStreeError = contractRPMOStreeError
	// Safe to mutate in place: cannedHostImageStatus allocates fresh
	// Deployment values on every call, so nothing else aliases this pointer.
	status.Booted.Version = ""
	status.Booted.Checksum = ""
	return status
}

// cannedHostImageStatusBootcFailed is the mirror-image failure fixture: bootc
// was advertised and did not answer, while rpm-ostree did. It is what the
// daemon's HostImageManager.Status actually produces in that case, so the
// fixture is representative rather than invented: bootc is authoritative for
// deployment *presence* (MergeHostImage clones its slots and nothing else
// creates them), so a bootc read failure leaves every slot nil, leaves
// soft-reboot eligibility unknown, and leaves rpm-ostree's supplementary
// version/checksum with no bootc-identified deployment to attach to — while
// RPMOStreeAvailable stays true, because that source did answer.
//
// Without this fixture the bootc half of the "present/absent under a given
// fixture, for both sources and both success and failure" criterion is never
// exercised: assertMaintenanceSurfaces' `data-source-error="bootc"` branch
// would be dead code that no fixture ever reaches, and the symmetric
// rendering that views.templ's hostImageSection implements would only ever be
// proven for rpm-ostree.
func cannedHostImageStatusBootcFailed() maintenance.HostImageStatus {
	return maintenance.HostImageStatus{
		BootcAvailable:     false,
		BootcError:         contractBootcError,
		RPMOStreeAvailable: true,
	}
}

// cannedMaintenanceState is the QueryMaintenanceState response the contract
// fixtures use. Like cannedStorageSnapshot, it is populated rather than
// zero-valued so that every conditionally-rendered element of the
// Maintenance page actually exists to be found or missed: RebootRequired
// true is what makes views.templ emit the "Reboot required" card and the
// admin-only POST /maintenance/reboot form, so a fixture without systemd can
// prove that form absent rather than vacuously absent.
//
// The staged-host-image reboot reason appears here (not synthesized by the
// web side) because the daemon's SystemManager.State is what turns the
// staged-deployment fact into a reason; the web process only renders it.
func cannedMaintenanceState() maintenance.State {
	return maintenance.State{
		// Deliberately unlike contractBootedVersion: the page prints
		// OSVersion in its summary strip, so sharing a value with the
		// rpm-ostree-supplementary version would make "no host-image detail
		// is rendered" assertions match the wrong element.
		OSVersion:      "contract-os 9.9.9",
		RebootRequired: true,
		RebootReasons: []string{
			"A staged host image deployment requires activation by reboot.",
		},
		Updates: []sysext.AvailableUpdate{
			{Feature: "contract", Component: "contract-ext", Current: "1.0.0", Newest: "1.1.0"},
		},
		Jobs: []maintenance.Job{
			{ID: 1, Action: "maintenance/reboot", Resource: "host", Status: "succeeded", RebootRequired: true},
		},
	}
}

// cannedStorageSnapshot returns a storage.Snapshot carrying two managed
// remote mounts, deliberately in different states so that every one of
// ManagedMountTable's per-mount remote-mount forms is actually rendered
// under the full-capability fixture:
//
//   - a *mounted* managed mount (ID "remote:"+sampleDefinitionID) renders
//     the Unmount and Delete forms (the Mount form is suppressed while a
//     mount is already mounted, per views.templ's state guard);
//   - an *unmounted* managed mount (ID "remote:"+sampleUnmountedDefinitionID)
//     renders the Mount and Delete forms (the Unmount form is suppressed
//     while a mount is not mounted).
//
// Per docs/agents/skills/canned-fixtures-need-populated-data-for-what-they-
// assert.md, an empty Snapshot can never render ManagedMountTable's
// per-mount forms under any fixture, so an assertion that those forms are
// absent under a gated fixture would be vacuously true — it would pass
// identically whether the gating logic correctly hid the forms or whether
// the forms were deleted outright. Crucially, a *single* mounted mount is
// also not enough: it never renders the per-row Mount form
// (internal/modules/storage/views.templ only emits `/storage/mounts/{id}/
// mount` when the mount's State is neither "mounted" nor "needs-attention"),
// so a regression that left that Mount form visible when systemd is absent
// would slip through. Carrying an unmounted mount as well means the
// no-systemd fixture's "no remote-mount controls / no dead links" assertion
// exercises the Mount form too, not only Unmount/Delete.
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
			{
				ID:      "remote:" + sampleUnmountedDefinitionID,
				Managed: true,
				State:   "unmounted",
				Health:  storage.HealthHealthy,
				Source:  "nfs.example.com:/export/contract-idle",
				Target:  "/mnt/contract-idle",
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
		if expectModuleAvailable(t, manifest.ID, caps) {
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
		if !expectModuleAvailable(t, manifest.ID, caps) {
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

// --- maintenance host-image surface -------------------------------------

// hostImageSectionPattern locates the Maintenance page's "Host image" card
// (internal/modules/maintenance/views.templ's hostImageSection). Every
// host-image assertion is scoped to this region rather than to the whole
// page, per docs/agents/skills/scope-html-assertions-to-the-region-under-
// test.md: an image reference or digest appearing anywhere else on the page
// must not be able to satisfy an assertion about the host-image section.
var hostImageSectionPattern = regexp.MustCompile(`(?s)<article\b[^>]*\bid="host-image"[^>]*>(.*?)</article>`)

// maintenanceRebootFormAction is the admin-only reboot form's action, which
// views.templ emits only inside the "Reboot required" card — itself rendered
// only when the Systemd-gated QueryMaintenanceState reported
// RebootRequired. cannedMaintenanceState sets that flag precisely so this
// form exists to be found (or missed) rather than being absent under every
// fixture.
const maintenanceRebootFormAction = `action="/maintenance/reboot"`

// hostImageDetailMarkers is every piece of markup or data the Maintenance
// page renders *only* from a QueryHostImageStatus response. A fixture with
// no host-image source must show none of them anywhere on the page, and the
// values all come from cannedHostImageStatus, so each marker is one that a
// host-image-carrying fixture demonstrably does render.
func hostImageDetailMarkers() []string {
	return []string{
		`id="host-image"`,
		`data-deployment="booted"`,
		`data-deployment="staged"`,
		`data-deployment="rollback"`,
		`data-soft-reboot=`,
		contractBootedImage,
		contractBootedDigest,
		contractStagedImage,
		contractStagedDigest,
		contractRollbackImage,
		contractRollbackDigest,
		contractBootedVersion,
		contractBootedChecksum,
		contractRPMOStreeError,
		contractBootcError,
	}
}

// contractDeploymentSlot names one of the three deployment slots together with
// the fixture values only that slot renders, so the per-slot audit below can
// assert presence *and* absence from the same table rather than hand-repeating
// six near-identical assertion blocks.
type contractDeploymentSlot struct {
	slug       string
	deployment *maintenance.Deployment
	image      string
	digest     string
}

// contractDeploymentSlots pairs each slot of a host-image fixture with the
// image reference and digest cannedHostImageStatus puts in it. A slot whose
// deployment is nil (the bootc-read-failure fixture, where bootc — the
// authority for deployment presence — did not answer) must render neither its
// data-deployment marker nor either of its values.
func contractDeploymentSlots(hostImage maintenance.HostImageStatus) []contractDeploymentSlot {
	return []contractDeploymentSlot{
		{"booted", hostImage.Booted, contractBootedImage, contractBootedDigest},
		{"staged", hostImage.Staged, contractStagedImage, contractStagedDigest},
		{"rollback", hostImage.Rollback, contractRollbackImage, contractRollbackDigest},
	}
}

// assertMaintenanceSurfaces drives GET /maintenance and checks the module's
// two independently-gated halves against expectations written out by hand
// from docs/capabilities.md, never from platform.Available/AvailableAny or
// from the module's own RequiredAnyCapabilities (docs/agents/skills/dont-
// use-the-gate-under-test-as-the-test-oracle.md):
//
//   - the module itself is present when any of systemd / bootc / rpm-ostree
//     is advertised (docs/capabilities.md, "Module-level defaults applied");
//   - the reboot-required card and its POST /maintenance/reboot form come
//     from QueryMaintenanceState and require systemd;
//   - the "Host image" section comes from QueryHostImageStatus and requires
//     bootc OR rpm-ostree (the table's one any-of row, exception #4).
//
// It also asserts the *calls*, not only the markup: the web side must never
// invoke a broker ID the fixture's host does not advertise, and — the
// converse, which requireAvailable alone cannot show — must actually invoke
// the ones it does.
func assertMaintenanceSurfaces(t *testing.T, run contractFixtureRun, caps capability.Set, hostImage maintenance.HostImageStatus) {
	t.Helper()

	// Hand-derived from docs/capabilities.md. Spelled out with Has() per
	// capability rather than HasAny/HasAll so this expectation cannot move
	// with a change to the any-of predicate the production gate calls.
	moduleAvailable := caps.Has(capability.Systemd) || caps.Has(capability.Bootc) || caps.Has(capability.RPMOStree)
	rebootAvailable := caps.Has(capability.Systemd)
	hostImageAvailable := caps.Has(capability.Bootc) || caps.Has(capability.RPMOStree)

	request := httptest.NewRequest(http.MethodGet, "/maintenance", nil)
	request.AddCookie(run.cookie)
	recorder := httptest.NewRecorder()
	run.handler.ServeHTTP(recorder, request)

	if !moduleAvailable {
		require.Equal(t, http.StatusNotFound, recorder.Code,
			"fixture advertises none of systemd/bootc/rpm-ostree, so GET /maintenance must 404")
		assert.Zero(t, run.brokerClient.called(broker.QueryMaintenanceState))
		assert.Zero(t, run.brokerClient.called(broker.QueryHostImageStatus))
		return
	}
	require.Equal(t, http.StatusOK, recorder.Code,
		"fixture advertises at least one of systemd/bootc/rpm-ostree, so GET /maintenance must render")
	body := recorder.Body.String()

	if rebootAvailable {
		assert.Positive(t, run.brokerClient.called(broker.QueryMaintenanceState),
			"a systemd host must actually fetch maintenance state; a zero call count would make the reboot assertions vacuous")
		assert.Contains(t, body, maintenanceRebootFormAction,
			"a systemd host whose state reports RebootRequired must render the reboot form")
	} else {
		assert.Zero(t, run.brokerClient.called(broker.QueryMaintenanceState),
			"the web side must never call the systemd-gated QueryMaintenanceState on a host without systemd")
		assert.NotContains(t, body, maintenanceRebootFormAction,
			"a host without systemd must not render the reboot form")
	}

	if !hostImageAvailable {
		assert.Zero(t, run.brokerClient.called(broker.QueryHostImageStatus),
			"the web side must never call QueryHostImageStatus on a host advertising neither bootc nor rpm-ostree")
		for _, marker := range hostImageDetailMarkers() {
			assert.NotContainsf(t, body, marker,
				"GET /maintenance rendered host-image detail (%s) on a host advertising neither bootc nor rpm-ostree", marker)
		}
		return
	}

	assert.Positive(t, run.brokerClient.called(broker.QueryHostImageStatus),
		"a host advertising bootc or rpm-ostree must actually fetch host-image status")
	section := extractRequiredSection(t, hostImageSectionPattern, body, "GET /maintenance", "host image")

	// bootc is authoritative for deployment presence and identity: each slot
	// and both of its identity fields render exactly when this fixture's
	// response carries that slot, and none of them render when it does not
	// (the bootc-read-failure fixture, where MergeHostImage produced no slots
	// at all). Asserting both directions per slot is what keeps the failure
	// fixtures from being no-op re-runs of the success fixture.
	for _, slot := range contractDeploymentSlots(hostImage) {
		marker := `data-deployment="` + slot.slug + `"`
		if slot.deployment == nil {
			assert.NotContainsf(t, section, marker,
				"the %s slot is absent from this fixture's response, so its row must not render", slot.slug)
			assert.NotContainsf(t, section, slot.image,
				"the %s slot is absent from this fixture's response, so its image reference must not render", slot.slug)
			assert.NotContainsf(t, section, slot.digest,
				"the %s slot is absent from this fixture's response, so its digest must not render", slot.slug)
			continue
		}
		assert.Containsf(t, section, marker, "the %s deployment must render when the response carries it", slot.slug)
		assert.Containsf(t, section, slot.image, "the %s deployment's bootc-authoritative image reference must render", slot.slug)
		assert.Containsf(t, section, slot.digest, "the %s deployment's bootc-authoritative digest must render", slot.slug)
	}

	// Soft-reboot eligibility is bootc's, rendered from HostImageStatus (never
	// from the systemd-gated State), and is three-state: the indicator appears
	// exactly when the response reports eligibility and stays away when bootc
	// did not answer at all.
	if hostImage.SoftRebootCapable != nil {
		assert.Contains(t, section, "data-soft-reboot=",
			"the response reports soft-reboot eligibility, so the indicator must render — sourced from HostImageStatus, not the systemd-gated State")
	} else {
		assert.NotContains(t, section, "data-soft-reboot=",
			"no soft-reboot indicator may render when the response reports no eligibility")
	}

	// rpm-ostree supplementary detail: present exactly when this fixture's
	// response carries it, absent when rpm-ostree failed to answer (and absent
	// too when bootc failed, since there is then no deployment to attach it
	// to). Asserting both directions is what keeps the failure fixtures from
	// being no-op re-runs of the success fixture.
	if hostImage.Booted != nil && hostImage.Booted.Version != "" {
		assert.Contains(t, section, "Version "+hostImage.Booted.Version,
			"rpm-ostree's supplementary version detail must render when the response carries it")
	} else {
		assert.NotContains(t, section, contractBootedVersion,
			"no rpm-ostree version detail may render when the response carries none")
	}
	if hostImage.Booted != nil && hostImage.Booted.Checksum != "" {
		assert.Contains(t, section, "Checksum "+hostImage.Booted.Checksum,
			"rpm-ostree's supplementary checksum detail must render when the response carries it")
	} else {
		assert.NotContains(t, section, contractBootedChecksum,
			"no rpm-ostree checksum detail may render when the response carries none")
	}

	// Per-source failure indicators are symmetric and independent: one
	// source failing never hides the other's data.
	if hostImage.RPMOStreeError != "" {
		assert.Contains(t, section, `data-source-error="rpm-ostree"`,
			"an rpm-ostree read failure must render its own unavailable indicator")
		assert.Contains(t, section, contractRPMOStreeError,
			"the rpm-ostree unavailable indicator must name the underlying failure")
	} else {
		assert.NotContains(t, section, `data-source-error="rpm-ostree"`,
			"no rpm-ostree unavailable indicator may render when rpm-ostree answered")
		assert.NotContains(t, section, contractRPMOStreeError,
			"no rpm-ostree failure detail may render when rpm-ostree answered")
	}
	if hostImage.BootcError != "" {
		assert.Contains(t, section, `data-source-error="bootc"`,
			"a bootc read failure must render its own unavailable indicator")
		assert.Contains(t, section, contractBootcError,
			"the bootc unavailable indicator must name the underlying failure")
	} else {
		assert.NotContains(t, section, `data-source-error="bootc"`,
			"no bootc unavailable indicator may render when bootc answered")
		assert.NotContains(t, section, contractBootcError,
			"no bootc failure detail may render when bootc answered")
	}
}

// TestCannedHostImageFixtureIsPopulated pins the shape of the default
// host-image fixture, which is what makes every "this element is absent under
// a degraded fixture" assertion in this file meaningful rather than vacuously
// true (docs/agents/skills/canned-fixtures-need-populated-data-for-what-they-
// assert.md).
//
// assertMaintenanceSurfaces now asserts each element present-or-absent from
// the fixture's own values, which is what lets the two per-source failure
// fixtures share it. That flexibility is exactly what needs pinning here: if
// cannedHostImageStatus quietly lost its staged slot, its rollback slot, its
// rpm-ostree supplementary detail, or its soft-reboot flag, every fixture
// would simply agree that the corresponding markup is expectedly absent and
// the whole matrix would keep passing while proving nothing about the
// conditional rendering under test.
func TestCannedHostImageFixtureIsPopulated(t *testing.T) {
	status := cannedHostImageStatus()
	require.True(t, status.BootcAvailable, "the default fixture must report bootc as having answered")
	require.True(t, status.RPMOStreeAvailable, "the default fixture must report rpm-ostree as having answered")
	require.Empty(t, status.BootcError, "the default fixture is the success case for bootc")
	require.Empty(t, status.RPMOStreeError, "the default fixture is the success case for rpm-ostree")
	for _, slot := range contractDeploymentSlots(status) {
		require.NotNilf(t, slot.deployment, "the default fixture must carry a %s deployment", slot.slug)
		require.Equalf(t, slot.image, slot.deployment.Image, "the %s deployment must carry its bootc-authoritative image reference", slot.slug)
		require.Equalf(t, slot.digest, slot.deployment.Digest, "the %s deployment must carry its bootc-authoritative digest", slot.slug)
	}
	require.Equal(t, contractBootedVersion, status.Booted.Version,
		"the default fixture must carry rpm-ostree's supplementary version detail on the booted deployment")
	require.Equal(t, contractBootedChecksum, status.Booted.Checksum,
		"the default fixture must carry rpm-ostree's supplementary checksum detail on the booted deployment")
	require.NotNil(t, status.SoftRebootCapable, "the default fixture must report soft-reboot eligibility")

	// The two failure fixtures must each differ from the success fixture in
	// exactly their own source's direction, so neither degenerates into a
	// second run of the success case.
	rpmFailed := cannedHostImageStatusRPMOStreeFailed()
	require.False(t, rpmFailed.RPMOStreeAvailable)
	require.Equal(t, contractRPMOStreeError, rpmFailed.RPMOStreeError)
	require.Empty(t, rpmFailed.BootcError, "the rpm-ostree failure fixture must leave bootc answering")
	require.NotNil(t, rpmFailed.Staged, "bootc still answered, so its deployment slots must survive")
	require.Empty(t, rpmFailed.Booted.Version, "rpm-ostree did not answer, so its supplementary detail must be gone")

	bootcFailed := cannedHostImageStatusBootcFailed()
	require.False(t, bootcFailed.BootcAvailable)
	require.Equal(t, contractBootcError, bootcFailed.BootcError)
	require.True(t, bootcFailed.RPMOStreeAvailable, "the bootc failure fixture must leave rpm-ostree answering")
	require.Nil(t, bootcFailed.Booted, "bootc is authoritative for deployment presence, so no slot survives its failure")
	require.Nil(t, bootcFailed.SoftRebootCapable, "soft-reboot eligibility is bootc's, so it is unknown when bootc fails")
}

// --- sub-routes not covered by any module's Manifest().Path ------------
//
// Every module's primary route is checked generically against manifest.Path,
// with the expected availability taken from the independent
// expectModuleAvailable oracle (never platform.Available). Several modules
// also mount secondary routes gated at a finer grain (route-level, or with
// a stricter capability requirement than the module's own
// RequiredCapabilities — the services journal tab and the whole logs
// module both need systemd AND journald). contractSubRoutes enumerates
// every one of those secondary routes so the degraded fixtures exercise
// them explicitly, per docs/agents/skills/gate-every-call-path-not-just-
// routes-and-nav.md and partial-gate-modules-need-full-view-element-audit.md.
var sampleUnit = "sample.service"
var sampleDefinitionID = strings.Repeat("0123456789abcdef", 2)          // 32 hex chars (a mounted managed mount)
var sampleUnmountedDefinitionID = strings.Repeat("fedcba9876543210", 2) // 32 hex chars (an unmounted managed mount)
var sampleContainerID = strings.Repeat("a1b2c3d4e5f60789", 4)           // 64 hex chars

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

// contractFixtureRun is the assembled fixture a run leaves behind: the
// authenticated handler, its session cookie, and the fake broker (whose
// recorded call log lets a caller assert that a gated-off broker ID was
// never invoked at all, and that an available one actually was).
type contractFixtureRun struct {
	brokerClient *fakeCapabilityBroker
	cookie       *http.Cookie
	handler      http.Handler
}

// runCapabilityContractFixture drives the full contract-test assertion
// suite against a single fixture identified by caps: it builds a real
// registry + web.NewServer over a fake broker configured with caps, logs
// in, then asserts — across all four registries the spec calls out —
// that no route, navigation entry, dashboard card, query, action, or
// stream reference exists for a capability caps does not have, while
// everything whose capability caps does have keeps working. Called with
// fullCapabilitySet() (no exclusions), this reduces exactly to the original
// full-capability assertions; called with a degraded fixture, the same
// code exercises the "gated absent" side without being a second,
// hand-duplicated implementation of either case.
//
// The returned contractFixtureRun lets a caller layer fixture-specific
// assertions on the very same server this runner just exercised.
func runCapabilityContractFixture(t *testing.T, caps capability.Set) contractFixtureRun {
	t.Helper()
	return runCapabilityContractFixtureWithHostImage(t, caps, cannedHostImageStatus())
}

// runCapabilityContractFixtureWithHostImage is runCapabilityContractFixture
// with an explicit QueryHostImageStatus response, so the same assertions can
// be replayed against the rpm-ostree read-failure shape.
func runCapabilityContractFixtureWithHostImage(t *testing.T, caps capability.Set, hostImage maintenance.HostImageStatus) contractFixtureRun {
	t.Helper()
	brokerClient := newFakeCapabilityBroker(t, caps, hostImage)
	registry, handler := newCapabilityContractServer(t, brokerClient)
	cookie := loginSession(t, handler)
	run := contractFixtureRun{brokerClient: brokerClient, cookie: cookie, handler: handler}

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
		available := expectModuleAvailable(t, manifest.ID, caps)

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
	// fixture) carries a mounted managed mount AND an unmounted managed
	// mount so every one of ManagedMountTable's per-mount remote-mount forms
	// — Mount (only on the unmounted row), Unmount (only on the mounted
	// row), and Delete (on both) — actually renders when available, proving
	// each is hidden when gated rather than vacuously absent from an empty
	// mount table (docs/agents/skills/canned-fixtures-need-populated-data-
	// for-what-they-assert.md). Covering the Mount form specifically matters:
	// a lone mounted mount never renders it, so a regression that left the
	// per-row Mount form visible when systemd is absent would otherwise slip
	// through (docs/agents/skills/partial-gate-modules-need-full-view-
	// element-audit.md).
	storageRequest := httptest.NewRequest(http.MethodGet, "/storage", nil)
	storageRequest.AddCookie(cookie)
	storageRecorder := httptest.NewRecorder()
	handler.ServeHTTP(storageRecorder, storageRequest)
	require.Equal(t, http.StatusOK, storageRecorder.Code,
		"fixture: storage inventory (GET /storage) must stay available regardless of capabilities")
	storageBody := storageRecorder.Body.String()
	mountFormAction := `action="/storage/mounts/` + sampleUnmountedDefinitionID + `/mount"`
	unmountFormAction := `action="/storage/mounts/` + sampleDefinitionID + `/unmount"`
	deleteMountedFormAction := `action="/storage/mounts/` + sampleDefinitionID + `/delete"`
	deleteUnmountedFormAction := `action="/storage/mounts/` + sampleUnmountedDefinitionID + `/delete"`
	if caps.Has(capability.Systemd) {
		assert.Contains(t, storageBody, "Add remote mount",
			"fixture: storage page should render the remote-mount control when systemd is present")
		assert.Contains(t, storageBody, mountFormAction,
			"fixture: storage page should render the per-mount Mount form (for the unmounted mount) when systemd is present")
		assert.Contains(t, storageBody, unmountFormAction,
			"fixture: storage page should render the per-mount Unmount form (for the mounted mount) when systemd is present")
		assert.Contains(t, storageBody, deleteMountedFormAction,
			"fixture: storage page should render the per-mount Delete form for the mounted mount when systemd is present")
		assert.Contains(t, storageBody, deleteUnmountedFormAction,
			"fixture: storage page should render the per-mount Delete form for the unmounted mount when systemd is present")
	} else {
		assert.NotContains(t, storageBody, "Add remote mount",
			"fixture: storage page rendered a remote-mount control despite systemd being absent")
		assert.NotContains(t, storageBody, mountFormAction,
			"fixture: storage page rendered a per-mount Mount form despite systemd being absent")
		assert.NotContains(t, storageBody, unmountFormAction,
			"fixture: storage page rendered a per-mount Unmount form despite systemd being absent")
		assert.NotContains(t, storageBody, deleteMountedFormAction,
			"fixture: storage page rendered a per-mount Delete form (mounted) despite systemd being absent")
		assert.NotContains(t, storageBody, deleteUnmountedFormAction,
			"fixture: storage page rendered a per-mount Delete form (unmounted) despite systemd being absent")
	}
	assertNoDeadLinks(t, handler, cookie, "/storage", storageBody)

	for _, route := range contractSubRoutes {
		expectAvailable := allOfPresent(caps, route.requirements)
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

	// Maintenance is this phase's composite surface — a whole-module any-of
	// gate over two independently gated halves — so it gets its own
	// per-element audit rather than only the generic module-level checks
	// above (docs/agents/skills/partial-gate-modules-need-full-view-element-
	// audit.md).
	assertMaintenanceSurfaces(t, run, caps, hostImage)

	return run
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

// TestWebSideUngatedExemptionExcludesHostImageSurfaces pins the one
// relaxation in this harness so it cannot quietly grow to cover the surfaces
// this phase adds. webSideUngatedBrokerIDs exists solely for the sysext web
// controls that #52 owns; if any maintenance or host-image broker ID were
// added to it, every "the web side never calls a gated-off broker ID"
// assertion about this phase would silently stop meaning anything.
func TestWebSideUngatedExemptionExcludesHostImageSurfaces(t *testing.T) {
	for _, id := range []string{
		broker.QueryHostImageStatus,
		broker.QueryMaintenanceState,
		broker.ActionMaintenanceReboot,
	} {
		assert.NotContainsf(t, webSideUngatedBrokerIDs, id,
			"%q must stay subject to the fake broker's capability check; the web-side gating exemption covers only the sysext controls deferred to #52", id)
	}
	assert.Len(t, webSideUngatedBrokerIDs, 4,
		"the web-side gating exemption is exactly the four sysext actions; growing it needs a deliberate decision and a docs/capabilities.md update")
}

// TestWebSideOracleTablesAreCompleteAndDisjoint pins the two hand-transcribed
// web-side oracle tables as a pair. Both properties matter and neither is
// implied by the fixture runs above, which only exercise the broker IDs the
// web side happens to call:
//
//   - Completeness. Together the tables must carry all 52 declared broker IDs
//     (35 Action* + 17 Query*), the same totals cmd/pilothoused's
//     TestCapabilityTableMirrorsBrokerAPIConstants pins against
//     internal/broker/api.go's live go/ast-parsed declarations. Every key here
//     is a broker.Action*/Query* constant reference, so a renamed constant is
//     a compile error and 52 distinct keys can only mean full coverage — which
//     is what makes requireAvailable's "not in either table" branch a genuine
//     tripwire for a newly added ID rather than a formality.
//   - Disjointness. An ID carries at most one registration guard, so appearing
//     in both tables is a contradiction rather than a redundancy.
//
// It then pins the any-of tables themselves — this phase's headline change —
// so the explicit any-of markers cannot silently drift back to all-of gates
// while every behavioral assertion above quietly relaxes with them.
func TestWebSideOracleTablesAreCompleteAndDisjoint(t *testing.T) {
	for id := range capabilityAnyRequirements {
		assert.NotContainsf(t, capabilityRequirements, id,
			"broker ID %q appears in both capabilityRequirements and capabilityAnyRequirements; an ID carries at most one registration guard", id)
	}
	assert.Equal(t, 52, len(capabilityRequirements)+len(capabilityAnyRequirements),
		"the two web-side broker-ID tables must together cover all 52 declared broker IDs (35 Action* + 17 Query*), matching docs/capabilities.md and cmd/pilothoused's capabilityTable")

	// Hand-written from docs/capabilities.md, not read back from the
	// production gates: QueryHostImageStatus is the API's one any-of ID
	// (bootc OR rpm-ostree, exception #4), and maintenance is the one module
	// whose whole-module gate is any-of (systemd OR bootc OR rpm-ostree).
	assert.Equal(t, map[string][]capability.ID{
		broker.QueryHostImageStatus: {capability.Bootc, capability.RPMOStree},
	}, capabilityAnyRequirements,
		"QueryHostImageStatus must remain the sole any-of broker ID, requiring bootc OR rpm-ostree")
	assert.Equal(t, map[string][]capability.ID{
		"maintenance": {capability.Systemd, capability.Bootc, capability.RPMOStree},
	}, moduleRequiredAnyCapabilities,
		"maintenance must remain the sole any-of module gate, requiring systemd OR bootc OR rpm-ostree")

	// The two oracle helpers must genuinely differ; collapsing one into the
	// other would silently turn every any-of expectation above into an all-of
	// one (or vice versa) while both tables still read correctly.
	onlyBootc := capability.New(capability.Bootc)
	bootcOrRPMOStree := []capability.ID{capability.Bootc, capability.RPMOStree}
	assert.True(t, anyOfPresent(onlyBootc, bootcOrRPMOStree), "any-of must be satisfied by a single present capability")
	assert.False(t, allOfPresent(onlyBootc, bootcOrRPMOStree), "all-of must not be satisfied by a single present capability")
	assert.True(t, allOfPresent(onlyBootc, nil), "all-of over no requirements means 'always available'")
	assert.False(t, anyOfPresent(onlyBootc, nil), "any-of over no requirements is never satisfied")
}

// TestCapabilityContractHostImageFixtures closes this phase's web-side
// matrix with the three host shapes the spec's acceptance criteria name,
// plus the rpm-ostree read-failure variant, each run through the same
// runCapabilityContractFixture assertions every other fixture uses and then
// pinned with the literal, hand-written expectations the criteria state.
func TestCapabilityContractHostImageFixtures(t *testing.T) {
	// "uCore fixture reports read-only bootc state with supplementary
	// rpm-ostree detail" — every host-image surface and every systemd
	// surface present at once.
	t.Run("ucore", func(t *testing.T) {
		run := runCapabilityContractFixture(t, ucoreCapabilitySet())
		assert.Positive(t, run.brokerClient.called(broker.QueryHostImageStatus),
			"uCore must fetch host-image status")
		assert.Positive(t, run.brokerClient.called(broker.QueryMaintenanceState),
			"uCore has systemd, so reboot posture must still be fetched")
	})

	// "Snosi without bootc remains supported; host-image state is omitted
	// rather than failing" — the module and its reboot half keep working
	// and the host-image half is simply not there.
	t.Run("snosi-without-bootc", func(t *testing.T) {
		caps := snosiWithoutBootcCapabilitySet()
		run := runCapabilityContractFixture(t, caps)
		assert.Zero(t, run.brokerClient.called(broker.QueryHostImageStatus),
			"Snosi without bootc advertises no host-image source, so the query must never be called")
		assert.Positive(t, run.brokerClient.called(broker.QueryMaintenanceState),
			"Snosi without bootc still has systemd, so reboot posture must still be fetched")

		request := httptest.NewRequest(http.MethodGet, "/maintenance", nil)
		request.AddCookie(run.cookie)
		recorder := httptest.NewRecorder()
		run.handler.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code,
			"Snosi without bootc must still serve GET /maintenance")
		assert.NotContains(t, recorder.Body.String(), `id="host-image"`,
			"host-image state must be omitted, not rendered empty or errored")
	})

	// The plan's inverse extreme: bootc and nothing else. This is the
	// fixture that proves maintenance's whole-module gate is a real OR.
	t.Run("bootc-only", func(t *testing.T) {
		caps := bootcOnlyCapabilitySet()
		run := runCapabilityContractFixture(t, caps)

		assert.Zero(t, run.brokerClient.called(broker.QueryMaintenanceState),
			"a bootc-only host has no systemd, so the web side must never call QueryMaintenanceState")
		assert.Positive(t, run.brokerClient.called(broker.QueryHostImageStatus),
			"a bootc-only host must still fetch host-image status")

		dashboardRequest := httptest.NewRequest(http.MethodGet, "/", nil)
		dashboardRequest.AddCookie(run.cookie)
		dashboardRecorder := httptest.NewRecorder()
		run.handler.ServeHTTP(dashboardRecorder, dashboardRequest)
		require.Equal(t, http.StatusOK, dashboardRecorder.Code)
		navSection := extractRequiredSection(t, navSectionPattern, dashboardRecorder.Body.String(), "GET /", "primary navigation")
		assert.Contains(t, navSection, `href="/maintenance"`,
			"a bootc-only host must keep maintenance's nav entry")

		pageRequest := httptest.NewRequest(http.MethodGet, "/maintenance", nil)
		pageRequest.AddCookie(run.cookie)
		pageRecorder := httptest.NewRecorder()
		run.handler.ServeHTTP(pageRecorder, pageRequest)
		assert.Equal(t, http.StatusOK, pageRecorder.Code,
			"a bootc-only host must still serve GET /maintenance")

		rebootRequest := httptest.NewRequest(http.MethodPost, "/maintenance/reboot", strings.NewReader(url.Values{"csrf": {contractCSRF}}.Encode()))
		rebootRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rebootRequest.AddCookie(run.cookie)
		rebootRecorder := httptest.NewRecorder()
		run.handler.ServeHTTP(rebootRecorder, rebootRequest)
		assert.Equal(t, http.StatusNotFound, rebootRecorder.Code,
			"a bootc-only host has no systemd, so POST /maintenance/reboot must 404")
	})

	// The failure half of the symmetry: rpm-ostree is advertised but did not
	// answer. bootc's data still renders, rpm-ostree's supplementary detail
	// does not, and the page carries a named per-source unavailable
	// indicator instead.
	t.Run("ucore-rpm-ostree-read-failure", func(t *testing.T) {
		run := runCapabilityContractFixtureWithHostImage(t, ucoreCapabilitySet(), cannedHostImageStatusRPMOStreeFailed())
		assert.Positive(t, run.brokerClient.called(broker.QueryHostImageStatus))
	})

	// The mirror image, so the per-source symmetry is proven in both
	// directions rather than only for rpm-ostree: bootc is advertised but did
	// not answer, while rpm-ostree did. bootc owns deployment presence, so the
	// section renders a named bootc unavailable indicator and no deployment
	// rows at all — while the module, its nav entry, and every systemd surface
	// stay exactly as they are on the working uCore fixture.
	t.Run("ucore-bootc-read-failure", func(t *testing.T) {
		run := runCapabilityContractFixtureWithHostImage(t, ucoreCapabilitySet(), cannedHostImageStatusBootcFailed())
		assert.Positive(t, run.brokerClient.called(broker.QueryHostImageStatus),
			"a bootc read failure is a per-source degradation, not a reason to stop querying host-image status")
		assert.Positive(t, run.brokerClient.called(broker.QueryMaintenanceState),
			"a bootc read failure must not disturb the systemd-gated reboot posture")
	})
}

// TestCapabilityContractDegradedFixtures exercises the three degraded
// fixtures named in the mill plan for issue #54, chunk c11: no-journald
// (services keeps working, journal/logs go absent), no-systemd (services,
// storage's remote-mount routes, backups, and logs all go absent, storage
// inventory and — since #51 — maintenance itself stay, with only
// maintenance's reboot sub-route gated off), and no-engines (podman/docker/incus
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
