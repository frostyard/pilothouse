package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
//   - false (the default, an AND row): "every capability in required is
//     present" must equal whether the ID ends up Registered() in its registry.
//   - true (an OR row, e.g. QueryHostImageStatus, whose registerHostImage
//     guard is HasAny(Bootc, RPMOStree)): "at least one capability in required
//     is present" must equal it instead.
//
// Both rules are evaluated by this file's own allOfPresent/anyOfPresent
// helpers, which combine single-membership capability.Set.Has checks with Go's
// own && / || -- deliberately NOT by calling capability.Set.HasAll/HasAny.
// Those two aggregation predicates are precisely what this phase's guards are
// built on (registerHostImage calls caps.HasAny(Bootc, RPMOStree)), so using
// them as the expectation would make the matrix tautological in exactly the
// way docs/agents/skills/dont-use-the-gate-under-test-as-the-test-oracle.md
// describes: an any-of guard that silently degraded into an all-of one would
// shift the expected and actual sides together and keep passing.
type capabilityRequirement struct {
	id         string
	registry   registryKind
	required   []capability.ID
	requireAny bool
}

// allOfPresent reports whether every id is in caps. It is the independent
// re-derivation of HasAll semantics (vacuously true for zero ids, which is how
// a "none" row spells "always registered"), built only from Has.
func allOfPresent(caps capability.Set, ids []capability.ID) bool {
	for _, id := range ids {
		if !caps.Has(id) {
			return false
		}
	}
	return true
}

// anyOfPresent reports whether at least one id is in caps. It is the
// independent re-derivation of HasAny semantics (false for zero ids -- "any of
// nothing" has nothing to satisfy it), built only from Has.
func anyOfPresent(caps capability.Set, ids []capability.ID) bool {
	for _, id := range ids {
		if caps.Has(id) {
			return true
		}
	}
	return false
}

// satisfiedBy reports whether a fixture's capability set satisfies this row,
// under whichever of the two satisfaction rules the row declares, without
// routing through the HasAll/HasAny predicates the production guards call.
func (row capabilityRequirement) satisfiedBy(caps capability.Set) bool {
	if row.requireAny {
		return anyOfPresent(caps, row.required)
	}
	return allOfPresent(caps, row.required)
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

// --- live source of truth: internal/broker/api.go ----------------------
//
// docs/capabilities.md states the 52/35/17 totals as a documented,
// reproducible fact ("grep -c '^[[:space:]]*Action' internal/broker/api.go"
// → 35, "grep -c '^[[:space:]]*Query' internal/broker/api.go" → 17; both
// re-run against this tree while writing this chunk). The helpers below turn
// that documented grep into an executed assertion by parsing the same file
// with go/ast, so the table above is compared against the *live* constant
// declarations rather than against a second hand-maintained list that could
// drift with it (docs/agents/skills/completeness-tests-need-live-source-of-
// truth.md: a fixture-vs-fixture length check would pass identically if a
// real ID were silently dropped and a placeholder substituted).

// brokerAPIPath is internal/broker/api.go relative to this package's
// directory, which is the working directory `go test` runs this file in.
const brokerAPIPath = "../../internal/broker/api.go"

// maintenanceSourceDir is internal/modules/maintenance relative to this
// package's directory, scanned by TestMaintenanceNeverReferencesZincati.
const maintenanceSourceDir = "../../internal/modules/maintenance"

// declaredBrokerID is one Action*/Query* constant as actually declared in
// internal/broker/api.go: its Go identifier and its string value (the wire
// ID). Both are captured because the spec's "no bootc/rpm-ostree mutation
// action" constraint is a claim about each, not only about the identifier.
type declaredBrokerID struct {
	name  string
	value string
}

// declaredBrokerIDs parses internal/broker/api.go and returns every declared
// constant whose identifier begins with Action or Query, in declaration
// order. Parsing the real file is the point: this is the "actual" side of
// every completeness assertion below, so a constant added, removed, or
// renamed in api.go changes this result immediately and without anyone
// remembering to update a mirror.
func declaredBrokerIDs(t *testing.T) []declaredBrokerID {
	t.Helper()
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, brokerAPIPath, nil, 0)
	require.NoErrorf(t, err, "parsing %s", brokerAPIPath)

	var declared []declaredBrokerID
	for _, decl := range parsed.Decls {
		genDecl, isGen := decl.(*ast.GenDecl)
		if !isGen || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec, isValue := spec.(*ast.ValueSpec)
			if !isValue {
				continue
			}
			for index, ident := range valueSpec.Names {
				if !strings.HasPrefix(ident.Name, "Action") && !strings.HasPrefix(ident.Name, "Query") {
					continue
				}
				require.Greaterf(t, len(valueSpec.Values), index, "%s: constant %s has no value expression", brokerAPIPath, ident.Name)
				literal, isLiteral := valueSpec.Values[index].(*ast.BasicLit)
				require.Truef(t, isLiteral && literal.Kind == token.STRING, "%s: constant %s is not a string literal", brokerAPIPath, ident.Name)
				value, unquoteErr := strconv.Unquote(literal.Value)
				require.NoErrorf(t, unquoteErr, "%s: constant %s has an unparsable string literal", brokerAPIPath, ident.Name)
				declared = append(declared, declaredBrokerID{name: ident.Name, value: value})
			}
		}
	}
	return declared
}

// TestCapabilityTableMirrorsBrokerAPIConstants is the completeness check
// capabilityTable's own length assertion cannot be: it diffs the table
// against internal/broker/api.go's live Action*/Query* declarations in both
// directions, so neither a new constant missing from the table nor a table
// row naming an ID that no longer exists can survive. It also pins the
// documented 35/17/52 totals to the parsed source rather than to the table,
// and cross-checks that an Action* constant is filed in an action registry
// and a Query* constant in a query registry.
func TestCapabilityTableMirrorsBrokerAPIConstants(t *testing.T) {
	declared := declaredBrokerIDs(t)

	declaredActions, declaredQueries := 0, 0
	declaredByValue := make(map[string]string, len(declared))
	for _, entry := range declared {
		assert.NotContainsf(t, declaredByValue, entry.value, "%s declares the wire ID %q more than once", brokerAPIPath, entry.value)
		declaredByValue[entry.value] = entry.name
		if strings.HasPrefix(entry.name, "Action") {
			declaredActions++
		} else {
			declaredQueries++
		}
	}
	assert.Equal(t, 35, declaredActions, "%s must declare 35 Action* constants (docs/capabilities.md: grep -c '^[[:space:]]*Action' %s)", brokerAPIPath, brokerAPIPath)
	assert.Equal(t, 17, declaredQueries, "%s must declare 17 Query* constants (docs/capabilities.md: grep -c '^[[:space:]]*Query' %s)", brokerAPIPath, brokerAPIPath)
	assert.Len(t, declared, 52, "%s must declare 52 broker IDs in total", brokerAPIPath)

	tableIDs := make(map[string]capabilityRequirement, len(capabilityTable))
	for _, row := range capabilityTable {
		tableIDs[row.id] = row
	}
	for _, entry := range declared {
		row, inTable := tableIDs[entry.value]
		if !assert.Truef(t, inTable, "%s declares %s (%q) but capabilityTable has no row for it; add the row and the matching docs/capabilities.md entry", brokerAPIPath, entry.name, entry.value) {
			continue
		}
		switch {
		case strings.HasPrefix(entry.name, "Action"):
			assert.Containsf(t, []registryKind{inActions, inStreamActions}, row.registry, "%s is an Action* constant but capabilityTable files it in a query registry", entry.name)
		default:
			assert.Containsf(t, []registryKind{inQueries, inStreamQueries}, row.registry, "%s is a Query* constant but capabilityTable files it in an action registry", entry.name)
		}
	}
	for id := range tableIDs {
		assert.Containsf(t, declaredByValue, id, "capabilityTable has a row for %q, which %s no longer declares as an Action*/Query* constant", id, brokerAPIPath)
	}
}

// TestNoHostImageMutationActionExists enforces the spec's "no bootc or
// rpm-ostree image mutation action is exposed anywhere" acceptance criterion
// as checkable behavior rather than prose: no Action* constant in
// internal/broker/api.go may name bootc or rpm-ostree, in its Go identifier
// or in its wire ID. Query* constants are deliberately exempt --
// QueryHostImageStatus is exactly the read-only surface this phase adds --
// so the check is scoped to the mutation half of the API by constant prefix,
// which is also how the broker's own ActionRegistry/QueryRegistry split
// works.
func TestNoHostImageMutationActionExists(t *testing.T) {
	forbidden := []string{"bootc", "ostree", "rpmostree", "rpm-ostree", "rpm_ostree"}
	actionCount := 0
	for _, entry := range declaredBrokerIDs(t) {
		if !strings.HasPrefix(entry.name, "Action") {
			continue
		}
		actionCount++
		lowerName := strings.ToLower(entry.name)
		lowerValue := strings.ToLower(entry.value)
		for _, needle := range forbidden {
			assert.NotContainsf(t, lowerName, needle, "%s declares mutation action %s, which names %q; #51 exposes no host-image mutation action", brokerAPIPath, entry.name, needle)
			assert.NotContainsf(t, lowerValue, needle, "%s declares mutation action %s with wire ID %q, which names %q; #51 exposes no host-image mutation action", brokerAPIPath, entry.name, entry.value, needle)
		}
	}
	require.Equal(t, 35, actionCount, "expected to have scanned all 35 Action* constants; a zero/short scan would make the assertions above vacuous")
}

// TestMaintenanceNeverReferencesZincati enforces the spec's "Maintenance does
// not special-case Zincati" acceptance criterion as checkable behavior: no
// non-comment source line under internal/modules/maintenance may mention
// Zincati. Comments are excluded deliberately -- explaining *why* Zincati is
// not consulted is legitimate documentation, while a token that reaches the
// compiler (an identifier, a unit name, a string literal) is the
// special-casing the spec forbids.
//
// Go files are tokenized with go/scanner in its comment-skipping mode, so
// comment exclusion is exact rather than heuristic. Non-Go files (.templ)
// have no such tokenizer available here, so they use the literal reading of
// the criterion: a line whose first non-space characters begin a comment is
// a comment line, everything else is a source line.
func TestMaintenanceNeverReferencesZincati(t *testing.T) {
	scanned := 0
	err := filepath.WalkDir(maintenanceSourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		extension := filepath.Ext(path)
		if extension != ".go" && extension != ".templ" {
			return nil
		}
		source, readErr := os.ReadFile(path)
		require.NoErrorf(t, readErr, "reading %s", path)
		scanned++
		mentions := nonCommentMentions(t, path, source, extension == ".go", "zincati")
		assert.Emptyf(t, mentions, "%s mentions Zincati outside a comment (%s); Maintenance must not special-case Zincati", path, strings.Join(mentions, "; "))
		return nil
	})
	require.NoErrorf(t, err, "walking %s", maintenanceSourceDir)
	require.Positive(t, scanned, "expected to have scanned at least one source file under %s; a zero-file walk would make the assertion above vacuous", maintenanceSourceDir)
}

// nonCommentMentions returns a "path:line: text" description of every
// non-comment occurrence of needle (matched case-insensitively) in source.
// Go input is tokenized with go/scanner in its comment-skipping mode, so
// comments are excluded exactly; other input drops whole lines whose first
// non-space characters open a comment, which is the literal reading of the
// acceptance criterion's "non-comment source line".
func nonCommentMentions(t *testing.T, path string, source []byte, isGo bool, needle string) []string {
	t.Helper()
	var mentions []string
	if isGo {
		fileSet := token.NewFileSet()
		file := fileSet.AddFile(path, fileSet.Base(), len(source))
		var goScanner scanner.Scanner
		goScanner.Init(file, source, func(position token.Position, message string) {
			t.Fatalf("%s: scanning failed at %s: %s", path, position, message)
		}, 0)
		for {
			pos, tok, literal := goScanner.Scan()
			if tok == token.EOF {
				break
			}
			text := literal
			if text == "" {
				text = tok.String()
			}
			if strings.Contains(strings.ToLower(text), needle) {
				mentions = append(mentions, fileSet.Position(pos).String()+": "+text)
			}
		}
		return mentions
	}
	for index, line := range strings.Split(string(source), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.Contains(strings.ToLower(line), needle) {
			mentions = append(mentions, path+":"+strconv.Itoa(index+1)+": "+trimmed)
		}
	}
	return mentions
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

// ucoreCapabilitySet is the spec's "uCore fixture": an image-based host that
// has systemd (and journald), both host-image sources, and every container
// engine, but no system-extension tooling. It is the fixture the acceptance
// criterion "uCore fixture reports read-only bootc state with supplementary
// rpm-ostree detail" names, and the one where every host-image surface in
// this phase is simultaneously present.
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
// fixture: a systemd host with sysext/updex tooling and container engines,
// but no bootc and no rpm-ostree. It is the fixture the acceptance criterion
// "Snosi without bootc remains supported; host-image state is omitted rather
// than failing" names -- QueryHostImageStatus is simply not registered here,
// while every systemd-gated surface (QueryMaintenanceState,
// ActionMaintenanceReboot, services, logs, backups, storage actions) keeps
// working exactly as before this phase.
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

// TestCapabilityContractHostImageOnNamedHostFixtures asserts the two
// spec-named host shapes against literal, hand-written true/false
// expectations rather than against capabilityTable's satisfiedBy rule.
//
// The matrix walk below is already independent of the production HasAll/
// HasAny predicates (satisfiedBy routes through this file's own allOfPresent/
// anyOfPresent helpers), but it still reads each row's *required capability
// list* from capabilityTable. That leaves one residual coupling this test
// closes: a row whose requirement was silently edited -- QueryHostImageStatus
// quietly regaining a Systemd requirement, say -- would shift the matrix's
// expectation and its observed behavior together and keep passing. The
// expectations here name the fixture, the ID, and the expected outcome
// outright, transcribed from docs/capabilities.md's table and the spec's
// acceptance criteria, so nothing about them can move with the code.
func TestCapabilityContractHostImageOnNamedHostFixtures(t *testing.T) {
	t.Run("ucore", func(t *testing.T) {
		registries := registerEverythingForFixture(t, ucoreCapabilitySet())
		assert.True(t, registries.queries.Registered(broker.QueryHostImageStatus),
			"uCore advertises bootc and rpm-ostree, so the read-only host-image query must be registered")
		assert.True(t, registries.queries.Registered(broker.QueryMaintenanceState),
			"uCore advertises systemd, so reboot posture must stay registered")
		assert.True(t, registries.actions.Registered(broker.ActionMaintenanceReboot),
			"uCore advertises systemd, so the reboot action must stay registered")
		assert.False(t, registries.actions.Registered(broker.ActionSysextUpdate),
			"uCore advertises no updex, so sysext update actions must stay withheld")
	})
	t.Run("snosi-without-bootc", func(t *testing.T) {
		registries := registerEverythingForFixture(t, snosiWithoutBootcCapabilitySet())
		assert.False(t, registries.queries.Registered(broker.QueryHostImageStatus),
			"Snosi without bootc advertises neither host-image source, so host-image state must be omitted rather than registered")
		assert.True(t, registries.queries.Registered(broker.QueryMaintenanceState),
			"Snosi without bootc still advertises systemd, so reboot posture must remain supported")
		assert.True(t, registries.actions.Registered(broker.ActionMaintenanceReboot),
			"Snosi without bootc still advertises systemd, so the reboot action must remain supported")
		assert.True(t, registries.actions.Registered(broker.ActionSysextUpdate),
			"Snosi without bootc advertises updex, so sysext update actions must remain supported")
	})
}

// TestCapabilityContractAcrossFixtureMatrix is the binding contract test the
// spec requires as the final chunk of this phase: for every fixture
// capability.Set below, every one of the 52 registered broker IDs must be
// present in its registry iff the fixture's Set satisfies that ID's
// documented requirement from capabilityTable (whose *set of IDs* is diffed
// against internal/broker/api.go's live go/ast-parsed declarations by
// TestCapabilityTableMirrorsBrokerAPIConstants, while each row's required
// capability list stays a hand-transcribed mirror of docs/capabilities.md,
// pinned for the host-image rows by
// TestCapabilityContractHostImageOnNamedHostFixtures) -- under all-of
// semantics for an ordinary row and any-of
// semantics for an any-of row, each evaluated by this file's own
// allOfPresent/anyOfPresent helpers rather than by the capability.Set.HasAll/
// HasAny predicates the production guards call, so the expectation cannot
// shift in lockstep with a regression in those predicates. The all-on and
// minimal fixtures get additional dedicated assertions below; every fixture
// (including the representative partials) is walked against the full table.
//
// The four synthetic host-image fixtures (bootc-only, rpm-ostree-only, both,
// and neither-with-systemd) exist so QueryHostImageStatus's any-of row is
// exercised in every direction: each source alone registers it, both
// register it, neither withholds it, and none of that changes with Systemd
// present or absent (bootc-only and rpm-ostree-only carry no Systemd;
// neither-plus-systemd carries it without a source).
//
// The last two fixtures are the spec's own named host shapes -- "ucore"
// (systemd + journald + bootc + rpm-ostree + every engine) and
// "snosi-without-bootc" (systemd + journald + updex/sysext + every engine,
// with neither host-image source) -- walked here against the full 52-row
// table so every row, not only the host-image ones, is proven on the two
// real-world combinations the acceptance criteria name.
// TestCapabilityContractHostImageOnNamedHostFixtures above additionally
// pins their host-image rows to hand-written expectations.
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
		{"ucore", ucoreCapabilitySet()},
		{"snosi-without-bootc", snosiWithoutBootcCapabilitySet()},
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
