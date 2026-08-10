# Handler capability table

This is the binding reference for the broker capability model (phase 1a of
issue #35, `.mill/spec.md`). It maps every broker ID registered today —
across all four registries (`QueryRegistry`, `ActionRegistry`,
`StreamQueryRegistry`, `StreamActionRegistry`) in `cmd/pilothoused/main.go` —
to the capability (or capabilities) it will require once its registration is
capability-guarded. `registerPodman`/`registerDocker`/`registerIncus`/
`registerK3s` (and
the new `QueryCapabilities` itself), plus `registerServices`,
`registerLogs`, `registerBackups`, `registerStorageActions`,
`registerMaintenance`, `registerHostImage`, and `registerSysextActions`, are
all actually capability-guarded — every row in this table reflects current,
landed behavior, not a future guarantee, and
`cmd/pilothoused/capability_contract_test.go` enforces the full table across
a fixture matrix of capability sets.

**Running total:** `internal/broker/api.go` declares exactly 40 `Action*`
constants and 24 `Query*` constants today — 64 IDs total, reproducible with:

```sh
grep -c '^[[:space:]]*Action' internal/broker/api.go   # 40
grep -c '^[[:space:]]*Query' internal/broker/api.go    # 24
```

(The POSIX `[[:space:]]` character class is used rather than a literal `\t`
escape, since a bare backslash-`t` is interpreted inconsistently across grep
implementations — GNU grep treats it as a tab as an extension even in BRE,
most other greps do not and silently match nothing.)

Every one of the 64 IDs is registered exactly once across the four
registries in `cmd/pilothoused/main.go`, including `ActionFilesUpload`
(registered via `StreamActionRegistry`) and `QueryFilesDownload` (registered
via `StreamQueryRegistry`) — both are members of the 40/24 above, not IDs
added on top. This table therefore has exactly 64 rows.

Both grep commands above were re-run against this tree when the totals were
last changed, and they are no longer only documentation:
`cmd/pilothoused/capability_contract_test.go`'s
`TestCapabilityTableMirrorsBrokerAPIConstants` parses `internal/broker/api.go`
with `go/ast` and diffs the declared `Action*`/`Query*` constants against
`capabilityTable` **in both directions**, so a constant added without a table
row, a table row naming an ID that no longer exists, or a drift away from
40/24/64 all fail the build. It additionally checks that an `Action*`
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
was added by phase 2 (#51) for read-only host-image reporting, raising the
totals from the 16/51 phase 1a ended with to 17/52. It is the table's first
**any-of** row: `registerHostImage` guards it with
`caps.HasAny(capability.Bootc, capability.RPMOStree)`, not `HasAll`, so either
source alone is enough (see exception #4 below).

`QueryAutoUpdateStatus` (`org.frostyard.pilothouse.maintenance.autoupdate_status`)
was added by #58 for read-only automatic-update reporting, raising the totals to
18/53. Like `QueryHostImageStatus` it is an
**any-of** row: `registerAutoUpdate` guards it with the same
`caps.HasAny(capability.Bootc, capability.RPMOStree)` gate, and for the same
reason — a no-updater image host must still be able to report the "not
configured" empty state (see exception #5 below).

`QueryExtensionsState` (`org.frostyard.pilothouse.sysext.state`) was added by
#52 for the read-only extension inventory, raising the totals to 19/54. It
belongs to the `sysext` (Extensions) module, not
maintenance, and is the table's third **any-of** row: `registerExtensions`
guards it with `caps.HasAny(capability.Updex, capability.Sysext)` — either tool
alone yields a usable inventory (see exception #6 below).

`QueryIncusInstance` (`org.frostyard.pilothouse.incus.instance`) and
`QueryIncusLogs` (`org.frostyard.pilothouse.incus.logs`) are the newest IDs,
added by the Incus instance-depth phase, raising the totals to 35/21/56. Both belong to the `incus` module and both are ordinary **all-of** rows
guarded by `caps.Has(capability.Incus)` in `registerIncus`, alongside the
already-present `QueryIncusState`. Neither has a mutating counterpart: that
phase added no `Action*` constant.

`QueryIncusInstance` returns one instance's configuration, devices,
interfaces and snapshots. Its configuration and device properties are built
by **allowlist** in `internal/modules/incus/detail.go` — `configKeys`,
`configPrefixes`, and the per-device-type `deviceProperties` map — never by
copying the instance's expanded configuration. An Incus instance's
configuration routinely carries secrets (`user.user-data` holds cloud-init
payloads with SSH keys and passwords, `environment.*` holds process
environment, `raw.*` holds passthrough runtime configuration), and none of
those namespaces is allowlisted, so none of them crosses the broker boundary.
A key or device type added by a future Incus release is excluded until it is
reviewed and added to the allowlist.

`QueryIncusLogs` takes a fixed `source` selector accepting exactly `console`
or `log` and **never a filename**. The daemon rejects any other value before
reading anything, and for `log` it derives the supervisor logfile from the
resolved instance's own type (`lxc.log` for a container, `qemu.log` for a
virtual machine), matching what `incus info --show-log` resolves `default`
to. Both are bounded to a 200-line, 256 KiB tail.

`ActionIncusSnapshotCreate`, `ActionIncusSnapshotDelete`,
`ActionIncusSnapshotRestore` and `ActionIncusStopForce` are the newest IDs,
added by the Incus snapshot phase, raising the totals to 39/21/60. All four belong to the `incus` module, are administrator-only, and
are ordinary all-of rows guarded by `caps.Has(capability.Incus)`.

The three snapshot actions are the first in the daemon to carry **three**
identifiers (`project`, `instance`, `snapshot`) rather than two, so they are
registered through `registerSnapshotActions` rather than
`registerProjectActions`. Their audit resource is the fully qualified
`incus/snapshot/<project>/<instance>/<snapshot>`, so two snapshots sharing a
name on different instances are distinct resources for confirmation and audit.
Delete and restore require confirmation; create only adds and does not.
Snapshots Pilothouse creates are always non-stateful — a stateful snapshot
needs CRIU on the host and there is no way to ask for one — and the daemon
refuses to restore a running instance, matching what Incus itself enforces for
a non-stateful snapshot.

`ActionIncusStopForce` is deliberately a **separate ID** from
`ActionIncusStop` rather than a `force` parameter on it. Killing an instance
outright is a materially more dangerous act than asking it to shut down, and a
distinct ID makes it read distinctly in the audit trail. It exists because the
graceful path gives an instance 30 seconds and then fails, which left a wedged
instance with no way to stop it from the console at all.

`QueryIncusNetwork` (`org.frostyard.pilothouse.incus.network`) and
`QueryIncusProfile` (`org.frostyard.pilothouse.incus.profile`) are the newest
IDs, added by the Incus networks-and-profiles phase, raising the totals to 39/23/62. Both belong to the `incus` module, both are ordinary
all-of rows guarded by `caps.Has(capability.Incus)`, and both are read-only:
that phase declared no `Action*` constant.
Neither rendered page contains a form, a button, or any other control.

`QueryIncusNetwork` is the third surface built on an **allowlist**, and it
needs one for the same concrete reason the others do rather than by analogy:
Incus network configuration carries `bgp.peers.<name>.password`, a BGP
session password, and the `ovn.*` and `tunnel.*` families carry credentials
and keys. `networkConfigKeys` in `internal/modules/incus/network.go` admits
only addressing, NAT, DHCP and DNS shape; every secret-bearing namespace is
excluded by omission. The list model's IPv4/IPv6 columns read through the
same predicate, so the cheaper summary cannot become a bypass around the
detail model's filter.

`QueryIncusProfile` deliberately **reuses the instance allowlists**
(`configKeys`, `configPrefixes`, `deviceProperties`) rather than defining its
own: a profile carries exactly the same configuration and device shape as an
instance, including the same `user.*`, `environment.*` and `raw.*`
namespaces. A profile is arguably the more sensitive of the two, since its
cloud-init payload applies to every instance that inherits it.

Both list sections report what the host actually has rather than only what
Incus manages: an Incus host's network list is mostly interfaces it merely
observes (physical NICs, loopback, a foreign bridge such as `docker0`), and
those are shown with an "Observed" badge. Leases are the one thing that
genuinely depends on management — Incus tracks them only for its own
networks — so `NetworkDetail` carries a separate `LeasesAvailable` flag that
distinguishes "no leases" from "leases cannot be read", and the unmanaged
case is never asked for leases at all.

`ActionIncusCreate` (`org.frostyard.pilothouse.incus.create`) was added by
the Incus instance-creation phase and brought the totals to 40/23/63. It
belongs to the `incus` module, is administrator-only,
and is an ordinary all-of row guarded by `caps.Has(capability.Incus)`.

It is the module's **only background action** and the daemon's first
outbound network operation. Creating an instance downloads an image, which
routinely takes minutes, so it is registered with `Background: true` and a
30-minute timeout: the broker enqueues a durable job, holds the new
instance's own `incus/instance/<project>/<name>` lock for the duration, and
returns immediately. The web notice therefore says creation *started*, not
that it finished; the outcome lands in Activity.

`QueryK3sState` (`org.frostyard.pilothouse.k3s.state`) is the newest ID,
raising the current totals to 40/24/64. It is a non-admin, read-only query
guarded by `caps.Has(capability.K3s)`. The capability is advertised only when
`--k3s <path>` is explicitly configured and that executable can read
`/version` through the fixed `/etc/rancher/k3s/k3s.yaml` kubeconfig. The
query runs only two fixed inventory commands: node JSON and all-namespace pod
JSON. Its response contains node readiness/version/runtime plus pod-health
totals grouped by namespace; it carries no individual pod identity, logs,
configuration, secret, or mutation.

**The image server is a compile-time constant, never a parameter.**
`imageRemote` in `internal/modules/incus/create.go` is
`https://images.linuxcontainers.org` — the server the `images:` remote
points at — and the action's parameter set (`project`, `name`, `image`,
`type`, `profile`) contains no remote, server or URL field at all. This is
the property that keeps the action a fixed operation rather than a generic
fetcher, and it is asserted from both sides: a manager-side test pins the
constant, and a web-side test submits `remote`/`server`/`url` form fields
and asserts none of them reaches the action's parameters.

Everything the operator does supply is validated broker-side before
anything reaches the network: the instance name against the same rule every
other instance action uses, the type against the closed pair
`container`/`virtual-machine`, the profile against the project's live
profile list, and the image alias against a shape check that rejects empty,
`.` and `..` path segments. The alias is then resolved explicitly on the
fixed remote — rather than handed to the daemon unresolved, which is what
the Incus CLI does for a simplestreams remote — so an alias that does not
exist fails immediately with a clear message instead of surfacing later as a
failed background job. Instances Pilothouse creates are never stateful and
carry no caller-supplied device or configuration overrides.

**#64 added no broker ID.** Both commands above were re-run against this
tree at the close of #64 (optional engines and `updex` become explicitly
opt-in, mock Fleet moves behind `--dev`) and still printed 35 and 19 — 54 IDs
total, unchanged at that point. That phase declares no new `Action*`/`Query*` constant and
removes none: it changes only *what causes* four already-declared
capabilities (`updex`, `podman`, `docker`, `incus`) to be advertised, which
is upstream of every row below. No row in this table, no row in
`cmd/pilothoused/capability_contract_test.go`'s `capabilityTable`, and no
count in either contract test moved. This is recorded rather than left
implicit precisely because "nothing changed" and "nobody checked" produce
identical diffs.

Canonical capability IDs: `systemd`, `journald`,
`updex`, `sysext`, `bootc`, `rpm-ostree`, `autoupdate-rpm-ostree`,
`autoupdate-bootc`, `podman`, `docker`, `incus`, `k3s`.

## Actions (40)

| Broker ID | Module | Capability |
|---|---|---|
| `ActionFilesUpload` | files | none |
| `ActionDockerRemove` | docker | docker |
| `ActionDockerRemoveImage` | docker | docker |
| `ActionDockerRestart` | docker | docker |
| `ActionDockerStart` | docker | docker |
| `ActionDockerStop` | docker | docker |
| `ActionIncusCreate` | incus | incus |
| `ActionIncusRemove` | incus | incus |
| `ActionIncusRemoveImage` | incus | incus |
| `ActionIncusRestart` | incus | incus |
| `ActionIncusSnapshotCreate` | incus | incus |
| `ActionIncusSnapshotDelete` | incus | incus |
| `ActionIncusSnapshotRestore` | incus | incus |
| `ActionIncusStart` | incus | incus |
| `ActionIncusStop` | incus | incus |
| `ActionIncusStopForce` | incus | incus |
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

## Queries (24)

| Broker ID | Module | Capability |
|---|---|---|
| `QueryActivity` | activity | none |
| `QueryAutoUpdateStatus` | maintenance (auto-update) | bootc OR rpm-ostree *(exception — see below)* |
| `QueryBackupsState` | backups | systemd |
| `QueryCapabilities` | capability | none *(unconditional — see below)* |
| `QueryDockerLogs` | docker | docker |
| `QueryDockerState` | docker | docker |
| `QueryExtensionsState` | sysext (extensions) | updex OR sysext *(exception — see below)* |
| `QueryHostImageStatus` | maintenance (host image) | bootc OR rpm-ostree *(exception — see below)* |
| `QueryIncusInstance` | incus | incus |
| `QueryIncusLogs` | incus | incus |
| `QueryIncusNetwork` | incus | incus |
| `QueryIncusProfile` | incus | incus |
| `QueryIncusState` | incus | incus |
| `QueryJobs` | jobs | none |
| `QueryK3sState` | k3s | k3s |
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
capability; k3s → k3s; system, files, activity, jobs → none. sysext is per-action, not
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
`docs/modules.md`). The web-side rendering of host-image status has landed
and is described here: `GET /maintenance` is wrapped in `platform.GateAny`
on that same `HasAny(Systemd, Bootc, RPMOStree)` set, its handler calls
`QueryHostImageStatus` only when the advertised set satisfies
`HasAny(Bootc, RPMOStree)` (`queryHostImage`), and the page's "Host image"
section — booted/staged/rollback deployments, the per-source
`data-source-error` indicators, and the soft-reboot-eligibility indicator —
is omitted entirely, rather than rendered empty or errored, when it does
not. See "Phase 2 (#51) — host-image contract parity" at the end of this
document for the fixtures that pin both sides. The sysext per-action rows
are:

- `ActionSysextRefresh` → `sysext`
- `ActionSysextUpdate` → `updex`
- `ActionSysextDisable` / `ActionSysextEnable` → `updex AND sysext`

The sysext module also has a standalone read query as of #52:
`QueryExtensionsState` (`org.frostyard.pilothouse.sysext.state`), guarded
`updex OR sysext` by `registerExtensions` — see exception #6 below. The
extension data `QueryMaintenanceState` still derives its reboot reasons from
is described in the extension-read note further down.

## Exceptions to the module-level defaults

Six rows in this table deviate from the spec's literal module-default
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

### 4. `QueryHostImageStatus` is `bootc OR rpm-ostree`, the first of the table's three any-of rows

Every ordinary row is an AND: the ID registers iff
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

### 5. `QueryAutoUpdateStatus` is `bootc OR rpm-ostree`, the table's second any-of row

`QueryAutoUpdateStatus` shares `QueryHostImageStatus`'s any-of gate exactly:
`registerAutoUpdate` (`cmd/pilothoused/main.go`) registers it iff
`caps.HasAny(capability.Bootc, capability.RPMOStree)`, and is a separate
function from both `registerMaintenance` and `registerHostImage`, consulting
neither `capability.Systemd` nor either other guard. Automatic-update reporting
only applies to an image-based host, and "no updater configured" is itself a
meaningful, reportable state — so gating on the `Autoupdate*` capabilities
instead would 404 the query on precisely the no-updater host, making the
required "not configured" empty state unreachable. The `AutoupdateBootc` /
`AutoupdateRPMOStree` capabilities (from #50) instead drive the per-updater
configured vs. not-configured split *inside* the `maintenance.AutoUpdateManager`
body: a configured updater carries a non-nil payload pointer, and a no-updater
image host returns both `*_configured=false` with nil payloads. The query
reuses the one systemd connection `cmd/pilothoused` already probes and opens
for backups/services/logs — no second D-Bus dial — and is read-only in the same
strong sense as `QueryHostImageStatus`: served by an `AutoUpdateSource`
interface with no mutating method, with no matching action in the broker's ID
vocabulary.

This query, too, now has a web consumer: `internal/modules/maintenance`'s
`queryAutoUpdate` (`module.go`) calls it from `collectPage` whenever the
advertised set satisfies `HasAny(Bootc, RPMOStree)` and returns `nil` (omitting
the page's whole "Automatic updates" section) when it does not, so the web side
never attempts the query on a host where it is unregistered. What it renders is
the Maintenance page's read-only "Automatic updates" section, which exposes no
control of any kind — there is no automatic-update action in the ID vocabulary
for one to target. Both sides are covered by
`cmd/pilothouse/capability_contract_test.go`, whose `capabilityAnyRequirements`
table carries this ID's any-of requirement, hand-transcribed from this document;
its fake broker fails the test outright if the web process ever invokes a broker
ID whose capability the fixture's host does not advertise. That harness also
calibrates each fixture's canned `AutoUpdateStatus` to the response the real
`AutoUpdateManager` would produce for that capability set, so a fixture
advertising `bootc`/`rpm-ostree` *without* the matching `Autoupdate*` capability
is served the zero-value, both-updaters-not-configured response rather than an
impossible populated one — the distinction the paragraph above turns on, pinned
in test code.

### 6. `QueryExtensionsState` is `updex OR sysext`, the table's third any-of row

`QueryExtensionsState` (`org.frostyard.pilothouse.sysext.state`, module
`sysext`) is the standalone extension-inventory read #52 adds.
`registerExtensions` (`cmd/pilothoused/main.go`) registers it iff
`caps.HasAny(capability.Updex, capability.Sysext)` — an any-of for the same
reason as exceptions #4 and #5: either tool alone yields a usable inventory
(updex knows which feature definitions are managed and what updates are
pending; `systemd-sysext` knows what is installed and merged), while a host
with neither has nothing to report and registers no query at all. The four
`ActionSysext*` rows keep their own, narrower per-action guards; this query
adds no mutation of any kind, being served by a `sysext.ExtensionsSource`
whose only method is a read.

**Request — no arguments, and no client say in which tools run.** The handler
that `registerExtensions` registers ignores its `map[string]string` payload
parameter outright, and the web side calls it with a nil payload:
`host.Query(ctx, broker.QueryExtensionsState, nil, &state)`. Which sources the
daemon attempts is deliberately *not* client input — `registerExtensions`
closes over the probed `capability.Set` and threads
`caps.Has(capability.Updex)` / `caps.Has(capability.Sysext)` into
`ExtensionsSource.State(ctx, updexAvailable, sysextAvailable)`, so the
unprivileged process cannot ask the daemon to invoke a tool the startup probe
did not find, and the flags the handler passes are the same facts its own
registration guard is built from. Like `QueryHostImageStatus` the query is
registered with `adminOnly` false — it reports facts about the host's
extensions rather than privileged content — and it is independent of
`registerMaintenance`'s `Systemd` guard, since reading the inventory needs no
reboot machinery.

The response follows `QueryHostImageStatus`'s **flat per-source
availability/error** convention rather than `QueryAutoUpdateStatus`'s
`*_configured` one: `sysext.ExtensionsState` carries
`UpdexAvailable`/`UpdexError` and `SysextAvailable`/`SysextError` at the top
level, so a source whose command fails sets its own pair and leaves the other
source's data intact, and the query itself returns no error for a source-level
failure. A source whose capability is absent is never attempted at all —
`Available` stays false and `Error` stays empty, keeping "never attempted"
distinguishable from "attempted and failed". A host whose tools are present but
which has no definitions, nothing installed, and nothing merged reports both
sources available and an empty `Extensions` slice: the empty *success* state,
which is a different fact from the no-tools host that registers no query.

Concretely, on a host where both tools answer, the payload below is the wire
form of the contract harness's own `cannedExtensionsState()` — reduced here to
two of its five rows, one managed extension that is merged and one component
behind, and one unmanaged extension `systemd-sysext` saw but updex has no
definition for. The three elided rows are a managed definition that is *not*
installed, an enabled-and-installed one waiting for the next merge, and a
merged-but-disabled one; the first of those reappears below:

```json
{
  "extensions": [
    {
      "description": "Merged, enabled, and one component behind",
      "enabled": true,
      "installed": true,
      "managed": true,
      "merged": true,
      "name": "contract-managed-merged",
      "path": "/var/lib/extensions/contract-managed-merged",
      "updates": [
        {
          "Extension": "contract-managed-merged",
          "Component": "contract-runtime",
          "Current": "1.0.0",
          "Newest": "1.1.0"
        }
      ],
      "version": "1.0.0"
    },
    {
      "enabled": false,
      "installed": true,
      "managed": false,
      "merged": false,
      "name": "contract-unmanaged-installed",
      "path": "/var/lib/extensions/contract-unmanaged-installed",
      "version": "2.0.0"
    }
  ],
  "sysext_available": true,
  "updex_available": true
}
```

`AvailableUpdate` carries no struct tags, so its four fields marshal under
their Go names — the one place in this payload where the wire spelling is not
snake_case. The four booleans have no `omitempty`, so a false one is always
present on the wire and "absent" never has to be inferred; `description`,
`path`, `updates`, and `version` do, so a source that did not contribute simply
leaves its own fields out.

The same host with `updex list` failing keeps the systemd-sysext half of the
union intact and reports the failure in place rather than as a query error.
Because `Managed`, `Enabled`, `Description`, and `Updates` are updex-only,
every surviving row drops to read-only and no pending update is reported
anywhere — the same two rows as above, now stripped to what `systemd-sysext`
alone contributed:

```json
{
  "extensions": [
    {
      "enabled": false,
      "installed": true,
      "managed": false,
      "merged": true,
      "name": "contract-managed-merged",
      "path": "/var/lib/extensions/contract-managed-merged",
      "version": "1.0.0"
    },
    {
      "enabled": false,
      "installed": true,
      "managed": false,
      "merged": false,
      "name": "contract-unmanaged-installed",
      "path": "/var/lib/extensions/contract-unmanaged-installed",
      "version": "2.0.0"
    }
  ],
  "sysext_available": true,
  "updex_available": false,
  "updex_error": "run updex list: exit status 1"
}
```

A name neither source contributed disappears from the union entirely rather
than appearing with blank fields: the elided managed-but-not-installed
definition is present in the first payload and gone from this one — updex
was the only source that knew the name, and neither `Installed` nor `Merged`
is true — which is a different fact from a row rendered with empty fields.
The symmetric `systemd-sysext` failure is the mirror image: `SysextAvailable`
false with `SysextError` set, every `Installed`/`Merged`/`Path`/`Version`
zeroed, and the unmanaged-installed row gone because updex never defined it.

`Extensions []Extension` is the union inventory, one entry per extension name,
each carrying `Managed` (updex enumerated a definition, so the extension keeps
enable/disable/update/refresh), `Installed`, and `Merged`, plus the fields the
surface renders — and an `Updates []AvailableUpdate` field holding the pending
component updates updex's feature check reported for that extension. `Updates`
is empty for an unmanaged extension in every case, since the check only ever
reports on definitions updex itself enumerated.

The web-side caller is `internal/modules/sysext`, which as of #52 reads
through this query and nothing else: `Module.Dashboard` and the `GET /sysext`
handler each call `host.Query(ctx, broker.QueryExtensionsState, nil, &state)`,
and both sit behind the module's `CapabilityGateAny(Updex, Sysext)` — so
`queryState` needs no capability check of its own and the web process never
invokes the query on a host where it is unregistered, exactly as
`maintenance`'s `queryHostImage` refrains from `QueryHostImageStatus` on a
non-image host. Beyond the inventory table, what that response feeds is the
**update-availability surface that moved off Maintenance in this same phase**,
every element of it derived from `Updates` alone and none of it from a second
query:

| Where | Element | Source |
|---|---|---|
| Dashboard `Summary` card | "Updates" mini-row with the aggregate pending count | `updateCount(state.Extensions)` |
| `GET /sysext` | "Available updates" table, one row per pending update, flattening every extension's `Updates` into Extension / Component / Current / Newest columns | `pendingUpdates(state.Extensions)` |
| `GET /sysext` | that table's "N pending" toolbar count | `updateCount(state.Extensions)` |
| `GET /sysext` inventory table | per-row "Update available" badge | `len(extension.Updates) > 0` |

`maintenance.State` carries no `Updates` field at all any more, and the
Maintenance page renders no updates table — the ownership statement in
`.mill/spec.md` ("extension inventory, update availability, and extension jobs
remain owned by Extensions and Activity") is enforced structurally, by the
field not existing, rather than by convention.

Both sides are covered by contract tests. On the daemon side,
`cmd/pilothoused/capability_contract_test.go` carries this ID as a `requireAny`
row (`{Updex, Sysext}`) and walks it across the whole fixture matrix —
`updex-without-sysext` and `sysext-without-updex` prove either source alone
registers it, `neither-host-image-source-plus-systemd` and the spec's
`snosi-without-bootc` prove both together do, and `minimal` proves a host with
neither withholds it. On the web side,
`cmd/pilothouse/capability_contract_test.go`'s `capabilityAnyRequirements`
table carries the same any-of requirement, hand-transcribed from this document;
its fake broker fails the test outright if the web process ever invokes a
broker ID whose capability the fixture's host does not advertise, and
`assertExtensionsSurfaces` audits every view element region by region — nav
entry, dashboard card and its Summary mini-row, `GET /sysext`'s intro actions,
the inventory rows' per-extension controls and badges, and the "Available
updates" table. Each fixture's canned response comes from
`calibratedExtensionsState(caps)`, which projects one fully-populated
`cannedExtensionsState()` down to what that capability set could actually
produce (updex contributes `Description`/`Enabled`/`Managed`/`Updates`,
systemd-sysext contributes `Installed`/`Merged`/`Path`/`Version`, and a name
neither contributed drops out of the union), so no fixture is served inventory
its host could not report. The two per-source read failures are not expressible
as capability sets — the tool is advertised and simply did not answer — so they
get explicit `cannedExtensionsStateUpdexFailed` / `cannedExtensionsStateSysextFailed`
fixtures, each keeping the other source's data intact, matching the two payloads
above. `TestCapabilityContractBootcSnosiFixture` pins the spec's coexistence
criterion: a bootc Snosi host renders read-only bootc lifecycle (and, having no
systemd, no reboot form) alongside the full Extensions surface, plus both
independence directions — Extensions with no host-image source, and Maintenance
with no extension tooling.

The `sysext.ExtensionsSource` behind the query does have a second
*daemon-internal* consumer: `maintenance.SystemManager` calls the same
interface (on the same instance) to derive its merged-but-disabled reboot
reason — see the extension-read note below.

## Extension-read note (`QueryMaintenanceState` / sysext)

`.mill/spec.md` says sysext reads are "updex OR sysext". As of #52 that
requirement has its own standalone read query, `QueryExtensionsState`
(exception #6 above); the paragraphs below describe how the *other*
extension-touching query, `QueryMaintenanceState`, consumes that same
aggregate. The *registration* of `QueryMaintenanceState` (and
`ActionMaintenanceReboot`) is guarded on `systemd` (the module-level default
for maintenance, matching the rows above) by `registerMaintenance`, which
takes the probed `capability.Set` and no-ops entirely when `systemd` is
absent, exactly like `registerBackups`/`registerStorageActions`.
`maintenance.NewSystemManager` has no D-Bus dependency of its own (it
depends only on the sysext extensions source, job store, and command
runner), so unlike backups/services/logs there is no construction-level
non-fatal-startup fix needed here — the manager is always constructed, and
this registration guard is the only thing withholding it.

**Mechanism.** `maintenance.SystemManager`'s `extensionState` method makes
exactly one call: `sysext.ExtensionsSource.State(ctx, updexAvailable,
sysextAvailable)`, with the two probed capability facts threaded in from
`NewSystemManager`'s `updexAvailable`/`sysextAvailable` parameters. That is
the same interface — and, in `cmd/pilothoused`, the same concrete
`*extctl.SystemManager` instance — `registerExtensions` serves
`QueryExtensionsState` from, so this is daemon-internal instance reuse
(exactly like `HostImageManager` in the host-image note below), not a second
broker round trip. It is instance reuse, not result reuse:
`extctl.SystemManager.State` has no cache of its own, so a
`QueryExtensionsState` and a `QueryMaintenanceState` in the same moment each
run their own read. The only cache in play is maintenance's own pre-existing
1-minute `extensionState` cache, which belongs to the maintenance manager
alone.

**Degrade guarantee — unconditional, and about failure as well as absence.**
`ExtensionsSource.State` owns the never-attempt rule for both sources (a tool
whose capability flag is false is never invoked) *and* the never-hard-error
rule: a source that is invoked and whose command fails sets only its own
`UpdexAvailable`/`UpdexError` or `SysextAvailable`/`SysextError` pair in the
returned `ExtensionsState` and leaves the other source's data intact,
returning no error of its own. `extensionState` therefore inherits the
guarantee rather than restating it, and handles the (contractually unused)
error result the same way `hostImageState` handles a failed host-image read:
drop this call's extension contribution, cache nothing, and carry on.

The consequence is the one spec resolution 3 requires: in **no** combination
— tool absent, tool present but its command failing, both failing, or the
source erroring outright — does `SystemManager.State` return an error because
of extensions, so `QueryMaintenanceState` stays a 200. What is lost on a
failed read is only the extension-derived reboot reasons; the OS-marker,
completed-job, and staged-host-image reasons, plus `Jobs` and `OSVersion`,
are computed exactly as before.
`internal/modules/maintenance/manager_test.go` has a dedicated test case per
combination, including one that drives the real `*extctl.SystemManager` with
a failing command runner.

**What maintenance derives, and what it no longer owns.** From the returned
`Extensions` slice, maintenance takes exactly one fact: a merged extension
that is known to be disabled becomes the reboot reason "<name> is disabled
but remains active until reboot." Nothing else.

"Known to be disabled" is narrower than `Merged && !Enabled`, and
deliberately so, because the aggregate's fields are populated by two
independently-failing sources. `Enabled` comes only from updex and `Merged`
only from systemd-sysext, so a source that was never attempted or that failed
leaves its own fields at Go's zero value — and `Enabled: false` because updex
answered is indistinguishable on the wire from `Enabled: false` because updex
never ran. `mergedButDisabledReasons` therefore reads `Enabled` only when
`UpdexAvailable` is true with an empty `UpdexError` (and `Merged` only under
the matching `SysextAvailable`/`SysextError` pair), and skips the extension
entirely otherwise. It also requires `Managed`: the aggregate is a *union*, so
an extension installed or merged straight through systemd-sysext with no updex
definition appears with `Managed: false` and an `Enabled` nobody populated.
That last guard is what keeps this reason byte-identical to the pre-#52
`List()`-based behavior, which iterated updex's feature list alone and so
never saw unmanaged extensions at all. The net effect is that an unknown
enabled-state contributes nothing, exactly like every other unreadable source
here — an absent or failed updex costs the extension-derived reasons rather
than inventing them.

Beyond that one reason, `maintenance.State` no longer carries an `Updates`
field, the Maintenance page no longer renders an "Available updates" table or
an update count, the dashboard Summary card no longer carries an "Updates"
mini-row, and `Module.Health` no longer emits the
`maintenance.updates` finding. Ownership of extension inventory and
per-extension/aggregate update availability has moved to Extensions, whose
`QueryExtensionsState` response already carries it per extension in
`Extension.Updates`. As of #52 the Extensions surface renders it in three
places: the dashboard Summary card's "Updates" mini-row (the aggregate sum of
`len(Extension.Updates)`, in the position maintenance's removed mini-row
used), an "Available updates" table on `GET /sysext` with the same
Extension/Component/Current/Newest columns and the same "Enabled extensions
are up to date." empty state the Maintenance page's removed table had, and an
"Update available" badge on each extension row whose own `Updates` is
non-empty. None of this needs its own capability flag: `Check()` is
updex-only, so `Extension.Updates` is empty whenever `UpdexAvailable` is
false and these surfaces render their empty/absent form on an updex-less host
without additional gating.

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

The **sysext web surface, which #54 did not cover, is gated as of #52.**
`sysext.Module` no longer constructs an extension manager of its own:
`cmd/pilothouse`'s `newRegistry` calls `sysext.New()` with no arguments, the
web binary's `--definitions-root`/`--updex` flags are gone, and every read
goes through `QueryExtensionsState`. The exec-backed implementation moved out
of `internal/modules/sysext` into the new `internal/modules/sysext/extctl`
subpackage in the same change, so the separation is structural rather than
conventional: `sysext` (which the web binary links) holds only the `Manager`
and `ExtensionsSource` interfaces and the types they exchange, and imports
neither `os/exec` nor any `CommandRunner`; `extctl` (which only
`cmd/pilothoused` links) holds `NewSystemManager`, `ExecRunner`, and every
`updex`/`systemd-sysext` invocation. The dependency runs one way only.
The gate is applied per logical group,
mirroring `cmd/pilothoused`'s `registerSysextActions` split exactly:

| Surface | Predicate | Where it is enforced |
|---|---|---|
| Nav entry, dashboard card | `HasAny(Updex, Sysext)` | `Module.RequiredAnyCapabilities` → `platform.AvailableAny` via `moduleAvailable` |
| `GET /sysext` | `HasAny(Updex, Sysext)` | `platform.GateAny(host, m.RequiredAnyCapabilities(), ...)` in `Mount` |
| `POST /sysext/{name}/{action}` (enable, disable) | `HasAll(Updex, Sysext)` | `platform.Gate(host, []capability.ID{capability.Updex, capability.Sysext}, ...)` in `Mount` |
| `POST /sysext/actions/refresh` | `Has(Sysext)` | `platform.GateAny` at the route plus an in-handler check that 404s without `sysext` |
| `POST /sysext/actions/update` | `Has(Updex)` | `platform.GateAny` at the route plus an in-handler check that 404s without `updex` |

The two global actions share one route pattern but not one requirement, so
the route-level gate is the module's any-of condition and the per-action
requirement is re-checked inside the handler; an action whose tool is absent
404s indistinguishably from an unknown action. The rendered controls follow
the same three predicates — the refresh button, the update button, and every
per-row enable/disable form each collapse with the route they target, and an
extension with `Managed: false` (installed through `systemd-sysext` with no
updex definition) renders no enable/disable control under any capability set.

`webSideUngatedBrokerIDs` **no longer exists.**
`cmd/pilothouse/capability_contract_test.go` used to carry it as a closed
four-entry exemption from its fake broker's "never invoke a gated-off broker
ID" check, covering the four `ActionSysext*` IDs; #52 deleted the map, its
`Len == 4` assertion, and the relaxation branch in `requireAvailable`, so
those four IDs are now subject to the ordinary capability check like every
other broker ID. `TestSysextBrokerIDsAreSubjectToTheOrdinaryCapabilityCheck`
replaces the old exemption test and pins each one's requirement instead. The
privilege boundary was never affected by the exemption: the *daemon* does not
register those actions at all without `updex`/`sysext`, which
`cmd/pilothoused/capability_contract_test.go`'s matrix proves for every
fixture, so such a call failed at the broker rather than executing. What
changed is that the UI no longer offers the control in the first place.

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
That fixture is served its own canned response rather than the shared one,
calibrated to what a host advertising bootc alone can actually produce:
`rpm_ostree_available: false` with an *empty* `rpm_ostree_error` (never
attempted, as distinct from attempted and failed) and no rpm-ostree
supplementary version/checksum. That is what lets the same assertion helper
prove rpm-ostree detail *absent* there instead of being handed data the
daemon could not have emitted for that capability set.

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
failure runs and the calibrated `bootc-only` run share one assertion helper),
`TestCannedHostImageFixtureIsPopulated`
pins that default response's shape directly: if it quietly lost its staged
slot, its rollback slot, its rpm-ostree detail, or its soft-reboot flag, every
fixture would simply agree the corresponding markup is expectedly absent and
the matrix would keep passing while proving nothing. The same test pins each
derived response too — the two failure ones and the calibrated `bootc-only`
one — so none of them can drift into a duplicate of another.

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
are disjoint and together cover all 64 IDs, and asserts the two helpers do not
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

## Phase 4a (#64) — four capabilities become explicitly opt-in

This phase changed no broker ID, no registry, and no row of this table (see
the "#64 added no broker ID" note near the top for the re-run counts). It
changed what makes four of the then-eleven canonical capability IDs present in
the probed `capability.Set` in the first place, which is the input every row
here is read against:

| Capability | Advertised only when | Zero value |
|---|---|---|
| `updex` | `--updex <path>` is set *and* the executable answers | empty — no command is run, and there is no `PATH` fallback |
| `podman` | `--podman-socket <path>` is set *and* the socket answers | empty — no client is constructed, nothing is dialled |
| `docker` | `--docker <endpoint>` is set *and* the endpoint answers | empty — no client is constructed; `dockerclient.FromEnv`/`DOCKER_HOST` is never consulted |
| `incus` | `--incus` is passed *and* `/var/lib/incus/unix.socket` answers | `false` — the fixed socket path is not contacted at all |

Reachability alone is no longer sufficient for any of the four: an
unconfigured probe returns an empty `Set` before performing any I/O.
`systemd`, `journald`, `sysext`, `bootc`, `rpm-ostree`, and the two
`autoupdate-*` pairs are unchanged — they remain presence-probed and carry
no flag.

Issue #88 later added `k3s` as a twelfth capability and a fifth optional
dependency. `--k3s <path>` defaults to empty, and `ProbeK3s` returns before
running anything in that state. When configured, the probe executes the
selected binary directly (never through a shell) with the fixed arguments
`kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml get --raw=/version`.

The consequence for this table is that on a default-flagged daemon every
`podman`, `docker`, and `incus` row is unregistered, as is every row whose
requirement *requires* `updex` (`ActionSysextUpdate`, and
`ActionSysextEnable`/`ActionSysextDisable` through their `updex AND sysext`
guard). The guard clauses are unchanged; their input is simply empty. The
one `updex`-mentioning row that survives is the any-of
`QueryExtensionsState` (`updex OR sysext`), which `registerExtensions` still
registers on a host where `systemd-sysext` alone is present — the inventory
then carries only what `systemd-sysext` contributes, with
`updex_available: false`, exactly as the `sysext-without-updex` fixture
already describes. Both contract harnesses are unaffected, because both drive
their fixtures from explicit `capability.Set` values rather than from a live
`Probe` — a fixture that names `podman` still means "a daemon on which
podman was configured and reachable," which is exactly what it always
meant.

### Two residual hits a whole-repo grep still finds, and why neither is a hole

The claims above are scoped to the probes, so a sweep for "does any
`PATH`-resolved `updex` or `dockerclient.FromEnv` survive anywhere in the
tree?" turns up two sites that are *not* counterexamples. Both are recorded
here so a future sweep does not have to re-derive the reasoning:

- **`extctl.NewSystemManager` still defaults an empty `updex` argument to the
  bare name `"updex"`** (`internal/modules/sysext/extctl/manager.go`), and
  `cmd/pilothoused` constructs that manager unconditionally, so on a daemon
  started without `--updex` the field does hold `"updex"`. It is never
  executed, because every method that runs `m.updex` — `Check`, `Enable`,
  `Disable`, `Update`, and `State`'s `updexInventory` branch — is reachable
  only behind a `capability.Updex` guard: `registerSysextActions` gates
  `ActionSysextUpdate` on `Updex` and `ActionSysextEnable`/`ActionSysextDisable`
  on `HasAll(Updex, Sysext)`, and `registerExtensions` passes
  `caps.Has(capability.Updex)` into `State`, which skips `updexInventory`
  when it is false. The one action that survives on a sysext-only host,
  `ActionSysextRefresh`, runs `systemd-sysext refresh` and never touches
  `m.updex`. The default is therefore dead code on an unconfigured daemon,
  not a second enablement path — but it is a live default, so a future
  change that adds an ungated exec path to that manager would resurrect the
  `PATH` fallback this phase removed from the probe.
- **`dockerclient.FromEnv` still appears in
  `internal/modules/docker/manager_live_test.go`**, an opt-in live test that
  runs only when `PILOTHOUSE_LIVE_DOCKER=1` is exported. It is test-only and
  builds its own client rather than going through `ProbeDocker` or `run()`;
  no production path constructs a Docker client from the environment. Both
  `ProbeDocker` and `cmd/pilothoused`'s `connectDocker` use
  `dockerclient.WithHost(endpoint)` and return early on an empty endpoint.
