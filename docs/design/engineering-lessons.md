# Engineering lessons

Living document. Rationale:
[ADR-0014](../adr/0014-adopt-the-repository-baseline.md),
[ADR-0007](../adr/0007-capability-table-contract-tests.md),
[ADR-0004](../adr/0004-hand-transcribed-packaging-contract-oracle.md).
Contracts: the guards named in each section.

## Overview

The rules below come from defects and rejected reviews in earlier work on
Pilothouse. Each rule was checked against the code when it was moved here;
each section names the code or the guard it applies to. Code comments and
docs link these sections by their anchor
(`docs/design/engineering-lessons.md#<anchor>`), so keep the anchors stable.

These lessons were first written as 50 separate files in
`docs/agents/skills/` by an automated spec-to-PR harness (the mill, now
archived). The baseline cleanup
([ADR-0014](../adr/0014-adopt-the-repository-baseline.md)) moved the
lessons that still hold into this file, under anchors named after the old
file names, and deleted the lessons about the mill's own plan and chunk
process; where such a lesson also held a reusable engineering rule, only
that rule is kept here, under the old anchor. Git history keeps the
originals.

Where a lesson names code that does not follow it yet, the section says so
under "Open gap". Fix the gap in its own change; do not copy the old shape.

## Design

### Contract and capability tests

<a id="completeness-tests-need-live-source-of-truth"></a>
#### A completeness test compares against the live source

A test that claims "every X is covered" must derive the actual set from the
live source, not from a second hand-kept list. A test that only checks the
fixture's own length or per-kind counts passes when a real ID is dropped and
a placeholder takes its place, because both hand-kept lists drift together.

- `cmd/pilothoused/capability_contract_test.go` parses
  `internal/broker/api.go` with `go/parser`/`go/ast` (`declaredBrokerIDs`)
  and compares `capabilityTable` with it
  (`TestCapabilityTableMirrorsBrokerAPIConstants`).
- `cmd/pilothouse/capability_contract_test.go` builds the real registry with
  `newRegistry(true)` and a real `web.NewServer`
  (`newCapabilityContractServer`) instead of a copied module list, and diffs
  `capabilityRequirements`/`capabilityAnyRequirements` against the same
  parsed constants.
- `packaging/drift_test.go` (`TestContractTablesMatchGoreleaserConfig`)
  compares the contract tables with the live `.goreleaser.yaml`. The
  contract itself stays hand-transcribed on purpose
  ([ADR-0004](../adr/0004-hand-transcribed-packaging-contract-oracle.md)).

When the true set cannot be derived mechanically, say so in the test's
comment. Do not present a fixture-against-fixture check as a completeness
guarantee.

<a id="new-capability-id-and-its-completeness-table-must-land-together"></a>
#### A new broker ID and its table row land in the same change

The contract tests read `internal/broker/api.go` as live source. When a new
`Action*`/`Query*` constant exists, or a web module calls a broker ID that is
missing from `capabilityRequirements`/`capabilityAnyRequirements`,
`go test ./...` fails until the row and the hard-coded totals change too
(`capabilityTable` in `cmd/pilothoused/capability_contract_test.go`,
`TestCapabilityTableHasExactlySixtyFourRows`). So the change that adds the
ID also adds its row and updates the totals, in `docs/capabilities.md` too.
Richer per-fixture coverage (canned responses, new fixtures) can follow in a
later change.

<a id="dont-use-the-gate-under-test-as-the-test-oracle"></a>
#### Do not use the gate under test as the test oracle

When a test checks a gating predicate across many modules and fixtures, the
expected value must not come from that same predicate
(`want := platform.Available(m, caps)`). A regression, such as a module that
gains a wrong `CapabilityGate`, then moves the expected and the actual side
together and the test stays green.

Write the expected matrix by hand from the spec, and evaluate it with code
that does not share the production predicate:

- `cmd/pilothouse/capability_contract_test.go`: the hand-written maps
  `moduleRequiredCapabilities` and `moduleRequiredAnyCapabilities`, read by
  `expectModuleAvailable`.
- `cmd/pilothoused/capability_contract_test.go`: its own
  `allOfPresent`/`anyOfPresent` helpers built from single `capability.Set.Has`
  calls and Go's `&&`/`||`, never `HasAll`/`HasAny`, because the daemon's
  guards use those (for example `registerHostImage` calls
  `caps.HasAny(Bootc, RPMOStree)`).

<a id="exercise-the-actual-boundary-not-a-precomputed-shim"></a>
#### Drive the real boundary, not a shim that takes its output

When a rule says behavior comes through a boundary (a fake `Host`'s
`Capabilities()`, the broker client, the process exit status), the test must
go through that boundary. A test-only helper that takes the boundary's return
value as a plain argument (a `capability.Set`, a computed result) proves the
downstream logic, but not the wiring: a broken or missing call still passes.
If no seam exists, add one to the production code.

For capability gating this means: build a real
`web.NewServer(registry, brokerClient, logger, secureCookie)` with a fake
broker that reports the capability set, use the module's real `New()`, render
through `Server.Handler()`, and assert on the HTML. Do not call
`platform.Available` with a hand-built set. Examples:
`cmd/pilothouse/fleet_test.go`, `cmd/pilothouse/capability_contract_test.go`,
and `cmd/verify-packages/integration_test.go`, which runs real artifacts
through `extract.Deb`/`extract.RPM` and `packaging.Verify`.

<a id="test-the-composing-function-not-its-merge-helper"></a>
#### Test the composing function, not its merge helper

A function that calls several probes or collectors and combines the results
needs tests that call that function, with fakes injected at its boundaries,
for partial success and for total failure. A test of only the merge helper
still passes when the function drops a probe or calls the wrong one.

- `internal/capability/probe.go`: `Probe` is tested directly
  (`TestProbeAllFailingYieldsEmptySet`,
  `TestProbePartialSuccessContainsExactlySucceedingIDs` in
  `internal/capability/probe_test.go`), not only `unionSets`.
- `cmd/verify-packages/main_test.go` drives `run`, whose return value is the
  exit status, never a reporting or dispatch helper.
- `packaging/extract/extract_test.go` and `packaging/extract/rpm_test.go`
  follow the same rule for the extractors.

If the function hard-codes host-dependent calls with no seam, add the seam.

<a id="dbus-fake-must-cover-full-success-path"></a>
#### A fake must reach the whole success path

When the SDK client is a concrete type (`*dbus.Conn` from
`github.com/coreos/go-systemd/v22/dbus`), a fake for the connector alone
cannot test the code after a successful connection: a zero `&dbus.Conn{}`
panics or hangs when its methods run. Split the dependency so that every call
on the success path goes through an interface a test can fake.

In `internal/capability/probe_systemd.go`, `probeSystemd` takes a
`sessionOpener`, and the unit-file listing goes through the `unitFileLister`
interface (`probeAutoupdatePairs`). So
`TestSystemdPresentWhenMarkerExistsAndConnectionSucceeds` tests
`probeSystemd` itself, not only `dialSystemd`. The same rule applies to
`AutoUpdateManager` in `internal/modules/maintenance/autoupdate_manager.go`
(see `autoupdate_manager_test.go`).

<a id="enumerate-the-full-test-matrix-for-multi-axis-criteria"></a>
#### Write out the full matrix for a multi-axis requirement

A requirement such as "Query, Execute, StreamAction and StreamQuery mark the
capability cache down on `ErrUnavailable`, leave it on other errors, and
never call `QueryCapabilities` themselves" is a cross-product: methods by
conditions by invariants. Write the matrix as a checklist and give every cell
an assertion. When a review names some missing cells, derive the matrix again
from the requirement; do not patch only the named cells.

Example: `internal/web/server_test.go`
(`TestExecuteMarksCapabilitiesDownOnlyOnUnavailable`,
`TestQueryMarksCapabilitiesDownOnlyOnUnavailableAndNeverRefetchesItself`,
`TestStreamActionMarksCapabilitiesDownOnlyOnUnavailable`,
`TestStreamQueryMarksCapabilitiesDownOnlyOnUnavailable`) and
`internal/web/capabilities_test.go`
(`TestCapabilityCacheNoteResultOnlyMarksDownOnUnavailable`).

<a id="scope-guard-assertions-to-the-exact-region-under-test"></a>
#### Scope each assertion to the region under test

`strings.Contains(page, name)` cannot tell "in the nav but not on the
dashboard" from "in both" when the same text can appear in more than one
region. Cut out each region first (by container id or class, a landmark, or a
region-specific anchor such as `href="/<module>"`), then assert on each region
alone. Write one present/absent pair per registry (nav, dashboard cards, nav
on other pages, routes). Examples: `extractRequiredSection` in
`cmd/pilothouse/capability_contract_test.go`, `systemPickerRegion` in
`internal/web/shell_test.go`, and `artifactBlock`/`assertVerifiedBlock` in
`cmd/verify-packages`, which apply the same rule to multi-artifact text
output.

The rule is not only for HTML. For a guard over shell, YAML or other source
text:

- Parse a structured format (YAML, JSON, TOML) and index into the job, step
  or key under test. A substring can also match an unrelated job.
- Match only executable lines. A commented-out assertion still contains the
  text.
- Anchor the match to the command it is about. A redirect such as
  `<"$journal_response"` can occur on more than one line of the same script.
- A regex meant per line needs `(?m)`, or a line-by-line scan
  (`requireNoPrivateKeyPrint` in `packaging/vm_harness_test.go`), and must
  match variable spellings too (`$VM_SSH_KEY`, not only `id_ed25519`).

Before you trust such a guard, break the behavior on purpose (delete the
assertion, comment it out, move it, use a variable) and check that the guard
fails.

Open gap: some guards still match raw text.
`TestPackagingWorkflowVMJobDownloadsOnlyTheMatchingFormat`
(`packaging/workflow_vm_job_test.go`) checks
`name: ${{ matrix.artifact }}` with `require.Containsf` on the raw workflow;
`TestVMRebootPostureDriftGuard` and
`TestVMCheckJournalAssertsTheDaemonsOwnLineInTheBrokerResponse`
(`packaging/vm_harness_test.go`) use `require.Contains` on the whole script.

<a id="drift-guard-membership-checks-need-the-converse-direction-too"></a>
#### A drift guard checks both directions

"Every expected item is declared" proves only a subset relation. When the
two sides must match, also collect every declared item in the category and
require set (or multiset) equality, so an extra declared row fails too.
Before you finish, add one bogus row in your head and check that the test
fails.

Example: `TestBinaryDestinationsMatchBuilds` in `packaging/drift_test.go`
compares the full sorted list of `/usr/bin` requirement destinations with
the build-derived list, so a missing, duplicate or extra row fails.

### Fixtures and fakes

<a id="canned-fixtures-need-populated-data-for-what-they-assert"></a>
#### Canned data must populate what the test asserts

When a fake returns an empty collection, per-item elements (a row's Mount,
Unmount or Delete form) never render under any fixture, so "the form is
absent under the gated fixture" is vacuously true. Put at least one record
in every collection whose per-item elements the test must prove present or
absent, and cover each conditional branch.

In `cmd/pilothouse/capability_contract_test.go`, `cannedStorageSnapshot`
carries a mounted and an unmounted managed mount (the per-row Mount form
renders only when a mount is neither "mounted" nor "needs-attention"), and
`cannedHostImageStatus` fills booted, staged and rollback slots plus
rpm-ostree `Version`/`Checksum`. `cannedQueryResponse` returns empty `{}` or
`[]` for every other query, so do not use it for a query whose per-item
markup a test asserts.

<a id="calibrate-canned-fixture-data-per-capability-set"></a>
#### Calibrate canned data to each capability set

One canned response shared by every fixture carries data that some
fixtures' hosts can never produce, for example rpm-ostree detail for a
bootc-only host. The test then cannot prove that "detail from an absent
source does not render". Give each fixture a response that its capability set
can produce, and add a failure fixture for every source the requirement
names (N sources need N failure fixtures).

In `cmd/pilothouse/capability_contract_test.go`:
`cannedHostImageStatusBootcOnly`, `cannedHostImageStatusRPMOStreeFailed`,
`cannedHostImageStatusBootcFailed`, `cannedExtensionsStateUpdexFailed`,
`cannedExtensionsStateSysextFailed`, and the `calibratedAutoUpdateStatus(caps)`
and `calibratedExtensionsState(caps)` helpers, which
`runCapabilityContractFixtureWith` always uses. Keep "never attempted"
(Available false, Error empty) apart from "attempted and failed"
(Error set), as `HostImageManager.Status` does.

<a id="dont-use-chmod-to-simulate-permission-denied-in-tests"></a>
#### Do not use chmod to force a permission failure

`chmod` cannot make a directory unwritable to root or to a process with
`CAP_DAC_OVERRIDE`, and nothing stops a test run as root (a root shell, a
root container, `sudo`). Then the failure branch never runs, and the result
depends on who ran the test. Use a failure that is the same for
every user: a destination that does not exist (ENOENT), an input that is not
the expected format, an unsupported option, or a fake that returns the
error. Example: `TestRPMPipeReportsFailureInEitherHalf` in
`packaging/extract/rpm_test.go` (the "destination half never starts" case
extracts into a missing directory).

### Capability gating and degraded hosts

<a id="gate-every-call-path-not-just-routes-and-nav"></a>
#### Gate every call path, not only routes and nav

`platform.Gate` and `platform.GateAny` (`internal/platform/capability.go`)
protect only the routes a module registers in `Mount()`. In-process code that
calls a module directly bypasses them. When a module gets a capability gate,
grep for every caller of its exported methods and of every cross-module
interface it implements (`platform.HealthProvider`), and gate each one too.

`attention.Module.findings` (`internal/modules/attention/module.go`)
type-asserts each provider to `platform.CapabilityGate` and
`platform.CapabilityGateAny` and skips it, with no `Health()` call, when the
capabilities are absent. Test at the aggregator with a spy that proves the
gated method is never called: `TestFindingsSkipsProviderWhenRequiredCapabilityAbsent`,
`TestFindingsSkipsProviderWhenNoneOfRequiredAnyCapabilitiesPresent` and
siblings in `internal/modules/attention/module_test.go`.

<a id="partial-gate-modules-need-full-view-element-audit"></a>
#### A partial-gate module hides every element of a gated route

A partial-gate module keeps its page and nav entry but gates some routes.
`storage` is the example: its inventory is always present, and the three
remote-mount routes (`GET /storage/mounts/new`, `POST /storage/mounts`,
`POST /storage/mounts/{id}/{action}`) are wrapped in `platform.Gate` with
`remoteMountCapabilities` (systemd). Hide every link, form or button that
targets a gated route, not only the ones the spec names: list the gated route
family (`{action}` covers mount, unmount and delete), grep the module's
`.templ` files for each `href`/`action`/`hx-post` into it, and put all of
them behind one condition. In `internal/modules/storage/views.templ` that
condition is the `remoteMountsAvailable` parameter of `ManagedPage`,
`ManagedSnapshotRegion` and `ManagedMountTable`.

<a id="map-every-spec-requirement-to-a-chunk-or-explicit-deferral"></a>
#### A move or a central change updates every consumer

Two rules for a change that moves behavior or changes a shared mechanism:

- **Move: the destination gets its checks first.** When a change moves a
  feature from one module or file to another, the destination gets the
  tests that assert the rendered output there (a section, a field, a table)
  in the same change that removes the source copy, or before it. "The data
  is still in the query" is not enough: the feature is gone for the user
  when no page renders it. Example: extension update availability moved
  from Maintenance to Extensions (#52).
  `TestAvailableUpdatesSection` (`internal/modules/sysext/views_test.go`)
  asserts the destination; `TestPageOmitsExtensionUpdateAvailability` and
  `TestSummaryOmitsExtensionUpdateAvailability`
  (`internal/modules/maintenance/views_test.go`) assert the removal.
- **Central change: update every chokepoint.** When a change alters a
  central mechanism, find every place that decides by that mechanism and
  update it in the same change. Example: whole-module gating has 2 shapes,
  `platform.CapabilityGate` (all-of) and `platform.CapabilityGateAny`
  (any-of). `moduleAvailable` in `internal/web/server.go` checks both
  (`platform.Available(...) && platform.AvailableAny(...)`) for the nav
  (`availableManifests`) and the dashboard; `attention.Module.findings`
  type-asserts both (see
  [gate-every-call-path-not-just-routes-and-nav](#gate-every-call-path-not-just-routes-and-nav)).
  A third gate shape needs both places, or the module stays visible.
  Guard: `TestModuleAvailabilityCoversCapabilityGateCapabilityGateAnyAndNeither`
  in `internal/web/server_test.go`.

Before you call such a change done, list each requirement of the change and
name the test or the code that meets it; a requirement with neither is not
done.

<a id="gate-zero-value-fields-on-their-sources-available-flag"></a>
#### Read a single-source field only when that source answered

In an aggregate fed by several independent sources (`sysext.ExtensionsState`,
`HostImageStatus`, `AutoUpdateStatus`), a field that only one source fills
stays at its zero value when that source is absent or failed. `false` from a
live answer and `false` from "never ran" are the same bytes. Check that
source's own `*Available`/`*Error` pair before you derive anything from the
field, and test the partial case (one source up, one down), not only all-up
and all-down.

Example: `mergedButDisabledReasons` in
`internal/modules/maintenance/manager.go` returns nothing unless both
`UpdexAvailable` (with empty `UpdexError`) and `SysextAvailable` (with empty
`SysextError`) hold, because `Extension.Enabled` comes from updex alone.
Guard: `TestStateSkipsExtensionReasonsWhenUpdexDidNotAnswer` in
`internal/modules/maintenance/manager_test.go`.

<a id="distinguish-transient-errors-from-auth-rejection"></a>
#### Clear session state only on a real rejection

A call to another process can fail by rejection (`broker.ErrUnauthorized`)
or transiently (`broker.ErrUnavailable`, a timeout, a refused connection).
Only the rejection clears client state. On a transient error, keep the cookie
and answer with a retryable error. Clearing it on any error logs the user out
during an outage and breaks the recovery path that depends on the session.

`Server.authenticate` in `internal/web/server.go` calls
`clearSessionCookie` only when `errors.Is(err, broker.ErrUnauthorized)`;
other errors call `capabilities.noteResult(err)` and return 503, and the
`staleAfterOutage` path refetches capabilities after recovery. Guard:
`TestAuthenticateRefetchesCapabilitiesAfterOutageRecovery` in
`internal/web/server_test.go` drives a wrapped `ErrUnavailable` and asserts
that the cookie is not cleared. A new error branch of this kind needs a test
that drives a transient error, not only the rejection.

<a id="reuse-bounded-context-after-capability-probe"></a>
#### Bound the real connection like the probe

When a probe with a timeout decides that a dependency is present and a second
connection is opened later to build the managers, that second connection
needs a bound too. `context.Background()` there brings back the startup hang
that the probe exists to prevent, if the dependency stops answering between
the two calls.

In `cmd/pilothoused/main.go`, `run` gives `connectSystemd` a context with
`systemdConnectTimeout` (5 seconds). Guards:
`TestConnectSystemdCancelsTimedOutConnectAndWarnsPromptly` and
`TestConnectSystemdClosesLateSuccessAfterTimeout` in
`cmd/pilothoused/main_test.go`.

### Go code

<a id="fix-the-defect-class-not-the-quoted-counterexample"></a>
<a id="validate-external-status-json-strictly"></a>
#### Parse external status JSON strictly; fix the class, not the example

A parser for JSON from a host tool (`bootc status --json`,
`rpm-ostree status --json`) must reject malformed input with an error and a
zero result, never return a confident empty success.

- Check a discriminator (`kind`, `apiVersion`) without a condition. A check
  behind `if field != ""` lets a document that omits the field pass, because
  `encoding/json` leaves it at the zero value.
- Check that every substructure the result depends on is present (`status`,
  `status.booted`) before you build the result.
- Test each failure mode alone: missing discriminator, wrong discriminator,
  correct discriminator with a missing substructure. `{}` alone does not
  exercise these branches.

`ParseBootcStatus` in `internal/modules/maintenance/hostimage.go` does all
of these; guard: `TestParseBootcStatusMalformed` in `hostimage_test.go`.

When a review quotes one input that slips through, it shows a class of bug.
Find the general rule the input breaks and fix it everywhere in the same
function. If the next review quotes a different input for the same defect,
the earlier fix patched the example, not the rule.

<a id="package-wide-rename-or-removal-needs-a-whole-package-grep"></a>
#### A package-wide rename or ban needs a whole-package grep

A rename ("`Feature` becomes `Extension`") or a ban ("no non-test code in this
package imports `os/exec`") applies to every file in the package, not only
the files a plan names. Grep the whole package (all `.go` and `.templ` files)
for the old name as a type, field, method and return type, and for the banned
import or call. Exported struct fields are the easiest to miss, because views
render them.

Example: package `internal/modules/sysext` imports no `os/exec` and no
`CommandRunner`; the exec-backed code lives in
`internal/modules/sysext/extctl`, and the exported `AvailableUpdate` in
`internal/modules/sysext/state.go` names its owner `Extension`.

<a id="gates-chunk-skips-build-tagged-files"></a>
#### Build-tagged files need a tagged build

Files behind `//go:build sdjournal` are not compiled by `go vet ./...`,
`go test ./...`, `make race` or `golangci-lint run` (`.golangci.yml` sets no
build tags). The tagged files are `internal/capability/probe_journald.go`,
`internal/modules/logs/journal/journal_sdjournal.go` (and its test) and
`internal/modules/services/journal/journal_sdjournal.go`. In the `Makefile`:

- `make build` compiles `./cmd/pilothoused` with `-tags sdjournal`, which
  imports all three packages.
- `make test` runs `go test -tags sdjournal` only for
  `./internal/modules/logs/journal/...`.

So a change to a tagged file needs `make build` (or
`go build -tags sdjournal ./...`) at least, not only vet and test.

<a id="templ-generated-files"></a>
#### Generated `*_templ.go` files stay untracked

`.gitignore` ignores `/**/*_templ.go`, and `git ls-files '*_templ.go'` is
empty. So a generated file never appears in a diff: do not ask for one in a
review, and check generated content by reading the regenerated file on disk.
Doc comments for a templ component go in the `.templ` file. A `.gitignore`
rule does not untrack a file Git already tracks: if a generated file shows up
as tracked, remove it from the index (`git rm --cached <path>`) in its own
change.

### Packaging and extraction

<a id="piped-subprocess-pairs-need-concurrent-wait"></a>
#### Piped subprocesses need a concurrent wait

Waiting on the producer and then the consumer deadlocks when the consumer
exits early on a payload larger than the pipe buffer: the producer blocks on
the full pipe, its `Wait` never returns, and `exec.CommandContext` kills only
on context cancellation. Reap both processes concurrently, and drop the
parent's copies of the pipe once both children run, so a producer without a
reader gets SIGPIPE.

`runPipe` in `packaging/extract/rpm.go` (`rpm2archive` into `tar`) uses an
`os.Pipe`, closes both parent ends after both `Start` calls, and waits on
both halves in goroutines. Guard: the last case of
`TestRPMPipeReportsFailureInEitherHalf` in `packaging/extract/rpm_test.go`,
where the consumer exits first while the producer still has more than a
pipe buffer to write. A happy-path or producer-fails-first case cannot reach
the deadlock.

<a id="classify-walked-entries-by-info-mode-not-direntry-type"></a>
#### Classify walked entries from one mode source

When a walk already calls `d.Info()` for the entry's mode, classify the entry
from that same `info.Mode()` (`IsDir()`, `IsRegular()`), not from
`d.IsDir()`/`d.Type()`. Two sources for the same fact can disagree, and a
zero `fs.FileMode` satisfies `IsRegular()`. Go's `os` package on Linux fills
an unknown `d_type` with `lstat`, so `filepath.WalkDir` over a real
directory is safe today; an `fs.DirEntry` from another `fs.FS` gives no such
promise.

Open gap: `debEntries` in `packaging/extract/deb.go` calls `d.Info()` for
`Mode` but still switches on `d.IsDir()` and `d.Type().IsRegular()`.

<a id="validate-declared-destinations-against-path-traversal"></a>
#### Reject `..` in a declared destination before you join it

A helper that drops only empty and `.` path components keeps `..`, so
`filepath.Join(root, part)` can walk out of the staging tree, and a declared
destination such as `/../../outside` creates directories, changes modes or
writes files outside it. This holds for test fixture builders as much as for
production extractors. Reject every `..` component, or clean the path and
check that the result stays under the root, before any filesystem call.

Open gap: `splitPath` in `internal/packagingtest/deb.go` keeps `..`
components, and nothing in `BuildDeb`/`BuildDebRaw` rejects them before
`makeDirs` and `writeFileMode`.

<a id="docker-run-must-not-trust-a-user-supplied-image-default"></a>
#### Pin `--platform` and `--user` when the image is a caller's variable

When a `docker run` image comes from a variable (`INSTALL_IMAGE`), Docker
picks the host architecture from a multi-architecture index unless
`--platform` is given, and runs as the image's `USER` unless `--user` is
given. If the target needs a fixed architecture or root, put both flags in
the command; prose and example values do not bind. `make
verify-package-install` passes `--platform linux/amd64 --user 0:0` (see the
comment above the target in the `Makefile` and
[ADR-0011](../adr/0011-digest-pinned-test-images-no-default-install-image.md)).
No test guards these two flags; check the `docker run` line itself in
review.

### Guards over source text

<a id="go-source-meta-guards-need-ast-parsing-not-regex"></a>
#### A guard over Go source parses the AST

A regex over Go source matches one spelling of a call. Go allows many more:
whitespace before `(`, an aliased or dot import, a method value, a call
through a variable, a non-literal argument. Parse with `go/parser` and walk
with `go/ast`:

- Match `*ast.CallExpr` and resolve the callee structurally
  (`*ast.SelectorExpr` plus the import), not by text.
- For "every call site is allowed", collect all matching calls and require
  that the count equals the allowed count, not "at least 1 good call".
- For "the argument is a literal", check for `*ast.BasicLit`.

Example: the process audit in `packaging/imagetest/image_process_test.go`
(`TestImageProcessLiveAudit`, `TestImageProcessDifferential`,
`TestImageProcessAuditIsNotVacuous`), with
`TestImageProcessGuardAnchor` in `image_guard_anchor_test.go` proving that
those tests still exist and carry no build constraint.

Open gap: `TestVMGuardsNeverExecuteTheHarness` in
`packaging/vm_harness_test.go` still checks `exec.Command(` call sites with a
regex.

<a id="harness-wide-guard-must-scan-every-file-not-one-named-entrypoint"></a>
#### A harness-wide guard scans every file of the harness

A guard that claims a property for the whole harness must scan every file
that can contain the policed primitive, not only the entry point a change
touched, and must discover files recursively (`filepath.WalkDir`, not
`os.ReadDir`), so a file added later in a subdirectory is still checked.
Derive the set from the tree, and check each call site, not one literal path
spelling.

In `packaging/vm_harness_test.go`, `vmHarnessFiles` walks all of `test/vm`
(used by `TestVMOrchestratorCopiesOnlyIntoTheStagingDirectory`,
`TestVMHarnessContainsNoCredentialLiteral` and other guards), and
`vmGuestScripts` walks `test/vm/guest` recursively.
Before you finish such a guard, add in your head 1 new file in each covered
directory and 1 call through a variable, and check that the guard catches
them.

<a id="verify-a-named-helpers-actual-behavior-before-building-a-criterion-on-it"></a>
#### Read a helper before you build a guard on it

Before a new guard reuses an existing helper, read what the helper does,
not its name. `vmCodeLines` in `packaging/vm_harness_test.go` strips shell
quoting (through `vmUnquote`), so a guard built on it cannot tell a quoted
literal such as `'$(curl ...)'` from a real command substitution. For that
distinction, use `vmRawCodeLines`, which keeps the quoting.

### Shell harness

<a id="dont-blanket-suppress-command-status-in-shell-harnesses"></a>
#### Do not discard a command's status to allow an expected side effect

`>/dev/null 2>&1 || true` around a command whose success drops the
connection (a reboot over SSH) also hides a rejected `sudo -n`, a missing
binary or any other real failure; the next wait loop then times out with a
misleading message. Capture the status and stderr, accept only the expected
symptoms, and fail at once, by name, on anything else.

`reboot_guest` in `test/vm/lib/ssh.sh` runs `systemctl reboot` through
`guest_sudo_probe`, accepts only status 0, 255 (the connection closed) and
124 (the probe's bound), and reports any other status with the remote output
before `wait_for_boot_id_change` runs.

<a id="async-subshell-ignores-interrupt"></a>
#### An asynchronous subshell starts with SIGINT and SIGQUIT ignored

Without job control (the default for non-interactive bash), bash starts
`( ... ) &` with SIGINT and SIGQUIT set to `SIG_IGN`. From bash 5.3 (Ubuntu 26.04), an `exec` in that
subshell keeps them ignored; bash 5.2 restored the defaults. A wrapper can
hide this: GNU `timeout` installs its own handler, so its command gets the
default action back, but uutils `timeout` (the Ubuntu 26.04 default) keeps
the inherited `SIG_IGN`. A group that ignores INT does not stop when the
owner forwards INT to it. Reset the dispositions in the subshell before
`exec` (`trap - INT QUIT`), and do not rely on a wrapper to do it. The
reset restores the dispositions the script started with: it cannot recover
a signal that was already ignored when the script started, because bash
does not let a non-interactive shell trap or reset such a signal.

`run_bounded` in `test/image/ucore-image-test.sh` does this (#255). Guards:
`TestUCoreRunBoundedRestoresDefaultInterruptAndQuit` reads the dispositions of
the phase command, and `TestUCoreOrchestratorForwardsSignalsAndWaits` also
runs with a `timeout` shim that keeps an ignored signal. Both fail without
the reset on bash 5.3 or later; on bash 5.2 they pass without it.

<a id="shell-helper-exports-die-in-command-substitution"></a>
#### A helper that exports cannot run in `$(...)`

Command substitution runs in a subshell, so every `export` or assignment the
function makes is lost; only stdout survives. Call such a helper directly and
pass any value back through a variable it sets or a file. Write the exact
call form in the helper's comment.

In `test/vm/lib/cloudinit.sh`, `generate_credentials` exports `VM_SSH_KEY`
and `VM_CREDS_ENV`, and `create_seed` also exports `VM_SEED_ISO`; both are
documented as called directly, never through a command substitution. Guard:
`TestVMCloudInitSeedExportsItsWorkspacePaths` in
`packaging/vm_harness_test.go`.

<a id="testable-entrypoint-needs-a-source-guard-not-a-bare-final-call"></a>
#### An entry point that tests source needs a source guard

A script that ends with a bare `main "$@"` runs the whole program when a
test sources it to reach its functions. If a test must source an entry
point, wrap the call:

```bash
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
```

and do not also require the last line to be a bare `main "$@"`. Today no Go
test sources an entry point: `test/vm/vm-boot-test.sh` ends with a bare
`main "$@"`, and `TestVMGuardsNeverExecuteTheHarness` requires the VM guards
to read the harness as text only.

<a id="prove-negative-properties-behaviorally-not-with-syntax-denylists"></a>
#### Prove a "never does X" property by behavior

A list of forbidden syntax forms is always incomplete (`[[ -e ]]` but not
`[[ -p ]]`, `stat` but not `file`, an `export` inside a subshell). Test the
runtime property instead:

- "Never inspects the path": the result is the same when the path exists,
  is a directory, is a dangling symlink, or is absent.
- "Never exports state": diff value-bearing output before and after a call
  in the current shell (normalized `export -p`, plus `declare -f` for each
  exported function). Name-only lists (`compgen -e`, `declare -Fx`) miss a
  changed value or a redefined body.
- "Never writes outside the root": snapshot the tree outside the root before
  and after, and diff.

Keep text checks for properties that are syntactic by nature (for example
"no `eval`").

<a id="scope-preservation-checks-to-cases-with-a-checkable-precondition"></a>
#### Check "input left unchanged" only where there is an input to check

"Every refusal leaves the presented entry unchanged" has a subject only when
the input names an entry that exists. Split a negative-case matrix by that
precondition:

- An existing entry (a directory, a symlink to one) gets the identity, type
  and mode check. For a symlink, check the link itself with `Lstat` and its
  target separately: a link removed and recreated to the same target passes
  a target-only check.
- No argument, an empty string, a relative path or a missing path have no
  entry; prove instead that nothing in the whole sandbox changed.

<a id="path-authorization-by-equality-must-bind-the-comparand-not-just-the-result"></a>
#### Equality with a recorded path is not containment

"The resolved target equals the recorded path" is not "the target is inside
the authorized root".

- Make the recorded value immutable. If the same variable records and
  authorizes, code can repoint it.
- A symlink outside the root can resolve to the recorded path. Also check
  that the path as given lies inside the root.
- Check by hand that the canonicalization handles every `.`, `..` and
  symlink spelling you claim it does.
- For a destructive command (`rm -rf -- "$target"`), know whether it acts on
  the link or on what it points to.

Example: `test/image/ucore-image-test.sh` builds `workspace` from a resolved,
non-symlink `/var/tmp` parent and a UUID nonce, marks it `readonly`, and
removes only that path (`rm -rf --one-file-system -- "$workspace"`), and only
after the exact-store reset.

### Documentation

<a id="grep-the-whole-doc-for-every-restatement-of-a-stale-claim"></a>
#### Find every restatement of a stale claim

A fact is often stated in more than one place, in different words. When one
statement goes stale, search the whole repository, not only the paragraph
you edit:

- the literal phrase, the feature or module name, and every count that
  looks related (ID totals, query totals, row counts);
- docs outside `docs/`: `README.md`, `AGENTS.md`, and the user pages in
  `site/content/`;
- code: test comments and test names hard-code totals too (for example
  `TestCapabilityTableHasExactlySixtyFourRows` and the comments around
  `capabilityTable` in `cmd/pilothoused/capability_contract_test.go`).

`docs/capabilities.md` records each phase's totals as history ("raising the
totals to 40/24/64"). A new phase reads every such sentence and greps for the
old number pairs themselves, not for one phrasing. When a review quotes the
same file and line twice, the earlier edit missed that line: diff that exact
line.

<a id="blanket-nil-input-claims-must-hold-per-backend"></a>
#### Check a blanket claim for every backend it covers

A sentence such as "a nil client behaves as if every read failed" about a
type that reports on several backends must hold for each backend. Trace each
backend's code path, and fix every place that restates the claim (the Go doc
comment and the matching `docs/` page).

Example: `AutoUpdateManager.Status`
(`internal/modules/maintenance/autoupdate_manager.go`, described again in
`docs/autoupdate.md`). A nil systemd client leaves
`BootcAutoUpdate.Policy` at custom/unknown, but
`RPMOStreeAutoUpdate.Policy` comes from `/etc/rpm-ostreed.conf` and does not
depend on the client.

<a id="dont-claim-a-shared-parse-without-verifying-memoization"></a>
#### "Shared" means a cached result, not a shared instance

Before you write that several consumers share one parse or one command run,
find the cache (a field, a `sync.Once`, a TTL) in the method. Without one,
each caller runs the work again, even through the same `*Manager`, and two
results in one page render can differ.

Example: `HostImageManager.Status`
(`internal/modules/maintenance/hostimage_manager.go`) has no cache. It runs
for `QueryHostImageStatus` (`registerHostImage` in
`cmd/pilothoused/main.go`) and again for `QueryMaintenanceState`
(`SystemManager.hostImageState` in `internal/modules/maintenance/manager.go`).
The only real cache on that path is the 1-minute extensions cache in
`SystemManager.extensionState`.

<a id="doc-worked-examples-must-match-the-fixture-function-they-cite"></a>
#### A worked example that names a fixture copies that fixture

When a doc attributes an example payload to a named function, its rows,
fields and the prose around it are checkable facts. Open the function, list
everything it builds (including what it inherits from a base fixture), and
make the payload and the prose match. Then read the prose against the payload
next to it.

Example: `docs/capabilities.md` shows `QueryExtensionsState` payloads from
`cannedExtensionsState`, `cannedExtensionsStateUpdexFailed` and
`cannedExtensionsStateSysextFailed` in
`cmd/pilothouse/capability_contract_test.go`; the failed variants project
`extensionsStateFromSources(updexAnswered, sysextAnswered)` and keep every
row the other source contributes.

<a id="or-composed-gate-prose-must-match-hasany-not-hasall"></a>
#### Prose about a missing capability checks HasAll against HasAny

Before you write "without capability X, module Y disappears", check Y's
gate. `RequiredCapabilities` (`platform.CapabilityGate`) is all-of;
`RequiredAnyCapabilities` (`platform.CapabilityGateAny`) is any-of. An
any-of module stays when one of its sources is absent; only what that source
alone backs goes away.

Example: `internal/modules/sysext` (Extensions) is any-of
`Updex`, `Sysext`. Without updex, the module, its nav entry, its dashboard
card and `QueryExtensionsState` stay when systemd-sysext is present; only
the operations that need updex go. `docs/modules.md` records each module's
gate (for `sysext`: `HasAny(Updex, Sysext)`).

<a id="dont-doc-ahead-of-the-chunk"></a>
#### Write each doc clause from the landed code

A sentence that describes a mechanism is a factual claim. Write it from the
code in the same change, not from the intended design, and check it clause
by clause:

- what a function takes and calls: `platform.Available(module, caps)`
  takes an already computed `capability.Set` and never calls `Host`;
- when state refreshes: the web capability cache refreshes at login and,
  after a call has seen `broker.ErrUnavailable`, at the next authenticated
  request (`refreshCapabilities` and `staleAfterOutage` in
  `internal/web/server.go`); nothing else refreshes it;
- what is logged, and in which order calls happen: `ProbeIncus` in
  `internal/capability/probe_engines.go` builds the client
  (`newIncusProbeClient`, no I/O) before the `enabled` check in
  `probeIncus`, so "a disabled probe builds no client" is false;
- every table that lists the siblings of a new item (a new flag goes into
  each flag list, with its default);
- an example command: the claim next to it must hold for that exact command
  (a `pilothoused` run without `--incus` probes no Incus socket and
  advertises no Incus capability, so the Incus nav entry and routes are
  absent).

A doc claim must also be true at the commit that adds it, not only after
later work lands.

<a id="verify-shell-acceptance-criteria"></a>
#### Run a command before you cite it

A command that a doc or a test comment gives as evidence (a count, a
property) must be run against the tree before it is written down. In a
single-quoted `grep` pattern, `\t` is not a tab: with the GNU grep on this
host, `grep -c '^\tAction' internal/broker/api.go` prints `0` while the file
has 40 `Action*` lines. Use `[[:space:]]`, as `docs/capabilities.md` does.
After `git add`, plain `git diff -- <file>` is empty; use
`git diff --cached` or `git diff <base>`. A `--stat` line count does not
prove which lines changed.

## Operational notes

- Add a lesson here in the change that shows it: a rule that a test cannot
  enforce, or one that applies to more than one change. When it comes from a
  review finding, also add a line to `.memory/corrections.jsonl`
  ([ADR-0014](../adr/0014-adopt-the-repository-baseline.md)).
- Prefer a guard test over a lesson. When a test enforces the rule, name it
  in the section.
- Keep anchors stable: code comments and docs cite them. When a lesson
  becomes untrue, change it in the same change; remove it when the code it
  names is gone, and fix the citations.
- An "Open gap" line records code that does not follow its lesson yet. Remove
  the line in the change that fixes the code.

## References

- Rationale: [ADR-0014](../adr/0014-adopt-the-repository-baseline.md),
  [ADR-0007](../adr/0007-capability-table-contract-tests.md),
  [ADR-0004](../adr/0004-hand-transcribed-packaging-contract-oracle.md),
  [ADR-0011](../adr/0011-digest-pinned-test-images-no-default-install-image.md)
- Contracts: [capabilities.md](../capabilities.md),
  [specs/artifact-contract.md](../specs/artifact-contract.md)
- Related: [overview](overview.md), [capability gating](capability-gating.md),
  [artifact extraction](artifact-extraction.md),
  [packaging test fixtures](packaging-test-fixtures.md),
  [VM harness](vm-harness.md), [image tier](image-tier.md),
  [modules](../modules.md), [autoupdate](../autoupdate.md)
