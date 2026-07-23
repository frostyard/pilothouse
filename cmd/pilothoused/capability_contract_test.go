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
// registration depends on: caps.HasAll(required...) must equal whether the
// ID ends up Registered() in its registry. An empty required slice means
// "none" -- always registered, unconditionally.
type capabilityRequirement struct {
	id       string
	registry registryKind
	required []capability.ID
}

// capabilityTable is the complete 51-row mirror of docs/capabilities.md as
// of c12. Every Action*/Query* constant declared in internal/broker/api.go
// (35 Action* + 16 Query*, the 16 including QueryCapabilities added in c6)
// appears exactly once. QueryServicesJournal and QueryLogs use the corrected
// "systemd AND journald" requirement (docs/capabilities.md exceptions #2 and
// #3), not "journald" alone.
var capabilityTable = []capabilityRequirement{
	// Actions (35).
	{broker.ActionFilesUpload, inStreamActions, nil},
	{broker.ActionDockerRemove, inActions, []capability.ID{capability.Docker}},
	{broker.ActionDockerRemoveImage, inActions, []capability.ID{capability.Docker}},
	{broker.ActionDockerRestart, inActions, []capability.ID{capability.Docker}},
	{broker.ActionDockerStart, inActions, []capability.ID{capability.Docker}},
	{broker.ActionDockerStop, inActions, []capability.ID{capability.Docker}},
	{broker.ActionIncusRemove, inActions, []capability.ID{capability.Incus}},
	{broker.ActionIncusRemoveImage, inActions, []capability.ID{capability.Incus}},
	{broker.ActionIncusRestart, inActions, []capability.ID{capability.Incus}},
	{broker.ActionIncusStart, inActions, []capability.ID{capability.Incus}},
	{broker.ActionIncusStop, inActions, []capability.ID{capability.Incus}},
	{broker.ActionMaintenanceReboot, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionPodmanRemove, inActions, []capability.ID{capability.Podman}},
	{broker.ActionPodmanRemoveImage, inActions, []capability.ID{capability.Podman}},
	{broker.ActionPodmanRestart, inActions, []capability.ID{capability.Podman}},
	{broker.ActionPodmanStart, inActions, []capability.ID{capability.Podman}},
	{broker.ActionPodmanStop, inActions, []capability.ID{capability.Podman}},
	{broker.ActionSysextDisable, inActions, []capability.ID{capability.Updex, capability.Sysext}},
	{broker.ActionSysextEnable, inActions, []capability.ID{capability.Updex, capability.Sysext}},
	{broker.ActionSysextRefresh, inActions, []capability.ID{capability.Sysext}},
	{broker.ActionSysextUpdate, inActions, []capability.ID{capability.Updex}},
	{broker.ActionServicesDisable, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionServicesEnable, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionServicesResetFailed, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionServicesRestart, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionServicesStart, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionServicesStop, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionStorageCreateNFS, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionStorageCreateSMBGuest, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionStorageCreateSMBCredentials, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionStorageCreateSMBGuestOwned, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionStorageCreateSMBCredentialsOwned, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionStorageMount, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionStorageUnmount, inActions, []capability.ID{capability.Systemd}},
	{broker.ActionStorageDelete, inActions, []capability.ID{capability.Systemd}},
	// Queries (16).
	{broker.QueryActivity, inQueries, nil},
	{broker.QueryBackupsState, inQueries, []capability.ID{capability.Systemd}},
	{broker.QueryCapabilities, inQueries, nil},
	{broker.QueryDockerLogs, inQueries, []capability.ID{capability.Docker}},
	{broker.QueryDockerState, inQueries, []capability.ID{capability.Docker}},
	{broker.QueryIncusState, inQueries, []capability.ID{capability.Incus}},
	{broker.QueryJobs, inQueries, nil},
	{broker.QueryLogs, inQueries, []capability.ID{capability.Systemd, capability.Journald}},
	{broker.QueryMaintenanceState, inQueries, []capability.ID{capability.Systemd}},
	{broker.QueryPodmanLogs, inQueries, []capability.ID{capability.Podman}},
	{broker.QueryPodmanState, inQueries, []capability.ID{capability.Podman}},
	{broker.QueryServicesJournal, inQueries, []capability.ID{capability.Systemd, capability.Journald}},
	{broker.QueryServicesState, inQueries, []capability.ID{capability.Systemd}},
	{broker.QueryStorageState, inQueries, nil},
	{broker.QueryFilesDownload, inStreamQueries, nil},
	{broker.QueryFilesList, inQueries, nil},
}

// moduleLevelNoneIDs is the exact 7 broker IDs whose documented requirement
// is "none" -- the only IDs a minimal (empty capability.Set) fixture should
// register. Verified against capabilityTable at TestCapabilityTableHasExactlyFiftyOneRows.
var moduleLevelNoneIDs = []string{
	broker.QueryFilesList,
	broker.QueryFilesDownload,
	broker.ActionFilesUpload,
	broker.QueryActivity,
	broker.QueryJobs,
	broker.QueryStorageState,
	broker.QueryCapabilities,
}

func TestCapabilityTableHasExactlyFiftyOneRows(t *testing.T) {
	require.Len(t, capabilityTable, 51, "docs/capabilities.md documents 51 broker IDs (35 Action* + 16 Query*, including QueryCapabilities)")
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
	}
	assert.Equal(t, 35, actionCount, "expected 35 Action* IDs")
	assert.Equal(t, 16, queryCount, "expected 16 Query* IDs")
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
// registerStorageActions, registerMaintenance, registerSysextActions,
// registerPodman, registerDocker, registerIncus, plus the always-on
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
// capability.Set below, every one of the 51 registered broker IDs must be
// present in its registry iff the fixture's Set satisfies that ID's
// documented required capabilities from capabilityTable (kept as a Go-side
// mirror of docs/capabilities.md, cross-checked by inspection, not parsed
// automatically). The all-on and minimal fixtures get additional dedicated
// assertions below; every fixture (including the representative partials)
// is walked against the full table.
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
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			registries := registerEverythingForFixture(t, fixture.caps)
			for _, row := range capabilityTable {
				want := fixture.caps.HasAll(row.required...)
				got := registries.registered(row)
				assert.Equal(t, want, got, "fixture=%s id=%s required=%v", fixture.name, row.id, row.required)
			}
		})
	}
}

// TestCapabilityContractAllOnReproducesPrePhaseBehavior asserts the all-on
// fixture registers every one of the 51 IDs -- reproducing pre-phase
// behavior exactly for the 50 pre-existing Action*/Query* constants, plus
// QueryCapabilities (51 total), since every documented requirement is a
// subset of the full capability vocabulary.
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
