# Pilothouse Overview

## Purpose

Pilothouse (`github.com/frostyard/pilothouse`) is a local web administration
console for image-based Linux systems. It presents
a live dashboard and management UI (system telemetry, sysext/`updex`
lifecycle, systemd services, Podman/Docker/Incus workloads, read-only k3s
health, journal search,
backups, storage/disk health and managed NFS/SMB mounts, file browsing,
maintenance/reboot) over HTMX-enhanced server-rendered HTML, while keeping
all privileged system access behind a single, fixed, root-only broker.

The defining architectural rule: an unprivileged web process (`pilothouse`)
never talks to root-equivalent APIs (systemd D-Bus, journald, Podman/Docker/
Incus sockets, the Kubernetes API, filesystem roots) directly. It only calls a small, fixed set
of broker queries/actions implemented by a root-only daemon
(`pilothoused`), connected over a protected Unix socket.

## Architecture

```
cmd/pilothouse/       unprivileged web binary (main.go) — TCP listener, no root
cmd/pilothoused/      privileged broker binary (main.go) — Unix socket only, requires euid==0
cmd/verify-packages/  repository tool (main.go) — reports packaging.Verify's findings for
                      built .deb/.rpm artifacts. NOT a shipped binary: absent from
                      `make build` and from .goreleaser.yaml's builds, so bin/ holds only
                      the two above. Unreachable from either of them; performs no
                      privileged operation (see docs/design/artifact-extraction.md)

internal/
  modules/<name>/     vertical feature slices (UI + domain logic), one per management area
  platform/           Module contract (platform.Module), Host interface, module Registry
  web/                HTTP server, session/auth middleware, shell.templ layout, embedded static assets
  broker/             fixed query/action/stream protocol, registries, HTTP-over-Unix-socket server+client
  audit/               durable action-history store (bbolt)
  jobs/                durable background-job store (bbolt), for long-running privileged mutations
  auth/, auth/pam/     NSS group resolution and PAM authentication (used only by pilothoused)
  packagingtest/       test-support helpers imported only by test files: the packaging-tool
                       skip-vs-fail gate and the fixture builders — BuildDeb and BuildRPM from
                       one shared, declarative Spec, BuildDebRaw from an already-staged tree
                       (see docs/design/packaging-test-fixtures.md). Ships in no
                       binary and imports no other repository package

docs/                 all documentation, in frostyard/core's four-category shape
                      (adr/, design/, specs/, plans/ + indexed README.md); this file is
                      docs/design/overview.md (formerly yeti/OVERVIEW.md), the living
                      architecture entry point. Authoritative subsystem docs:
  authentication.md    login, session, authorization, audit, PAM policy, deployment rules
  modules.md           how to add a new module: contract, file layout, action/query rules
  capabilities.md      binding table mapping every broker ID to its required host capability
  design/, specs/      per-subsystem design docs and exact contracts — see "Further
                       Reading" below and the index in docs/README.md

packaging/            systemd units, PAM policy, sysusers declaration, the two
                      commented-out environment files, the shared postinstall.sh
                      scriptlet, the per-distro-family deb/ and rpm/ variants, the
                      artifact-contract Go package (model.go, finding.go, contract.go,
                      verify.go) with its configuration-level tests, and the
                      verify-install.sh install-validation script. The exact contract
                      and each file's role: docs/specs/artifact-contract.md and
                      docs/design/install-validation.md
  extract/            subpackage (package extract) whose only job is to produce
                      a packaging.Model from a real artifact on disk (dpkg-deb and
                      rpm/rpm2archive backends); its command-line entry point is
                      cmd/verify-packages, run by `make verify-packages`, which stays
                      outside `ci`/`docker-ci` (see docs/design/artifact-extraction.md)

test/vm/              the booted-VM harness (Layer B, #67): vm-boot-test.sh entry
                      point, images.env pinning table, sourced lib/ host libraries and
                      guest/ scripts; run by .github/workflows/packaging.yml's vm-boot
                      job and guarded structurally by packaging/vm_harness_test.go and
                      packaging/workflow_vm_job_test.go (see docs/design/vm-harness.md)
test/image/           the image-tier (#80) validation of the released RPM on a
                      uCore/bootc host: releaserpm/ fixture command, compose-ucore.sh,
                      ucore-vm-test.sh, and the root-only lifecycle owner
                      ucore-image-test.sh invoked by .github/workflows/image-tier.yml
                      (see docs/design/image-tier.md)
.docker/              development container image (Go + PAM + systemd headers,
                      systemd, shellcheck, jq, rpm tooling) for docker-* make targets;
                      declares PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1 so tool-dependent
                      tests fail rather than skip inside it
                      (see docs/design/packaging-test-fixtures.md)
```

### Two binaries, one protocol

- **`pilothouse`** (`cmd/pilothouse/main.go`): binds a TCP listener (default
  `127.0.0.1:8888`, loopback HTTP), instantiates all modules, and wires them
  to a `broker.Client` that dials `/run/pilothouse/broker.sock`. Runs as an
  unprivileged user. A non-loopback bind fails closed to HTTPS: operator
  `--tls-cert`/`--tls-key` material wins, otherwise `internal/tlscert`
  prepares a persistent self-signed certificate (refusing to start if it
  cannot), and plaintext beyond loopback requires the explicit
  `--allow-insecure-http` acknowledgment; `cmd/pilothouse/listen.go` holds
  the flag→env→default resolution and the serve-mode decision. Some modules perform genuinely unprivileged local reads
  directly (e.g. `system` collects `/proc`, `/sys`, `/etc/os-release`
  telemetry) — this is allowed because it requires no elevated access.
- **`pilothoused`** (`cmd/pilothoused/main.go`): refuses to start unless
  `euid == 0`. Probes optional host capabilities (`internal/capability`) up
  front, then opens root-owned bbolt databases for audit and jobs, builds
  `broker.QueryRegistry` / `broker.ActionRegistry` / stream registries, and
  registers every privileged implementation (services, Podman, Docker, Incus,
  k3s, sysext, files, logs, backups, storage/remote-mounts, maintenance) — each
  registration guarded by the probed capability set so an absent optional
  dependency degrades only that registration instead of aborting startup.
  Serves HTTP only over a Unix socket with `0660 root:<socket-group>`
  permissions — never a TCP listener.

Both packaged broker units (`packaging/deb/pilothoused.service` and
`packaging/rpm/pilothoused.service`, which differ only in `--admin-group`)
declare no `Wants=` on any engine socket, so
installing and starting the broker never pulls in or activates
`incus.socket` or `podman.socket`; an operator enables those units
themselves (see the README's Podman note). The unit keeps
`After=incus.socket systemd-sysext.service podman.socket`, which only orders
the broker behind those units when something else has already started them.
That change removed only the unit-level pull-in; presence-based enablement
was removed separately, per engine, by the `--podman-socket`, `--docker`, and
`--incus` flags described in [capability-gating.md](capability-gating.md). As of
this commit none of the three container engines is enabled by socket presence:
an engine socket that happens to be running is not probed at all unless its
flag is set.

### Modules (`internal/modules/<name>`)

Each module is a vertical slice: collector/manager, `module.go` (routes +
manifest + dashboard cards), `views.templ`, and tests, all under one
directory. Two modules push their privileged, host-tool-invoking half into a
subpackage the web binary never links — `services/journal` and, as of #52,
`sysext/extctl` — so the unprivileged process cannot reach a host tool even
by accident. Current modules:

| Module | Purpose |
|---|---|
| `system` | Unprivileged host telemetry (CPU/mem/disk/load/net/os) from `/proc`, `/sys`, `/etc/os-release`; utilization helpers return zero for a zero total and saturate at 0–100% across wrapped or oversized counters, and the composed resource-summary rendering is regression-tested; emits health findings. |
| `storage` | Block/mount inventory (`lsblk`/`findmnt`) enriched with optional SMART/NVMe, MD RAID, LVM, device-mapper/LUKS, multipath, ZFS, and Btrfs backends; emits health findings; admins can create/mount/unmount/delete Pilothouse-managed NFS and SMB (guest or credentialed) automounts. SMB creation optionally supports paired numeric local UID/GID mapping. Expected immutable EROFS mounts retain their inventory usage and read-only state but are excluded from capacity and read-only health findings; other filesystems retain those checks. |
| `attention` | Aggregates `platform.HealthProvider` findings from other modules (bounded 2s/provider) into one "needs attention" view. |
| `services` | Systemd service/socket/timer inventory and lifecycle/enablement control via system D-Bus; bounded journal diagnostics. |
| `sysext` | `updex` definition/install state and `systemd-sysext` merge state, read entirely through the broker's `QueryExtensionsState` aggregate (no local `updex`/`systemd-sysext` invocation in the web process — the exec-backed implementation lives in the `sysext/extctl` subpackage that only `cmd/pilothoused` links); surfaces per-extension and aggregate component update availability (the responsibility that moved off Maintenance in #52); install/remove/update/refresh actions. Whole-module `CapabilityGateAny` on `updex OR sysext`, with narrower per-route/per-action guards. |
| `podman` | System (rootful) Podman inventory (containers/pods/images) via Libpod API; bounded logs; lifecycle actions. |
| `docker` | System Docker daemon inventory, bounded logs, lifecycle/image removal. |
| `incus` | Local-only Incus inventory (projects/instances/images/pools/volumes/buckets) via `/var/lib/incus/unix.socket`, with per-instance live state (globally-scoped addresses, memory, CPU time, processes, start time, snapshot count); a per-instance detail page carrying allowlisted configuration, devices, interfaces and snapshots; bounded console and supervisor logs; non-stateful snapshot create/restore/delete; read-only network and profile inventory with per-network DHCP leases and allowlisted configuration; instance creation from a fixed public image server as a background job; lifecycle actions including a distinct force stop. Storage, network and profile reads each degrade independently rather than failing the page. |
| `k3s` | Opt-in, read-only cluster visibility through fixed `k3s kubectl` reads against `/etc/rancher/k3s/k3s.yaml`: node readiness/version/runtime and aggregate pod-health totals per namespace. Unknown future pod phases count as not ready rather than failing the complete view. No individual pod identity, logs, configuration, secret, Kubernetes API proxy, or mutation crosses the broker boundary. |
| `logs` | Admin-only bounded system-journal search (message/priority/unit/time-window filters, ≤200 entries). |
| `files` | Admin-only browsing/download/atomic upload within explicitly configured filesystem roots (256 MiB bound). |
| `backups` | Monitors explicitly configured systemd backup timers: enabled/active state, last result, freshness, next run. |
| `maintenance` | Read-only host-image status (booted/staged/rollback deployments with bootc's image references and digests, supplemented by rpm-ostree version/checksum detail, plus soft-reboot eligibility when bootc reports it), maintenance-job state, reboot posture (including the merged-but-disabled-extension reason it derives from the shared `sysext.ExtensionsSource` aggregate), confirmed reboot. No host-image mutation. Read-only automatic-update (updater policy/timer) status is reported daemon-side through `QueryAutoUpdateStatus`, consumed web-side by the module's own `queryAutoUpdate`, and rendered by the Maintenance page's "Automatic updates" section — one independent subsection per updater (bootc, rpm-ostree) carrying its timer active/unit-file state, next trigger, service active state and last result, normalized policy, and both drop-in-presence booleans, or an explicit "not configured" statement when that updater has no payload. The section is fetched and shown under the same `HasAny(bootc, rpm-ostree)` gate as the host-image section, so it is absent entirely on a host advertising neither. No automatic-update mutation: the section carries no control and the broker's ID vocabulary has no matching action. |
| `activity` | Admin-only view over durable audit history (`QueryActivity`) and background jobs (`QueryJobs`). |
| `fleet` | Static UI preview only — no real multi-system transport/enrollment exists yet. Because of that it is **not registered in production**: `cmd/pilothouse`'s `newRegistry(dev bool)` appends `fleet.New()` only under the `--dev` flag (default `false`, and `packaging/pilothouse.service` does not pass it). Without `--dev` the module is never constructed, so `Mount` never runs and `/fleet`, `/fleet/enroll`, and `/fleet/systems/{id}` are unregistered routes (a mux 404, not a capability 404), with no nav entry and no sidebar system-picker link. |

See `docs/modules.md` for the module contract, recommended file layout, and
rules for adding a new module (routes, actions, queries).

The host-image status surface (#51) and its automatic-update companion
(#58/#60) — the bootc/rpm-ostree parsers, `HostImageManager`, the
`QueryHostImageStatus`/`QueryAutoUpdateStatus` queries, and how
`maintenance.SystemManager` consumes them — are in [host-image.md](host-image.md).

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
- **Bounded login backoff remains effective at capacity.** Failed authentication
  keys receive exponential per-user/per-address backoff in
  `internal/broker/server.go`. The tracker holds at most 4,096 keys, reclaims
  entries idle for ten minutes, and then evicts the least recently failed key
  when necessary. The failure that triggered saturation is always inserted;
  unseen successful logins are never globally locked out merely because the
  tracker is full.
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
- **Capability probing and gating.** `pilothoused` probes optional host
  capabilities once at startup (`internal/capability.Probe`) and registers a
  privileged handler only when its capability is present; optional tooling
  (`updex`, Podman, Docker, Incus, k3s) is additionally off unless its flag
  is set (#64). The web process fetches the advertised `capability.Set` after
  login, filters nav and dashboard through
  `platform.Available`/`AvailableAny`, and gates routes per request with
  `platform.Gate`/`GateAny` — routes stay mounted, absence 404s (#54). The
  whole mechanism, each module's gates, and the opt-in rule are described in
  [capability-gating.md](capability-gating.md); the broker-ID-to-capability
  binding table is `docs/capabilities.md`.
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

### Incus depth: allowlisted detail, snapshots, networks, creation

The Incus module's four depth phases — instance detail behind a fixed
config/device allowlist with bounded console/supervisor logs, non-stateful
snapshots plus a distinct force stop, read-only networks and profiles, and
instance creation from a fixed public image server as the module's one
background action — are described in [incus.md](incus.md).

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
- Process-level web E2E tests live in `test/e2e/`. They build and start the
  real `pilothouse` binary on an ephemeral local port, then exercise its
  public and authentication-gated HTTP surfaces without requiring a broker.
  `web_test.go` covers the loopback HTTP default; `tls_test.go` covers
  operator-supplied certificates on loopback, the self-signed HTTPS path on
  a wildcard bind (with `STATE_DIRECTORY` pointed at a temp dir), and the
  guardrail's refusal branch (nonzero exit plus the remedy message when no
  certificate can be prepared).
- Optional live integration tests are gated behind env vars:
  `PILOTHOUSE_LIVE_PODMAN`, `PILOTHOUSE_LIVE_DOCKER`, `PILOTHOUSE_LIVE_INCUS`,
  `JOURNAL_SMOKE`.

**Native build dependencies:** PAM (`libpam0g-dev`) and systemd
(`libsystemd-dev`) headers; `pilothoused` is built with `-tags sdjournal`. If
unavailable locally, use `make docker-build` / `make docker-test` /
`make docker-fmt` / `make docker-lint` / `make docker-generate`, which build
and reuse the repo's dev container image.

### Packaging and validation

Packaging correctness is asserted in layers, each with its own doc:

- [../specs/artifact-contract.md](../specs/artifact-contract.md) — the exact
  artifact contract: model and finding vocabulary, required destinations and
  modes, dependency lists, forbidden roots, `packaging.Verify`'s checks, the
  drift guards, and the packaging configuration they are transcribed from.
- [packaging-test-fixtures.md](packaging-test-fixtures.md) — the
  `internal/packagingtest` tool gate and declarative `.deb`/`.rpm` fixture
  builders, and the dev image's `PILOTHOUSE_REQUIRE_PACKAGING_TOOLS` rule.
- [artifact-extraction.md](artifact-extraction.md) — the `packaging/extract`
  backends that turn a real artifact into a `packaging.Model`, the
  `cmd/verify-packages` command, `make verify-packages`/`make package`, and
  the CI packaging gate (`.github/workflows/packaging.yml`).
- [install-validation.md](install-validation.md) — Layer A:
  `packaging/verify-install.sh` container installs, the removal matrix, and
  the `install` CI job.
- [vm-harness.md](vm-harness.md) — Layer B: the `test/vm` booted-VM harness
  (activation, PAM, journal read-back, reboot posture) and the `vm-boot` CI
  job.
- [image-tier.md](image-tier.md) — the #80 image-tier validation on a
  uCore/bootc host and `.github/workflows/image-tier.yml`.

## Configuration

No config-file parser; configuration is command-line flags plus a couple of
environment variables, typically supplied via systemd `EnvironmentFile`.

**`pilothouse` (web) flags** — `cmd/pilothouse/main.go`:
- `--listen` (default `127.0.0.1:8888`; env fallback `PILOTHOUSE_LISTEN`),
  `--broker-socket` (default `/run/pilothouse/broker.sock`)
- `--tls-cert` / `--tls-key` (must be set together; env fallbacks
  `PILOTHOUSE_TLS_CERT`/`PILOTHOUSE_TLS_KEY`) — enable HTTPS on any address
- `--allow-insecure-http` (env fallback `PILOTHOUSE_ALLOW_INSECURE_HTTP`) —
  explicit acknowledgment for plaintext HTTP on a non-loopback address
- for the four options above, resolution is explicit flag → env → default
  (`flag.Visit`-based, so a flag passed with its default value still beats
  the env var); `packaging/pilothouse.service` passes no `--listen` flag so
  the env file can set it. The self-signed certificate for a non-loopback
  bind without TLS material persists in `$STATE_DIRECTORY` (the packaged
  unit declares `StateDirectory=pilothouse/web`, nested under the broker's
  root-owned `/var/lib/pilothouse` — ordering is guaranteed by
  `Requires=/After=pilothoused.service`) or `~/.local/state/pilothouse`
  outside systemd; `internal/tlscert` regenerates it at startup when it is
  unparseable, expired, within its 30-day renewal window, or missing the
  listen host from its SANs
- repeatable `--allowed-origin`; also augmented by `PILOTHOUSE_ALLOWED_ORIGINS`
- `--secure-cookie` (set behind a TLS-terminating proxy; when the process
  itself terminates TLS the secure-cookie behavior engages automatically)
- `--dev` (default `false`) — registers in-development preview modules not
  backed by real functionality; today that is exactly one module, `fleet`.
  It is a bare bool with no companion environment variable, matching
  `--secure-cookie` rather than the repeatable env-augmented flags, and
  `packaging/pilothouse.service`'s `ExecStart` does not pass it

**`pilothoused` (broker) flags** — `cmd/pilothoused/main.go`:
- `--admin-group` (flag default `sudo`; the packaged units override it per
  distro family — `packaging/deb/pilothoused.service` passes `sudo`,
  `packaging/rpm/pilothoused.service` passes `wheel` — the Go default itself is
  unchanged), `--login-group` (optional, restricts login)
- `--pam-service` (default `pilothouse`)
- `--socket` (default `/run/pilothouse/broker.sock`), `--socket-group`
  (default `pilothouse`)
- `--audit-db`, `--jobs-db` bbolt DB paths (default under `/var/lib/pilothouse`)
- backup timer name(s) and `--backup-max-age` (default `48h`); also augmented
  by `PILOTHOUSE_BACKUP_TIMERS`
- sysext definitions root; `--updex` executable path (default empty — updex
  requires explicit configuration to enable)
- `--podman-socket` (default empty — Podman requires explicit configuration
  to enable)
- `--docker` endpoint, e.g. `unix:///var/run/docker.sock` (default empty —
  Docker requires explicit configuration to enable; unset means no docker
  client is constructed at all, in the probe or in `run()`)
- `--incus` bool (default `false` — Incus requires this explicit opt-in to
  enable; unset means `ProbeIncus` returns an empty set without contacting
  the socket, so `registerIncus` registers nothing). The socket path itself
  is not configurable: it stays fixed at `/var/lib/incus/unix.socket`, and
  the flag gates only whether that path is probed. `incus.NewLocalClient()`
  in `run()` is still constructed unconditionally — it performs no I/O —
  since the capability guard is what withholds registration
- `--k3s` executable path (default empty — k3s visibility requires explicit
  configuration; unset means no command runs). The probe and manager both
  use `/etc/rancher/k3s/k3s.yaml` directly and accept no kubeconfig path
- repeatable `--files-root id=/absolute/path` (read-only) and
  `--files-write-root id=/absolute/path` (writable) — validated: absolute,
  non-root, unique IDs, no symlink roots (`internal/modules/files/config.go`)

**Environment files** (systemd `EnvironmentFile=-`, optional):
`/etc/pilothouse/pilothouse.env`, `/etc/pilothouse/pilothoused.env`. Both are
shipped by the `.deb`/`.rpm` packages (sources: `packaging/pilothouse.env`,
`packaging/pilothoused.env`) as nfpm `type: config` entries, mode `0640`
`root:pilothouse`. They are not inert placeholders: each documents, commented
out, the one real environment variable its binary reads —
`PILOTHOUSE_ALLOWED_ORIGINS` (`cmd/pilothouse/main.go`, merged into
`--allowed-origin` after `flag.Parse`) and `PILOTHOUSE_BACKUP_TIMERS`
(`cmd/pilothoused/main.go`, merged into `--backup-timer`), each a
comma-separated list. Every line ships commented out, so installing the
package changes no runtime behavior. Neither file carries `--admin-group`:
that stays a per-format unit-file argument.

Package-owned configuration directories, runtime dependency lists, the
postinstall scriptlet, and the configuration-assertion test
(`packaging/goreleaser_config_test.go`) are covered under "Packaging
configuration" in
[../specs/artifact-contract.md](../specs/artifact-contract.md).

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

The mill configuration (`.mill.toml`), cross-agent instruction surfaces,
risk-tier classification, the knowledge index, harvested skills, and the
Copilot/Claude automation workflows are described in
[agent-workflows.md](agent-workflows.md).

## Further Reading

Subsystem design docs (in this directory):

- [host-image.md](host-image.md) — host-image and automatic-update reporting
  (#51/#58/#60): parsers, manager, queries, degrade rules.
- [capability-gating.md](capability-gating.md) — capability probing,
  guarded registration, web-side fetch/cache and gating, per-module
  adoption, and the optional-tooling opt-in rule (#50/#54/#64).
- [incus.md](incus.md) — the Incus module's depth phases.
- [packaging-test-fixtures.md](packaging-test-fixtures.md),
  [artifact-extraction.md](artifact-extraction.md),
  [install-validation.md](install-validation.md),
  [vm-harness.md](vm-harness.md), [image-tier.md](image-tier.md) — the
  packaging and validation chain (see "Packaging and validation" above).
- [agent-workflows.md](agent-workflows.md) — agent/automation tooling.
- [../specs/artifact-contract.md](../specs/artifact-contract.md) (spec) —
  the exact packaging artifact contract.

Other authoritative docs:

- `docs/authentication.md` — login flow, session/CSRF model, authorization,
  audit trail, PAM policy, deployment rules.
- `docs/modules.md` — module contract, recommended file layout, and the
  concrete rules for adding actions/queries (fixed IDs only, validation,
  timeouts, no shell invocation, HTMX redirect conventions, capability-guarded
  registration).
- `docs/capabilities.md` — binding table mapping every broker `Query*`/
  `Action*` ID to its required capability (or capabilities), plus documented
  exceptions to the module-level defaults.
- `docs/autoupdate.md` — the automatic-update reporting surface end to end:
  response schema, policy normalizers, the daemon-side `AutoUpdateManager`,
  and the Maintenance page's read-only section, which exposes no mutation.
- `docs/branding.md` — the neutral-branding rules and the allowlist of sites
  naming sweeps must leave unchanged (test fixtures, `docs/capabilities.md`
  fixture prose, the `release.yml` dispatch, mock Fleet data, and the
  historical phase narrative formerly in `yeti/OVERVIEW.md`, now spread
  across this directory's subsystem docs).
