package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/modules/backups"
	"github.com/frostyard/pilothouse/internal/modules/logs"
	"github.com/frostyard/pilothouse/internal/modules/maintenance"
	"github.com/frostyard/pilothouse/internal/modules/services"
	"github.com/frostyard/pilothouse/internal/modules/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registryKind identifies which of the four broker registries a capability
// contract entry lives in, since the files module splits across all four
// (ActionFilesUpload is a StreamActionRegistry entry, QueryFilesDownload is
// a StreamQueryRegistry entry, and everything else is a plain Action/Query).
type registryKind int

const (
	inActions registryKind = iota
	inQueries
	inStreamActions
	inStreamQueries
)

// capabilityRequirement is one row of docs/capabilities.md's binding table,
// mirrored here in Go so the table is actually exercised by a test rather
// than only documented. required is the exact set of capability.IDs the ID's
// registration depends on. An empty required slice means "none" -- always
// registered, unconditionally.
//
// requireAny selects which satisfaction rule the row uses, matching the
// gating primitive its registerX function actually calls:
//
//   - false (the default, an AND row): caps.HasAll(required...) must equal
//     whether the ID ends up Registered() in its registry.
//   - true (an OR row, e.g. QueryHostImageStatus's HasAny(Bootc,
//     RPMOStree)): caps.HasAny(required...) must equal it instead.
//
// Both sides are evaluated with capability.Set's own predicates -- the same
// functions the production guards call -- rather than a second
// reimplementation of set membership here.
type capabilityRequirement struct {
	id         string
	registry   registryKind
	required   []capability.ID
	requireAny bool
}

// satisfiedBy reports whether a fixture's capability set satisfies this row,
// under whichever of the two satisfaction rules the row declares.
func (row capabilityRequirement) satisfiedBy(caps capability.Set) bool {
	if row.requireAny {
		return caps.HasAny(row.required...)
	}
	return caps.HasAll(row.required...)
}

// capabilityTable is the complete 52-row mirror of docs/capabilities.md.
// Every Action*/Query* constant declared in internal/broker/api.go (35
// Action* + 17 Query*, the 17 including QueryCapabilities and
// QueryHostImageStatus) appears exactly once. QueryServicesJournal and
// QueryLogs use the corrected "systemd AND journald" requirement
// (docs/capabilities.md exceptions #2 and #3), not "journald" alone, and
// QueryHostImageStatus is the table's one any-of row (exception #4).
//
// Columns, in positional order: broker ID, registry, required capabilities,
// and whether the requirement is satisfied by any one of them (true) rather
// than all of them (false).
var capabilityTable = []capabilityRequirement{
	// Actions (35).
	{broker.ActionFilesUpload, inStreamActions, nil, false},
	{broker.ActionDockerRemove, inActions, []capability.ID{capability.Docker}, false},
	{broker.ActionDockerRemoveImage, inActions, []capability.ID{capability.Docker}, false},
	{broker.ActionDockerRestart, inActions, []capability.ID{capability.Docker}, false},
	{broker.ActionDockerStart, inActions, []capability.ID{capability.Docker}, false},
	{broker.ActionDockerStop, inActions, []capability.ID{capability.Docker}, false},
	{broker.ActionIncusRemove, inActions, []capability.ID{capability.Incus}, false},
	{broker.ActionIncusRemoveImage, inActions, []capability.ID{capability.Incus}, false},
	{broker.ActionIncusRestart, inActions, []capability.ID{capability.Incus}, false},
	{broker.ActionIncusStart, inActions, []capability.ID{capability.Incus}, false},
	{broker.ActionIncusStop, inActions, []capability.ID{capability.Incus}, false},
	{broker.ActionMaintenanceReboot, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionPodmanRemove, inActions, []capability.ID{capability.Podman}, false},
	{broker.ActionPodmanRemoveImage, inActions, []capability.ID{capability.Podman}, false},
	{broker.ActionPodmanRestart, inActions, []capability.ID{capability.Podman}, false},
	{broker.ActionPodmanStart, inActions, []capability.ID{capability.Podman}, false},
	{broker.ActionPodmanStop, inActions, []capability.ID{capability.Podman}, false},
	{broker.ActionSysextDisable, inActions, []capability.ID{capability.Updex, capability.Sysext}, false},
	{broker.ActionSysextEnable, inActions, []capability.ID{capability.Updex, capability.Sysext}, false},
	{broker.ActionSysextRefresh, inActions, []capability.ID{capability.Sysext}, false},
	{broker.ActionSysextUpdate, inActions, []capability.ID{capability.Updex}, false},
	{broker.ActionServicesDisable, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionServicesEnable, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionServicesResetFailed, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionServicesRestart, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionServicesStart, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionServicesStop, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionStorageCreateNFS, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionStorageCreateSMBGuest, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionStorageCreateSMBCredentials, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionStorageCreateSMBGuestOwned, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionStorageCreateSMBCredentialsOwned, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionStorageMount, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionStorageUnmount, inActions, []capability.ID{capability.Systemd}, false},
	{broker.ActionStorageDelete, inActions, []capability.ID{capability.Systemd}, false},
	// Queries (17).
	{broker.QueryActivity, inQueries, nil, false},
	{broker.QueryBackupsState, inQueries, []capability.ID{capability.Systemd}, false},
	{broker.QueryCapabilities, inQueries, nil, false},
	{broker.QueryDockerLogs, inQueries, []capability.ID{capability.Docker}, false},
	{broker.QueryDockerState, inQueries, []capability.ID{capability.Docker}, false},
	{broker.QueryHostImageStatus, inQueries, []capability.ID{capability.Bootc, capability.RPMOStree}, true},
	{broker.QueryIncusState, inQueries, []capability.ID{capability.Incus}, false},
	{broker.QueryJobs, inQueries, nil, false},
	{broker.QueryLogs, inQueries, []capability.ID{capability.Systemd, capability.Journald}, false},
	{broker.QueryMaintenanceState, inQueries, []capability.ID{capability.Systemd}, false},
	{broker.QueryPodmanLogs, inQueries, []capability.ID{capability.Podman}, false},
	{broker.QueryPodmanState, inQueries, []capability.ID{capability.Podman}, false},
	{broker.QueryServicesJournal, inQueries, []capability.ID{capability.Systemd, capability.Journald}, false},
	{broker.QueryServicesState, inQueries, []capability.ID{capability.Systemd}, false},
	{broker.QueryStorageState, inQueries, nil, false},
	{broker.QueryFilesDownload, inStreamQueries, nil, false},
	{broker.QueryFilesList, inQueries, nil, false},
}

// moduleLevelNoneIDs is the exact 7 broker IDs whose documented requirement
// is "none" -- the only IDs a minimal (empty capability.Set) fixture should
// register. Verified against capabilityTable at TestCapabilityTableHasExactlyFiftyTwoRows.
var moduleLevelNoneIDs = []string{
	broker.QueryFilesList,
	broker.QueryFilesDownload,
	broker.ActionFilesUpload,
	broker.QueryActivity,
	broker.QueryJobs,
	broker.QueryStorageState,
	broker.QueryCapabilities,
}

func TestCapabilityTableHasExactlyFiftyTwoRows(t *testing.T) {
	require.Len(t, capabilityTable, 52, "docs/capabilities.md documents 52 broker IDs (35 Action* + 17 Query*, including QueryCapabilities and QueryHostImageStatus)")
	seen := make(map[string]bool, len(capabilityTable))
	actionCount, queryCount := 0, 0
	for _, row := range capabilityTable {
		assert.False(t, seen[row.id], "duplicate id %s in capabilityTable", row.id)
		seen[row.id] = true
		switch row.registry {
		case inActions, inStreamActions:
			actionCount++
		case inQueries, inStreamQueries:
			queryCount++
		}
		// An any-of row with no candidates would be satisfied by nothing
		// (HasAny() is false), which is a broken row rather than a "none"
		// row -- "none" is spelled as an all-of row with no requirements.
		if row.requireAny {
			assert.NotEmpty(t, row.required, "any-of row %s must list at least one capability", row.id)
		}
	}
	assert.Equal(t, 35, actionCount, "expected 35 Action* IDs")
	assert.Equal(t, 17, queryCount, "expected 17 Query* IDs")
	none := 0
	for _, row := range capabilityTable {
		if len(row.required) == 0 {
			none++
		}
	}
	assert.Equal(t, len(moduleLevelNoneIDs), none, "moduleLevelNoneIDs must match the number of none-requirement rows in capabilityTable")
}

// contractRegistries bundles the four broker registries a fixture registers
// against, plus a helper to check whether a given ID ended up registered in
// whichever one it belongs to.
type contractRegistries struct {
	queries       *broker.QueryRegistry
	actions       *broker.ActionRegistry
	streamQueries *broker.StreamQueryRegistry
	streamActions *broker.StreamActionRegistry
}

func (registries contractRegistries) registered(row capabilityRequirement) bool {
	switch row.registry {
	case inActions:
		return registries.actions.Registered(row.id)
	case inQueries:
		return registries.queries.Registered(row.id)
	case inStreamActions:
		return registries.streamActions.Registered(row.id)
	case inStreamQueries:
		return registries.streamQueries.Registered(row.id)
	default:
		return false
	}
}

// registerEverythingForFixture calls every registerX function from this
// phase's conversion chunks (registerServices, registerLogs, registerBackups,
// registerStorageActions, registerMaintenance, registerHostImage,
// registerSysextActions, registerPodman, registerDocker, registerIncus, plus the always-on
// registerStorage/registerFiles/registerActivity/registerJobs/
// registerCapabilities) against fresh registries and fake managers for the
// given capability.Set, following c7's nil-manager convention: for the four
// managers whose real construction (buildSystemdManagers) depends on a live
// systemd D-Bus connection -- storage's remote-mount controller, backups,
// services, logs -- a fixture lacking Systemd gets a nil manager exactly as
// buildSystemdManagers would produce, so this test doubles as regression
// coverage that the two mechanisms (construction-time nil-out from c7,
// registration-time capability guard from c8/c9) never disagree. Every
// other manager (maintenance, sysext, podman, docker, incus) has no
// systemd-dependent construction, so it is always a live fake; withholding
// registration for those is registerX's own capability-guard job alone.
func registerEverythingForFixture(t *testing.T, caps capability.Set) contractRegistries {
	t.Helper()
	queries := broker.NewQueryRegistry()
	actions := broker.NewActionRegistry()
	streamQueries := broker.NewStreamQueryRegistry()
	streamActions := broker.NewStreamActionRegistry()

	require.NoError(t, registerCapabilities(queries, caps))
	require.NoError(t, registerFiles(queries, streamQueries, streamActions, &fakeFilesManager{}))

	auditStore, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"), 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, auditStore.Close()) })
	require.NoError(t, registerActivity(queries, auditStore))

	jobStore, err := jobs.Open(filepath.Join(t.TempDir(), "jobs.db"), 10)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jobStore.Close()) })
	require.NoError(t, registerJobs(queries, jobStore))

	require.NoError(t, registerStorage(queries, fakeStorageManager{}))

	// c7's nil-manager convention: buildSystemdManagers only constructs
	// these four managers when a live systemd client was obtained, which
	// requires the Systemd capability. Mirror that here instead of always
	// handing registerX a live fake, so this test also proves construction
	// and registration never disagree.
	var remoteManager storage.RemoteManager
	var backupManager backups.Manager
	var servicesManager services.Manager
	var logsManager logs.Manager
	if caps.Has(capability.Systemd) {
		remoteManager = &fakeRemoteManager{}
		backupManager = fakeBackupsManager{}
		servicesManager = &fakeServicesManager{}
		logsManager = &fakeLogsManager{}
	}
	require.NoError(t, registerStorageActions(actions, remoteManager, caps))
	require.NoError(t, registerBackups(queries, backupManager, caps))
	require.NoError(t, registerServices(actions, queries, servicesManager, caps))
	require.NoError(t, registerLogs(queries, logsManager, caps))

	// maintenance.NewSystemManager and sysext.NewSystemManager have no
	// systemd D-Bus dependency, so their fakes are always live; only the
	// registration-time capability guard withholds anything.
	require.NoError(t, registerMaintenance(actions, queries, &fakeMaintenanceManager{}, caps))
	require.NoError(t, registerSysextActions(actions, fakeSysextManager{}, caps))

	// The host-image reporter is the real maintenance.HostImageManager, built
	// from this fixture's capability set exactly as run() builds it (only the
	// bootc/rpm-ostree executables themselves are faked), so the fixture
	// cannot register a manager production would have configured differently.
	require.NoError(t, registerHostImage(queries, maintenance.NewHostImageManager(&fakeHostImageRunner{}, caps.Has(capability.Bootc), caps.Has(capability.RPMOStree)), caps))

	// podman/docker/incus client construction never depends on a probed
	// capability either (a bad socket/env just makes the engine
	// unreachable, which capability.Probe already accounts for), so their
	// fakes are always live too.
	require.NoError(t, registerPodman(actions, queries, fakePodmanManager{}, caps))
	require.NoError(t, registerDocker(actions, queries, fakeDockerManager{}, caps))
	require.NoError(t, registerIncus(actions, queries, fakeIncusManager{}, caps))

	return contractRegistries{queries: queries, actions: actions, streamQueries: streamQueries, streamActions: streamActions}
}

// allCapabilityIDs is every canonical capability.ID from the spec's fixed
// vocabulary, used to build the all-on fixture.
var allCapabilityIDs = []capability.ID{
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
}

// TestCapabilityContractAcrossFixtureMatrix is the binding contract test the
// spec requires as the final chunk of this phase: for every fixture
// capability.Set below, every one of the 52 registered broker IDs must be
// present in its registry iff the fixture's Set satisfies that ID's
// documented requirement from capabilityTable (kept as a Go-side mirror of
// docs/capabilities.md, cross-checked by inspection, not parsed
// automatically) -- under HasAll for an ordinary row and HasAny for an any-of
// row, each evaluated by capability.Set's own predicate, the same one the
// production guard calls. The all-on and minimal fixtures get additional
// dedicated assertions below; every fixture (including the representative
// partials) is walked against the full table.
//
// The four host-image fixtures (bootc-only, rpm-ostree-only, both, and
// neither-with-systemd) exist so QueryHostImageStatus's any-of row is
// exercised in every direction the moment it lands: each source alone
// registers it, both register it, neither withholds it, and none of that
// changes with Systemd present or absent (bootc-only and rpm-ostree-only
// carry no Systemd; neither-plus-systemd carries it without a source).
func TestCapabilityContractAcrossFixtureMatrix(t *testing.T) {
	fixtures := []struct {
		name string
		caps capability.Set
	}{
		{"all-on", capability.New(allCapabilityIDs...)},
		{"minimal", capability.New()},
		{"systemd-only", capability.New(capability.Systemd)},
		{"journald-only", capability.New(capability.Journald)},
		{"systemd-plus-journald-no-engines", capability.New(capability.Systemd, capability.Journald)},
		{"engines-only", capability.New(capability.Podman, capability.Docker, capability.Incus)},
		{"updex-without-sysext", capability.New(capability.Updex)},
		{"sysext-without-updex", capability.New(capability.Sysext)},
		{"systemd-plus-one-engine", capability.New(capability.Systemd, capability.Podman)},
		{"bootc-only", capability.New(capability.Bootc)},
		{"rpm-ostree-only", capability.New(capability.RPMOStree)},
		{"bootc-plus-rpm-ostree", capability.New(capability.Bootc, capability.RPMOStree)},
		{"neither-host-image-source-plus-systemd", capability.New(capability.Systemd, capability.Updex, capability.Sysext)},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			registries := registerEverythingForFixture(t, fixture.caps)
			for _, row := range capabilityTable {
				want := row.satisfiedBy(fixture.caps)
				got := registries.registered(row)
				assert.Equal(t, want, got, "fixture=%s id=%s required=%v requireAny=%t", fixture.name, row.id, row.required, row.requireAny)
			}
		})
	}
}

// TestCapabilityContractAllOnReproducesPrePhaseBehavior asserts the all-on
// fixture registers every one of the 52 IDs -- reproducing pre-phase
// behavior exactly for the 50 pre-existing Action*/Query* constants, plus
// QueryCapabilities and QueryHostImageStatus (52 total), since every
// documented requirement is a subset of the full capability vocabulary.
func TestCapabilityContractAllOnReproducesPrePhaseBehavior(t *testing.T) {
	registries := registerEverythingForFixture(t, capability.New(allCapabilityIDs...))
	for _, row := range capabilityTable {
		assert.True(t, registries.registered(row), "all-on fixture must register %s", row.id)
	}
}

// TestCapabilityContractMinimalRegistersOnlyModuleLevelNoneIDs asserts the
// minimal (empty capability.Set) fixture registers exactly the 7
// module-level-none IDs -- QueryFilesList, QueryFilesDownload,
// ActionFilesUpload, QueryActivity, QueryJobs, QueryStorageState,
// QueryCapabilities -- and no others, and separately proves the injectable
// fake systemd-connect function supplied to connectSystemd is never invoked
// when the Systemd capability is absent (no dbus dial attempted at all).
func TestCapabilityContractMinimalRegistersOnlyModuleLevelNoneIDs(t *testing.T) {
	minimal := capability.New()
	registries := registerEverythingForFixture(t, minimal)

	wantRegistered := make(map[string]bool, len(moduleLevelNoneIDs))
	for _, id := range moduleLevelNoneIDs {
		wantRegistered[id] = true
	}
	registeredCount := 0
	for _, row := range capabilityTable {
		got := registries.registered(row)
		assert.Equal(t, wantRegistered[row.id], got, "minimal fixture: id=%s", row.id)
		if got {
			registeredCount++
		}
	}
	assert.Equal(t, len(moduleLevelNoneIDs), registeredCount, "minimal fixture must register exactly the 7 module-level-none IDs")

	called := false
	client := connectSystemd(context.Background(), minimal, func(context.Context) (*dbus.Conn, error) {
		called = true
		return &dbus.Conn{}, nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert.Nil(t, client)
	assert.False(t, called, "connectSystemd must never invoke connect (no dbus dial attempted) when the Systemd capability is absent")
}
