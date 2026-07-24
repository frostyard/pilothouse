# Handler capability table

This is the binding reference for the broker capability model (phase 1a of
issue #35, `.mill/spec.md`). It maps every broker ID registered today —
across all four registries (`QueryRegistry`, `ActionRegistry`,
`StreamQueryRegistry`, `StreamActionRegistry`) in `cmd/pilothoused/main.go` —
to the capability (or capabilities) it will require once its registration is
capability-guarded. `registerPodman`/`registerDocker`/`registerIncus` (and
the new `QueryCapabilities` itself), plus `registerServices`,
`registerLogs`, `registerBackups`, `registerStorageActions`,
`registerMaintenance`, `registerHostImage`, and `registerSysextActions`, are
all actually capability-guarded — every row in this table reflects current,
landed behavior, not a future guarantee, and
`cmd/pilothoused/capability_contract_test.go` enforces the full table across
a fixture matrix of capability sets.

**Running total:** `internal/broker/api.go` declares exactly 35 `Action*`
constants and 17 `Query*` constants today — 52 IDs total, reproducible with:

```sh
grep -c '^[[:space:]]*Action' internal/broker/api.go   # 35
grep -c '^[[:space:]]*Query' internal/broker/api.go    # 17
```

(The POSIX `[[:space:]]` character class is used rather than a literal `\t`
escape, since a bare backslash-`t` is interpreted inconsistently across grep
implementations — GNU grep treats it as a tab as an extension even in BRE,
most other greps do not and silently match nothing.)

Every one of the 52 IDs is registered exactly once across the four
registries in `cmd/pilothoused/main.go`, including `ActionFilesUpload`
(registered via `StreamActionRegistry`) and `QueryFilesDownload` (registered
via `StreamQueryRegistry`) — both are members of the 35/17 above, not IDs
added on top. This table therefore has exactly 52 rows.

Both grep commands above were re-run against this tree when the totals were
last changed, and they are no longer only documentation:
`cmd/pilothoused/capability_contract_test.go`'s
`TestCapabilityTableMirrorsBrokerAPIConstants` parses `internal/broker/api.go`
with `go/ast` and diffs the declared `Action*`/`Query*` constants against
`capabilityTable` **in both directions**, so a constant added without a table
row, a table row naming an ID that no longer exists, or a drift away from
35/17/52 all fail the build. It additionally checks that an `Action*`
constant is filed in an action registry and a `Query*` constant in a query
registry.

`QueryCapabilities` (`org.frostyard.pilothouse.capabilities.list`) landed
during phase 1a alongside the engine conversions and is included in both the
count above and the query table below — this document is updated in the same
chunk that registers a new ID, per the "every currently registered broker ID"
invariant stated above. `cmd/pilothoused/capability_contract_test.go`'s
`capabilityTable` mirrors this document row for row and, per the check just
described, is compared against the live constant declarations rather than
against a second hand-maintained list.

`QueryHostImageStatus` (`org.frostyard.pilothouse.maintenance.host_image_status`)
is the newest ID, added by phase 2 (#51) for read-only host-image reporting,
and is the reason the totals above read 17/52 rather than the 16/51 phase 1a
ended with. It is the table's first **any-of** row: `registerHostImage`
guards it with `caps.HasAny(capability.Bootc, capability.RPMOStree)`, not
`HasAll`, so either source alone is enough (see exception #4 below).

Canonical capability IDs (from `.mill/spec.md`): `systemd`, `journald`,
`updex`, `sysext`, `bootc`, `rpm-ostree`, `autoupdate-rpm-ostree`,
`autoupdate-bootc`, `podman`, `docker`, `incus`.

## Actions (35)

| Broker ID | Module | Capability |
|---|---|---|
| `ActionFilesUpload` | files | none |
| `ActionDockerRemove` | docker | docker |
| `ActionDockerRemoveImage` | docker | docker |
| `ActionDockerRestart` | docker | docker |
| `ActionDockerStart` | docker | docker |
| `ActionDockerStop` | docker | docker |
| `ActionIncusRemove` | incus | incus |
| `ActionIncusRemoveImage` | incus | incus |
| `ActionIncusRestart` | incus | incus |
| `ActionIncusStart` | incus | incus |
| `ActionIncusStop` | incus | incus |
| `ActionMaintenanceReboot` | maintenance | systemd |
| `ActionPodmanRemove` | podman | podman |
| `ActionPodmanRemoveImage` | podman | podman |
| `ActionPodmanRestart` | podman | podman |
| `ActionPodmanStart` | podman | podman |
| `ActionPodmanStop` | podman | podman |
| `ActionSysextDisable` | sysext | updex AND sysext |
| `ActionSysextEnable` | sysext | updex AND sysext |
| `ActionSysextRefresh` | sysext | sysext |
| `ActionSysextUpdate` | sysext | updex |
| `ActionServicesDisable` | services | systemd |
| `ActionServicesEnable` | services | systemd |
| `ActionServicesResetFailed` | services | systemd |
| `ActionServicesRestart` | services | systemd |
| `ActionServicesStart` | services | systemd |
| `ActionServicesStop` | services | systemd |
| `ActionStorageCreateNFS` | storage (remote-mount) | systemd |
| `ActionStorageCreateSMBGuest` | storage (remote-mount) | systemd |
| `ActionStorageCreateSMBCredentials` | storage (remote-mount) | systemd |
| `ActionStorageCreateSMBGuestOwned` | storage (remote-mount) | systemd |
| `ActionStorageCreateSMBCredentialsOwned` | storage (remote-mount) | systemd |
| `ActionStorageMount` | storage (remote-mount) | systemd |
| `ActionStorageUnmount` | storage (remote-mount) | systemd |
| `ActionStorageDelete` | storage (remote-mount) | systemd |

## Queries (17)

| Broker ID | Module | Capability |
|---|---|---|
| `QueryActivity` | activity | none |
| `QueryBackupsState` | backups | systemd |
| `QueryCapabilities` | capability | none *(unconditional — see below)* |
| `QueryDockerLogs` | docker | docker |
| `QueryDockerState` | docker | docker |
| `QueryHostImageStatus` | maintenance (host image) | bootc OR rpm-ostree *(exception — see below)* |
| `QueryIncusState` | incus | incus |
| `QueryJobs` | jobs | none |
| `QueryLogs` | logs | systemd AND journald *(exception — see below)* |
| `QueryMaintenanceState` | maintenance | systemd |
| `QueryPodmanLogs` | podman | podman |
| `QueryPodmanState` | podman | podman |
| `QueryServicesJournal` | services | systemd AND journald *(exception — see below)* |
| `QueryServicesState` | services | systemd |
| `QueryStorageState` | storage (inventory) | none *(exception — see below)* |
| `QueryFilesDownload` | files | none |
| `QueryFilesList` | files | none |

## Module-level defaults applied

Per `.mill/spec.md`: services state/actions → systemd; services journal →
journald; logs → journald; storage remote-mount actions → systemd; backups
→ systemd; maintenance → systemd; podman/docker/incus → their engine
capability; system, files, activity, jobs → none. sysext is per-action, not
module-level.

Maintenance's "→ systemd" default is now a **per-surface** requirement rather
than a whole-module one, per `.mill/spec.md`'s phase-2 re-grounding: reboot
posture and the reboot action (`QueryMaintenanceState`,
`ActionMaintenanceReboot`, guarded by `registerMaintenance`) still require
systemd, while host-image reporting (`QueryHostImageStatus`, guarded by the
separate `registerHostImage`) requires a host-image source instead and no
systemd at all — a bootc host without systemd gets the latter and not the
former. The web module's presence follows suit: `maintenance.Module`
implements `platform.CapabilityGateAny` with
`HasAny(Systemd, Bootc, RPMOStree)`, so the nav entry, dashboard card, and
`GET /maintenance` survive on a bootc-only host while `POST
/maintenance/reboot` stays behind its own `Systemd`-only gate (see
`docs/modules.md`). The web-side rendering of host-image status is not yet
landed and is not described here. The sysext per-action rows are:

- `ActionSysextRefresh` → `sysext`
- `ActionSysextUpdate` → `updex`
- `ActionSysextDisable` / `ActionSysextEnable` → `updex AND sysext`

There is no standalone sysext read query in the registry today (no
`QuerySysext*` constant exists); the sysext module's data reaches the web
page through `QueryMaintenanceState` (see the extension-read note below).

## Exceptions to the module-level defaults

Four rows in this table deviate from the spec's literal module-default
prose. Each is grounded in the actual manager code, not just spec wording —
the module defaults describe steady-state intent; these are the exceptions
section is precisely where actual code dependencies that exceed that intent
belong.

### 1. `QueryStorageState` stays `none`

The spec's module defaults say "storage remote-mount actions → systemd" but
are silent on the inventory read. `internal/modules/storage/manager.go`'s
`NewSystemManager`/`NewSystemManagerWithEnrichers` (lines 50, 54) take only
`Adapter`/`Enricher` values — lsblk, findmnt, SMART, mdraid, LVM,
device-mapper, multipath, ZFS, Btrfs — and never open a D-Bus connection or
otherwise depend on systemd. `registerStorage` is fed by this plain
`storage.Manager` (`storageManager` in `run()`, built by `newStorageManager`
before any systemd dialing happens), while `registerStorageActions` (the
systemd-unit-creating remote-mount lifecycle) is fed by a separate
`storage.RemoteManager` that only exists inside `buildSystemdManagers` when
a systemd client was actually obtained. So `QueryStorageState` is
unconditional/`none` as a real *construction-level* fact, not merely a
registration-level guard bolted onto a manager whose construction could
still depend on systemd: `storageManager`'s construction has no systemd
dependency at all, and `registerStorage(queries, storageManager)` runs
whether or not `connectSystemd` ever returns a non-nil client.

### 2. `QueryServicesJournal` is `systemd AND journald`, not `journald` alone

The spec's module-default prose says "services journal → journald."
`internal/modules/services/manager.go`'s `Journal()` calls
`m.resolveUnit(ctx, name)` before reading journal entries, which uses the
systemd D-Bus client (`m.client`) to validate/resolve the unit — so the
query cannot function without systemd, regardless of journald availability.
As of c7, `services.NewSystemManager` no longer opens that D-Bus connection
itself: it accepts a pre-opened `systemdClient` from its caller
(`cmd/pilothoused/main.go`'s `buildSystemdManagers`), which only calls it at
all when `connectSystemd` already obtained a live connection. A connection
failure is therefore no longer a construction-time error from this
package's constructor; it surfaces upstream as `connectSystemd` returning
`nil` (logged as a warning, never fatal), and `services.NewSystemManager`
simply never gets called in that case. As of c8, `registerServices` also
guards each registration individually against the probed `capability.Set`:
`QueryServicesState` and every services lifecycle action register when
`caps.Has(capability.Systemd)`, while `QueryServicesJournal` additionally
requires `caps.HasAll(capability.Systemd, capability.Journald)` — so a host
with systemd but no journald still gets full service management, with only
the journal query withheld. This is recorded as a refinement of the spec's
stated module default, not a deviation from it: the module-level default
describes the feature's intent ("read the journal"), and the exception
records the actual code dependency that intent doesn't mention.

### 3. `QueryLogs` (the whole logs module) is `systemd AND journald`, not `journald` alone

Same shape as above. `internal/modules/logs/manager.go`'s `Logs()` calls
`m.client.ListUnitsContext(ctx)` and `m.client.ListUnitFilesContext(ctx)` —
both systemd D-Bus calls — to build the returned unit allowlist before any
journal entries are filtered, so the query's true requirement is `systemd
AND journald`. As of c7, `logs.NewSystemManager` likewise no longer dials
D-Bus itself; it accepts a pre-opened `systemdClient`, opened once by
`cmd/pilothoused/main.go`'s `connectSystemd` and passed through
`buildSystemdManagers`. An absent or unreachable systemd bus means
`connectSystemd` returns `nil` and `buildSystemdManagers` never calls
`logs.NewSystemManager` — startup is never aborted by this path. As of c8,
`registerLogs` also guards its single registration directly against the
probed `capability.Set`, requiring `caps.HasAll(capability.Systemd,
capability.Journald)` before registering `QueryLogs` at all. Documented
here as the exceptions section's job: recording a real code dependency that
exceeds the module default's literal wording.

### 4. `QueryHostImageStatus` is `bootc OR rpm-ostree`, the table's only any-of row

Every other row is an AND: the ID registers iff
`caps.HasAll(required...)`. `QueryHostImageStatus` is the first row whose
guard is `caps.HasAny(capability.Bootc, capability.RPMOStree)`
(`registerHostImage` in `cmd/pilothoused/main.go`), because either source
alone yields a usable report — bootc is authoritative for deployment identity
and rpm-ostree is supplementary, so a host with only one of them still has
something honest to say and a host with neither has nothing to report at all.
Inside the handler, `maintenance.HostImageManager.Status` runs only the
sources whose capability was actually probed present (`bootc status --json`
and/or `rpm-ostree status --json`, both read-only, both through an injected
command runner, never a shell), and a source that fails to run or to parse
degrades to its own `*Available: false` / `*Error` pair on the response
rather than failing the query.

This row is also deliberately **independent of maintenance's systemd
requirement**: `registerHostImage` is a separate function from
`registerMaintenance` and consults neither `capability.Systemd` nor the
other's guard, so a bootc host without systemd registers
`QueryHostImageStatus` while `QueryMaintenanceState` and
`ActionMaintenanceReboot` stay withheld, and a systemd host with no image
stack gets the reverse. The response carries raw host-image facts only —
booted/staged/rollback deployments, image references and digests,
supplementary rpm-ostree version/checksum detail, soft-reboot eligibility
when bootc exposes it, and each source's availability/error — and never
reboot-required posture, which remains `QueryMaintenanceState`'s alone.

`cmd/pilothoused/capability_contract_test.go` mirrors the distinction with a
`requireAny` column on its table rows and exercises this one across
bootc-only, rpm-ostree-only, bootc-plus-rpm-ostree, neither-plus-systemd,
`ucore`, and `snosi-without-bootc` fixtures.

The query now has a web consumer: `internal/modules/maintenance`'s
`queryHostImage` calls it whenever the advertised set satisfies
`HasAny(Bootc, RPMOStree)` and returns `nil` (omitting the page's whole
"Host image" section) when it does not. Both sides are covered by
`cmd/pilothouse/capability_contract_test.go`, whose
`capabilityAnyRequirements` table carries this ID's any-of requirement,
hand-transcribed from this document; its fake broker fails the test outright
if the web process ever invokes a broker ID whose capability the fixture's
host does not advertise. The maintenance module's nav, routes, and dashboard
are gated separately, on `HasAny(Systemd, Bootc, RPMOStree)`; that gate
reads no host-image data — it only records whether a host-image source
exists.

It does have one in-process consumer, and only one: `cmd/pilothoused` passes
the same `maintenance.HostImageManager` instance it registers this query
from into `maintenance.NewSystemManager` as a `HostImageSource`, so
`QueryMaintenanceState` can read the staged-deployment fact without a second
path to bootc. That consumption does not blur the two queries'
responsibilities — `QueryHostImageStatus` still returns raw facts and no
reboot-required field, and `QueryMaintenanceState` is still the sole owner of
reboot-required posture, which is exactly where the staged deployment becomes
the reason "A staged host image deployment requires activation by reboot."
`QueryMaintenanceState`'s response also gains `soft_reboot_capable`, copied
verbatim from `HostImageStatus.SoftRebootCapable` (three-state: omitted when
the host's bootc does not report eligibility, never a synthesized false) —
an independent copy of the same parsed value, not a recomputation, and
informational only: it never makes `reboot_required` true and no soft-reboot
action exists. See the extension-read note below for how the bootc leg
degrades.

## Extension-read note (`QueryMaintenanceState` / sysext)

`.mill/spec.md` says sysext reads are "updex OR sysext" — there is no
standalone sysext read query; that describes the extension-read subpath
inside `QueryMaintenanceState`, which the spec notes already performs
daemon-side extension reads today (maintenance's update source invokes
updex). The *registration* of `QueryMaintenanceState` (and
`ActionMaintenanceReboot`) is guarded on `systemd` (the module-level default
for maintenance, matching the rows above) by `registerMaintenance`, which
takes the probed `capability.Set` and no-ops entirely when `systemd` is
absent, exactly like `registerBackups`/`registerStorageActions`.
`maintenance.NewSystemManager` has no D-Bus dependency of its own (it
depends only on the sysext manager, job store, and command runner), so
unlike backups/services/logs there is no construction-level non-fatal-
startup fix needed here — the manager is always constructed, and this
registration guard is the only thing withholding it.

Separately, as of c10, `maintenance.SystemManager`'s `extensionState` method
degrades its extension-read subpath gracefully based on the probed
`updex`/`sysext` capabilities threaded into `NewSystemManager`'s new
`updexAvailable`/`sysextAvailable` parameters, rather than erroring:
`sysext.SystemManager.Check()` (which produces `Updates`) only ever invokes
`updex`, while `List()` (which produces `Features`, driving "disabled but
merged, reboot required" reasons) invokes `updex` to enumerate feature
definitions and additionally `systemd-sysext` to attach installed/merged
status.

- updex and sysext both present: unchanged pre-chunk behavior — `Updates`
  and `Features`/merged-derived reboot reasons both populate.
- updex present, sysext absent: `Check()` still runs (`Updates` populates,
  since `Check` never touches `systemd-sysext`); `List()` is skipped
  entirely (merged-but-disabled reboot reasons omitted, no attempt, no
  error), since installed/merged status is meaningless without
  `systemd-sysext`.
- updex absent (sysext present or absent): neither `Check()` nor `List()`
  runs — both require `updex` to enumerate feature definitions in the first
  place — so `Updates` and feature-derived reboot reasons are both omitted.
  Recorded as an honest limitation of today's `sysext.SystemManager`
  (enumeration is updex-only by construction), not a phase-1a shortfall.

In no combination does `State()` return an error because of missing
updex/sysext, and non-extension fields (`Jobs`, `OSVersion`, reboot-marker-
derived reasons) are unaffected in every combination.
`internal/modules/maintenance/manager_test.go` has one dedicated test case
per combination.

### Host-image read note (`QueryMaintenanceState` / bootc)

`NewSystemManager` takes a third capability flag, `bootcAvailable`
(`caps.Has(capability.Bootc)`), paired with the `HostImageSource` described
above, and its host-image read follows the identical convention:

- bootc present: the source is read exactly once per `State()` call. A
  non-nil staged deployment adds the staged-deployment reboot reason and
  factors into `reboot_required`; `SoftRebootCapable` is copied onto
  `soft_reboot_capable` regardless of whether anything is staged.
- bootc present but the read fails: the failure is not propagated — no
  staged reason and no `soft_reboot_capable` for that call. Per-source
  availability and errors belong to `QueryHostImageStatus`
  (`bootc_available`/`bootc_error`); the aggregate posture stays answerable
  when one input cannot be read.
- bootc absent: the source is never called at all — not attempted and
  failed, simply not attempted — so no staged reason appears and
  `soft_reboot_capable` stays omitted, whatever the source would have
  reported.

`State()` never returns an error because bootc is absent or unreadable, and
`internal/modules/maintenance/manager_test.go` covers each case, proving the
absent-bootc case with a call-counting source.

## `jobs` query

`QueryJobs` is not named in the spec's "system, files, activity → none"
list, but it is generic job-store infrastructure tied to no probed
capability, exactly like `QueryActivity` — treated the same way:
unconditional/`none`.

## `QueryCapabilities` query

`QueryCapabilities` (`org.frostyard.pilothouse.capabilities.list`), added in
c6, is registered unconditionally by `registerCapabilities` in
`cmd/pilothoused/main.go` — capability discovery itself requires no
capability, since it is what reports the probed `capability.Set` in the
first place. It is an ordinary authenticated broker query, not a new
unauthenticated endpoint: any authenticated identity may call it (non-admin,
like `QueryActivity`/`QueryJobs`), and its handler returns exactly the
`capability.Set` produced by `internal/capability.Probe` at startup, whose
`MarshalJSON` already yields the sorted, present-only
`{"capabilities": [...]}` shape the query contract requires. This row is
therefore `none` in the same sense as `QueryActivity`/`QueryJobs` above: no
guard is possible or needed, because the query's entire purpose is to
report what the guard inputs currently are.

## Phase 1b (#54) — web-side gating complete

Phase 1a (#50) taught `pilothoused` to gate its own privileged registrations
on the probed `capability.Set` and published this table as the binding
ID→capability map. Phase 1b (#54) is complete: the unprivileged web process
(`cmd/pilothouse`) now derives its **effective module set, navigation, routes,
dashboard cards, and actions** from this same table. It fetches the advertised
`capability.Set` via `QueryCapabilities` on login (and re-fetches on the first
successful authenticated request after a broker outage), filters navigation
and dashboard cards through `platform.Available`, and gates individual routes
with `platform.Gate`. `platform.Registry` itself is still built
unconditionally at startup and every module's `Mount` still runs — routes stay
mounted on the shared mux, and absence is enforced per request: a request for
a route whose capability is missing 404s at request time, and the module's
nav entry and dashboard card are omitted from that render. See `docs/modules.md`'s
"Whole-module web-side capability gating" and `yeti/OVERVIEW.md`'s "Web-side
capability gating (end state, #54)" for the mechanism and the exact
module→capability mapping the web process applies.

The **sysext web surface is unchanged and out of scope for #54.** The web
process still constructs `sysext.NewSystemManager` directly from its own
`--updex` config, and no `platform.CapabilityGate` or `platform.Gate` is
applied to any sysext route, navigation entry, dashboard card, or action.
Web-side capability-gating of sysext reads is deferred to **#52**, where
those reads move behind the broker. The sysext *action* rows above
(`ActionSysext*`) describe the daemon-side (`cmd/pilothoused`) per-action guard
from phase 1a (#50); they are the broker's registration guard, not a web-side
gate, and #54 does not touch them.

Because the sysext web controls render unconditionally, a fixture whose host
advertises neither `updex` nor `sysext` can still reach an `ActionSysext*`
call from a rendered control. `cmd/pilothouse/capability_contract_test.go`
records this as `webSideUngatedBrokerIDs`, a closed four-entry exemption from
its fake broker's "never invoke a gated-off broker ID" check, and
`TestWebSideUngatedExemptionExcludesHostImageSurfaces` pins it so it cannot
grow to cover any maintenance or host-image ID. The privilege boundary is not
affected: the *daemon* does not register those actions at all without
`updex`/`sysext`, which
`cmd/pilothoused/capability_contract_test.go`'s matrix proves for every
fixture, so such a call fails at the broker rather than executing. Deleting
the four entries is what re-arms the web-side check once #52 lands the gate.

## Phase 2 (#51) — host-image contract parity (daemon + web)

Phase 2 adds exactly one broker ID, `QueryHostImageStatus`, and no mutation
action. Both contract-test harnesses are now driven by the same fixture
vocabulary, so the daemon's registration guards and the web process's gates
are proven to agree on every host shape this phase cares about.

**Named fixtures.** Two fixtures are named directly after the spec's
acceptance criteria and exist in both harnesses:

| Fixture | Capabilities | What it proves |
|---|---|---|
| `ucore` | `systemd`, `journald`, `bootc`, `rpm-ostree`, `podman`, `docker`, `incus` | Read-only bootc state with supplementary rpm-ostree detail: `QueryHostImageStatus` and `QueryMaintenanceState` are both registered and both called, and the Maintenance page renders the booted/staged/rollback deployments together with the reboot-required card. |
| `snosi-without-bootc` | `systemd`, `journald`, `updex`, `sysext`, `podman`, `docker`, `incus` | Snosi without bootc remains supported: `QueryHostImageStatus` is not registered, the web side never calls it, and the page omits the "Host image" section entirely rather than erroring — while every systemd-gated surface keeps working unchanged. |

The web harness adds a third, `bootc-only` (`bootc` and nothing else), which
is what proves maintenance's whole-module gate is a genuine OR rather than a
disguised systemd gate: the nav entry and `GET /maintenance` are present,
`POST /maintenance/reboot` 404s, and `QueryMaintenanceState` is never called.
Two further runs replay the `ucore` fixture with one source failing, so the
symmetric `bootc_available`/`bootc_error` and
`rpm_ostree_available`/`rpm_ostree_error` pairs are each exercised in *both*
directions rather than only where the source works:

- **`ucore-rpm-ostree-read-failure`** (`rpm_ostree_available: false` plus
  `rpm_ostree_error`). bootc still answered, so all three deployment rows and
  their image references and digests still render; what goes away is
  rpm-ostree's supplementary version/checksum detail, replaced by a named
  `data-source-error="rpm-ostree"` indicator.
- **`ucore-bootc-read-failure`** (`bootc_available: false` plus `bootc_error`,
  with rpm-ostree answering). This is what `HostImageManager.Status` actually
  produces in that case: bootc is authoritative for deployment *presence*, so
  no deployment row renders at all and soft-reboot eligibility is unknown,
  while a named `data-source-error="bootc"` indicator appears and every
  systemd-gated surface — including `QueryMaintenanceState` and the reboot
  form — is undisturbed.

Because each source's failure is asserted alongside the other source's
success, neither failure run can degenerate into a second copy of the success
run.

**Populated fixture data.** The web harness's canned broker responses for
`QueryHostImageStatus` and `QueryMaintenanceState` carry representative data —
all three deployment slots, bootc-authoritative image references and digests,
rpm-ostree-supplementary version and checksum, soft-reboot eligibility, and a
reboot-required posture with a reason — so that assertions about a rendered
element being *absent* under a degraded fixture are meaningful rather than
vacuously true against an empty response. Because the per-element assertions
are driven from each fixture's own response (which is what lets the two
failure runs share one assertion helper), `TestCannedHostImageFixtureIsPopulated`
pins that default response's shape directly: if it quietly lost its staged
slot, its rollback slot, its rpm-ostree detail, or its soft-reboot flag, every
fixture would simply agree the corresponding markup is expectedly absent and
the matrix would keep passing while proving nothing.

**Independent oracles.** Both harnesses decide what a fixture *should* see
using local `allOfPresent` / `anyOfPresent` helpers that combine
single-membership `capability.Set.Has` checks with Go's own `&&` / `||` —
never by calling `capability.Set.HasAll` / `HasAny`, `platform.Available`, or
`platform.AvailableAny`. That matters specifically because this phase's gates
*are* those aggregation predicates (`platform.CapabilityGateAny` →
`AvailableAny` → `HasAny(Systemd, Bootc, RPMOStree)` for the module,
`HasAny(Bootc, RPMOStree)` for `queryHostImage` and `registerHostImage`).
Evaluating the expectation with the predicate under test would be
tautological: if `HasAny` silently degraded into `HasAll`, expectation and
behavior would move together and every any-of fixture would keep passing.
With the split in place, that mutation fails the `bootc-only` and
`snosi-without-bootc` web fixtures and the `bootc-only` / `rpm-ostree-only`
daemon fixtures. `TestWebSideOracleTablesAreCompleteAndDisjoint` additionally
pins the two any-of tables literally, checks the two web-side broker-ID tables
are disjoint and together cover all 52 IDs, and asserts the two helpers do not
collapse into each other.

**Static guarantees.** Two of the spec's constraints are enforced as
executable checks in `cmd/pilothoused/capability_contract_test.go` rather than
as prose only:

- `TestNoHostImageMutationActionExists` parses `internal/broker/api.go` and
  fails if any `Action*` constant's identifier or wire ID names `bootc` or
  `rpm-ostree`/`ostree`. `Query*` constants are exempt by design —
  `QueryHostImageStatus` is precisely the read-only surface this phase adds.
- `TestMaintenanceNeverReferencesZincati` walks `internal/modules/maintenance`
  and fails if any **non-comment** source line mentions Zincati (Go files are
  tokenized with `go/scanner` so comment exclusion is exact). Explaining in a
  comment why Zincati is not consulted stays allowed; a token that reaches the
  compiler does not.
