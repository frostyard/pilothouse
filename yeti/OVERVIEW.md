# Pilothouse Overview

## Purpose

Pilothouse (`github.com/frostyard/pilothouse`) is a local web administration
console for [Snosi](https://github.com/frostyard/snosi) systems. It presents
a live dashboard and management UI (system telemetry, sysext/`updex`
lifecycle, systemd services, Podman/Docker/Incus workloads, journal search,
backups, storage/disk health and managed NFS/SMB mounts, file browsing,
maintenance/reboot) over HTMX-enhanced server-rendered HTML, while keeping
all privileged system access behind a single, fixed, root-only broker.

The defining architectural rule: an unprivileged web process (`pilothouse`)
never talks to root-equivalent APIs (systemd D-Bus, journald, Podman/Docker/
Incus sockets, filesystem roots) directly. It only calls a small, fixed set
of broker queries/actions implemented by a root-only daemon
(`pilothoused`), connected over a protected Unix socket.

## Architecture

```
cmd/pilothouse/       unprivileged web binary (main.go) — TCP listener, no root
cmd/pilothoused/      privileged broker binary (main.go) — Unix socket only, requires euid==0

internal/
  modules/<name>/     vertical feature slices (UI + domain logic), one per management area
  platform/           Module contract (platform.Module), Host interface, module Registry
  web/                HTTP server, session/auth middleware, shell.templ layout, embedded static assets
  broker/             fixed query/action/stream protocol, registries, HTTP-over-Unix-socket server+client
  audit/               durable action-history store (bbolt)
  jobs/                durable background-job store (bbolt), for long-running privileged mutations
  auth/, auth/pam/     NSS group resolution and PAM authentication (used only by pilothoused)

docs/                 authoritative subsystem docs (kept here, not duplicated into yeti/):
  authentication.md    login, session, authorization, audit, PAM policy, deployment rules
  modules.md           how to add a new module: contract, file layout, action/query rules
  capabilities.md      binding table mapping every broker ID to its required host capability

packaging/            systemd units, PAM policy, sysusers declaration
.docker/              development container image (Go + PAM + systemd headers) for docker-* make targets
```

### Two binaries, one protocol

- **`pilothouse`** (`cmd/pilothouse/main.go`): binds a loopback/TCP listener
  (default `127.0.0.1:8888`), instantiates all modules, and wires them to a
  `broker.Client` that dials `/run/pilothouse/broker.sock`. Runs as an
  unprivileged user. Some modules perform genuinely unprivileged local reads
  directly (e.g. `system` collects `/proc`, `/sys`, `/etc/os-release`
  telemetry) — this is allowed because it requires no elevated access.
- **`pilothoused`** (`cmd/pilothoused/main.go`): refuses to start unless
  `euid == 0`. Probes optional host capabilities (`internal/capability`) up
  front, then opens root-owned bbolt databases for audit and jobs, builds
  `broker.QueryRegistry` / `broker.ActionRegistry` / stream registries, and
  registers every privileged implementation (services, Podman, Docker, Incus,
  sysext, files, logs, backups, storage/remote-mounts, maintenance) — each
  registration guarded by the probed capability set so an absent optional
  dependency degrades only that registration instead of aborting startup.
  Serves HTTP only over a Unix socket with `0660 root:<socket-group>`
  permissions — never a TCP listener.

### Modules (`internal/modules/<name>`)

Each module is a vertical slice: collector/manager, `module.go` (routes +
manifest + dashboard cards), `views.templ`, and tests, all under one
directory. Current modules:

| Module | Purpose |
|---|---|
| `system` | Unprivileged host telemetry (CPU/mem/disk/load/net/os) from `/proc`, `/sys`, `/etc/os-release`; emits health findings. |
| `storage` | Block/mount inventory (`lsblk`/`findmnt`) enriched with optional SMART/NVMe, MD RAID, LVM, device-mapper/LUKS, multipath, ZFS, and Btrfs backends; emits health findings; admins can create/mount/unmount/delete Pilothouse-managed NFS and SMB (guest or credentialed) automounts. SMB creation optionally supports paired numeric local UID/GID mapping. Expected immutable EROFS mounts retain their inventory usage and read-only state but are excluded from capacity and read-only health findings; other filesystems retain those checks. |
| `attention` | Aggregates `platform.HealthProvider` findings from other modules (bounded 2s/provider) into one "needs attention" view. |
| `services` | Systemd service/socket/timer inventory and lifecycle/enablement control via system D-Bus; bounded journal diagnostics. |
| `sysext` | Snosi `updex` feature discovery/install state and `systemd-sysext` merge state; install/remove/update/refresh actions. |
| `podman` | System (rootful) Podman inventory (containers/pods/images) via Libpod API; bounded logs; lifecycle actions. |
| `docker` | System Docker daemon inventory, bounded logs, lifecycle/image removal. |
| `incus` | Local-only Incus inventory (projects/instances/images/pools/volumes/buckets) via `/var/lib/incus/unix.socket`; lifecycle actions. |
| `logs` | Admin-only bounded system-journal search (message/priority/unit/time-window filters, ≤200 entries). |
| `files` | Admin-only browsing/download/atomic upload within explicitly configured filesystem roots (256 MiB bound). |
| `backups` | Monitors explicitly configured systemd backup timers: enabled/active state, last result, freshness, next run. |
| `maintenance` | Extension update availability, maintenance-job state, reboot posture, confirmed reboot. |
| `activity` | Admin-only view over durable audit history (`QueryActivity`) and background jobs (`QueryJobs`). |
| `fleet` | Static UI preview only — no real multi-system transport/enrollment exists yet. |

See `docs/modules.md` for the module contract, recommended file layout, and
rules for adding a new module (routes, actions, queries).

**In progress (#51, host-image status): the parsers, the manager, the broker
query, and the reboot posture that consumes it.**
`internal/modules/maintenance/hostimage.go` adds the read-only host-image
domain types — `Deployment` (bootc's image reference + manifest digest, plus
rpm-ostree's supplementary version + ostree checksum) and `HostImageStatus`
(the booted/staged/rollback deployment slots, a three-state
`SoftRebootCapable`, and a symmetric availability/error pair per source:
`BootcAvailable`/`BootcError` and `RPMOStreeAvailable`/`RPMOStreeError`) —
plus `ParseBootcStatus`, a pure decoder for `bootc status --json`;
`ParseRPMOStreeStatus`, a pure decoder for `rpm-ostree status --json` into an
unexported supplement type; and `MergeHostImage`, which combines the two under
a bootc-authoritative precedence rule.

As of this commit those parsers have exactly one caller:
`internal/modules/maintenance/hostimage_manager.go`'s `HostImageManager`,
which in turn has exactly two consumers, both wired to the *same* instance in
`cmd/pilothoused/main.go`: the broker query `QueryHostImageStatus`
(`org.frostyard.pilothouse.maintenance.host_image_status`, registered by
`registerHostImage`), and `maintenance.SystemManager`, which takes it as a
`HostImageSource` and reads it while computing `QueryMaintenanceState`'s
posture. There is still no web-side consumer and no view for host-image
status, and the `maintenance` module's nav, routes, and dashboard are
unchanged; `QueryMaintenanceState`'s response is the one thing that changed
shape (see the `State` bullet below). What the daemon side now does:

- `NewHostImageManager(runner, bootcAvailable, rpmOstreeAvailable)` takes the
  probed `capability.Bootc`/`capability.RPMOStree` facts and runs, at most,
  `bootc status --json` and `rpm-ostree status --json` — each at most once per
  `Status` call, only when its flag is true, always through the injected
  `Runner`, never a shell and never a second subcommand. `Status` merges the
  two with `MergeHostImage` and returns raw facts only; it computes no
  reboot-required posture (still `SystemManager.State`'s job) and exposes no
  mutation.
- Per-source failure is symmetric and never fatal: an exec failure *or* a
  parse failure on either source sets that source's `*Available` to false and
  its `*Error` to the message, leaving the other source's data intact.
  `Status` returns no error of its own for a source-level failure, so a host
  where only one tool answers still gets an honest, partial report. A source
  whose capability is absent is never attempted at all, and reports neither
  availability nor an error.
- `registerHostImage` guards the query with
  `caps.HasAny(capability.Bootc, capability.RPMOStree)` — the first any-of
  guard in the daemon's registration code — and is deliberately independent of
  `registerMaintenance`'s `Systemd` guard, so a bootc host without systemd gets
  host-image reporting while the reboot posture query and reboot action stay
  withheld. `docs/capabilities.md`'s binding table carries the row (52 IDs,
  17 queries) and `cmd/pilothoused/capability_contract_test.go` exercises it
  across bootc-only, rpm-ostree-only, both, and neither fixtures.
- `maintenance.SystemManager` consumes the staged-deployment fact. `State` is
  where reboot-required posture is assembled and, per the spec's
  "reboot-required posture lives in exactly one place" rule, the only place a
  staged bootc deployment becomes a reason:
  `NewSystemManager(..., hostImage HostImageSource, ..., bootcAvailable bool)`
  reads the source **once** per `State` call, when `bootcAvailable`, and uses
  the single result for two independent purposes. A non-nil `Staged`
  deployment appends "A staged host image deployment requires activation by
  reboot." (`stagedHostImageReason`) alongside the `/run/reboot-required`
  marker, the merged-but-disabled extension reasons, and the completed-job
  reason, and factors into `RebootRequired` the same way. Independently,
  `HostImageStatus.SoftRebootCapable` is copied verbatim onto the new
  `State.SoftRebootCapable *bool` (`soft_reboot_capable,omitempty`) — copied,
  never recomputed, so there is no second source of truth — and is purely
  informational: it is reported whether or not anything is staged and never
  makes `RebootRequired` true on its own. Its three states survive the copy:
  nil means "this bootc does not report eligibility," never a synthesized
  false. The bootc leg follows the same degrade convention as the
  `updexAvailable`/`sysextAvailable` legs: with `bootcAvailable` false the
  source is never called at all (no staged reason, `SoftRebootCapable` nil,
  whatever the source would have said), and when it is called and fails, the
  failure is dropped rather than propagated — per-source availability and
  errors are `QueryHostImageStatus`'s to report (`BootcAvailable`/`BootcError`),
  and the aggregate posture stays answerable. `State` never returns an error
  because of bootc. Only the existing full reboot action is exposed; nothing
  performs a soft reboot.

Contracts of the parsers themselves, worth knowing before consuming them:

- `hostimage.go` executes nothing. Its imports are limited to
  `encoding/json`/`fmt`/`strings`, enforced mechanically by a test over the
  file's AST, so no bootc invocation — least of all a mutation such as
  upgrade, switch, rebase, or rollback — can originate there. Obtaining the
  bytes is the manager's job, and `hostimage_manager.go` imports only
  `context`, so the injected `Runner` is provably the only way a command
  leaves the package.
- A structurally malformed payload returns a non-nil error together with a
  zero `HostImageStatus` (`BootcAvailable` false), never partial data. The
  caller decides whether to record that as `HostImageStatus.BootcError` on an
  otherwise usable report; `ParseBootcStatus` itself never sets `BootcError`.
- "Malformed" covers substance, not just syntax, because a confident but empty
  success would mislead every downstream consumer. Beyond non-JSON, truncated
  JSON, and wrong-typed fields, the parser rejects a document that omits any
  element bootc always emits: `apiVersion` and `kind` are both *required*
  discriminators (an omitted `apiVersion` is a failure, not a bypass — only its
  value is matched loosely, by prefix, so `org.containers.bootc/v2` still
  parses), and the `status` object and its `booted` deployment must be present.
  A payload that satisfies the discriminators but reports nothing — for
  instance `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost"}` —
  is an error rather than a successful `HostImageStatus` with every slot nil.
  Consequently `Booted` is always non-nil on success. Only `staged` and
  `rollback` are optional: a host with nothing pending and nothing to roll
  back to is ordinary, so those slots stay nil without error.
- `SoftRebootCapable` is three-state: non-nil true/false when the host's bootc
  exposes soft-reboot eligibility, nil when it does not. The key is
  `softRebootCapable` on a boot entry, confirmed against bootc's published
  schema (`crates/lib/src/spec.rs`: `BootEntry.soft_reboot_capable`, camelCase,
  `#[serde(default)]`; `HostStatus` has no such field). The parser prefers the
  staged entry — the deployment a soft reboot would activate — and falls back
  to the booted entry when nothing is staged (upstream computes the flag per
  deployment for every reported slot, booted included, so that fallback is not
  a reinterpretation of a staged-only field). A bootc new enough to have the
  field always emits it — it is a plain `bool` with no `skip_serializing_if`,
  so it serializes even when false — which means an absent key reliably
  indicates a bootc predating soft-reboot support: unknown, never a parse
  error and never false.
- rpm-ostree is the *supplementary* source, and its parser's return type says
  so: `ParseRPMOStreeStatus` yields an unexported `rpmOStreeSupplement` (per
  deployment: version string, ostree checksum, plus image/digest/role used
  only for matching), never a `HostImageStatus`, so rpm-ostree output cannot
  stand alone as a host-image report even by accident. rpm-ostree's document
  has no apiVersion/kind discriminator, so the required top-level
  `deployments` array plays that role: a payload without it is a parse error,
  while a payload whose array is empty is a *success* with nothing to add.
  That distinction is the point — it lets the caller tell "rpm-ostree ran but
  its output could not be read" (record `RPMOStreeError`) from "rpm-ostree
  read fine and had nothing to say."
- `MergeHostImage(bootc, rpmOstree)` encodes the spec's precedence rule as
  behavior, not prose. bootc owns deployment identity outright: which slots
  exist, their image reference, their digest, and `SoftRebootCapable` all come
  from bootc alone, and rpm-ostree can only ever fill in `Version`/`Checksum`
  on a slot bootc already reported. It cannot add, remove, or rename a slot —
  merging a full supplement into a failed bootc parse still yields no
  deployments. Entries are matched by the role rpm-ostree itself flags (booted,
  staged) and, for the rollback slot it does not flag, by identity — the digest
  bootc reported, or the image reference when neither side reports a digest,
  compared after stripping the ostree transport decoration rpm-ostree puts in
  front of a reference (`ostree-unverified-registry:`, `…:docker://…`) and
  bootc does not. On conflict the entry is dropped *whole*, version and
  checksum included: a deployment the two sources describe differently is not
  evidently the same deployment, so the failure direction is always less
  detail, never wrong detail.
- `MergeHostImage` returns `RPMOStreeAvailable`/`RPMOStreeError` at their zero
  value and does not carry over an incoming value for either. It only ever
  receives an already-parsed supplement, so it cannot know whether rpm-ostree
  failed, reported nothing, or was never run; the caller that runs the command
  owns those fields and sets them after merging, exactly as it does for bootc.
  The merged result also shares no memory with either argument, so it never
  writes back into the caller's own parse.

## Key Patterns

### The broker is the only privilege boundary

- **Fixed IDs only.** Every privileged read is a fixed `broker.Query*` ID;
  every privileged mutation is a fixed `broker.Action*` ID
  (`internal/broker/api.go`). There is no generic command execution,
  filesystem proxy, or socket proxy in the protocol — never add one.
- **Registration only in `cmd/pilothoused`.** Modules never call system
  D-Bus, journald, or container-engine sockets from the web process. A
  module's web-side code calls `host.Query(...)` / `host.Execute(...)` /
  `host.StreamQuery(...)` / `host.StreamAction(...)` (the `platform.Host`
  interface); the corresponding privileged handler is registered exactly
  once in `cmd/pilothoused/main.go` and re-validates identifiers before
  every mutation.
- **Re-authorization per call.** The broker re-resolves the caller's system
  group membership on every request (not just at login), so removing a user
  from the admin group takes effect immediately, without waiting for
  session expiry.
- **Durable audit before mutation.** Action intent is recorded in a
  root-owned bbolt database *before* the action runs; if the audit store is
  unavailable, the action does not run. Long-running mutations (extension
  update/refresh) run as durable background jobs so a browser disconnect
  doesn't cancel in-flight work.
- **Per-resource action serialization, keyed per subsystem.** Every action
  definition resolves a lock key — `LockResource` when set, otherwise the
  audited `Resource` — and `internal/broker`'s action registry holds it for
  the action's duration, so conflicting operations on one resource cannot
  overlap. The keys are deliberately per subsystem: the sysext lifecycle
  actions (enable/disable/refresh/update) share `sysext/global`; storage
  remote-mount lifecycle actions key on their opaque
  `storage/mount/<id>` with creation on `storage/mounts`; and
  `ActionMaintenanceReboot` holds `maintenance/global`
  (`maintenanceLockResource` in `cmd/pilothoused/main.go`). Reboot formerly
  reused sysext's key, which was reuse rather than an intentional coupling —
  it now serializes only against another reboot, and an in-flight extension
  refresh/update no longer refuses a reboot (nor the reverse). Confirmation,
  admin authorization, and the audited `maintenance/reboot` resource are
  unchanged. `cmd/pilothoused/main_test.go` proves both halves through real
  `broker.ActionRegistry.Execute` calls.
- **Streams for large/blocking data.** File upload/download use fixed
  `stream-actions`/`stream-queries` registrations with explicit size caps
  (256 MiB) rather than the generic action/query path.
- **Storage executable validation.** Core and optional storage commands use
  fixed absolute candidates. Optional candidates may be symlinks for distro
  multicall tools such as LVM, but the broker validates the fully resolved
  target as a root-owned, non-group/world-writable regular file while executing
  the original entry-point path. Broken or unsafe present candidates fail
  startup; absent optional tools degrade only their backend to unsupported.
- **Capability probing at startup.** `pilothoused` probes optional host
  capabilities once, early in `cmd/pilothoused/main.go`'s `run()`, before any
  module manager is constructed (`internal/capability.Probe`): systemd,
  journald, `updex`, `systemd-sysext`, bootc, rpm-ostree, the
  `rpm-ostreed-automatic`/`bootc-fetch-apply-updates` automatic-update
  unit-file pairs, and the Podman/Docker/Incus engine sockets. Every
  individual probe narrows to "absent" on any error rather than failing —
  probing itself is never fatal. The resulting `capability.Set` is not
  cached or re-probed later; a daemon restart re-probes from scratch. It is
  advertised over the fixed, authenticated, non-admin
  `org.frostyard.pilothouse.capabilities.list` query
  (`broker.QueryCapabilities`), returning `{"capabilities": [...]}` —
  present capabilities only, sorted, canonical IDs — and restart re-probes
  from scratch (nothing is cached). The same `capability.Set` gates
  privileged registration: see `docs/capabilities.md` for the binding
  table mapping every broker ID to its required capability, and
  `docs/modules.md`'s "Capability-guarded registration" section for the
  convention new modules follow. `registerPodman`/`registerDocker`/
  `registerIncus` are the first full conversions — each takes `caps
  capability.Set` and registers nothing for its engine when the
  corresponding capability is absent (an unreachable or misconfigured
  engine, including a Docker client that fails to construct, is logged as
  a warning, never a fatal `run()` error). `registerServices` and
  `registerLogs` are the next conversions: `registerServices` guards
  `QueryServicesState` and every services lifecycle action on
  `caps.Has(capability.Systemd)`, and `QueryServicesJournal` separately on
  `caps.HasAll(capability.Systemd, capability.Journald)` — guarded
  individually per `docs/capabilities.md`'s corrected mapping, so a host
  with systemd but no journald still gets full service management with
  only the journal query withheld; `registerLogs` guards its single
  `QueryLogs` registration on that same `caps.HasAll(capability.Systemd,
  capability.Journald)`. `registerStorageActions` and `registerBackups` are
  the next conversions: both guard their whole function on
  `caps.Has(capability.Systemd)` alone (every remote-mount action generates
  or controls systemd units, and backups monitors systemd timers, so
  neither has a services-style mixed per-call requirement); their
  nil-manager check is retained alongside the capability check as a
  defensive backstop for directly-injected test fakes. `QueryStorageState`
  itself, registered separately against the plain, non-systemd
  `storageManager`, remains unconditional per `docs/capabilities.md`'s
  documented exception.
- **Maintenance: guarded registration plus a real handler-level degrade.**
  `registerMaintenance` (`cmd/pilothoused/main.go`) is the next conversion:
  it takes the probed `capability.Set` and no-ops both
  `QueryMaintenanceState` and `ActionMaintenanceReboot` when `systemd` is
  absent, exactly like `registerBackups`/`registerStorageActions`.
  `maintenance.NewSystemManager` has no D-Bus dependency of its own (it
  depends only on the sysext manager, job store, and command runner), so
  unlike backups/services/logs there is no construction-level non-fatal-
  startup fix to make here; the manager is always constructed regardless of
  systemd, and the registration guard above is the only thing withholding
  it. Separately — and this is the real behavioral change in this chunk —
  `maintenance.SystemManager.State`'s extension-read subpath
  (`extensionState`, which calls `UpdateSource.Check` for `Updates` and
  `UpdateSource.List` for `Features`/merged-status-derived reboot reasons)
  degrades gracefully instead of erroring when `updex`/`systemd-sysext` are
  unavailable, driven by two new `updexAvailable`/`sysextAvailable`
  parameters on `NewSystemManager` fed from `cmd/pilothoused/main.go`'s
  probed `caps.Has(capability.Updex)`/`caps.Has(capability.Sysext)`: with
  both present, behavior is byte-for-byte unchanged; with updex present but
  sysext absent, `Check()` still runs (`Updates` populates) but `List()` is
  skipped entirely (merged-but-disabled reboot reasons omitted); with updex
  absent (sysext present or absent), neither call runs and both `Updates`
  and feature-derived reboot reasons are omitted — a documented limitation
  of today's `sysext.SystemManager`, whose enumeration is updex-only by
  construction, not a phase 1a gap. `State` never returns an error because
  of missing updex/sysext in any combination; `Jobs`, `OSVersion`, and
  reboot-marker-derived reasons are computed exactly as before regardless.
  See `docs/capabilities.md`'s extension-read note for the full table and
  `internal/modules/maintenance/manager_test.go` for one dedicated test case
  per combination. (`NewSystemManager` has since grown a third
  `hostImage`/`bootcAvailable` pair for the host-image leg described in the
  #51 section above; it follows the same degrade convention and leaves the
  updex/sysext behavior here untouched.)
- **Sysext: the one module guarded per-action, not per-function.**
  `registerSysextActions` (`cmd/pilothoused/main.go`) is the final capability
  conversion in this phase, and the only one where the four registrations
  don't share a single requirement: `ActionSysextDisable`/`ActionSysextEnable`
  (registered together via the shared `registerNamedActions` helper) require
  `updex AND sysext` together, so that pair is guarded as one group;
  `ActionSysextRefresh` requires `sysext` alone and `ActionSysextUpdate`
  requires `updex` alone — those two already lived in a separate local loop,
  so each entry there now carries its own required capability, checked
  in-loop, without changing `registerNamedActions`/`registerProjectActions`
  (every other caller has a uniform per-call requirement). `sysext.NewSystemManager`
  has no systemd D-Bus dependency (exec/`CommandRunner`-based only), so — like
  maintenance — there is no construction-level non-fatal-startup fix needed;
  `sysextManager` is constructed unconditionally regardless of capability, and
  the per-action registration guards above are what withhold each action. See
  `docs/capabilities.md`'s sysext rows and module-level-defaults section for
  the full per-action table.
- **Web-side capability fetch/cache.** `internal/web.Server` keeps its own
  opportunistically-refreshed view of the broker's advertised
  `capability.Set`, separate from `pilothoused`'s own probe/advertise cycle
  above. `internal/web/capabilities.go`'s
  `capabilityCache` (a field on `Server`, zero-value valid) holds the last
  fetched `Set` plus a `down` flag; `Server.Capabilities(ctx
  context.Context)` (added to the widened `platform.Host` interface so both
  HTTP handlers and `Module.Dashboard(ctx, host)` can call it) returns
  whatever is cached, or the zero (all-absent) `Set` before any successful
  login or fetch. `Server.refreshCapabilities(ctx, token)` issues a
  `broker.QueryCapabilities` query under its own 2s bounded context derived
  from the caller's `ctx` (never `context.Background()`, per the
  reuse-bounded-context lesson from #50) and is wired at exactly two
  checkpoints: right after a successful `login`, and in the `authenticate`
  middleware after `Session()` succeeds, but only when the cache is
  `staleAfterOutage()` — i.e. only the first authenticated request after a
  prior `broker.ErrUnavailable`-wrapped failure triggers a refetch, not
  every request. `Session()`'s own transport-failure branch, and the
  `Query`/`Execute`/`StreamAction`/`StreamQuery` wrapper methods, all call
  `capabilityCache.noteResult(err)` after their underlying broker call to
  mark the cache down on an `ErrUnavailable`-wrapped error; none of them
  ever clear the flag or trigger a refetch themselves — only the two
  checkpoints above do that, so one request never issues more than one
  capability refetch. Authorization failures, request-validation errors,
  and arbitrary domain errors never mark the cache down or trigger a
  refetch. `capability.Set` gained `UnmarshalJSON` (mirroring the existing
  `MarshalJSON`) to decode this query's `{"capabilities": [...]}` response.
- **Whole-module web-side capability gating (mechanism only).**
  `internal/platform/capability.go` adds the primitives every later
  capability-gated module will use, on top of the web-side capability
  fetch/cache above: `CapabilityGate` is an interface
  (`RequiredCapabilities() []capability.ID`) a `Module` optionally
  implements to declare that its whole surface (nav entry, dashboard cards,
  routes) needs some set of host capabilities present (`Set.HasAll`
  semantics); a `Module` that does not implement it has no requirement and
  is always available — the default for `system`/`files`/`activity`/`fleet`
  and storage's own inventory reads. `Gate(host Host, ids []capability.ID,
  next http.HandlerFunc) http.HandlerFunc` wraps a `Mount`-registered
  handler so the route itself stays mounted on the shared mux, but 404s at
  request time when `host.Capabilities(ctx)` doesn't `HasAll(ids...)` —
  this is what "routes stay mounted, capability absence 404s instead of
  changing the mux" means concretely for a module's `Mount`. A second,
  exported function, `Available(module Module, caps capability.Set) bool`,
  applies the same `CapabilityGate`-or-default-available test to a whole
  module rather than a single request — it type-asserts `CapabilityGate`
  and defaults to available when a module doesn't implement it, exactly
  mirroring the check `Gate` makes per-request. `internal/web/server.go`
  wires the interface (not `Gate`, which individual modules call from their
  own `Mount`) into the two web-side registries the spec calls out: an
  unexported `moduleAvailable(module platform.Module, caps capability.Set)
  bool` delegates the gating decision to `internal/platform` rather than
  reimplementing it — in this chunk that was `platform.Available` alone; the
  next bullet's any-of work changed the body to
  `platform.Available(module, caps) && platform.AvailableAny(module, caps)`,
  and it remains the single choke point both web-side registries call, with
  each half implemented once in `internal/platform` and shared with that
  package's own tests — and `Render` now builds the shell's `Modules` nav list from a new
  `s.availableManifests(ctx)` (filters `s.registry.Modules()` through
  `moduleAvailable` before mapping to `Manifest`, replacing the previous
  unfiltered `s.registry.Manifests()` call) and the `dashboard` handler's
  per-module loop skips a capability-gated-absent module entirely — no
  `Dashboard()` call, no card, no error-card placeholder, since an
  unavailable surface is not rendered at all, not shown degraded. `Mount()`
  at server construction (`internal/web/server.go`, around where the
  registry's modules are wired to the mux) stays unfiltered: every module's
  routes remain mounted regardless of capability, per the "routes stay
  mounted" requirement above; only the nav list and the dashboard loop are
  filtered by `moduleAvailable`. No production module implemented
  `CapabilityGate` in this chunk — the mechanism was proven with a
  synthetic fake module in `internal/platform/capability_test.go` (which
  exercises `Available` through a fake `Host`'s real `Capabilities()`
  method, not a capability.Set passed in directly, so the test covers the
  same `Host`-integration boundary the production code depends on) and
  `internal/web/server_test.go`, and every real module's
  nav/dashboard/route behavior was unchanged. `services` is the first real
  module to adopt it — see the next bullet.
- **`HasAny`/`CapabilityGateAny`/`GateAny`/`AvailableAny`: an any-of sibling
  (mechanism only).** `internal/capability.Set` gained `HasAny(ids ...ID)
  bool`, reporting true iff at least one given id is present; unlike
  `HasAll`'s zero-ids case (vacuously true), `HasAny()` with zero ids is
  always false ("any of nothing" has no capability to satisfy), and a
  nil/zero-value `Set`'s `HasAny` is nil-safe like `Has`/`HasAll`.
  `internal/platform` mirrors `CapabilityGate`/`Gate`/`Available` with a
  parallel any-of trio — `CapabilityGateAny` (`RequiredAnyCapabilities()
  []capability.ID`), `GateAny(host, ids, next)`, and `AvailableAny(module,
  caps)` — kept as separate types rather than folding an any-of flag into
  `CapabilityGate`, since no module needs both AND and OR semantics on its
  whole-module gate at once. `moduleAvailable` now composes both:
  `platform.Available(module, caps) && platform.AvailableAny(module,
  caps)`. Because `Available` defaults to `true` for a module that doesn't
  implement `CapabilityGate` and `AvailableAny` defaults to `true` for a
  module that doesn't implement `CapabilityGateAny`, this
  AND-of-two-defaults composition is correct for all three shapes a module
  can be in (`CapabilityGate` only, `CapabilityGateAny` only, or neither)
  with no type-switching in `server.go` itself. The one other place that
  gates a module's surface outside `Mount`/nav/dashboard —
  `internal/modules/attention.Module.findings`, which calls
  `HealthProvider.Health` directly — was updated in the same chunk to
  type-assert `CapabilityGateAny` alongside `CapabilityGate` and skip a
  provider when either gate is unsatisfied, so a future `CapabilityGateAny`
  module can't be hidden from nav/dashboard and 404 on its routes while
  `/attention` still calls its `Health`. No production module
  implements `CapabilityGateAny` yet — the mechanism was proven the same
  way `CapabilityGate` was before its first real adopter: a synthetic fake
  module in `internal/platform/capability_test.go` (exercising `AvailableAny`
  through a fake `Host`'s real `Capabilities()`) and a synthetic fake module
  registered into a real `*web.Server` in `internal/web/server_test.go`
  proving nav/dashboard/route behavior through a real registry and HTTP
  round trip.
- **Services module: the first real `CapabilityGate` adopter.**
  `internal/modules/services.Module` now implements
  `RequiredCapabilities() []capability.ID`, returning
  `[]capability.ID{capability.Systemd}` — so the whole module (nav entry,
  dashboard card, and future `Health` inclusion) is available only when the
  web process's cached `capability.Set` has `Systemd`, matching #50's daemon-
  side `registerServices` gating. Each route `services.Module.Mount`
  registers is individually wrapped in `platform.Gate`: `GET /services` and
  `POST /services/{unit}/{action}` require only `{capability.Systemd}`;
  `GET /services/{unit}/logs` requires `{capability.Systemd,
  capability.Journald}` (`Gate`'s `HasAll` semantics cover the AND), so a
  host with `Systemd` but not `Journald` keeps full service state and
  lifecycle control while the journal sub-feature 404s. `views.templ`'s
  `Page(...)` takes a new `journalAvailable bool` parameter and only renders
  the per-unit `Logs` link when it is true; the `GET /services` handler in
  `module.go` derives it from `host.Capabilities(r.Context()).Has(capability.Journald)`
  (Systemd is already guaranteed true inside a `Gate`-wrapped handler, so no
  redundant check is needed there). `module_test.go`'s `testHost` gained a
  configurable `caps capability.Set`/`capsSet bool` pair (defaulting to a
  full-capability set matching the pre-#54 behavior) so tests can exercise
  Systemd-present/-absent and Journald-present/-absent independently via
  real `ServeMux` round trips through `Mount`, rather than calling handler
  logic directly.
- **Backups and maintenance: whole-module `Systemd` gates.**
  `internal/modules/backups.Module` and `internal/modules/maintenance.Module`
  now also implement `RequiredCapabilities() []capability.ID`, each returning
  `[]capability.ID{capability.Systemd}` — unlike services, neither has a
  sub-feature with a broader requirement, so there is exactly one
  `platform.Gate(host, []capability.ID{capability.Systemd}, ...)` wrap per
  route: backups' single `GET /backups`, and maintenance's `GET /maintenance`
  and `POST /maintenance/reboot`. With `Systemd` absent, the whole module
  disappears — nav entry, dashboard card, and every route 404s at request
  time; with `Systemd` present, both modules behave exactly as before this
  chunk. Neither module's `views.templ` changed: an absent module 404s
  before any page renders, so there is no conditional view content to add,
  unlike services' `journalAvailable` parameter. Maintenance's existing
  extension-read degrade (`QueryMaintenanceState`'s updex/sysext handling,
  from #50) is untouched by this chunk; the systemd gate sits on top of it,
  at the module/route level, not inside the query handler. Both
  `module_test.go` files gained the same configurable `caps
  capability.Set`/`capsSet bool` pair on their fake `Host` that services'
  test uses (defaulting to a full-capability set), so gated/ungated route
  behavior is exercised via real `ServeMux` round trips through `Mount`.
  `platform.Gate`/`Available` only guard requests that arrive through a
  module's own `Mount`-registered routes or the web-side nav/dashboard
  loops, though — they do nothing for other in-process code that holds a
  `platform.HealthProvider` reference and calls `Health` directly.
  `internal/modules/attention.Module.findings` is exactly that: it iterates
  every registered provider (including `backupModule` and
  `maintenanceModule`, passed into `attention.New(...)` in
  `cmd/pilothouse/main.go`) and previously called `provider.Health(ctx,
  host)` unconditionally, so a `Systemd`-absent host still reached
  `QueryBackupsState`/`QueryMaintenanceState` through `/attention` and
  rendered a degraded "status is unavailable" finding instead of the
  provider being absent entirely. `findings` now type-asserts each
  provider to `platform.CapabilityGate` and, when the host's cached
  `capability.Set` doesn't `HasAll` its `RequiredCapabilities`, skips it
  outright — no `Health` call and no "unavailable" finding, since an absent
  module is not the same as one whose status collection failed. This is
  the same `CapabilityGate` type-assert-and-check `Gate`/`Available`
  already apply, generalized to this aggregator's direct method calls;
  `internal/modules/attention/module_test.go` proves it with a
  Health-call-counting fake provider, at both the absent- and
  present-capability ends. (The any-of bullet below later extended the same
  skip to `platform.CapabilityGateAny`/`HasAny`; see "Attention's
  per-provider capability skip" in the current-state section for the
  composed behavior.)
- **Logs: whole-module `Systemd AND Journald` gate.**
  `internal/modules/logs.Module` now implements
  `RequiredCapabilities() []capability.ID`, returning
  `[]capability.ID{capability.Systemd, capability.Journald}` — matching
  `docs/capabilities.md`'s `QueryLogs` exception (the manager resolves units
  via the systemd D-Bus client before reading journal entries, so the
  module needs both, not journald alone). Its single route,
  `GET /logs`, is wrapped with `platform.Gate(host,
  []capability.ID{capability.Systemd, capability.Journald}, ...)` in
  `internal/modules/logs/handler.go`; with either capability absent the
  whole module disappears — nav entry and the route 404 at request time —
  and with both present it behaves exactly as before this chunk. Unlike
  services, logs has no sub-feature with a narrower requirement, so there
  is exactly one `Gate` wrap. `logs.Module.Dashboard` already returns
  `(nil, nil)` unconditionally and logs is not a `platform.HealthProvider`
  (see the module table above), so no dashboard or `attention` aggregator
  change was needed here, unlike backups/maintenance. `module_test.go`
  gained the same configurable `caps capability.Set`/`capsSet bool` pair
  on its fake `Host` that services/backups/maintenance use (defaulting to
  a full-capability set), so gated/ungated route behavior — including the
  systemd-only and journald-only partial cases — is exercised via real
  `ServeMux` round trips through `Mount`. `platform.Available` is also
  exercised directly against the module's `RequiredCapabilities` as a
  unit-level check, but the nav claim itself is proven end-to-end:
  `TestLogsNavEntryFollowsCapabilityGateEndToEnd` builds a real
  `internal/web.Server` (via `platform.NewRegistry(New())`, the same
  constructor path `cmd/pilothouse` uses) backed by a fake broker, drives an
  actual `POST /login` then `GET /` through `server.Handler()`, and asserts
  the rendered dashboard HTML omits `href="/logs"`/`Logs` when either
  capability is missing and includes them when both are present — so the
  nav-filtering predicate (wired generically in `internal/web/server.go`
  since c2) is confirmed against this real module's adoption, not just a
  synthetic gated module or a direct `platform.Available` call.
- **Storage: route-level `Systemd` gate, not a whole-module gate.**
  `internal/modules/storage.Module` deliberately does *not* implement
  `platform.CapabilityGate` — its nav entry, dashboard card, and
  `GET /storage` inventory page stay available regardless of `Systemd`,
  matching `docs/capabilities.md`'s `QueryStorageState` exception (the
  daemon-side `registerStorageActions`/`registerBackups` split from #50).
  Only the three remote-mount routes in `internal/modules/storage/module.go`
  — `GET /storage/mounts/new`, `POST /storage/mounts`, and
  `POST /storage/mounts/{id}/{action}` (which covers mount, unmount, *and*
  delete) — are individually wrapped in `platform.Gate(host,
  []capability.ID{capability.Systemd}, ...)`. This is the one module in the
  phase where a capability gate is scoped to a subset of routes rather than
  the module's whole surface, so the corresponding view had to be audited
  for every element targeting one of those three routes, not just the ones
  named in the spec by example: `views.templ`'s `ManagedPage`/
  `ManagedSnapshotRegion`/`ManagedMountTable` all gained a sibling
  `remoteMountsAvailable bool` parameter (alongside the existing `admin
  bool`), and `ManagedMountTable` collapses the *entire* per-mount
  `<div class="actions">` block — Mount, Unmount, and Delete together — on
  that one flag, evaluated once before the per-state Mount/Unmount `if`s,
  rather than hiding each form independently; `ManagedPage` also omits the
  "Add remote mount" link on the same flag. `module.go`'s `GET /storage`
  handler derives the flag from
  `host.Capabilities(r.Context()).Has(capability.Systemd)` and passes it to
  `ManagedPage` alongside the existing `admin` argument. With `Systemd`
  absent, storage inventory/capacity/findings keep rendering exactly as
  before, but no link, form, or button anywhere on the page still points at
  one of the now-404ing remote-mount routes; with `Systemd` present,
  rendering is byte-for-byte unchanged from before this chunk.
  `storage/module_test.go`'s fake `Host` gained the same configurable `caps
  capability.Set`/`capsSet bool` pair the other gated modules' tests use
  (defaulting to a full-capability set), and a dedicated test asserts
  `storage.Module` does *not* satisfy `platform.CapabilityGate` while
  `platform.Available` still reports it available under a no-`Systemd`
  fixture — the two assertions together are what "storage stays in c2's
  available-modules filter" means concretely for a partial-gate module.
- **Podman and docker: whole-module engine-capability gates.**
  `internal/modules/podman.Module` and `internal/modules/docker.Module` now
  implement `RequiredCapabilities() []capability.ID`, returning
  `[]capability.ID{capability.Podman}` and `[]capability.ID{capability.Docker}`
  respectively — matching `docs/capabilities.md`'s one-capability-per-engine
  mapping and #50's daemon-side `registerPodman`/`registerDocker` gating.
  Each module has the same four-route shape (state page, container logs,
  container action, image action), and every one is wrapped in
  `platform.Gate(host, []capability.ID{capability.Podman|Docker}, ...)` in
  the module's own `Mount`: `GET /podman`/`GET /docker`,
  `GET /{podman,docker}/containers/{id}/logs`,
  `POST /{podman,docker}/containers/{id}/{action}`, and
  `POST /{podman,docker}/images/{id}/{action}`. Neither module has a
  sub-feature with a broader or narrower requirement (unlike services'
  journal split), so there is exactly one `Gate` wrap per route, all sharing
  the module's single capability. With the engine capability absent, the
  whole module disappears — nav entry, dashboard card, and all four routes
  404 at request time — while the sibling engine and the rest of the app are
  unaffected; with the capability present, both modules behave exactly as
  before this chunk. Neither module's `views.templ` changed: an absent
  module 404s before any page renders, so there is no conditional view
  content to add, the same as backups/maintenance/logs. Neither module is a
  `platform.HealthProvider` (see the module table above), so no `attention`
  aggregator change was needed here either. Both `module_test.go` files
  gained the same configurable `caps capability.Set`/`capsSet bool` pair on
  their fake `Host` that the other gated modules' tests use (defaulting to a
  full-capability set), so gated/ungated route behavior — and that gating
  one engine leaves the other engine's routes and the rest of the mux
  unaffected — is exercised via real `ServeMux` round trips through `Mount`.
- **Incus: whole-module engine-capability gate.**
  `internal/modules/incus.Module` now implements
  `RequiredCapabilities() []capability.ID`, returning
  `[]capability.ID{capability.Incus}` — the same one-capability-per-engine
  mapping podman and docker use, matching `docs/capabilities.md` and #50's
  daemon-side `registerIncus` gating. Unlike podman/docker, incus has no
  separate logs route (its state page nests project/instance detail inline),
  so it has exactly three routes, all wrapped in
  `platform.Gate(host, []capability.ID{capability.Incus}, ...)` in the
  module's own `Mount`: `GET /incus`,
  `POST /incus/instances/{name}/{action}`, and
  `POST /incus/images/{fingerprint}/{action}`. With incus absent, the whole
  module disappears — nav entry, dashboard card, and all three routes 404 at
  request time — while podman, docker, and the rest of the app are
  unaffected; with incus present, the module behaves exactly as before this
  chunk. `views.templ` is unchanged: an absent module 404s before any page
  renders, so there is no conditional view content to add, the same as
  podman/docker/backups/maintenance/logs. Incus is not a
  `platform.HealthProvider` either, so no `attention` aggregator change was
  needed. `module_test.go` gained the same configurable
  `caps capability.Set`/`capsSet bool` pair on its fake `Host` that the other
  gated modules' tests use (defaulting to a full-capability set), so
  gated/ungated route behavior — and that gating incus leaves the rest of
  the mux unaffected — is exercised via real `ServeMux` round trips through
  `Mount`.
- **Storage SMB ownership mapping.** The fixed administrator-only
  `org.frostyard.pilothouse.storage.create-smb-guest-owned` and
  `org.frostyard.pilothouse.storage.create-smb-credentials-owned` actions
  require paired canonical numeric `uid` and `gid` values. The privileged
  manager validates them independently, persists mapped definitions as manifest
  version 2, and deterministically renders manager-controlled CIFS `uid=` and
  `gid=` options. Version 1 definitions remain supported without migration.
  The web process cannot resolve names or provide free-form mount options, and
  no generic command, filesystem, or socket capability is introduced.

See `docs/capabilities.md` for the full broker-ID-to-capability table and
`docs/authentication.md` for the full login/session/authorization/audit
model and deployment rules (cookie flags, allowed origins, PAM policy).

### Web-side capability gating (end state, #54)

Several bullets above narrate individual pieces of #54 (phase 1b of the #35
decomposition, per `docs/capabilities.md`) as they landed — the web-side
fetch/cache, the gating mechanism, and each adopting module. This subsection
is the consolidated end-state contract for the whole issue. The unprivileged web
process (`cmd/pilothouse`) derives its navigation, dashboard cards, routes,
and actions from the broker's advertised `capability.Set`, so a host missing
optional tooling never shows a dead link or a button that always fails.

- **Capability fetch/cache lifecycle** (`internal/web/capabilities.go`,
  `internal/web/server.go`). `broker.QueryCapabilities` is an *authenticated*
  query, so the set cannot be fetched before login. `Server.refreshCapabilities`
  fetches it (1) on each successful `login`, once a session token exists, and
  (2) in the `authenticate` middleware on the first successful authenticated
  request *after* a broker transport/unavailable failure — the cache's `down`
  flag is set by `capabilityCache.noteResult`, which the `Session` branch of
  `authenticate` and the `Query`/`Execute`/`StreamAction`/`StreamQuery`
  wrappers call after their underlying broker call whenever the error wraps
  `broker.ErrUnavailable` (as does `refreshCapabilities` itself on a failed
  fetch, so an outage that also swallows the refetch stays marked and is
  retried on the following request). Only `staleAfterOutage()` triggers a
  refetch, at most one per request, and it runs inside `authenticate` *after*
  that request's own `Session()` validation has already succeeded — not at the
  literal top of the handler, and never for `publicPath` requests, which skip
  the authenticated branch entirely. It is **never fetched pre-login**
  (the login page needs no capabilities; `Server.Capabilities` returns the
  zero, all-absent `Set` until the first successful fetch) and **never cached
  for the process lifetime**: the filtered nav/dashboard/route view is
  re-derived from the latest fetched set on every request, and any
  `ErrUnavailable` — which is what a broker restart looks like to the
  stateless per-request client — marks the cache stale (the previously fetched
  set is kept and still served meanwhile, only the `down` flag flips) so the
  next successful authenticated request refetches. A restarted broker advertising a different
  set is therefore followed without restarting the web process. Authorization
  failures, request-validation errors, and domain errors never mark the cache
  down or trigger a refetch. `refreshCapabilities` derives a bounded 2s
  timeout from the caller's context and, on failure, leaves the previous set
  in place rather than clearing it.
- **`platform.CapabilityGate` / `platform.Gate` mechanism**
  (`internal/platform/capability.go`). A `Module` optionally implements
  `CapabilityGate` (`RequiredCapabilities() []capability.ID`) to declare that
  its whole surface — nav entry, dashboard cards, routes — needs those
  capabilities present (`Set.HasAll` semantics). `platform.Available(module,
  caps)` applies that test to a whole module (default-available when the
  module doesn't implement the interface); `internal/web/server.go`'s
  `moduleAvailable`/`availableManifests` filter the shell's nav list, and the
  `dashboard` loop skips a gated-absent module entirely (no `Dashboard()`
  call, no card, no error placeholder). `platform.Gate(host, ids, next)`
  wraps an individual `Mount`-registered handler and 404s when
  `host.Capabilities(r.Context())` doesn't `HasAll(ids...)`. `Gate` reads the
  set itself per request; `Available` takes an already-fetched set, which
  `web.Server` obtains from the same source: `Capabilities(context.Context)
  capability.Set`, added to the `platform.Host` interface in #54 and satisfied
  by `web.Server` from the cache above. Because it takes a `context.Context`
  rather than an `*http.Request`, it is callable from both HTTP handlers and
  `Module.Dashboard(ctx, host)`. `internal/platform` also has an any-of
  sibling set — `CapabilityGateAny` (`RequiredAnyCapabilities()
  []capability.ID`), `GateAny`, and `AvailableAny`, using `Set.HasAny`
  semantics instead of `HasAll` — and `moduleAvailable` composes both:
  `platform.Available(module, caps) && platform.AvailableAny(module,
  caps)`. No production module implements `CapabilityGateAny` yet; see the
  mechanism-only bullet above for detail. Modules implementing
  `CapabilityGate` (whole-module gate):
  - `services` → `Systemd` (plus a `Systemd AND Journald` `Gate` on just
    `GET /services/{unit}/logs`)
  - `logs` → `Systemd AND Journald`
  - `backups` → `Systemd`
  - `maintenance` → `Systemd`
  - `podman` → `Podman`
  - `docker` → `Docker`
  - `incus` → `Incus`

  Modules with partial or no gating: `storage` deliberately does *not*
  implement `CapabilityGate` — its inventory (nav, dashboard card,
  `GET /storage`) is always available, and only its three remote-mount routes
  (`GET /storage/mounts/new`, `POST /storage/mounts`, and
  `POST /storage/mounts/{id}/{action}`, which covers mount, unmount, and
  delete) are wrapped in `platform.Gate(host, {Systemd}, ...)`, with the "Add
  remote mount" link and the entire per-mount actions block collapsed behind
  the same `Systemd` flag in `views.templ`. `system`, `files`, `activity`, and
  `fleet` declare no capability requirement and are always available.
- **Attention's per-provider capability skip**
  (`internal/modules/attention/module.go`). The attention aggregator holds
  `[]platform.HealthProvider` and calls `Health` directly — outside any
  `Mount` route or the nav/dashboard filter — so its `findings` type-asserts
  each provider to *both* whole-module gate interfaces and skips it outright
  when either is unsatisfied: `platform.CapabilityGate` when
  `host.Capabilities(ctx)` doesn't `HasAll` its `RequiredCapabilities`, and
  `platform.CapabilityGateAny` when it doesn't `HasAny` its
  `RequiredAnyCapabilities`. Skipping means no `Health` call and no
  "unavailable" finding, since an absent module is not a failed one. The two
  checks are an AND of two defaults — a provider implementing neither
  interface is always collected — mirroring `moduleAvailable`'s composition,
  so this call path stays gate-complete for a module of any of the three
  shapes. (`platform.Available`/`AvailableAny` take a `platform.Module`,
  which a `HealthProvider` need not be, so `findings` applies the same two
  tests to the provider value rather than calling them directly.) On a
  no-systemd host, `services`/`maintenance`/`backups` contribute nothing to
  `/attention` rather than a degraded placeholder.
- **Routes stay mounted; absence 404s at request time.** No route is ever
  conditionally registered at startup based on capability — every module's
  `Mount` runs unconditionally and mounts all its routes on the shared mux.
  Absence is enforced at request time by `platform.Gate` (per route) and
  reflected in nav/dashboard by `moduleAvailable` (per render), so a gated-off
  surface is indistinguishable from a route that does not exist, both in the
  UI and at the URL.
- **sysext is out of scope for #54.** The sysext web surface is unchanged:
  `cmd/pilothouse`'s `newRegistry` still constructs `sysext.NewSystemManager`
  directly from the web process's own `--updex` config, and `sysext.Module`
  implements neither `CapabilityGate` nor any route-level `platform.Gate`.
  Web-side capability-gating of sysext reads is deferred to #52, when those
  reads move behind the broker. The daemon-side per-action
  `registerSysextActions` guard described earlier is #50's phase-1a work in
  `cmd/pilothoused`, not a web-side gate.

### templ + HTMX, server-rendered, progressive enhancement

- `internal/web/shell.templ` provides the base `Layout`, sidebar navigation
  (built from registered module `Manifest`s), flash messages, and shared
  components (icons, confirmation UI, dashboard card composition).
- Each module has its own `views.templ`; a handler builds a
  `platform.Page{Active, Body, Eyebrow, Title}` and calls `host.Render`,
  which wraps the module body in the shared `Layout`.
- HTMX is used for auto-refresh (dashboard every 15s targeting `#dashboard`,
  storage snapshot every 30s targeting `#storage-snapshot`, container/journal
  log views every 5s) and, for most module mutation handlers, for redirect
  handling: handlers return `HX-Redirect` for HTMX requests and a plain `303`
  for normal form posts. Two handlers intentionally skip that branch and
  always return a plain `303` regardless of request type — `POST
  /maintenance/reboot` (`internal/modules/maintenance/module.go`) and `POST
  /logout` (`internal/web/server.go`) — since both end the current session or
  system state, so a full-page redirect is correct either way. Mutating forms
  are otherwise plain POSTs (often with `hx-boost="false"`) — **pages must
  remain usable without JavaScript.**
- Run `make generate` (or `make docker-generate`) after editing any
  `*.templ` file. Never hand-edit the generated `*_templ.go` files.
- **Composition rule:** put component calls like `@web.Icon("chevron")` on
  their own template node, never inline inside a text node (`View all
  @web.Icon("chevron")` renders the call literally as text). Every new/
  changed templ invocation needs a rendering test asserting the component's
  actual output is present and that no literal `@web.` call syntax leaked
  into the HTML (grep existing `*_test.go` next to a `.templ` file for the
  pattern).
- **Storage snapshot anchors.** Storage allocates fragment IDs once per
  snapshot and puts them on visible inventory, mount, or Attention rows.
  Topology links consume the same resource-to-fragment map. Do not restore
  empty anchor spans as direct children of `.storage-snapshot`: it is a CSS
  grid, so each span creates an empty grid row and accumulates visible gaps.

### Module contract (`internal/platform/module.go`)

```go
type Module interface {
    Dashboard(context.Context, Host) ([]DashboardCard, error)
    Manifest() Manifest
    Mount(*http.ServeMux, Host)
}
```

`Manifest` drives sidebar nav; `Dashboard` contributes templ components to
the overview page; `Mount` registers `net/http` 1.22-style method+path
routes and receives a `Host` for rendering and broker calls. Modules are
constructed and registered into `platform.Registry` once in
`cmd/pilothouse/main.go`.

A module may optionally implement `platform.HealthProvider`
(`Health(context.Context, Host) ([]Finding, error)` plus `Manifest`) to
contribute findings to the `attention` module's aggregated view. Health-
producing modules must also be added to the `attention.New(...)` provider
list in `cmd/pilothouse/main.go`, not just registered in
`platform.Registry`. Current health providers: `system`, `services`,
`maintenance`, `backups`, `storage`.

### Testing

- Unit tests live beside source (`*_test.go`): domain managers use fake
  systemd/container/Incus/journal clients; HTTP handlers use fake
  `platform.Host` implementations; broker tests cover sessions, actions,
  stream limits, and serialization (`internal/broker/*_test.go`).
- templ rendering tests render a component directly into a
  `strings.Builder` and assert on the output HTML (see
  `internal/modules/services/views_test.go`, `internal/web/shell_test.go`).
- Optional live integration tests are gated behind env vars:
  `PILOTHOUSE_LIVE_PODMAN`, `PILOTHOUSE_LIVE_DOCKER`, `PILOTHOUSE_LIVE_INCUS`,
  `JOURNAL_SMOKE`.

## Configuration

No config-file parser; configuration is command-line flags plus a couple of
environment variables, typically supplied via systemd `EnvironmentFile`.

**`pilothouse` (web) flags** — `cmd/pilothouse/main.go`:
- `--listen` (default `127.0.0.1:8888`), `--broker-socket`
  (default `/run/pilothouse/broker.sock`)
- `--definitions-root`, `--updex` (sysext support)
- repeatable `--allowed-origin`; also augmented by `PILOTHOUSE_ALLOWED_ORIGINS`
- `--secure-cookie` (set behind a TLS-terminating proxy)

**`pilothoused` (broker) flags** — `cmd/pilothoused/main.go`:
- `--admin-group` (default `sudo`), `--login-group` (optional, restricts login)
- `--pam-service` (default `pilothouse`)
- `--socket` (default `/run/pilothouse/broker.sock`), `--socket-group`
  (default `pilothouse`)
- `--audit-db`, `--jobs-db` bbolt DB paths (default under `/var/lib/pilothouse`)
- backup timer name(s) and `--backup-max-age` (default `48h`); also augmented
  by `PILOTHOUSE_BACKUP_TIMERS`
- sysext definitions root and `updex` executable path
- `--podman-socket` (default `/run/podman/podman.sock`)
- repeatable `--files-root id=/absolute/path` (read-only) and
  `--files-write-root id=/absolute/path` (writable) — validated: absolute,
  non-root, unique IDs, no symlink roots (`internal/modules/files/config.go`)

**Environment files** (systemd `EnvironmentFile=-`, optional):
`/etc/pilothouse/pilothouse.env`, `/etc/pilothouse/pilothoused.env`.

**Native build dependencies:** PAM (`libpam0g-dev`) and systemd
(`libsystemd-dev`) headers; `pilothoused` is built with `-tags sdjournal`. If
unavailable locally, use `make docker-build` / `make docker-test` /
`make docker-fmt` / `make docker-lint` / `make docker-generate`, which build
and reuse the repo's dev container image.

## Release workflow

`make bump` (backed by `scripts/bump.sh`) cuts a release: it requires a
clean `main` checkout exactly matching `origin/main` (rejects dirty, ahead,
behind, divergent, feature-branch, and detached-HEAD states), runs
verification and semantic-version calculation (`svu`) inside the dev
container, then uses authenticated *host* Git (not the container) to create
and push an annotated tag. Never run the full `bump` target inside an ad
hoc container or pass Git credentials into the image — see
`docs/superpowers/specs/2026-07-21-bump-workflow-design.md` and
`docs/superpowers/plans/2026-07-21-bump-workflow.md` for the design
rationale.

## Agent workflow tooling

- `.mill.toml` configures the [frostyard/mill](https://github.com/frostyard/mill)
  spec→PR harness for this repo: `[gates].chunk` (`make generate`, `gofmt`,
  `go vet`, `go test`) runs after every chunk, `[gates].deep` (`make
  docker-ci`) runs before the ship decision, and `[context].docs` lists
  `AGENTS.md`, `yeti/OVERVIEW.md`, and `docs/modules.md` as required reading
  for every mill agent. The mill engine itself lives in the separate
  `frostyard/mill` repo; this repo carries only config, learned skills, and
  cross-agent surface links (`CLAUDE.md`, `GEMINI.md`,
  `.github/copilot-instructions.md`, all pointing back to `AGENTS.md`).
- `docs/agents/skills/` holds durable lessons harvested from previous mill
  runs (e.g. `templ-generated-files.md` on gitignored `*_templ.go` output).
  `AGENTS.md` requires reading every file there before planning,
  implementing, or reviewing changes — treat them as binding guidance.
- `workflows/` holds standalone [Conductor](https://github.com/microsoft/conductor)
  multi-agent workflow definitions unrelated to the mill: `test-triage.yaml`
  (gate chain, only escalates to an LLM on failure), `code-review.yaml`
  (parallel security/correctness reviewers plus a synthesizer), and
  `module-audit.yaml` (fans out one audit agent per `internal/modules/*`
  directory). See `workflows/README.md` for setup and schema gotchas.

## Further Reading

- `docs/authentication.md` — login flow, session/CSRF model, authorization,
  audit trail, PAM policy, deployment rules.
- `docs/modules.md` — module contract, recommended file layout, and the
  concrete rules for adding actions/queries (fixed IDs only, validation,
  timeouts, no shell invocation, HTMX redirect conventions, capability-guarded
  registration).
- `docs/capabilities.md` — binding table mapping every broker `Query*`/
  `Action*` ID to its required capability (or capabilities), plus documented
  exceptions to the module-level defaults.
