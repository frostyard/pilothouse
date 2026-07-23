# Handler capability table

This is the binding reference for the broker capability model (phase 1a of
issue #35, `.mill/spec.md`). It maps every broker ID registered today —
across all four registries (`QueryRegistry`, `ActionRegistry`,
`StreamQueryRegistry`, `StreamActionRegistry`) in `cmd/pilothoused/main.go` —
to the capability (or capabilities) it will require once its registration is
capability-guarded. This chunk is documentation-only: no registration is
guarded yet. Later chunks in this phase convert registrations to match this
table module by module; the final chunk's contract test enforces it.

**Running total:** `internal/broker/api.go` declares exactly 35 `Action*`
constants and 15 `Query*` constants today — 50 IDs total, reproducible with:

```sh
grep -c '^[[:space:]]*Action' internal/broker/api.go   # 35
grep -c '^[[:space:]]*Query' internal/broker/api.go    # 15
```

(The POSIX `[[:space:]]` character class is used rather than a literal `\t`
escape, since a bare backslash-`t` is interpreted inconsistently across grep
implementations — GNU grep treats it as a tab as an extension even in BRE,
most other greps do not and silently match nothing.)

Every one of the 50 IDs is registered exactly once across the four
registries in `cmd/pilothoused/main.go`, including `ActionFilesUpload`
(registered via `StreamActionRegistry`) and `QueryFilesDownload` (registered
via `StreamQueryRegistry`) — both are members of the 35/15 above, not IDs
added on top. This table therefore has exactly 50 rows.

This total becomes **51** once c6 lands `QueryCapabilities` (the new
authenticated query that advertises the probed capability set). c12's
contract test cross-checks its own ID count against this document — 50
pre-phase, 51 once c6 has landed.

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

## Queries (15)

| Broker ID | Module | Capability |
|---|---|---|
| `QueryActivity` | activity | none |
| `QueryBackupsState` | backups | systemd |
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
otherwise depend on systemd. `registerStorage` (main.go:307) is fed by this
plain `storage.Manager`, while `registerStorageActions` (main.go:318, the
systemd-unit-creating remote-mount lifecycle) is fed by a separate
`storage.RemoteManager`. So `QueryStorageState` is unconditional/`none`
today, and — per the plan — c7 makes this a real *construction-level* fact
(the inventory manager is built without any systemd dependency), not merely
a registration-level guard bolted onto a manager whose construction could
still depend on systemd.

### 2. `QueryServicesJournal` is `systemd AND journald`, not `journald` alone

The spec's module-default prose says "services journal → journald."
`internal/modules/services/manager.go`'s `Journal()` (line 114) calls
`m.resolveUnit(ctx, name)` (line 118; `resolveUnit` defined at line 279)
before reading journal entries. `resolveUnit` uses the systemd D-Bus client
(`m.client`, populated via `dbus.NewSystemConnectionContext` in
`NewSystemManager`, lines 100–106) to validate/resolve the unit. The query
cannot function — and the backing `services.SystemManager` cannot even be
*constructed* (`NewSystemManager` fails outright if the D-Bus connection
fails) — without systemd, regardless of journald availability. This is
recorded as a refinement of the spec's stated module default, not a
deviation from it: the module-level default describes the feature's intent
("read the journal"), and the exception records the actual code dependency
that intent doesn't mention.

### 3. `QueryLogs` (the whole logs module) is `systemd AND journald`, not `journald` alone

Same shape as above. `internal/modules/logs/manager.go`'s `Logs()` (line
159) calls `m.client.ListUnitsContext(ctx)` (line 163) and
`m.client.ListUnitFilesContext(ctx)` (line 167) — both systemd D-Bus calls —
to build the returned unit allowlist before any journal entries are
filtered. `NewSystemManager` (line 147) itself calls
`dbus.NewSystemConnectionContext` and returns an error if that connection
fails, so the manager cannot be constructed at all without systemd. The
query's true requirement is `systemd AND journald`, documented here as the
exceptions section's job: recording a real code dependency that exceeds the
module default's literal wording.

## Extension-read note (`QueryMaintenanceState` / sysext)

`.mill/spec.md` says sysext reads are "updex OR sysext" — there is no
standalone sysext read query; that describes the extension-read subpath
inside `QueryMaintenanceState` (`main.go:546`), which the spec notes already
performs daemon-side extension reads today (maintenance's update source
invokes updex). The *registration* of `QueryMaintenanceState` is guarded on
`systemd` (the module-level default for maintenance, matching the row
above); separately, once guarded, its extension-read subpath must degrade
gracefully when updex/sysext capabilities are absent — extension-derived
fields are omitted, never errors. This degrade behavior is c10's scope; this
table only fixes `QueryMaintenanceState`'s registration-level capability at
`systemd`.

## `jobs` query

`QueryJobs` is not named in the spec's "system, files, activity → none"
list, but it is generic job-store infrastructure tied to no probed
capability, exactly like `QueryActivity` — treated the same way:
unconditional/`none`.
