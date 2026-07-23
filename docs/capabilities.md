# Handler capability table

This is the binding reference for the broker capability model (phase 1a of
issue #35, `.mill/spec.md`). It maps every broker ID registered today —
across all four registries (`QueryRegistry`, `ActionRegistry`,
`StreamQueryRegistry`, `StreamActionRegistry`) in `cmd/pilothoused/main.go` —
to the capability (or capabilities) it will require once its registration is
capability-guarded. As of c11, `registerPodman`/`registerDocker`/
`registerIncus` (and the new `QueryCapabilities` itself), plus
`registerServices`, `registerLogs`, `registerBackups`,
`registerStorageActions`, `registerMaintenance`, and `registerSysextActions`,
are all actually capability-guarded — every row in this table now reflects
current, landed behavior, not a future guarantee. The final chunk's contract
test enforces the full table.

**Running total:** `internal/broker/api.go` declares exactly 35 `Action*`
constants and 16 `Query*` constants today — 51 IDs total, reproducible with:

```sh
grep -c '^[[:space:]]*Action' internal/broker/api.go   # 35
grep -c '^[[:space:]]*Query' internal/broker/api.go    # 16
```

(The POSIX `[[:space:]]` character class is used rather than a literal `\t`
escape, since a bare backslash-`t` is interpreted inconsistently across grep
implementations — GNU grep treats it as a tab as an extension even in BRE,
most other greps do not and silently match nothing.)

Every one of the 51 IDs is registered exactly once across the four
registries in `cmd/pilothoused/main.go`, including `ActionFilesUpload`
(registered via `StreamActionRegistry`) and `QueryFilesDownload` (registered
via `StreamQueryRegistry`) — both are members of the 35/16 above, not IDs
added on top. This table therefore has exactly 51 rows.

`QueryCapabilities` (`org.frostyard.pilothouse.capabilities.list`) landed in
c6 alongside the engine conversions and is included in both the count above
and the query table below — this document is updated in the same chunk
that registers the new ID, per the "every currently registered broker ID"
invariant stated above. c12's contract test cross-checks its own ID count
against this document, which is now 51 pre- and post- that chunk (the count
does not change again for the rest of this phase).

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

## Queries (16)

| Broker ID | Module | Capability |
|---|---|---|
| `QueryActivity` | activity | none |
| `QueryBackupsState` | backups | systemd |
| `QueryCapabilities` | capability | none *(unconditional — see below)* |
| `QueryDockerLogs` | docker | docker |
| `QueryDockerState` | docker | docker |
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
module-level:

- `ActionSysextRefresh` → `sysext`
- `ActionSysextUpdate` → `updex`
- `ActionSysextDisable` / `ActionSysextEnable` → `updex AND sysext`

There is no standalone sysext read query in the registry today (no
`QuerySysext*` constant exists); the sysext module's data reaches the web
page through `QueryMaintenanceState` (see the extension-read note below).

## Exceptions to the module-level defaults

Three rows in this table deviate from the spec's literal module-default
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
