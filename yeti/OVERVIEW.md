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
  `euid == 0`. Opens root-owned bbolt databases for audit and jobs, builds
  `broker.QueryRegistry` / `broker.ActionRegistry` / stream registries, and
  registers every privileged implementation (services, Podman, Docker, Incus,
  sysext, files, logs, backups, storage/remote-mounts, maintenance). Serves HTTP
  only over a Unix socket with `0660 root:<socket-group>` permissions — never a
  TCP listener.

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
- **Streams for large/blocking data.** File upload/download use fixed
  `stream-actions`/`stream-queries` registrations with explicit size caps
  (256 MiB) rather than the generic action/query path.
- **Storage executable validation.** Core and optional storage commands use
  fixed absolute candidates. Optional candidates may be symlinks for distro
  multicall tools such as LVM, but the broker validates the fully resolved
  target as a root-owned, non-group/world-writable regular file while executing
  the original entry-point path. Broken or unsafe present candidates fail
  startup; absent optional tools degrade only their backend to unsupported.
- **Capability probing and advertisement.** `pilothoused` probes host
  capabilities once at startup (`internal/capability.Probe`, called early
  in `cmd/pilothoused/main.go`'s `run()`, before any module manager is
  constructed) — systemd, journald, `updex`, `systemd-sysext`, bootc,
  rpm-ostree, the `rpm-ostreed-automatic`/`bootc-fetch-apply-updates`
  automatic-update unit-file pairs, and the Podman/Docker/Incus engine
  sockets — and probing itself never fails fatally: every probe narrows to
  "absent" on any error instead of erroring. As of this chunk that
  guarantee is fully wired through to daemon startup for the engine
  capabilities (Podman/Docker/Incus) and for systemd: a host with any of
  these absent or unreachable still starts the daemon.
- **Capability-gated, non-fatal construction of systemd-backed managers.**
  Storage's remote-mount unit controller and the backups/services/logs
  managers all need a live system D-Bus connection, and their exported
  constructors (`backups.NewSystemManager`, `services.NewSystemManager`,
  `logs.NewSystemManager`) no longer open that connection themselves — each
  accepts a pre-opened client (an unexported `systemdClient` interface per
  package, structurally satisfied by `*dbus.Conn`) from its caller.
  `cmd/pilothoused/main.go`'s `connectSystemd(ctx, caps, connect, logger)`
  opens that connection at most once, only when the probed `Systemd`
  capability is present; a connection failure is logged as a warning and
  degrades to a nil client exactly like an absent capability — never a
  fatal `run()` error. `buildSystemdManagers` constructs the remote-mount
  controller and the backups/services/logs managers only when that client
  is non-nil, leaving each nil otherwise; `registerStorageActions`/
  `registerBackups`/`registerServices`/`registerLogs` no-op on a nil
  manager as a stopgap (the full `capability.Set`-based registration guard
  mirroring `registerPodman`/`registerDocker`/`registerIncus` lands in a
  later chunk). `QueryStorageState` is registered separately against the
  plain, non-systemd `storageManager` built earlier in `run()`, so storage
  inventory reads never depend on systemd at all — not even via a
  registration-level guard, unlike the remote-mount actions. Independent of
  systemd's presence, `backups.ValidateConfiguration` (timer name pattern,
  positive max age) still runs unconditionally in `run()` before any of
  this and fails startup fatally on genuine flag misconfiguration; only the
  D-Bus reachability failure mode is non-fatal. Later modules that
  construct a privileged manager from an optional host resource should
  follow this same shape: accept a pre-opened/pre-resolved dependency from
  the caller, gate opening that dependency on the relevant probed
  capability, and never let its absence fail `run()`. Maintenance and
  sysext manager construction (which use `sysext.ExecRunner`, not systemd
  D-Bus) are unaffected by this chunk and remain unconditionally
  registered until their own conversion. The probed `capability.Set` is
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
  a warning, never a fatal `run()` error). Storage/backups/services/logs
  registration is nil-guarded (this chunk) but not yet `capability.Set`-
  guarded; maintenance and sysext stay unconditionally registered until
  their own conversion.
- **Storage SMB ownership mapping.** The fixed administrator-only
  `org.frostyard.pilothouse.storage.create-smb-guest-owned` and
  `org.frostyard.pilothouse.storage.create-smb-credentials-owned` actions
  require paired canonical numeric `uid` and `gid` values. The privileged
  manager validates them independently, persists mapped definitions as manifest
  version 2, and deterministically renders manager-controlled CIFS `uid=` and
  `gid=` options. Version 1 definitions remain supported without migration.
  The web process cannot resolve names or provide free-form mount options, and
  no generic command, filesystem, or socket capability is introduced.

See `docs/authentication.md` for the full login/session/authorization/audit
model and deployment rules (cookie flags, allowed origins, PAM policy).

### templ + HTMX, server-rendered, progressive enhancement

- `internal/web/shell.templ` provides the base `Layout`, sidebar navigation
  (built from registered module `Manifest`s), flash messages, and shared
  components (icons, confirmation UI, dashboard card composition).
- Each module has its own `views.templ`; a handler builds a
  `platform.Page{Active, Body, Eyebrow, Title}` and calls `host.Render`,
  which wraps the module body in the shared `Layout`.
- HTMX is used for auto-refresh (dashboard every 15s targeting `#dashboard`,
  log views every 5s) and for redirect handling: handlers return
  `HX-Redirect` for HTMX requests and a plain `303` for normal form posts.
  Mutating forms are otherwise plain POSTs (often with `hx-boost="false"`) —
  **pages must remain usable without JavaScript.**
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

## Further Reading

- `docs/authentication.md` — login flow, session/CSRF model, authorization,
  audit trail, PAM policy, deployment rules.
- `docs/modules.md` — module contract, recommended file layout, and the
  concrete rules for adding actions/queries (fixed IDs only, validation,
  timeouts, no shell invocation, HTMX redirect conventions).
