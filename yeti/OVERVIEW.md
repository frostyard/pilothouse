# Pilothouse Overview

## Purpose

Pilothouse (`github.com/frostyard/pilothouse`) is a local web administration
console for image-based Linux systems. It presents
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
cmd/verify-packages/  repository tool (main.go) — reports packaging.Verify's findings for
                      built .deb/.rpm artifacts. NOT a shipped binary: absent from
                      `make build` and from .goreleaser.yaml's builds, so bin/ holds only
                      the two above. Unreachable from either of them; performs no
                      privileged operation (see "Artifact extraction" below)

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
                       (see "Packaging test fixtures" below). Ships in no binary and imports no
                       other repository package

docs/                 authoritative subsystem docs (kept here, not duplicated into yeti/):
  authentication.md    login, session, authorization, audit, PAM policy, deployment rules
  modules.md           how to add a new module: contract, file layout, action/query rules
  capabilities.md      binding table mapping every broker ID to its required host capability

packaging/            systemd units, PAM policy, sysusers declaration, and the two
                      commented-out environment files (pilothouse.env documents
                      PILOTHOUSE_ALLOWED_ORIGINS, pilothoused.env documents
                      PILOTHOUSE_BACKUP_TIMERS — both are real variables the
                      binaries read, shipped with every setting commented out so
                      installing the package changes no runtime behavior); deb/ and
                      rpm/ hold the per-distro-family variants (broker unit's
                      --admin-group, PAM stack names) selected by
                      .goreleaser.yaml's nfpms overrides. postinstall.sh is the
                      single shared package scriptlet (deb postinst and rpm
                      %post). verify-install.sh is the install-validation
                      script that runs inside a distro container; it is not a
                      packaged file. model.go, finding.go, contract.go and
                      verify.go
                      make this a real Go package with an exported surface: the
                      artifact-contract model types, the finding vocabulary,
                      the embedded repository sources with the per-format
                      requirement table, dependency lists and forbidden
                      systemd-managed roots, and Verify (see
                      "Artifact contract model" below). units_test.go,
                      postinstall_test.go, verify_install_test.go and
                      goreleaser_config_test.go are its configuration-level
                      tests: the first runs the real
                      `systemd-analyze verify` against both broker units and
                      asserts they differ in exactly one line, the second runs
                      the real `shellcheck` against postinstall.sh and
                      exercises it against a temporary root, the third guards
                      verify-install.sh (the install-validation shell script,
                      see "Install validation" below) without ever executing
                      it, the fourth parses
                      ../.goreleaser.yaml and asserts the nfpms packaging
                      contract. finding_test.go pins the finding codes'
                      string values and verify_test.go holds the
                      artifact-contract behavioral tests; drift_test.go holds
                      the two guards tying contract.go's hand-written tables to
                      the live ../.goreleaser.yaml
  extract/            subpackage (package extract) whose only job is to produce
                      a packaging.Model from a real artifact on disk. At this
                      commit it holds two backends: Deb, which shells out to
                      dpkg-deb, and RPM, which shells out to rpm and to
                      rpm2archive piped into tar. Its command-line entry point is
                      cmd/verify-packages, which `make verify-packages` runs;
                      that target stays outside `ci`/`docker-ci`, so nothing
                      runs either automatically. Being a separate package is what keeps
                      the parent's run-time-inert guarantee mechanically true
                       (see "Artifact extraction" below)
test/vm/              the booted-VM harness (Layer B, #67). vm-boot-test.sh is the
                      one entry point (--family debian|fedora --artifact-dir <dir>),
                      committed executable and meant to be run through an explicit
                      interpreter (`bash test/vm/vm-boot-test.sh`); packaging.yml's
                      vm-boot job is what calls it. images.env is the single pinning site recording
                      each distro family's cloud image URL, checksum algorithm and
                      digest. lib/ holds the sourced, non-executable bash libraries:
                      images.sh (fetch and verify against that pin), cloudinit.sh
                      (generate the run-time credentials into a 0700 workspace and
                      emit the NoCloud seed), vm.sh (QEMU/KVM boot of a qcow2 overlay
                      plus the serial console channel), ssh.sh (the guest's SSH
                      lifecycle) and diagnostics.sh (the failure-time discriminator).
                      guest/ is the single directory holding every guest-side script:
                      the sourced, non-executable lib.sh plus install-package.sh,
                      check-activation.sh, check-pam.sh, check-journal.sh,
                      capture-pre-reboot.sh and check-reboot-posture.sh;
                      every executed one is committed 100755 and invoked as
                      `sudo -n sh ~/vm-boot/guest/<name>.sh`. At this commit a run
                      ends once the guest has come back from a real reboot with both
                      units active unaided, the same capability set, a destroyed and
                      recreated /run/pilothouse and a persisted /var/lib/pilothouse,
                      run by .github/workflows/packaging.yml's vm-boot job on main and
                      on the vm-boot pull-request label; packaging/vm_harness_test.go and
                      packaging/workflow_vm_job_test.go guard it structurally
                      without executing it (see the four "Booted-VM harness"
                      sections below)
test/image/releaserpm/
                      test-only Go command for #80's released-RPM fixture.
                      It resolves the latest stable GitHub release, selects
                      exactly one x86_64 RPM, verifies release size and SHA-256
                      while downloading, and writes an explicit manifest plus
                      RPM below a caller-owned ephemeral workspace. It is not a
                      shipped binary and no workflow invokes acquisition yet;
                      ordinary repository gates still analyze and test it (see
                      "Image-tier released-RPM fixture" below)
test/image/compose-ucore.sh
                      third #80 slice: verifies the uCore index and linux/amd64
                      member, revalidates the released RPM, and builds distinct
                      baseline/update derivatives in workspace-local Podman
                      storage; no workflow invokes it yet (see "Image-tier
                      uCore composition" below)
test/image/ucore-vm-test.sh
                      fourth #80 slice: consumes those two local images,
                      installs the baseline through bootc's composefs path,
                      boots QEMU/OVMF, validates enforcing SELinux and truthful
                      capabilities, switches to the update through guest-local
                      containers-storage, then rolls back with digest-slot
                      continuity checks. It quiesces every live resource it
                      owns but leaves exact-store reset and workspace deletion
                      to the later enclosing job (see "Image-tier uCore VM
                      consumer" below)
.docker/              development container image (Go + PAM + systemd headers, plus the systemd
                      package so `systemd-analyze` exists and `shellcheck` for the
                      packaging scriptlet) for docker-* make targets. It includes
                      `jq` so the real uCore composer runs in offline tests. It also
                      installs `rpm` (which on the Debian bookworm base provides
                      `rpm`, `rpmbuild` and `rpm2cpio`) and `cpio`, the latter
                      only because `cmd/verify-packages/integration_test.go`
                      still resolves it in its rpm tool list — the RPM extractor
                      itself runs `rpm2archive` piped into `tar`; `dpkg-deb`
                      already comes from the Debian base image, so no package is
                      needed for it. The image declares
                      `ENV PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1`, which reaches
                      every docker-* target through the Makefile's `DOCKER_RUN`
                      with no per-target flag: because the image guarantees those
                      tools, a tool-dependent test that would otherwise skip when
                      one is missing must fail inside this image instead. The one
                      reader in Go code is `internal/packagingtest.LookTool`,
                      which exposes the variable's name as
                      `packagingtest.RequireEnv`. `make
                      docker-tools-check` asserts the whole set — it resolves
                      `dpkg-deb`, `rpm`, `rpmbuild`, `rpm2archive` and `tar` and
                      prints the flag's value, alongside the `svu` and
                      `golangci-lint` checks it has always run — and stays
                      outside `ci`/`docker-ci`
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
`--incus` flags described under "Capability probing at startup" below. As of
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
| `system` | Unprivileged host telemetry (CPU/mem/disk/load/net/os) from `/proc`, `/sys`, `/etc/os-release`; emits health findings. |
| `storage` | Block/mount inventory (`lsblk`/`findmnt`) enriched with optional SMART/NVMe, MD RAID, LVM, device-mapper/LUKS, multipath, ZFS, and Btrfs backends; emits health findings; admins can create/mount/unmount/delete Pilothouse-managed NFS and SMB (guest or credentialed) automounts. SMB creation optionally supports paired numeric local UID/GID mapping. Expected immutable EROFS mounts retain their inventory usage and read-only state but are excluded from capacity and read-only health findings; other filesystems retain those checks. |
| `attention` | Aggregates `platform.HealthProvider` findings from other modules (bounded 2s/provider) into one "needs attention" view. |
| `services` | Systemd service/socket/timer inventory and lifecycle/enablement control via system D-Bus; bounded journal diagnostics. |
| `sysext` | `updex` definition/install state and `systemd-sysext` merge state, read entirely through the broker's `QueryExtensionsState` aggregate (no local `updex`/`systemd-sysext` invocation in the web process — the exec-backed implementation lives in the `sysext/extctl` subpackage that only `cmd/pilothoused` links); surfaces per-extension and aggregate component update availability (the responsibility that moved off Maintenance in #52); install/remove/update/refresh actions. Whole-module `CapabilityGateAny` on `updex OR sysext`, with narrower per-route/per-action guards. |
| `podman` | System (rootful) Podman inventory (containers/pods/images) via Libpod API; bounded logs; lifecycle actions. |
| `docker` | System Docker daemon inventory, bounded logs, lifecycle/image removal. |
| `incus` | Local-only Incus inventory (projects/instances/images/pools/volumes/buckets) via `/var/lib/incus/unix.socket`; lifecycle actions. |
| `logs` | Admin-only bounded system-journal search (message/priority/unit/time-window filters, ≤200 entries). |
| `files` | Admin-only browsing/download/atomic upload within explicitly configured filesystem roots (256 MiB bound). |
| `backups` | Monitors explicitly configured systemd backup timers: enabled/active state, last result, freshness, next run. |
| `maintenance` | Read-only host-image status (booted/staged/rollback deployments with bootc's image references and digests, supplemented by rpm-ostree version/checksum detail, plus soft-reboot eligibility when bootc reports it), maintenance-job state, reboot posture (including the merged-but-disabled-extension reason it derives from the shared `sysext.ExtensionsSource` aggregate), confirmed reboot. No host-image mutation. Read-only automatic-update (updater policy/timer) status is reported daemon-side through `QueryAutoUpdateStatus`, consumed web-side by the module's own `queryAutoUpdate`, and rendered by the Maintenance page's "Automatic updates" section — one independent subsection per updater (bootc, rpm-ostree) carrying its timer active/unit-file state, next trigger, service active state and last result, normalized policy, and both drop-in-presence booleans, or an explicit "not configured" statement when that updater has no payload. The section is fetched and shown under the same `HasAny(bootc, rpm-ostree)` gate as the host-image section, so it is absent entirely on a host advertising neither. No automatic-update mutation: the section carries no control and the broker's ID vocabulary has no matching action. |
| `activity` | Admin-only view over durable audit history (`QueryActivity`) and background jobs (`QueryJobs`). |
| `fleet` | Static UI preview only — no real multi-system transport/enrollment exists yet. Because of that it is **not registered in production**: `cmd/pilothouse`'s `newRegistry(dev bool)` appends `fleet.New()` only under the `--dev` flag (default `false`, and `packaging/pilothouse.service` does not pass it). Without `--dev` the module is never constructed, so `Mount` never runs and `/fleet`, `/fleet/enroll`, and `/fleet/systems/{id}` are unregistered routes (a mux 404, not a capability 404), with no nav entry and no sidebar system-picker link. |

See `docs/modules.md` for the module contract, recommended file layout, and
rules for adding a new module (routes, actions, queries).

**Host-image status (#51) — landed end state.** Maintenance is no longer a
systemd-only surface: it is a read-only host-image lifecycle *and* reboot-posture
module. Every piece has landed — the parsers, the manager, the broker query, the
reboot posture that consumes it, the module's any-of capability gate, and the
Maintenance page section that renders it. Two things it deliberately does **not**
do, and no sentence below should be read as saying otherwise: it exposes no bootc
or rpm-ostree mutation of any kind — no upgrade, switch, rebase, or rollback
action exists, and `cmd/pilothoused/capability_contract_test.go`'s
`TestNoHostImageMutationActionExists` keeps it that way by asserting that none of
`internal/broker/api.go`'s 35 `Action*` constants names bootc, ostree, or
rpm-ostree in either its Go identifier or its wire ID (`Query*` constants are
deliberately exempt — `QueryHostImageStatus` is the read-only surface this phase
adds) — and #51 itself does no automatic-update reporting: normalized updater
policy/timer reporting was split out of #51 into #58, and its web-render
surface into #60. Both have since landed in full, daemon side and web side:
`cmd/pilothoused`'s `run()` now registers
`broker.QueryAutoUpdateStatus`
(`org.frostyard.pilothouse.maintenance.autoupdate_status`) via `registerAutoUpdate`,
guarded by `caps.HasAny(capability.Bootc, capability.RPMOStree)` exactly like
`registerHostImage` and independent of both `registerMaintenance`'s and
`registerHostImage`'s own guards. It constructs one `maintenance.AutoUpdateManager`
whose per-updater configured/not-configured split is driven by the probed
`capability.AutoupdateBootc`/`capability.AutoupdateRPMOStree` flags — so the
`autoupdate-bootc`/`autoupdate-rpm-ostree` capabilities #50 probes and advertises
over `QueryCapabilities` now have a production consumer that gates on and reports
them, not only tests' full-capability fixtures. The manager reuses the same probed
systemd `*dbus.Conn` `cmd/pilothoused` already opens for backups/services/logs (no
second D-Bus dial), passing it through only when non-nil so a typed-nil never
reaches the manager's own nil-client guard. The query's web consumer is
`internal/modules/maintenance`'s `queryAutoUpdate`, which `collectPage` calls
under the same `HasAny(Bootc, RPMOStree)` gate `queryHostImage` uses — never on
a host advertising neither, where the query is unregistered — threading the
response into `Page`'s `autoUpdate *AutoUpdateStatus` parameter and its
`autoUpdateSection` view. That section is read-only in the same strong sense the
host-image one is — it renders no updater control and no automatic-update action
exists in the broker's ID vocabulary for one to target — and its per-updater
configured/not-configured rendering is spelled out in the "Maintenance:
whole-module `HasAny(Systemd, Bootc, RPMOStree)` gate" bullet in the web-side
gating narrative below. Zincati is neither queried nor special-cased:
`TestMaintenanceNeverReferencesZincati` fails on any non-comment mention of it in
any `.go` or `.templ` file under `internal/modules/maintenance`.

The per-surface capability split is the thing to hold in mind — Maintenance was
the first module where module presence, one route, and each individual broker
call are gated on *different* capability expressions (Extensions/`sysext` is the
other, since #52; see the sysext bullet in the web-side gating narrative below):

| Surface | Gate | Where |
|---|---|---|
| Module presence: nav entry, dashboard card | `HasAny(Systemd, Bootc, RPMOStree)` | `Module.RequiredAnyCapabilities` → `platform.AvailableAny`, via `internal/web/server.go`'s `moduleAvailable` |
| `GET /maintenance` | `HasAny(Systemd, Bootc, RPMOStree)` | `platform.GateAny` in `Mount` (`internal/modules/maintenance/module.go`) |
| `POST /maintenance/reboot` | `Has(Systemd)` | a separate, plain `platform.Gate` in the same `Mount` |
| `/attention` health collection | `HasAny(Systemd, Bootc, RPMOStree)` | `attention.Module.findings`' `CapabilityGateAny` type-assert |
| `QueryMaintenanceState` (reboot posture, reasons, jobs; no extension update availability — that is `QueryExtensionsState`'s) | `Has(Systemd)` | `queryState` web-side; `registerMaintenance` daemon-side |
| `ActionMaintenanceReboot` | `Has(Systemd)` | `registerMaintenance` (`cmd/pilothoused/main.go`); serialized on its own `maintenance/global` lock, no longer `sysext/global` — see "Per-resource action serialization" below |
| `QueryHostImageStatus` (booted/staged/rollback, digests, soft-reboot eligibility) | `HasAny(Bootc, RPMOStree)` | `queryHostImage` web-side; `registerHostImage` daemon-side |
| `QueryAutoUpdateStatus` (per-updater timer/service state, next trigger, policy, drop-in presence) | `HasAny(Bootc, RPMOStree)` | `queryAutoUpdate` web-side; `registerAutoUpdate` daemon-side. The `Autoupdate*` capabilities gate nothing here — they drive the configured/not-configured split *inside* the response, so the "no updater configured" report stays reachable |

What makes the first row real is `internal/web/server.go`'s
`moduleAvailable(module, caps)` — the single choke point `availableManifests`
(nav) and the dashboard loop both call — which composes the two whole-module
tests as `platform.Available(module, caps) && platform.AvailableAny(module,
caps)`. Each half defaults to `true` for a module that doesn't implement its
interface, so this AND-of-two-defaults is exactly right for all three shapes a
module can be in, and maintenance — which implements `CapabilityGateAny` only —
is filtered purely by the `HasAny` half with no type-switching in `server.go`.

Neither `Dashboard` nor `Health` fetches `QueryHostImageStatus`: both call only
`queryState`, so the *host-image section* — deployments, digests, per-source
unavailable indicators, the soft-reboot indicator — is rendered on
`GET /maintenance` and nowhere else. That is not the same as the card and
`/attention` being host-image-free, and the distinction matters:
`QueryMaintenanceState`'s `State` is itself partly host-image-derived on the
daemon side. `SystemManager.State` calls `HostImageSource.Status` (only when the
probed `capability.Bootc` flag passed to `NewSystemManager` is true and the
source is non-nil), appends `stagedHostImageReason` — "A staged host image
deployment requires activation by reboot." — to `State.RebootReasons` when a
staged deployment exists, and copies `SoftRebootCapable` onto `State`. `Summary`
renders `rebootSummary(state)`, which is `state.RebootReasons[0]` whenever
`RebootRequired`, and `Health`'s `maintenance.reboot` finding uses that same
`RebootReasons[0]` as its detail. Reasons are appended in a fixed order —
`/run/reboot-required` marker, staged host image, merged-but-disabled extension,
completed extension update — so on a host whose only reboot reason is a staged
host-image deployment, that host-image-derived sentence *is* what the dashboard
card and the `/attention` finding display. What the card and `/attention` never
show is raw host-image data (image references, digests, rollback slots,
soft-reboot eligibility); the reboot posture they show can be host-image-caused.

Soft-reboot eligibility reaches exactly three places:
`HostImageStatus.SoftRebootCapable` on `QueryHostImageStatus`'s response (set by
`ParseBootcStatus`, preserved by `MergeHostImage`); `State.SoftRebootCapable` on
`QueryMaintenanceState`'s response, copied verbatim by `SystemManager.State` from
the same single `hostImageState` read that supplies that call's
staged-deployment reboot reason; and exactly one UI indicator, rendered by
`hostImageSection`.

Do **not** read that as one parse feeding all three legs. `hostImageState`
reading the source once is a guarantee *within a single `State` call* and
nothing more. `QueryMaintenanceState` and `QueryHostImageStatus` are separate
broker queries, and `HostImageManager.Status` memoizes nothing — it re-runs
`bootc status --json` (and, when rpm-ostree is advertised,
`rpm-ostree status --json`) on every call. One `GET /maintenance` on a
systemd-plus-bootc host therefore runs bootc twice: `collectPage` calls
`queryState`, whose daemon handler reaches `Status` through
`SystemManager.State` → `hostImageState`, and then `queryHostImage`, whose
daemon handler calls `Status` directly. The single `HostImageManager` instance
wired in `cmd/pilothoused/main.go` shares the *code path* to bootc, not the
*result*: `State.SoftRebootCapable` and the `HostImageStatus` the page renders
come from two independent runs and are not guaranteed to agree.

The UI leg reads `HostImageStatus.SoftRebootCapable` — **not** `State.SoftRebootCapable` —
so the indicator's availability follows `HasAny(Bootc, RPMOStree)` and never
`Systemd`: it renders identically on a bootc-only host with no systemd, where
`QueryMaintenanceState` is never called and `State.SoftRebootCapable` is
therefore always nil. `State.SoftRebootCapable` still lands and is still
populated — it is the fact's API-surface leg for any consumer reading the full
systemd-gated posture response in one call — it is simply not what the page
renders from. All three legs keep the value's three states (non-nil true, non-nil
false, nil for "this bootc does not report it"), and nothing in the tree performs
a soft reboot: only the pre-existing full `ActionMaintenanceReboot` exists.

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

Those parsers have exactly one caller:
`internal/modules/maintenance/hostimage_manager.go`'s `HostImageManager`,
which in turn has exactly two consumers, both wired to the *same* instance in
`cmd/pilothoused/main.go`: the broker query `QueryHostImageStatus`
(`org.frostyard.pilothouse.maintenance.host_image_status`, registered by
`registerHostImage`), and `maintenance.SystemManager`, which takes it as a
`HostImageSource` and reads it while computing `QueryMaintenanceState`'s
posture. Sharing the instance shares the *path*, not the *result*: `Status`
memoizes nothing, so each of the two consumers re-runs the underlying commands.
The query now has a web-side consumer as well: `GET /maintenance`
fetches it when the host advertises `HasAny(Bootc, RPMOStree)` and renders it
as the page's "Host image" section — see the "Maintenance: whole-module
`HasAny(Systemd, Bootc, RPMOStree)` gate" bullet below for exactly what that
section renders. `QueryMaintenanceState`'s response also changed shape (see
the `State` bullet below). The `maintenance` module's own
nav/route/dashboard gating was reworked separately, to
`HasAny(Systemd, Bootc, RPMOStree)` — see the "Maintenance: whole-module
`HasAny(Systemd, Bootc, RPMOStree)` gate" bullet in the web-side gating
narrative below. What the daemon side now does:

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
  withheld. `docs/capabilities.md`'s binding table carries the row (54 IDs,
  19 queries, the 19th being #52's `QueryExtensionsState`) and
  `cmd/pilothoused/capability_contract_test.go` exercises it
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
  probing itself is never fatal. `updex`, Podman, Docker, and Incus are
  additionally gated on explicit configuration: `--updex`,
  `--podman-socket`, and `--docker` all default to empty and `--incus`
  defaults to `false`, and an unset value makes
  `ProbeUpdex`/`ProbePodman`/`ProbeDocker`/`ProbeIncus` report the
  capability absent without running any command, performing any I/O, or
  dialling anything. The "no client is built" half of that holds literally
  for Docker — `probeDocker`'s empty-endpoint guard sits ahead of its
  constructor — but not for Incus: `ProbeIncus` evaluates
  `newIncusProbeClient()` in its call to `probeIncus`, before the `enabled`
  guard, so a disabled probe does allocate that struct. It is a pure
  allocation with no dial and no I/O, and `probeIncus` returns early
  without ever calling its `Server` method. So a host that merely happens
  to have `updex` on
  `PATH`, a socket at the conventional path, `DOCKER_HOST` exported, or a
  live `/var/lib/incus/unix.socket` never enables the tool/engine. Docker's
  non-empty endpoint is also the *only* input its client is built from —
  `ProbeDocker` and `cmd/pilothoused`'s live client both use
  `dockerclient.WithHost(endpoint)`, never `dockerclient.FromEnv`, so the
  SDK's implicit `DOCKER_HOST`/default-socket resolution is never
  consulted. Incus is the opposite shape: its socket path is *not*
  configurable — it stays fixed at `/var/lib/incus/unix.socket` — so
  `capability.Config.IncusEnabled` is a plain bool carrying `--incus`, and
  `ProbeIncus(ctx, false)` returns an empty set before its client's
  `Server` call is ever reached. The resulting `capability.Set` is not
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
  corresponding capability is absent. No engine state is ever a fatal
  `run()` error, but only one of them is warned about. A *configured but
  unreachable* engine degrades silently: `ProbeDocker` narrows to absent on
  a `Ping` failure without logging anything, and `registerDocker` then
  registers nothing. The single warnable case is a Docker client that fails
  to construct from a configured `--docker` endpoint (e.g. a malformed
  endpoint), which `connectDocker` logs as a warning before returning nil.
  An *unconfigured* engine — `--docker` left empty — is not a warnable
  condition either, so `connectDocker` returns nil silently and no client
  is built. Podman's and Incus's client constructors stay
  unconditional in `run()` because neither performs I/O at construction;
  for Incus, `--incus` acts entirely through the probe, so
  `registerIncus`'s `caps.Has(capability.Incus)` guard is what leaves the
  engine with no registered query or action when the flag is false.
  `registerServices` and
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
  depends only on the sysext extensions source, job store, and command
  runner), so unlike backups/services/logs there is no construction-level
  non-fatal-startup fix to make here; the manager is always constructed
  regardless of systemd, and the registration guard above is the only thing
  withholding it. Separately — and this is the real behavioral change —
  `maintenance.SystemManager.State`'s extension-read subpath never fails the
  query. As of #52 `extensionState` makes one call,
  `sysext.ExtensionsSource.State(ctx, updexAvailable, sysextAvailable)`,
  passing through the two capability facts `cmd/pilothoused/main.go` probes
  as `caps.Has(capability.Updex)`/`caps.Has(capability.Sysext)`. That
  interface owns both the never-attempt rule (a tool whose flag is false is
  never invoked) and the never-hard-error rule (a source whose command
  *fails* sets only its own `UpdexAvailable`/`UpdexError` or
  `SysextAvailable`/`SysextError` pair in the returned `ExtensionsState`,
  leaves the other source intact, and returns no error), so `extensionState`
  inherits the degrade for free rather than branching per capability itself;
  the contractually-unused error result is handled the same way
  `hostImageState` handles a failed host-image read — drop this call's
  contribution, cache nothing, carry on. This closes spec resolution 3's
  requirement: an extension provider that errors costs the extension-derived
  reboot reasons and nothing else, so `QueryMaintenanceState` stays a 200
  instead of 5xx-ing as it did when the subpath propagated `Check`/`List`
  failures. `Jobs`, `OSVersion`, the OS-marker and completed-job reasons, and
  the staged-host-image reason are computed exactly as before regardless.
  From the returned `Extensions` slice maintenance derives exactly one thing —
  a merged extension *known* to be disabled becomes "<name> is disabled but
  remains active until reboot." — and nothing else. "Known" is the load-bearing
  word: `Enabled` is populated only by updex and `Merged` only by
  systemd-sysext, so `mergedButDisabledReasons` reads each field only when its
  own source's `*Available`/`*Error` pair says that source actually answered,
  and additionally requires `Managed`, since an extension merged through
  systemd-sysext with no updex definition has an `Enabled` nobody set. A plain
  `Merged && !Enabled` filter would report every merged extension as disabled
  on a host without updex and would newly over-report unmanaged ones — the
  `Managed` guard is what holds this reason identical to the pre-#52
  `List()`-based behavior, which only ever iterated updex's own feature list.
  Beyond that reason, `State.Updates`, the "Available updates" page section,
  the Summary card's "Updates" mini-row, and the `maintenance.updates` Health
  finding are all gone, update availability being Extensions' to own. The source is shared with `registerExtensions`
  as one daemon-internal *instance*, not one *result*:
  `extctl.SystemManager.State` has no cache, so the two consumers each run
  their own read; maintenance's own 1-minute `extensionState` cache is the
  only cache involved and is this manager's alone. See
  `docs/capabilities.md`'s extension-read note for the full mechanism and
  `internal/modules/maintenance/manager_test.go` for the per-combination
  cases, including one driving the real `*extctl.SystemManager` with a
  failing command runner. (`NewSystemManager` also carries a
  `hostImage`/`bootcAvailable` pair for the host-image leg described in the
  #51 section above; it follows the same degrade convention and leaves the
  extension behavior here untouched.)
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
  (every other caller has a uniform per-call requirement). `extctl.NewSystemManager`
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
  semantics); a `Module` that does not implement it has no capability
  requirement and is available whenever it is registered — the default for
  `system`/`files`/`activity`/`fleet` and storage's own inventory reads.
  (Registration is a separate switch from capability gating: `fleet` carries
  no `CapabilityGate`, but `cmd/pilothouse` only registers it under `--dev`,
  so on a production process it is absent from the registry entirely rather
  than gated within it.) `Gate(host Host, ids []capability.ID,
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
  (added as mechanism by #54, adopted by `maintenance` in #51).**
  `internal/capability.Set` gained `HasAny(ids ...ID)
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
  `/attention` still calls its `Health`. No production module implemented
  `CapabilityGateAny` in this chunk — the mechanism was proven the same
  way `CapabilityGate` was before its first real adopter: a synthetic fake
  module in `internal/platform/capability_test.go` (exercising `AvailableAny`
  through a fake `Host`'s real `Capabilities()`) and a synthetic fake module
  registered into a real `*web.Server` in `internal/web/server_test.go`
  proving nav/dashboard/route behavior through a real registry and HTTP
  round trip. #51 then made `maintenance` the first production adopter, with
  `RequiredAnyCapabilities()` returning `{Systemd, Bootc, RPMOStree}`; see the
  Maintenance bullet below for the per-surface split that adoption produced.
  #52 added the second, `sysext` → `{Updex, Sysext}`; see the sysext bullet
  below.
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
- **Backups: whole-module `Systemd` gate.**
  `internal/modules/backups.Module` now also implements
  `RequiredCapabilities() []capability.ID`, returning
  `[]capability.ID{capability.Systemd}` — unlike services, it has no
  sub-feature with a broader requirement, so there is exactly one
  `platform.Gate(host, []capability.ID{capability.Systemd}, ...)` wrap, on
  its single `GET /backups` route. With `Systemd` absent, the whole module
  disappears — nav entry, dashboard card, and the route 404s at request
  time; with `Systemd` present, the module behaves exactly as before this
  chunk. Its `views.templ` did not change: an absent module 404s before any
  page renders, so there is no conditional view content to add, unlike
  services' `journalAvailable` parameter. `module_test.go` gained the same
  configurable `caps capability.Set`/`capsSet bool` pair on its fake `Host`
  that services' test uses (defaulting to a full-capability set), so
  gated/ungated route behavior is exercised via real `ServeMux` round trips
  through `Mount`.
  `platform.Gate`/`Available` only guard requests that arrive through a
  module's own `Mount`-registered routes or the web-side nav/dashboard
  loops, though — they do nothing for other in-process code that holds a
  `platform.HealthProvider` reference and calls `Health` directly.
  `internal/modules/attention.Module.findings` is exactly that: it iterates
  every registered provider (including `backupModule`, passed into
  `attention.New(...)` in `cmd/pilothouse/main.go`) and previously called
  `provider.Health(ctx, host)` unconditionally, so a `Systemd`-absent host
  still reached `QueryBackupsState` through `/attention` and
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
  present-capability ends. (The any-of bullet above later extended the same
  skip to `platform.CapabilityGateAny`/`HasAny`; see "Attention's
  per-provider capability skip" in the current-state section for the
  composed behavior.)
- **Maintenance: whole-module `HasAny(Systemd, Bootc, RPMOStree)` gate
  (reworked by #51).** `internal/modules/maintenance.Module` adopted #54's
  whole-module `Systemd` `CapabilityGate` alongside backups; #51 replaced
  it. `module.go` now implements `platform.CapabilityGateAny`
  (`RequiredAnyCapabilities()` returning `[]capability.ID{capability.Systemd,
  capability.Bootc, capability.RPMOStree}`), **not**
  `platform.CapabilityGate`, because the module reports on two independent
  sources: systemd-gated reboot posture and jobs
  (`QueryMaintenanceState`), and separately gated host-image status
  (`QueryHostImageStatus`, `Bootc OR RPMOStree`). `GET /maintenance` is
  wrapped in `platform.GateAny(host, {Systemd, Bootc, RPMOStree}, ...)`;
  `POST /maintenance/reboot` remains wrapped in a separate, plain
  `platform.Gate(host, {Systemd}, ...)`, unchanged, since rebooting is a
  systemd operation and `ActionMaintenanceReboot` is registered only under
  `Systemd`. So: with none of the three capabilities present the whole
  module disappears (no nav entry, no dashboard card, `GET /maintenance`
  404s); with any one of them present the nav entry, dashboard card, and
  `GET /maintenance` are available, while `POST /maintenance/reboot` remains
  available only when `Systemd` specifically is present, regardless of
  bootc/rpm-ostree. Each broker call the module makes is independently
  capability-gated so no newly-possible capability combination turns an
  available module into an error: `queryState` calls
  `QueryMaintenanceState` only when the host advertises `Systemd` and
  substitutes the zero `State` otherwise, so `Page`, `Summary`, and `Health`
  degrade to "nothing to report" on a bootc-only host rather than the
  handler 503ing (or the dashboard card/`attention` finding erroring) on a
  query the daemon never registered there; symmetrically, `queryHostImage`
  calls `QueryHostImageStatus` only when the host advertises
  `HasAny(Bootc, RPMOStree)` and returns `nil` otherwise, which is what the
  page renders from. `views.templ`'s `Page` therefore takes a
  `hostImage *HostImageStatus` alongside `state`, and nil-ness *is* the
  availability flag — the page can only render host-image content it actually
  fetched. When it is non-nil, `Page` renders the conditional
  `hostImageSection` (unexported — `Page` is its only caller): the booted,
  staged, and rollback deployments bootc reported (image
  reference and digest, plus rpm-ostree's supplementary version and checksum
  where the broker-side merge attached them), an independent per-source
  unavailable indicator for each of `BootcError` and `RPMOStreeError` so one
  failed source never hides the other's data, and — exactly once on the whole
  page — the soft-reboot-eligibility indicator, read straight from
  `HostImageStatus.SoftRebootCapable` (non-nil true renders "a soft reboot may
  be sufficient…", non-nil false "a full reboot is required…", nil renders
  nothing). That indicator is gated on `HasAny(Bootc, RPMOStree)`, **not** on
  `Systemd`, so it renders identically on a bootc-only host and a
  bootc-plus-systemd one; it is deliberately not duplicated inside the
  `Systemd`-gated reboot-posture area, which still renders only the
  pre-existing reboot-required card and reboot form.
  `State.SoftRebootCapable` remains the same fact's API-surface leg on
  `QueryMaintenanceState`'s full posture response, it is just not the page's
  rendering source. With neither bootc nor rpm-ostree advertised the section
  is omitted outright rather than rendered empty or errored. Nothing in the
  section is a control: no upgrade, switch, rebase, rollback, or
  automatic-update link, button, or form exists anywhere on the page for
  bootc or rpm-ostree, and `views_test.go` asserts that mechanically across
  every host-image fixture. The dead-control audit a
  whole-module-present/route-gated combination normally demands is unchanged
  by all of this: the only view element targeting the systemd-only reboot
  route is still the admin "Reboot host" form nested inside
  `if state.RebootRequired`, and the zero `State` substituted when `Systemd`
  is absent leaves that condition false, so the form cannot render on a host
  where the route 404s (`TestPageRendersNoRebootControlWithoutSystemd` pins
  both ends of that).
  The page's "Automatic updates" section (#60) is the exact same shape one
  level over: `queryAutoUpdate` calls `QueryAutoUpdateStatus` only under
  `HasAny(Bootc, RPMOStree)` — the same gate as `queryHostImage`, never the
  `Autoupdate*` pair, which would 404 the query on precisely the no-updater
  host whose "not configured" report is the point — and returns `nil`
  otherwise, so `Page(state, hostImage, autoUpdate, csrf, admin)` renders the
  unexported `autoUpdateSection` (again, `Page` is its only caller) only when
  `autoUpdate != nil`, and a host advertising neither source omits the section
  outright rather than rendering it empty
  (`TestPageOmitsAutoUpdateSectionWithoutAnySource`). Non-nil is not the same
  as populated: within the section, each updater's subsection is chosen by that
  updater's `BootcConfigured`/`RPMOStreeConfigured` flag paired with its payload
  pointer — which the daemon sets from the probed
  `AutoupdateBootc`/`AutoupdateRPMOStree` capabilities — so a plain bootc or
  rpm-ostree host with no updater units gets a non-nil, zero-value response and
  renders two explicit, updater-naming "not configured" statements, never a
  blank or hidden subsection. A configured updater renders its timer active and
  unit-file states, next trigger, service active state and last result,
  normalized policy, and both drop-in-presence booleans; the two subsections are
  independent, so one updater being unconfigured never hides the other's detail.
  Because the section's gate is `HasAny(Bootc, RPMOStree)` and not `Systemd`, it
  renders identically on a bootc-only host with no systemd
  (`TestPageRendersAutoUpdateSectionWithoutSystemd`). Like the host-image
  section, it contains **no control of any kind** — no link, button, form, or
  HTMX request that enables, disables, triggers, or reconfigures either updater,
  and no broker action exists for one to target
  (`TestPageExposesNoAutoUpdateMutationControl` asserts it across all four
  payload combinations).
  Because the module's whole-module gate is
  now an any-of gate, `attention.Module.findings` reaches it through the
  `platform.CapabilityGateAny`/`AvailableAny` (`HasAny`) type-assert rather
  than the `platform.CapabilityGate` (`HasAll`) one, so maintenance is
  skipped there — no `Health` call, no "unavailable" placeholder — only when
  a host has none of the three. Maintenance's existing extension-read
  degrade (`QueryMaintenanceState`'s updex/sysext handling, from #50) is
  untouched by either chunk: the capability gating sits on top of it, at the
  module/route level, not inside the query handler.
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
  content to add, the same as backups/logs (maintenance's `views.templ`
  gained conditional host-image content in #51; see the Maintenance bullet
  above for its now-different behavior). Neither module is a
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
  podman/docker/backups/logs (maintenance's `views.templ` gained conditional
  host-image content in #51; see the Maintenance bullet above for its
  now-different behavior). Incus is not a
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
  caps)`. Modules implementing `CapabilityGate` (whole-module gate):
  - `services` → `Systemd` (plus a `Systemd AND Journald` `Gate` on just
    `GET /services/{unit}/logs`)
  - `logs` → `Systemd AND Journald`
  - `backups` → `Systemd`
  - `podman` → `Podman`
  - `docker` → `Docker`
  - `incus` → `Incus`

  Modules implementing `CapabilityGateAny` (whole-module any-of gate, added
  by #51):
  - `maintenance` → `HasAny(Systemd, Bootc, RPMOStree)`
  - `sysext` → `HasAny(Updex, Sysext)` (added by #52)

  `POST /maintenance/reboot` is additionally, separately gated by a plain
  `Systemd`-only `platform.Gate` outside the whole-module mechanism, so it
  404s on a bootc-only host whose maintenance module is otherwise present.
  `sysext` has the same shape with three narrower sub-guards instead of one:
  `POST /sysext/{name}/{action}` (enable, disable) carries a plain
  `platform.Gate` on `HasAll(Updex, Sysext)`, and
  `POST /sysext/actions/{action}` carries the module's own `GateAny` plus a
  per-action check inside the handler (`refresh` needs `Sysext`, `update`
  needs `Updex`) because the two actions share one route pattern but not one
  requirement — see the sysext bullet below.

  Modules with partial or no gating: `storage` deliberately does *not*
  implement `CapabilityGate` — its inventory (nav, dashboard card,
  `GET /storage`) is available on every capability set — `storage` is
  registered unconditionally, so unlike `fleet` there is no registration
  switch either — and only its three remote-mount routes
  (`GET /storage/mounts/new`, `POST /storage/mounts`, and
  `POST /storage/mounts/{id}/{action}`, which covers mount, unmount, and
  delete) are wrapped in `platform.Gate(host, {Systemd}, ...)`, with the "Add
  remote mount" link and the entire per-mount actions block collapsed behind
  the same `Systemd` flag in `views.templ`. `system`, `files`, `activity`, and
  `fleet` declare no capability requirement, so they are available on every
  capability set they are registered under. `fleet` is the one module whose
  exposure is decided at registration rather than by capabilities:
  `cmd/pilothouse`'s `newRegistry(dev bool)` appends `fleet.New()` only under
  the `--dev` flag (default `false`), so a production process has no fleet
  module, no fleet nav entry, no fleet system-picker link, and no mounted
  fleet routes at all.
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
  no-systemd host, `services`/`backups` contribute nothing to `/attention`
  rather than a degraded placeholder, via the plain
  `CapabilityGate`/`HasAll` check. `maintenance` is skipped on a different
  condition: it implements `CapabilityGateAny`, so it contributes nothing
  only when *none* of `Systemd`/`Bootc`/`RPMOStree` is present (the
  `CapabilityGateAny`/`AvailableAny` `HasAny` check added by #51) — a
  bootc-only host still collects its `Health`, which reports what it can
  without the systemd-gated query.
- **Routes stay mounted; absence 404s at request time.** No route is ever
  conditionally registered at startup based on capability — every module's
  `Mount` runs unconditionally and mounts all its routes on the shared mux.
  Absence is enforced at request time by `platform.Gate` (per route) and
  reflected in nav/dashboard by `moduleAvailable` (per render), so a gated-off
  surface is indistinguishable from a route that does not exist, both in the
  UI and at the URL.
- **sysext, which #54 did not cover, got its web-side gate in #52.**
  `cmd/pilothouse`'s `newRegistry` now calls `sysext.New()` with no arguments
  — the web process constructs no extension manager and has no
  `--definitions-root`/`--updex` flags — and reads the inventory through
  `broker.QueryExtensionsState`. The exec-backed manager moved out of
  `internal/modules/sysext` into `internal/modules/sysext/extctl` in the same
  change, so the boundary is enforced by the package graph rather than by
  convention: `sysext` keeps only the `Manager`/`ExtensionsSource` interfaces
  and their types and imports no `os/exec`, while `extctl` — linked by
  `cmd/pilothoused` alone — owns `NewSystemManager`, `ExecRunner`, and every
  `updex`/`systemd-sysext` invocation. `sysext.Module` implements
  `platform.CapabilityGateAny` returning `{Updex, Sysext}` (and, like
  `maintenance`, deliberately not `CapabilityGate`), which gates its nav
  entry, dashboard card, and `GET /sysext`. Its mutating routes keep the
  daemon's narrower requirements, mirroring `registerSysextActions` exactly:
  `POST /sysext/{name}/{action}` (enable, disable) is wrapped in
  `platform.Gate` on `HasAll(Updex, Sysext)`, while
  `POST /sysext/actions/{action}` is wrapped in `platform.GateAny` at the
  route and re-checks per action in the handler — `refresh` 404s without
  `sysext`, `update` 404s without `updex` — because the two actions share one
  route pattern but not one requirement. The rendered controls follow the same
  three predicates via `Page`'s `enableDisableAvailable`/`refreshAvailable`/
  `updateAvailable` parameters, and an extension with `Managed: false` renders
  no enable/disable control at all. `cmd/pilothouse/capability_contract_test.go`'s
  `webSideUngatedBrokerIDs` exemption (the four `ActionSysext*` IDs) and its
  `Len == 4` assertion were deleted in the same change, so those IDs are now
  subject to the ordinary web-side capability check.

### Optional tooling is explicitly opt-in (end state, #64)

Several bullets above narrate individual pieces of #64 (sub-phase 1 of 4
split from #53, phase 4 of the #35 arc). This is the landed end state in one
place, so a reader who lands here first does not have to reassemble it.

- **The rule.** An optional dependency is enabled only by explicit
  configuration *plus* a reachable endpoint. Presence on the host is never
  enough. Four dependencies are optional in this sense — `updex`, Podman,
  Docker, Incus — and each is now off unless its flag is set: `--updex`
  (path, default empty), `--podman-socket` (path, default empty), `--docker`
  (endpoint, default empty), `--incus` (bool, default `false`). At the zero
  value each probe returns an empty `Set` before doing any I/O: no command
  is run and no socket is dialled, with no `PATH` fallback for `updex` and
  no `dockerclient.FromEnv` for Docker. `ProbePodman`/`ProbeDocker` do not
  reach their client constructors at all; `ProbeIncus` allocates its client
  struct before the guard, but that allocation performs no I/O and the
  client's only dialling method is never called.
- **What did not change.** `systemd`, `journald`, `sysext`, `bootc`,
  `rpm-ostree`, and the two `autoupdate-*` capabilities stay presence-probed
  and carry no flag. Every `registerX` guard in `cmd/pilothoused` is
  untouched — the guards were already correct, and the defect was entirely
  in what fed them. No broker ID was added or removed: `internal/broker/api.go`
  still declares 35 `Action*` and 19 `Query*` constants (54 total), and
  `docs/capabilities.md`'s binding table and both capability contract tests
  are unchanged in shape and count. Both contract harnesses build fixtures
  from explicit `capability.Set` values rather than from a live `Probe`, so a
  fixture naming `podman` still means "podman was configured and reachable."
- **Systemd units.** Both packaged broker units
  (`packaging/deb/pilothoused.service` and `packaging/rpm/pilothoused.service`)
  declare no `Wants=` on
  any engine socket (only `After=`, ordering without pull-in), and their
  `ExecStart` passes none of the four flags — so a stock install runs with
  every optional dependency off, and starting the broker never activates
  `podman.socket` or `incus.socket`.
- **Mock Fleet.** `fleet` is a static preview with no real transport, so it
  is gated at *registration*, not by capabilities: `newRegistry(dev bool)`
  appends `fleet.New()` only under `--dev`. With the flag off the module is
  never constructed, so there is no nav entry, no sidebar system-picker link
  (that link derives from `data.Modules` rather than a hardcoded href), and
  `/fleet`, `/fleet/enroll`, and `/fleet/systems/{id}` are unregistered — a
  mux 404, not a `platform.Gate` 404.
- **Net effect on a bare host.** `pilothoused` on a host with nothing
  configured probes clean, advertises whatever presence-probed capabilities
  it found, registers no engine or `updex` query/action, and starts
  successfully; the console renders the surfaces that remain and omits the
  rest entirely rather than showing them broken.

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

### Packaging test fixtures (`internal/packagingtest`)

`internal/packagingtest` is an ordinary Go package — its implementation files
carry no `_test.go` suffix — whose only consumers are test files. It exists
because the helpers below are needed by the tests of more than one package, and
an unexported helper living in a `_test.go` file is unreachable from another
package. It sits under `internal/` because it ships in no binary, and it imports
nothing else in this repository, so it can hold no knowledge of any packaging
contract: a fixture it builds describes only what its caller declared.

**The tool gate.** `LookTool(t, name)` resolves an external packaging tool and
is the only place in the package that looks a name up on `PATH`; every
tool-dependent test goes through it, so none can reach a tool around the gate.
When the tool cannot be resolved, the behaviour depends on `RequireEnv`
(`PILOTHOUSE_REQUIRE_PACKAGING_TOOLS`, which `.docker/Dockerfile` sets to `1`):

- variable unset — `Skipf` with a reason naming the tool and pointing at
  `.docker/Dockerfile`, following the wording of `packaging/units_test.go`'s
  `systemd-analyze` skip and `packaging/postinstall_test.go`'s `shellcheck`
  skip;
- variable set to `1` — `Fatalf` instead, because the environment declares the
  tool present and skipping there would silently hide the check.

The parameter is a local three-method `TestingT` interface (`Helper`, `Skipf`,
`Fatalf`) that `*testing.T` satisfies, rather than `testing.TB`, which has an
unexported method and cannot be implemented outside `testing`. That is what
lets a recording fake observe both branches from a test that itself passes, and
it keeps `testing` out of the package's non-test imports.

**The shared fixture vocabulary.** One hand-written `Spec` feeds both builders:
`Dirs` and `Files`, each with a declared mode, an `Owner` and a `Group`; a
per-file `Config` flag; `Depends`; and a `*string` `Postinstall`. An empty
`Owner` or `Group` means `DefaultOwner` (`alpha`) or `DefaultGroup` (`beta`).
Those defaults are placeholder names rather than `root` on purpose: an assertion
that an entry's ownership was read out of a package's own metadata would pass
against a reader that hardcoded `root` and would prove nothing, whereas against
a placeholder it cannot. Only `BuildRPM` records ownership; `BuildDeb` ignores
both fields, for the reason given below.

Sharing one `Spec` across two formats has one caveat, and it is in `Depends`:
the strings are written into each format's own metadata **verbatim**, never
translated, so a `Spec` handed to both builders must use syntax both parse
alike — plain dependency names. Each format's own constraint syntax is
format-specific: a deb-only fixture may write `alpha | beta` and
`gamma (>= 1)`, while an rpm-only fixture needs the **spaced** form
`gamma >= 1`, since `gamma>=1` without the spaces is parsed by rpm as a
dependency whose *name* is that whole string.

**The deb fixture builder.** `BuildDeb(t, outDir, spec)` builds a throwaway
`.deb` from a `Spec` and returns the artifact's path. `outDir` is
caller-supplied, so fixtures can be built straight into a directory the caller
later scans; the staging tree is scratch space inside it. `Depends` entries pass
through verbatim, joined with `", "`, so alternatives and version constraints
are never rewritten.
`DEBIAN/conffiles` is written only when at least one `File` is marked `Config`.
`Postinstall` keeps three states apart: nil ships no `DEBIAN/postinst` member at
all, a pointer to `""` ships a zero-byte one, and a pointer to a body ships
those bytes. `dpkg-deb` is resolved through `LookTool`, so a host without it
skips with an explicit reason and the dev image cannot skip.

`BuildDeb` **ignores** `Owner` and `Group` on every entry. It builds with
`dpkg-deb --root-owner-group`, which records every archived path as `root/root`,
and a deb's payload is read back by extracting it to disk, from which the
archived ownership cannot be recovered at all — so honouring the fields would
record something no reader of the artifact could observe.

Two measured `dpkg-deb` constraints shape the builder. `dpkg-deb --build`
refuses a control directory outside 0755-0775 (`control directory has bad
permissions 700`) while `t.TempDir()` and `os.MkdirTemp` produce 0700
directories, so `DEBIAN` is chmodded to 0755 explicitly. And `dpkg-deb`
synthesizes the intermediate directories of every declared path into the
archive, so each intermediate directory the builder creates is chmodded to 0755,
making every synthesized parent's archived mode determinate rather than a
product of the caller's umask. Declared modes are applied with an explicit
`os.Chmod` for the same reason, rather than relying on the permission argument
of `os.Mkdir`/`os.WriteFile`.

**The raw escape hatch.** `BuildDebRaw(t, outDir, tree, modes)` packs an
already-staged tree verbatim instead of rendering a `Spec`: `tree` maps a
relative path to the bytes of a regular file there — `DEBIAN/control` included,
since nothing is synthesized — and `modes` gives a path's mode, naming a
directory when `tree` has no such file. It exists because a control area a
broken package could genuinely carry is not always expressible through `Spec`:
`dpkg-deb --build` **rejects** a `DEBIAN/conffiles` line that is not an absolute
path outright (`conffile name 'etc/phx/relative.conf' is not an absolute
pathname`, exit 2) — the message the package's own test requires it to print.
So the only way to obtain that artifact is `--nocheck`, which `dpkg-deb`
documents as building any archive you want no matter how broken. `BuildDebRaw`
passes `--nocheck` and `BuildDeb` does not, which keeps malformed metadata in
reach of only the tests that ask for it rather than making it expressible by
every `Spec`. It carries `BuildDeb`'s umask defences over: every intermediate
directory it creates is chmodded to 0755, each staged file is chmodded to its
declared mode explicitly, declared directory modes are applied deepest path
first once every file is written, and `DEBIAN` is chmodded to 0755 last.

**The rpm fixture builder.** `BuildRPM(t, outDir, spec)` builds a throwaway
`.rpm` from the same `Spec` and returns the artifact's path, so one fixture
declaration can be built in both formats. Everything is scoped to scratch space
inside `outDir`: a throwaway `_topdir` holding the generated spec file, the
staged payload tree and `rpmbuild`'s own working directories. The spec file
declares `%global debug_package %{nil}`, `AutoReqProv: no` and
`BuildArch: noarch`, so the package holds exactly the declared payload, and
every declared dependency appears verbatim. The requires set is not limited to
the declared ones: outside the builder's control rpm also records the
`rpmlib(...)` capabilities `rpmbuild` writes into every artifact, plus
`/bin/sh` whenever a `%post` section is present — both even under
`AutoReqProv: no`. A caller asserting on requires therefore checks each
declared entry is present, not that the set is exactly the declared one. Each
`Depends` element becomes one `Requires:` line verbatim. `%files` emits
`%dir %attr(<mode>, <owner>, <group>)` per `Dir` and
`%attr(...)` per `File`, prefixed with `%config` where declared, so rpm records
each entry's mode and ownership from the declaration rather than from the build
account — measured to hold when building as an unnamed uid 1000 with
`HOME=/tmp`, which is exactly how the `make docker-*` targets invoke the dev
image. The build runs `rpmbuild -bb` with `_topdir` and `_rpmdir` defined, then
copies the single artifact produced into `outDir`. `rpmbuild` is resolved
through `LookTool` like every other tool here.

Because rpm owns only what `%files` lists, an rpm fixture holds **exactly** its
declared destinations and no synthesized parents. That is the opposite of the
deb builder, where `dpkg-deb` archives the intermediate directories of every
declared path, and the two must never be stated as one claim.

`BuildRPM` calls `Fatalf` when `Postinstall` is non-nil but empty, naming the
limitation rather than quietly building something else. Measured, an empty
`%post` builds but records **no body**: `rpm -qp --scripts` prints only
`postinstall program: /bin/sh` with no scriptlet header, `%{POSTIN}` is
`(none)`, and rpm's own tag-presence marker `%|POSTIN?{HASPOST}:{NOPOST}|` reads
`NOPOST`. A zero-byte rpm scriptlet is therefore not a state an artifact can
represent, let alone one a reader could tell apart from shipping none — so the
empty-but-present postinstall case is exercised for **deb only**, and only the
`nil` side is available in both formats. Two further measured caveats bind a
declared body, and `BuildRPM`'s doc comment records both: rpm strips **all**
trailing newlines from a recorded scriptlet, so a caller wanting byte-exact
equality downstream declares a body without one; and a body line beginning with
`%` at column 0 would be read by `rpmbuild` as the start of a new spec section.

The package's own tests check each builder against that format's own tooling,
never against other Go code: the deb builders against `dpkg-deb` — `-c` for the
path/mode table, `-f` for the dependency field, `-e` for the control directory —
and `BuildRPM` against `rpm` — `-qp -l` for the owned paths, `--requires` for
the dependencies, `-qpc` for the configuration files, `--scripts` for the
postinstall body, and a `%{FILENAMES}`/`%{FILEUSERNAME}`/`%{FILEGROUPNAME}`
query proving both declared ownership pairs reached the header per entry. The
gate's two branches and `BuildRPM`'s empty-`Postinstall` refusal are driven
through a recording `TestingT`, in tests that need no external tool on any host.
`BuildDebRaw`'s tests additionally pin the premise behind its `--nocheck`: an
ordinary `--build` of the very same staged tree is required to fail, so the
escape hatch's justification is a runnable claim rather than a comment.

## Configuration

No config-file parser; configuration is command-line flags plus a couple of
environment variables, typically supplied via systemd `EnvironmentFile`.

**`pilothouse` (web) flags** — `cmd/pilothouse/main.go`:
- `--listen` (default `127.0.0.1:8888`), `--broker-socket`
  (default `/run/pilothouse/broker.sock`)
- repeatable `--allowed-origin`; also augmented by `PILOTHOUSE_ALLOWED_ORIGINS`
- `--secure-cookie` (set behind a TLS-terminating proxy)
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

**Package-owned configuration directories and runtime dependencies.**
`.goreleaser.yaml`'s `nfpms[0].overrides.<format>.contents` declares two
`type: dir` entries in both formats — `/etc/pilothouse` (`root:pilothouse`,
mode `0750`, group-readable so the units' `EnvironmentFile=` works) and
`/etc/pilothouse/storage/credentials` (`root:root`, mode `0700`, stricter than
its parent because only the root broker reads remote-mount secrets).
`/run/pilothouse` and `/var/lib/pilothouse` are deliberately not packaged;
the broker unit's `RuntimeDirectory=`/`StateDirectory=` own them. Runtime
dependencies are declared per format, naming the direct provider of each
role rather than relying on transitive requires — deb: `libc6`, `libpam0g`,
`libpam-modules`, `libpam-runtime`, `libsystemd0`, `systemd`; rpm: `glibc`,
`pam-libs`, `pam`, `authselect-libs`, `systemd-libs`, `systemd`. The six
roles line up one-to-one across the platforms: linked C library, PAM shared
library, PAM modules providing `pam_nologin.so`, provider of the PAM stacks
the policy includes, libsystemd shared library, and systemd itself.

**Postinstall scriptlet.** nfpm's static owner/group metadata alone does not
produce correct install-time ownership on either format: the payload is
extracted before the `pilothouse` account exists, and nfpm's DEB tar metadata
leaves numeric UID/GID at zero. `packaging/postinstall.sh` is wired once as
`nfpms[0].scripts.postinstall` (nfpm's `scripts` key is not per-format, so the
same script is the deb `postinst` and the rpm `%post`). It creates the account
by invoking `systemd-sysusers` scoped to this package's own
`/usr/lib/sysusers.d/pilothouse.conf` — never bare — falling back to a
guarded `groupadd`/`useradd` pair reproducing the sysusers declaration
(system account, primary group `pilothouse`, home `/nonexistent`, shell
`/usr/sbin/nologin`) when `systemd-sysusers` is absent. It then re-applies
`root:pilothouse` 0750 to `/etc/pilothouse`, `root:root` 0700 to
`/etc/pilothouse/storage/credentials`, and `root:pilothouse` 0640 to each env
file *that still exists* (both are `type: config`, so a deliberate deletion
must survive an upgrade rather than fail it). `set -e` is its first effective
line, so any failing repair exits non-zero — on Debian that leaves the package
not-configured; on RPM the payload stays installed but the scriptlet error is
reported, since a `%post` runs after extraction and cannot roll back its
transaction. `PILOTHOUSE_ROOT` is a test-only seam: it defaults to empty (so
production operands are exactly the paths above) and prefixes *only* the three
filesystem operands the script hands to `chown`/`chmod`/`[ -e ]`, letting
`packaging/postinstall_test.go` run the real script against a `t.TempDir()`
with PATH-injected `chown`/`chmod`/`systemd-sysusers` fakes. The
sysusers positional config path (relocated by `--root` instead) and the
fallback account's home and shell values stay unprefixed. Nothing here runs
privileged Go code or touches the broker socket — it is package-manager
shell, invoked outside the running daemon.

**Configuration-assertion test.** `packaging/goreleaser_config_test.go` parses
`.goreleaser.yaml` with `gopkg.in/yaml.v3` — a direct module requirement as of
this test — into a minimal set of structs covering only the `nfpms` shape this
repository owns. It deliberately does **not** decode into GoReleaser's own
`config.Project` type and performs no schema validation: that type cannot
represent this repo's GoReleaser Pro `nightly` block, and the goal is to pin
*this repository's* packaging contract, not GoReleaser's schema. Unknown keys
are ignored, so `builds`, `archives`, `changelog`, `release`, and `nightly`
parse and are discarded. What it pins: each format's override contains exactly
one `/etc/pam.d/pilothouse` entry and exactly one
`/usr/lib/systemd/system/pilothoused.service` entry, sourced from that
format's own file, with the *other* format's PAM policy and unit asserted
absent from the same list; both dependency lists match their six expected
elements exactly when sorted (so an extra, missing, or duplicated element
fails); both configuration directories and both env files carry the
type/mode/owner/group above in both formats; `scripts.postinstall` is
`./packaging/postinstall.sh`; `formats` is exactly `[deb, rpm]`; and no
content entry in either format installs to `/run/pilothouse` or
`/var/lib/pilothouse` or anything under them — a plain-prefix check that also
rejects a near-miss sibling like `/run/pilothouse-helper` on purpose, and that
is deliberately *broader* than the artifact-level rule below; see "The
forbidden roots" for why the two must not be harmonized. The comparison helpers
(`checkDependencies`, `checkNoSrc`, `checkNoSystemdManagedPaths`) return errors
rather than asserting inline, so companion tests can mutate a *test-local deep
copy* of the parsed data and prove each check actually fires — the real file is
never written. The test needs no container: it only reads a YAML file from
disk, so it runs under plain `make test` as well as `make docker-ci`.

### Artifact contract model (`packaging/model.go`, `packaging/finding.go`, `packaging/contract.go`, `packaging/verify.go`)

The `packaging` directory is a real Go package, not only a home for tests. It
holds the data types an artifact is described with, the codes a violation is
named by, the destinations the contract requires, the two roots it forbids, and
`Verify`, which reports every violation the contract names.

**Model types** (`packaging/model.go`). `Format` is a string type with exactly
two values, `FormatDeb` (`"deb"`) and `FormatRPM` (`"rpm"`). `Entry` describes
one installed file: `Dest`, `Mode` (`fs.FileMode`), `Config` (whether the
packaging metadata designates it a configuration file), `Content`, plus `Owner`
and `Group`. `Scriptlet` holds a maintainer script's `Content`. `Model` ties
them together: `Format`, `Entries`, `Dependencies`, and `Postinstall`, a
`*Scriptlet` whose nil value is the representation of "this package ships no
postinstall scriptlet" — a state the model keeps distinct from shipping a
scriptlet with unexpected bytes. Populating a `Model` from a real artifact is
out of scope for this package: both extractors live in the `packaging/extract`
subpackage ("Artifact extraction" below).

**M1 — modes are asserted from the payload; ownership is proved by the
scriptlet; owner and group are never asserted.** `Entry.Mode` drives a real
assertion (see `wrong_mode` below). `Entry.Owner` and `Entry.Group` do not:
they exist for the convenience of an extractor that happens to surface them and
no code in this package reads them. Whether an extractor fills them in is a
per-backend detail, never one claim about "the extractors": the **deb** backend
leaves both empty, because a `dpkg-deb`-extracted tree cannot recover the
archive's recorded ownership, while the **rpm** backend populates both per entry
from the `%{FILEUSERNAME}`/`%{FILEGROUPNAME}` header tags. Neither changes what
this package asserts, because it reads neither field.
An artifact cannot prove installed ownership: nfpm's
DEB payload records numeric UID/GID 0 by construction (which is why #66 added
the postinstall scriptlet), and the RPM equivalent is unconfirmed against a
real nfpm build.
`grep -nE '\.Owner|\.Group' packaging/contract.go packaging/verify.go` printing
nothing is the mechanical form of that half of the rule.

The positive half is the scriptlet check below. Correct install-time ownership
is produced by `packaging/postinstall.sh` and by nothing else — the **Postinstall
scriptlet** paragraph above describes what that script does and
`packaging/postinstall_test.go` proves it, by running the real script. So this
package proves ownership the only way an artifact can: by asserting that the
package **ships exactly that script**, present and byte-for-byte. A package
whose payload claimed `root:pilothouse` would prove nothing; a package that
ships the scriptlet whose behavior is already pinned proves everything the
artifact is capable of proving. On-disk ownership after a real install is
asserted outside this Go package, by `packaging/verify-install.sh`
("Install validation" below); ownership on a booted host is Layer B's, verified
by the booted-VM harness in `test/vm` and run in CI by
`.github/workflows/packaging.yml`'s `vm-boot` job.

**Finding shape and code vocabulary** (`packaging/finding.go`). `Finding` has a
`Code`, a `Path`, and a `Message`. `Code` is a stable exported string — this
package's tests and `cmd/verify-packages` both key off it, so its value is part of
the contract, and `packaging/finding_test.go` pins each value literally and
asserts the nine are pairwise distinct. `Path` is the destination a finding
concerns and is empty for findings that are not path-scoped. `Message` is
human-readable and deliberately unstable; nothing may match on its wording.

The nine codes are `missing_path`, `wrong_mode`, `wrong_content`,
`missing_config_flag`, `dependency_mismatch`, `forbidden_path`,
`duplicate_entry`, `missing_scriptlet`, and `unknown_format`. The last two are
additions to the explicitly-minimal list in the issue: a missing scriptlet is
not path-scoped, so reporting it as `missing_path` would require inventing a
fake `Path`, and a code for an unrecognised `Format` is what keeps a zero-value
`Model` from ever being accepted as satisfying the contract.

**Embedded repository sources** (`packaging/contract.go`). The nine files in
this directory that the packages ship — `pilothouse.service`,
`pilothouse.pam`, `pilothouse.sysusers`, `pilothouse.env`, `pilothoused.env`,
`postinstall.sh`, `deb/pilothoused.service`, `rpm/pilothouse.pam` and
`rpm/pilothoused.service` — are compiled into the package with `//go:embed` and
read through the unexported `sourceBytes(name)`, which panics on a name that is
not embedded (the set is fixed at compile time, so a miss is a programming
error, not runtime input). This embed is why the artifact-contract code lives
in `packaging/` rather than under `internal/`: a `//go:embed` pattern may not
contain `..`, and `Verify`'s signature is fixed, so there is no seam through
which an `fs.FS` could be injected instead. The embedded bytes are consumed by
`Verify`'s content comparison (below) and by the test fixture, which builds a
contract-satisfying `Model` from the same real repository sources — neither
reads the working tree.

**The requirement table** (`packaging/contract.go`). `requirements(format)`
returns the contract table for a format, and `false` for a format this package
does not know. Both `deb` and `rpm` require the same ten destinations:
`/usr/lib/systemd/system/pilothouse.service`,
`/usr/lib/systemd/system/pilothoused.service`, `/etc/pam.d/pilothouse`,
`/usr/lib/sysusers.d/pilothouse.conf`, `/etc/pilothouse`,
`/etc/pilothouse/storage/credentials`, `/etc/pilothouse/pilothouse.env`,
`/etc/pilothouse/pilothoused.env`, `/usr/bin/pilothouse` and
`/usr/bin/pilothoused`. A row also carries the mode the contract pins for the
destination, whether the packaging metadata must designate it a configuration
file, and the embedded repository source whose bytes the entry must equal. The
table's rows carry only the fields the checks that exist actually read; a field
is added by the change that first asserts on it, so no field is ever dead.

A row's `mode` is zero for every destination `.goreleaser.yaml` gives no
`file_info`, and zero means "the contract states no mode, assert nothing" —
inventing a default would pin something the source of truth does not state. The
four destinations that do carry one are `/etc/pilothouse` (**0750**),
`/etc/pilothouse/storage/credentials` (**0700**),
`/etc/pilothouse/pilothouse.env` (**0640**) and
`/etc/pilothouse/pilothoused.env` (**0640**). The three config-designated
destinations are `/etc/pam.d/pilothouse`, `/etc/pilothouse/pilothouse.env` and
`/etc/pilothouse/pilothoused.env`. Both sets are identical in `deb` and `rpm`.

**Provenance: which entries are byte-compared, and which are deliberately
not.** A row's `source` names the embedded repository source the destination is
built from, and the empty string means "the contract compares no content here".
**Six** destinations are byte-compared, and the `deb` and `rpm` tables differ in
exactly the two rows whose source is per-format:

| Destination | `deb` source | `rpm` source |
| --- | --- | --- |
| `/usr/lib/systemd/system/pilothouse.service` | `pilothouse.service` | `pilothouse.service` |
| `/usr/lib/systemd/system/pilothoused.service` | `deb/pilothoused.service` | `rpm/pilothoused.service` |
| `/etc/pam.d/pilothouse` | `pilothouse.pam` | `rpm/pilothouse.pam` |
| `/usr/lib/sysusers.d/pilothouse.conf` | `pilothouse.sysusers` | `pilothouse.sysusers` |
| `/etc/pilothouse/pilothouse.env` | `pilothouse.env` | `pilothouse.env` |
| `/etc/pilothouse/pilothoused.env` | `pilothoused.env` | `pilothoused.env` |

The **four** destinations that are deliberately *not* content-compared are
`/usr/bin/pilothouse` and `/usr/bin/pilothoused` — a binary's bytes differ per
build (version stamps, build IDs, toolchain), so only its destination and
multiplicity are contract-relevant — and the two directory entries
`/etc/pilothouse` and `/etc/pilothouse/storage/credentials`, which have no
content at all. Whatever an extractor records at those four, including nothing,
is not a finding.

**The forbidden roots, and the deliberate divergence from the
configuration-level check** (`packaging/contract.go`, `packaging/verify.go`).
`forbiddenRoots` is `/run/pilothouse` and `/var/lib/pilothouse` — the two
directories the broker unit owns through `RuntimeDirectory=` and
`StateDirectory=`. systemd creates and removes them at unit start and stop with
the ownership the broker needs, so a package-owned copy would fight it and the
package must install **nothing** there. The slice is named `forbiddenRoots`
rather than the obvious `systemdManagedPaths` because
`goreleaser_config_test.go` already declares that name in this same package,
the same collision `contractDependencies` avoids.

Containment is **component-aware**: a destination `d` violates a root `m` when
`d == m` or `strings.HasPrefix(d, m+"/")`. Whole path components are compared,
so both the bare root and a nested descendant (`/run/pilothouse/broker.sock`,
`/var/lib/pilothouse/storage/mounts`) are reported, and a sibling that merely
shares a textual prefix — `/run/pilothouse-helper` — is not.

That rule is **intentionally narrower** than
`goreleaser_config_test.go`'s configuration-level `checkNoSystemdManagedPaths`,
whose plain `strings.HasPrefix` over the same two roots *does* reject
`/run/pilothouse-helper`, on purpose and as its own comment states: a
destination written into `.goreleaser.yaml` that merely looks like a managed
root is far likelier to be a typo for it than a deliberate path, and this
repository configures none. The two are **not in conflict** — anything
genuinely nested under a root is rejected by both, and they differ only on
names sharing a textual prefix without sharing a path component, where the
configuration check is the stricter of the two. The narrower rule belongs in
`Verify` because it judges a real artifact's payload, where an entry at
`/run/pilothouse-helper` is a path the package genuinely owns and no
systemd-managed directory is being fought over. **Do not "harmonize" the two**:
`checkNoSystemdManagedPaths` and the `systemdManagedPaths` slice it reads stay
exactly as they are, and `packaging/verify.go` carries a comment saying so, so
that neither side is "fixed" into the other later.

**The scriptlet source** (`packaging/contract.go`). The postinstall scriptlet's
expectation is the lone constant `postinstallSource = "postinstall.sh"` rather
than a row in the requirement table, because the scriptlet is not path-scoped:
nfpm's `scripts` key is a single value with no destination and no per-format
variant, so every field a row carries — `dest`, `mode`, `config` — is
meaningless for it. Its bytes come from the same `//go:embed` as the payload
sources, so the scriptlet assertions read nothing at run time. (Contrast
`packaging/postinstall_test.go`, which deliberately `os.ReadFile`s the same
script and execs `shellcheck` and `/bin/sh` against it: that file proves the
script's *behavior*, this package proves the artifact *ships* it.)

**The dependency lists** (`packaging/contract.go`). `contractDependencies(format)`
returns the runtime dependency list the contract requires for a format, and
`false` for a format this package does not know. It is named
`contractDependencies` and not the more obvious `wantDependencies` because
`goreleaser_config_test.go` already declares `wantDependencies` in this same
package for the configuration-level assertion. Each list names the **direct**
provider of the same six runtime roles on both platforms — the linked C
library, the PAM shared library `pilothoused` links via cgo, the package
providing the PAM modules the policy loads, the package providing the PAM
stacks the policy includes, the libsystemd shared library, and systemd itself:

| Role | `deb` | `rpm` |
| --- | --- | --- |
| C library | `libc6` | `glibc` |
| PAM shared library | `libpam0g` | `pam-libs` |
| PAM modules | `libpam-modules` | `pam` |
| PAM stacks the policy includes | `libpam-runtime` | `authselect-libs` |
| libsystemd shared library | `libsystemd0` | `systemd-libs` |
| systemd | `systemd` | `systemd` |

**Provenance:** both lists are hand-written constants transcribed from
`.goreleaser.yaml`'s per-format `overrides.<format>.dependencies` at
`b1294e1`. No non-test file in this package reads that file — keeping the
expectation hand-written is what makes it an independent statement of the
contract rather than a restatement of whatever the config happens to say. Tying
the two together mechanically is the job of the drift guards in
`packaging/drift_test.go` ("The drift guards" below), which do read the live
config.

**Comparison, order-independent and multiplicity-sensitive.** `Verify` compares
*sorted clones* of the declared and the expected list with `slices.Equal`,
never mutating the model. Nothing in the contract fixes the order the packaging
metadata lists dependencies in, so order is not asserted; but the comparison is
on slices, not set membership, so a missing, extra, **duplicated** or misspelled
element all fail — including a list that repeats one name and omits another,
whose set of names would still be a subset of the contract's.

**The alternatives rule (N2).** Debian permits alternatives
(`libc6 | libc6-udeb`), which satisfy a requirement only by accident of which
alternative the resolver picks. The contract requires plain package names, so
**any declared expression containing `|` is reported on its own**, independently
of the sorted comparison and once per offending expression. A list carrying both
faults — an otherwise-correct list with one element rewritten as an alternative
— therefore produces two findings: the whole-list mismatch, and one naming that
expression.

**`Verify`** (`packaging/verify.go`). The signature is
`func Verify(m Model) []Finding`, fixed by the issue. **`Verify` accumulates:**
no check returns early and no check is skipped because an earlier one produced
a finding, so the result holds every independent violation the model exhibits,
and a nil or empty result means the model satisfies the contract — at this
commit every assertion the contract calls for is implemented, so a clean result
is a complete verdict rather than a partial one.

`Verify` performs exactly ten checks at this commit:

- **`unknown_format`** when `Format` is neither `deb` nor `rpm`, including the
  zero-value `Model`. The finding is not path-scoped, so its `Path` is empty.
  An unknown format produces no other finding: the contract for such a package
  is unknown, not violated.
- **`missing_path`**, with `Path` set to the destination, for each of the ten
  required destinations no entry installs to. A file installed somewhere else —
  a binary under `/usr/local/bin`, say — leaves its contract destination
  uninstalled and is reported this way.
- **`duplicate_entry`**, with `Path` set to the destination, when two or more
  entries install to the same required destination — the multiplicity half of
  "exactly one entry at `/usr/bin/pilothouse` and one at
  `/usr/bin/pilothoused`", applied uniformly to all ten. Exactly **one**
  finding is emitted per duplicated destination however many copies there are:
  findings are identified by their `Code`/`Path` pair, so the N−1 further
  identical findings a per-copy rule would emit carry no information.
- **`wrong_mode`**, with `Path` set to the destination and a `Message` naming
  want and got in octal, when the entry's mode differs from the one the
  contract pins. Only the four modes listed above are asserted.
- **`missing_config_flag`**, with `Path` set to the destination, when an entry
  at one of the three config-designated destinations has `Config` false.
- **`wrong_content`**, with `Path` set to the destination and a `Message`
  naming the expected `packaging/<source>`, when the entry's bytes are not
  exactly the bytes of the source the table pins for that destination in that
  format. Only the six byte-compared destinations above are checked. This is
  the check that makes "the `rpm` shipped the `deb`'s PAM file" detectable at
  all: such a package installs a valid file at the right destination with the
  right mode and config designation, and nothing but the bytes gives it away.
- **`dependency_mismatch`**, with `Path` **empty** — a dependency concerns no
  destination — in the two independent shapes described above: one finding
  naming got and want when the sorted declared list differs from the format's,
  and one further finding per declared expression containing `|`, naming that
  expression. The check is skipped entirely for an unknown format, for the same
  reason the destination checks are: the contract for such a package is
  unknown, not violated.
- **`missing_scriptlet`**, with `Path` **empty**, when `Postinstall` is nil —
  the model's representation of "this package ships no postinstall scriptlet".
  The distinct code (rather than `missing_path`) exists because the scriptlet
  has no destination to name and because `cmd/verify-packages` has to tell "no
  scriptlet" apart from "wrong scriptlet".
- **`wrong_content`**, with `Path` **also empty** and a `Message` naming
  `packaging/postinstall.sh`, when the scriptlet's bytes are not that embedded
  source's. This is the one `wrong_content` finding that is not path-scoped;
  the empty `Path` is exactly what distinguishes it from a payload entry's.
  Both scriptlet checks are gated on a known format for the same reason
  everything else is.
- **`forbidden_path`**, with `Path` set to the *offending entry's own*
  destination (not the root it violates), for every entry installed to a
  systemd-managed root or to anything nested under one — see the forbidden
  roots below. Exactly **one** finding per offending destination however many
  entries install there, for the same (`Code`, `Path`) reason
  `duplicate_entry` is reported once per destination, and gated on a known
  format like everything else. An entry at a destination the contract neither
  requires nor forbids is *not* a finding: a real `.deb`/`.rpm` also carries
  `/usr/share/doc` and similar tooling artifacts, so the contract is "these
  files, correct, and never the forbidden roots", not "exactly these files and
  nothing else".

Three rules keep the mode, config and content checks honest:

- **Permission bits only.** Modes are compared as `Entry.Mode.Perm()`, so an
  extractor that sets `fs.ModeDir` on the two directory entries is not falsely
  reported; type bits are not part of the contract.
- **An unexpected config designation is not a finding.** The contract pins a
  minimum and the vocabulary has no `unexpected_config_flag`, so
  `missing_config_flag` is the only asserted direction — designating, say, the
  sysusers file a config file passes.
- **The *payload* content comparison is exact and normalizes nothing.** It is a
  plain `bytes.Equal` against the embedded source: a payload entry extracted
  from a real archive carries the shipped bytes verbatim, so a single added
  newline is reported like any other difference. A byte-compared entry whose
  `Content` is `nil` is reported too, since every embedded source is non-empty —
  an extractor that failed to capture the bytes must not verify clean, or
  `packaging/extract`'s own bugs become invisible.

A duplicate does not suppress the other checks for its destination: `Verify`
evaluates the first entry installing there for mode, config designation and
content, and still accumulates.

**The scriptlet rule: exact bytes, with at most one trailing newline
normalized.** The scriptlet comparison is the single place any normalization
happens, and it is deliberately as narrow as it can be: `trimFinalNewline`
applies `bytes.TrimSuffix(x, []byte("\n"))` — **at most one** trailing `"\n"` —
to each side, and then compares with the same exact `bytes.Equal`.
`packaging/postinstall.sh` ends with exactly one newline, so the rule has
exactly three consequences, all intended:

| Scriptlet `Content` | Result |
| --- | --- |
| the script with its single trailing newline removed | clean |
| the script exactly as the repository holds it | clean |
| the script with one further newline appended | `wrong_content` |

That asymmetry is the design, not an oversight. The normalization exists for
one reason only — whether a script is shipped with or without a *final* newline
is not a contract violation, and an extractor may present it either way. The
rejected alternative, `bytes.TrimRight(x, "\n")`, strips *all* trailing
newlines and would silently accept a script padded with arbitrary blank lines:
a real byte difference in a file whose entire contract is "ships exactly these
bytes".

**No shell parsing.** Nothing in the scriptlet check tokenizes shell, applies a
regular expression to the script, or looks for any individual command inside
it — `grep -nE 'chown|chmod|systemd-sysusers' packaging/verify.go` prints
nothing. That is not a gap to be filled later. The script's fail-fast,
idempotent, presence-guarded repairs are already proven by
`packaging/postinstall_test.go`, which runs the real script; all this package
has to add is that the artifact ships exactly those bytes (M1 above).

**The accumulate guarantee, demonstrated end to end.** Those ten checks are the
whole of the contract `Verify` asserts — every assertion the artifact contract
calls for is implemented. What the drift guards below add is not a further
`Verify` check: they judge the contract *tables* against `.goreleaser.yaml`,
never a `Model`.
Because the check list is complete, the accumulate guarantee is now proven
rather than merely stated: `TestVerifyAccumulatesEveryFaultAtOnce` seeds **one**
model with eight unrelated faults spanning **seven** distinct codes — a missing
directory, a wrong env-file mode, a cleared PAM config designation, a perturbed
unit file, a duplicated binary, a dropped dependency, a mutated scriptlet and an
entry under a forbidden root — and matches the whole result as a **set** of
(`Code`, `Path`) pairs with `require.ElementsMatch`, independent of order and
index. No check may return early or be skipped because an earlier one fired, or
that assertion loses pairs. `TestVerifyProducesEveryFindingCode` closes the
other half: each of the nine codes declared in `packaging/finding.go` is
produced by a model that breaks exactly the thing that code names.

`packaging/verify_test.go` holds the behavioral tests: the shared
`contractModel(t, format)` fixture that every mutation starts from, a
`findingsFor(findings, code, path)` matcher (findings are matched by the
`Code`/`Path` pair; a `Message` is read in only two places, each matching a
required substring and never the surrounding wording), and table-driven
mutations covering
each required destination in each format (missing, then duplicated), a
relocated binary in each format, a wrong mode on each of the four pinned
destinations, a cleared config designation on each of the three, a perturbed
and then a `nil` `Content` on each of the six byte-compared destinations, and
pairwise combinations of unrelated faults (a duplicate with a missing path, a
wrong content with a missing path, a dependency or scriptlet fault with a
missing path) on top of the whole-model accumulation proof described above.
The dependency table is N2's five
faults in each format, ten cases in all: a missing, an extra, a duplicated and
a misspelled element, and an element rewritten as an alternative
(`libc6 | libc6-udeb` for `deb`, `glibc | glibc-minimal-langpack` for `rpm`),
with three companion tests pinning the rules those cases rest on — the
fixture's list reversed verifies clean (order-independence), replacing one
element with a duplicate of another is reported even though the list keeps its
length and every declared name remains one the contract expects
(multiplicity), and the alternative case reports two findings, one of which
names the offending expression (independence from the sorted comparison) — the
first of the two `Message` reads.

The scriptlet cases run in each format too: `Postinstall` set to nil yields
`missing_scriptlet` with an empty `Path`, and two byte mutations — a line
dropped and a command appended — each yield `wrong_content` with an empty
`Path` and a `Message` naming `packaging/postinstall.sh`, which is the second
`Message` read. Three further cases pin the newline rule exactly as implemented
(final newline removed → clean, shipped unchanged → clean, one extra newline
appended → `wrong_content`); nothing asserts that an appended newline verifies
clean, and the test first asserts the premise that `packaging/postinstall.sh`
ends with exactly one newline. Both scriptlet faults are also shown coexisting
with an unrelated `missing_path`, and a model with a broken scriptlet *and* a
broken payload entry produces two `wrong_content` findings told apart by `Path`
alone. The scriptlet mutations name their target positionally, never by what
the affected line does, which is the same posture the production check takes.

The forbidden-path cases use `withExtraEntry`, which adds an entry *alongside*
everything the contract requires (and fails if the fixture already installs
there) — a package owning a systemd-managed path ships an extra entry, it does
not relocate a contract one. Eight mutation cases run the two roots and the two
nested descendants in each format, each asserting `forbidden_path` at the
entry's own destination and nothing else; a further test pins one finding per
destination when two entries install to the same forbidden path. A dedicated
test then pins the deliberate divergence described above — an entry at
`/run/pilothouse-helper` or `/var/lib/pilothouse-helper` yields **no**
`forbidden_path` finding here, while `checkNoSystemdManagedPaths` rejects that
sibling at configuration level on purpose — and its comment, like
`packaging/verify.go`'s, records that the configuration test must not be
changed to match.

Two cross-format tests are the point of the payload
content check: an `rpm` model carrying `packaging/pilothouse.pam`'s
bytes and a `deb` model carrying `packaging/rpm/pilothouse.pam`'s are each
reported at `/etc/pam.d/pilothouse`, and the same pair of substitutions is made
for the broker unit at `/usr/lib/systemd/system/pilothoused.service`. Four
tests pin the deliberate silences: an `fs.ModeDir`-bearing directory entry
verifies clean, changing the mode of a unit file or a binary produces nothing,
a config designation on the sysusers file produces nothing, and arbitrary or
`nil` content on either binary or either directory produces nothing. Its
expected destinations, modes, config designations, byte-compared set,
dependency names and forbidden roots are written out by hand rather than read
back from `requirements`, `contractDependencies` or `forbiddenRoots`, since
those tables are the thing
under test and may not also be the oracle — `postinstallSource` included, whose
value is written out as a literal in the scriptlet tests. The mutation helpers'
vacuity guards are load-bearing here: `withContent` fails if the bytes it
installs are the ones the entry already carried, so the cross-format tests
cannot silently degrade to no-ops if the two formats' sources ever converge,
and `withScriptlet`/`withoutScriptlet` fail the same way if the fixture ever
stops shipping a scriptlet or already carried the mutated bytes.

**The drift guards** (`packaging/drift_test.go`). Everything above rests on
tables that were *transcribed by hand* from `.goreleaser.yaml`. Two guards keep
the transcription honest, and they live in
their own file because that file is the one *artifact-contract* file that reads
the working tree — see the four tiers below, which are what keep that
statement from being confused with a claim about the whole package.

They are **not** a restatement of `goreleaser_config_test.go`. That test asserts
*the config* matches hand-written expectations; these assert *`Verify`'s tables*
match the live config. Opposite direction, and the only thing standing between
`contract.go` and silent divergence
(`docs/agents/skills/completeness-tests-need-live-source-of-truth.md` is why an
embedded snapshot of the YAML would not do: the snapshot and the tables would
drift together, undetected).

**Guard 1 — `TestContractTablesMatchGoreleaserConfig`.** It parses
`../.goreleaser.yaml` through `goreleaser_config_test.go`'s existing
`goreleaserConfigPath` constant and its `loadNFPMEntry`, `loadOverride`,
`normalizeSrc` and `entriesWithDst` helpers, so it holds no second copy of the
configuration. Per format it asserts:

- **Converse direction.** Every `dst` a format's override actually packages
  appears in `requirements(format)`, so adding a packaged file to
  `.goreleaser.yaml` without updating `contract.go` fails here.
- **Forward direction.** Each of the **eight** requirements whose `dest` is not
  under `/usr/bin` is installed by exactly one live entry, and the row's three
  metadata fields agree with it. Two of the three comparisons are conditional,
  because the config has an absent case for each:
  - **source** — when the row's `source` is empty the entry must carry no `src`
    *and* be `type: dir`; otherwise `normalizeSrc(entry.Src)` must equal
    `"packaging/" + source`. The split is forced: the two directory entries
    (`/etc/pilothouse`, `/etc/pilothouse/storage/credentials`) are the four
    `type: dir` entries in the file — two per format — and none carries a `src`
    key, and `normalizeSrc("")` is `""`, which can never equal `"packaging/"`.
    The `type: dir` half is what keeps the directory branch from degenerating
    into "assert nothing".
  - **mode** — when the entry has `file_info` the row's mode must equal
    `fs.FileMode(entry.FileInfo.Mode)` (the field is an `int` holding the YAML
    octal literal, so `0750` arrives as 488); when it has none the row's mode
    must be `0`, which is the "the contract states no mode" encoding.
  - **config** — the total comparison `requirement.config == (entry.Type ==
    "config")`, so both an unrequired designation and a missing one fail.
- **Dependencies.** `contractDependencies(format)` equals the live
  `overrides.<format>.dependencies`, both sorted.
- **Scriptlet source.** `scripts.postinstall` is `./packaging/postinstall.sh` —
  the file `postinstallSource` names and whose bytes the scriptlet check
  compares against.

**What guard 1 deliberately does not cover.** The two binary destinations
`/usr/bin/pilothouse` and `/usr/bin/pilothoused` appear in no assertion above.
They are nFPM **build outputs**, not override contents: goreleaser installs each
`builds[]` entry's binary itself, so neither destination is written into
`overrides.<format>.contents` and neither exists in the config this guard walks.
That is a gap in what the config can be asked, not a hole to be closed by
widening the guard — guard 2 is their cover, and the guard's doc comment says
so.

**Guard 2 — `TestBinaryDestinationsMatchBuilds`.** The existing parser discards
the `builds` section, so this guard declares its own minimal local types
(`binaryProvenanceConfig`, `buildTarget`, `bindirEntry` — names chosen not to
collide with `goreleaserConfig`, `nfpmEntry`, `contentEntry`, `fileInfo`,
`formatOverride` or `nfpmScripts`) and parses the same path through the same
reused `goreleaserConfigPath` constant. It asserts the whole chain that makes
`/usr/bin/<binary>` a contract destination in the first place: `builds[].binary`
is exactly the two-element set `{pilothouse, pilothoused}`; `nfpms[0].bindir` is
unset, so nFPM's `/usr/bin` default applies; the **complete multiset** of
`requirements(format)` destinations under `/usr/bin` equals the set the builds
produce, in **both** formats; and no override content entry in either format
installs to `/usr/bin` or anything under it — which is what confirms the
binaries are genuinely outside guard 1's reach rather than merely overlooked by
it.

The multiset comparison is deliberate and the guard is wrong without it. Merely
asserting that each expected binary has exactly one requirement leaves the check
one-directional: guard 1 skips every `/usr/bin` row, so `contract.go` could gain
an unsupported row such as `/usr/bin/extra` and pass both guards. Comparing the
whole sorted set rejects a missing destination, a duplicate, and an extra one
alike.

Neither guard has a companion mutation test, and that is deliberate: the thing
they guard is `contract.go` itself, so the way to demonstrate they fire is a
*temporary local edit to its tables* (a changed source name, mode, config flag,
dependency or binary destination, or a deleted row), never a checked-in mutation
of `.goreleaser.yaml`.

**Artifact-contract portability: four tiers, and why they must not be
blurred.** The one claim
that holds without qualification across all of this work is: **no file added by
the artifact-contract phase executes an external command.** That phase is #70's,
and it added only files in `packaging/` itself; the extractors added since are a
*separate package* and are tier (d) below, and `cmd/verify-packages` — which
runs them — is not under `packaging/` at all. Below that, the four tiers
differ, and a sentence true of one is false of another.

- **(a) The contract model, `Verify`, and their behavioral tests** —
  `packaging/model.go`, `finding.go`, `contract.go`, `verify.go`,
  `finding_test.go` and `verify_test.go`. Pure Go over bytes embedded at compile
  time by `//go:embed`. They read **no file** and run **no command** at run
  time; every expected byte comes from the FS compiled into the test binary.
  `grep -nE 'os\.ReadFile|os\.Open|os\.Stat|os/exec|exec\.Command'` over those
  six files prints nothing.
- **(b) The drift guards** — `packaging/drift_test.go`, plus
  `goreleaser_config_test.go` (which declares the `goreleaserConfigPath`
  constant and the loader they share) and the drift guard in
  `verify_install_test.go`. These files **do** read the live
  `../.goreleaser.yaml` from the working tree, deliberately: a guard
  compared against an embedded snapshot could not detect drift at all. Reading
  the live config is the named exception, they read that one path plus, in
  `verify_install_test.go`'s case, `verify-install.sh` itself, and
  `drift_test.go` and `goreleaser_config_test.go` still run **no external
  command** — so they run under a plain `go test ./packaging/` on any machine
  with the repository checked out.
- **(c) The pre-existing configuration-level tests and the install-script
  guards** — `packaging/units_test.go`, `packaging/postinstall_test.go` and
  `packaging/verify_install_test.go`. These **do** exec external tools:
  `systemd-analyze` (`units_test.go`), `shellcheck`
  plus `/bin/sh` (`postinstall_test.go`, which runs the real scriptlet against a
  `t.TempDir()`), and `shellcheck` again (`verify_install_test.go`, which only
  lints `verify-install.sh` — it never executes it, because that script needs a
  package manager and a network). The optional tools are resolved with
  `exec.LookPath` and
  their tests **skip with an explanatory message** when the tool is absent from
  `PATH`; `/bin/sh` is invoked directly, as a POSIX shell is assumed present.
  The skipping is exactly why **`make docker-ci` remains the full gate** — the
  dev image installs the systemd package and `shellcheck`, so those checks
  actually run there and quietly skip elsewhere.
- **(d) The extractors** — `packaging/extract/`. A **different package**,
  and the only tier here whose *non-test* code runs an external command: `Deb`
  invokes `dpkg-deb` three times, and `RPM` runs four `rpm -qp` queries plus one
  `rpm2archive`-into-`tar` pipe. It is a subpackage precisely so the
  guarantee below stays scoped to `packaging/*.go` — the glob is **not**
  recursive, so `packaging/extract/*.go` is outside it and tier (d) added
  nothing to that listing. Its tool-dependent tests resolve every tool through
  `internal/packagingtest.LookTool`, so they skip with an explicit reason on a
  host without it and **fail** rather than skip inside `make docker-ci`; the deb
  missing-tool test drives `PATH=""`, and the internal tables over the
  unexported `conffiles`/`Depends` parsers and over the rpm file-table,
  dependency-pairing and postinstall parsers read bytes the test itself staged,
  so none of them needs a tool on any host.

Because of (c), no sentence anywhere may claim that the `packaging` package's
*whole test suite* runs no external command, nor that the contract tests *as a
group* operate only over embedded bytes — the drift guards do not. Claims have
to name the tier they describe. At this commit,
`grep -lE 'os/exec|exec\.Command' packaging/*.go` lists exactly
`postinstall_test.go`, `units_test.go`, `verify_install_test.go` and
`vm_harness_test.go`. The extractors in tier (d) and the image-test subpackage
remain outside that non-recursive listing. Within `packaging/imagetest`,
`image_child_test.go` imports `os/exec`, while `image_process_test.go` is a
textual match only because its adversarial source fixtures contain both search
terms. The VM and image harnesses are later test-infrastructure layers, not a
fifth artifact-contract tier, and these greps are source inventories rather
than proof of which files execute children.

**Image-tier process-test foundation (#80, first slice).**
`packaging/imagetest/image_child_test.go` adds deterministic test support only;
there is no `test/image/` shell harness yet. One `t.TempDir()` owns `cwd`, `home`,
`runner`, `tmp` and `bin` subdirectories. This is the complete test-owned
workspace advertised through environment and cwd, not a filesystem or network
namespace. Children receive only the explicit environment. The tier's sole
child constructor applies a one-second context deadline, starts a dedicated
process group and kills the whole group on expiry. Stdout and stderr use
anonymous regular files created below `tmp`: inherited descriptors cannot
hold a pipe open, no named filesystem entry remains for the child to reopen,
and a no-op child leaves a whole-sandbox snapshot unchanged. A read retains
at most 4 MiB and reads one additional sentinel byte to reject larger output;
this is a read-side memory bound, not a write-side disk quota. A recursive
snapshot records each entry's stat-backed type,
ordinary and special permission bits, size, streaming SHA-256 digest for
regular-file content, and symlink target. Isolated self-tests prove each
recorded field, the layout and non-inherited environment, separated output
and exit status, same-process-group cleanup after both timeout and normal
parent exit, a bounded direct non-shell tool, no-op composition, the capture
read limit, and tool lookup.
Daemonizing into a different session or process group is outside the helper's
contract and must be rejected by later shell-harness policy.
Whole-workspace lifecycle and cleanup, images, registries, VMs, SELinux,
update/rollback and CI wiring remain later #80 slices.

`packaging/imagetest/image_process_test.go` now enforces the image-tier process
contract over every live, non-empty Go file in that isolated test subpackage
with Go's parser and AST,
not source-text matching. It resolves import names structurally; rejects
alternate, dot and blank imports of the restricted packages; closes the
allowed `os/exec`, `context`, `os.StartProcess` and build-constraint surface;
and permits only the four named, shape- and count-pinned `syscall` selectors
needed to create and kill the child process group. The isolated package cannot
reach legacy unexported packaging-test helpers, every new local helper joins
the audited file set, and new dependencies are rejected. Restricted import
names may not be shadowed. The one
`CommandContext` must consume the uniquely bound timeout context in
`imageRunChild`; neither that context nor its uniquely bound cancel function
may be reassigned, aliased by address or used anywhere beyond their exact
binding/call/defer positions. The resulting `command` may not be aliased or
used to mutate its cancellation or process identity outside the pinned
process-group flow. Process-group setup, cancellation registration and
the single `Run` execution must be unconditional top-level runner statements;
the cancellation callback has a pinned two-statement straight-line body, and
the runner has exactly one final direct result return. Critical predeclared
identifiers used by those checks may not be shadowed. The constructor call may
not hide in a function literal, and the context cancel must be deferred
directly in the runner body. A small `go/constant` evaluator
proves the package timeout is in `(0, 10s]`. The guard includes one deliberately
invalid temporary source fixture so a vacuous implementation cannot pass.
`image_guard_anchor_test.go` is compiled independently and fails if either
guarded file gains a build constraint or if the guard and process-group
runtime proofs lose any named, top-level `func(*testing.T)` test.

The guard is also validated differentially through the same directory-audit
entry point. Hand-written expectations are compared as exact finding
multisets including file and line, retaining duplicate rule/detail violations.
Clean and unusually reformatted
sources prove the guard is structural rather than text-shaped; negative
fixtures cover import aliases and shadows, every closed spawning surface,
hidden helpers in the complete package set, timeout bounds and context flow,
direct and indirect mutation, command alias and field mutation, nested calls
and cancellation, conditional process setup/cancellation/execution, pinned
process group syscalls, missing execution, early return, predeclared-name
shadowing, ill-typed constants, forbidden imports and build constraints.
This is a closed enumeration of direct Go surfaces, not a claim that arbitrary
future dependencies cannot spawn; adding a dependency or spawning mechanism
requires an explicit guard and specification expansion.

**Image-tier released-RPM fixture (#80, second slice).**
`test/image/releaserpm` is a repository test command, not a product binary and
not a package input. Its only CLI is
`go run ./test/image/releaserpm --workspace ABSOLUTE_PATH`. The workspace must
already exist, be a canonical absolute real directory rather than a symlink,
must be private to one invocation and not be mutated concurrently, and must
not contain `fixture-release-rpm`; the command creates that directory at
`0700` and refuses reuse instead of replacing caller data. On failure,
including failure to write the manifest path to standard output, it removes
only the known files it created and then removes the directory nonrecursively.
Unknown entries are never recursively deleted. On success it deliberately
leaves the fixture for the later image composer, whose enclosing ephemeral
workspace owns final cleanup.

Discovery is fixed to GitHub's
`/repos/frostyard/pilothouse/releases/latest` API. The response is bounded to
1 MiB and must identify a positive-ID, non-draft, non-prerelease release with
a strictly valid semantic-version tag. Exactly one asset name may end in
`.x86_64.rpm`; that asset must have a positive ID, the exact tag-correlated
`frostyard-pilothouse-<version>-1.x86_64.rpm` basename, a size in
`(0, 256 MiB]`, a lowercase `sha256:` digest, and the exact query-free
`https://github.com/frostyard/pilothouse/releases/download/<tag>/<name>` URL.
Userinfo is forbidden. This makes `latest` a one-time discovery input rather
than a filename used again later.

The download is bounded to the advertised size plus one sentinel byte and is
accepted only when HTTP `Content-Length` (when present), bytes written and
streaming SHA-256 all match the same release metadata. Partial files are
`0600`, synchronized and published without replacing an existing destination
only after verification. The
`0600` JSON manifest records schema/kind, release and asset IDs, tag, name,
size, digest, validated release-asset URL and the fixture-relative artifact
basename; it carries no token or host-absolute path. `GITHUB_TOKEN` is optional
for API rate limits and is attached only to `api.github.com` or `github.com`,
only over HTTPS on the default or explicit 443 port, and never to an allowed
`githubusercontent.com` redirect. Redirects have the same scheme and port
restriction. The command performs no install, image operation, upload,
publication or retention.

`main_test.go` uses an in-memory HTTP transport: no test reaches the network.
It proves the happy-path bytes, manifest, modes, request media types and lack
of partial files; rejects ambiguous architecture selection, unstable or
unidentified releases, unsafe metadata, oversized assets, URL disagreement,
HTTP/size/digest failures (including unknown-length short and overlong
streams) and non-canonical destinations; proves cancellation, response-body
closure and close-error rollback, standard-output rollback, known-entry-only
cleanup and no-replace collision preservation; and pins redirect and token
scheme/host/port/userinfo containment.

**Image-tier uCore composition (#80, third slice).**
`test/image/compose-ucore.sh --workspace ABSOLUTE_PATH --run-id LOWERCASE_ID`
consumes the second slice's manifest and RPM from the same private,
non-concurrently mutated workspace. It rechecks the artifact's exact basename,
size and SHA-256 before making any image operation, so mutation between
acquisition and composition is detected. The output directory
`fixture-ucore-images` must not already exist. It is retained inside the
caller-owned workspace on both success and failure; the eventual enclosing
job removes the whole workspace only after all bounded children have exited.
This deliberately avoids recursively deleting a container store while a
failed builder may still have a helper process unwinding. The composer does
not claim to contain a tool that deliberately detaches into another session,
does not delete or reset its Podman store, and leaves tool progress on
caller-owned stdout/stderr. The later production orchestrator must bound its
log sink, terminate and wait for composer/tool helpers, then reset this exact
Podman store and wait for reset to finish, and only then remove the enclosing
workspace. That orchestrator is deliberately outside this composition slice.

Source discovery is exactly `ghcr.io/ublue-os/ucore:latest`. Skopeo resolves
that name once to the OCI index digest. Cosign verifies the digest with the
reviewed uCore key vendored at `test/image/ucore/cosign.pub`; the raw index is
streamed through a 4 MiB limit plus one sentinel byte before it reaches disk,
then required to contain exactly one linux/amd64 member without a variant.
Cosign verifies that member digest separately. Every pull and `FROM` after
discovery names the immutable member digest. The vendored key's upstream
commit and local SHA-256 are recorded beside it in
`test/image/ucore/README.md`; an automated checksum assertion makes key
rotation an explicit code-review event.

Podman's graph root, explicit image store, runroot, libpod temporary directory
and image-download temporary directory all live below the output directory
instead of touching the caller's normal container store. The composer clears
inherited Podman connection and storage environment selectors, points both
configuration surfaces away from ambient files, clears the late
`CONTAINERS_CONF_OVERRIDE` selector, disables file events and remote mode, and
pins `--imagestore`. General configuration uses the explicit empty `/dev/null`
file. A generated mode-0600 `storage.conf` repeats the
overlay driver and all graph/image/run paths, and its path is recorded in the
fixture manifest. Normal system and per-user Podman configuration therefore
cannot redirect writes. It pulls the verified member and builds two distinct,
fixture-labelled local references: `baseline` and `update`. Both use the same
released RPM; their immutable
`/usr/lib/pilothouse-image-test/slot` markers create two deployments for the
later bootc switch/rollback test. The Containerfile installs only the local
RPM with every package repository disabled and build networking set to none,
then runs `bootc container lint`. The build context is only the released-RPM
fixture directory, never the workspace-local container store. The output
manifest records the source digests, both local refs and IDs, and all five
storage paths. There is no push, upload, host-store image or workflow wiring
in this slice. It also records the composer's effective UID. A composition
intended for the VM consumer must be run as UID 0: bootc disk installation is
rootful, and rootful Podman cannot safely reopen a rootless store. Compose,
consume, exact-store reset and workspace removal therefore stay in one
rootful ownership domain.

`packaging/imagetest/ucore_compose_test.go` executes the real composer only
against bounded fake Skopeo, Cosign and Podman tools through the image tier's
sole one-second test process helper. That deadline bounds fake-test failures;
it is not a production composition runner. Strict fake argv and environment
checks prove immutable
digest, offline build, local-storage and remote-mode boundaries. The suite
also proves both signature-failure positions, the raw-index byte cap,
ambiguous-member rejection, retained partial storage, exact manifest
contents, distinct slots and absence of a push. An effective
instruction parser prevents commented-out local-RPM installation or
`bootc container lint` from satisfying the Containerfile contract, and a
SHA-256 assertion pins the vendored key.

The fourth slice consumes these fixtures as described next. Workflow wiring
and the enclosing acquire/compose/reset/remove lifecycle remain later #80
slices.

**Image-tier uCore VM consumer (#80, fourth slice).**
`test/image/ucore-vm-test.sh --workspace ABSOLUTE_PATH [--ssh-port PORT]` is a
root-only consumer of a completed `fixture-ucore-images/fixture.json`. It
accepts no source image or artifact argument. Before booting anything it
requires the manifest's graphroot, imagestore, runroot, libpod tmpdir, image
tmpdir and storage configuration to equal their fixed paths below the supplied
canonical workspace, requires the baseline/update refs to share the one
fixture prefix, requires the recorded producer UID to be 0, and re-inspects
both local IDs. Every Podman call carries the
same explicit remote-off, root, imagestore, runroot, tmpdir, no-events,
overlay-driver and configuration isolation as the composer.

The baseline goes through `bootc install to-disk --generic-image
--via-loopback --skip-fetch-check --composefs-backend --filesystem btrfs`.
The host creates a sparse 20-GiB disk and passes one run-local root SSH public
key through bootc's `--root-ssh-authorized-keys` option. It does not mount or
write a guessed partition, so bootc owns the layout and SELinux labeling.
QEMU runs as a foreground child of the harness (backgrounded only by the
owning shell), with
KVM, q35, OVMF pflash, a raw virtio disk and one loopback SSH forward. Its
serial output and stderr remain on the caller's standard streams instead of
growing unbounded workspace log files. SSH connection attempts, copies and
every synchronous Podman operation have explicit timeouts.

`test/image/guest/validate-ucore.sh` is copied into the guest and invoked
through an explicit `sh` interpreter. It creates one random-credential,
wheel-group account solely because the broker capability and host-image
queries require authentication; this is a prerequisite, not a repetition of
#67's PAM positive/negative matrix. It neither restarts nor asserts activation
of the services #67 already covers. On the baseline, update and rolled-back
baseline it requires the immutable `/usr/lib/pilothouse-image-test/slot`
marker, enforcing SELinux and a functional `bootc status --json`. It compares
the broker's exact sorted capability list with a list produced independently
from systemd, journal, sysext, bootc, rpm-ostree and automatic-update unit-file
observations. The response must be one JSON document containing only canonical
lowercase capability identifiers; embedded line breaks and other output-shape
injections are rejected before `jq -r` can turn them into comparison lines.
Opt-in capabilities remain absent because the packaged unit configures none.
It also requires the read-only host-image broker query to
report bootc available. Shape alone is insufficient: the broker's booted,
staged and rollback image/digest pairs are normalized and compared exactly
with an independent `bootc status --json` captured in the same guest phase.

The SELinux smoke establishes a journal cursor immediately before the two
broker reads and fails on any AVC denial after that cursor. It separately
scans the current boot for AVC denials naming Pilothouse, its daemon, runtime
directory or state directory. The test does not claim a dedicated Pilothouse
SELinux domain; the released RPM ships no policy. It intentionally does not
repeat #67's directory ownership, root-login rejection, wrong-password,
journald read-back, runtime sentinel or plain-reboot posture assertions.

For update transfer, the host exports the already-local update fixture as a
job-local OCI archive, copies it into the guest, loads it with Podman and calls
`bootc switch --transport containers-storage`; there is no local registry and
no push. Before reboot the staged image name and digest must be present. After
reboot, that exact staged digest must be booted and the former booted digest
must occupy rollback. `bootc rollback` plus a second proven reboot must reverse
those two digests and restore the baseline slot marker.

The runner never uses `setsid`, `nohup`, daemonization or recursive deletion.
Its exit path stops and waits for QEMU, force-removes and waits for the one
named bootc-install container, enumerates and detaches every loop device backed
by the exact private disk (including one a killed installer left behind), and
verifies no loop reference remains. It retains both
the VM fixture directory and private container store. The later enclosing job
must invoke acquisition and composition with a bounded log sink, wait for
their helpers and this consumer, reset the exact private store and wait for
that reset, then remove the workspace. No workflow invokes the image tier yet.

`packaging/imagetest/ucore_vm_test.go` parses both harnesses with
`mvdan.cc/sh/v3/syntax` and inspects executable calls in either the selected
function body or the top-level main region. Named functions must have exactly
one definition across the complete AST, and each script's complete function
name set is fixed so a new function cannot shadow `set`, `trap`, `timeout`,
`cmp` or another reviewed builtin/program. The reviewed shell error modes must
be the first executable statements; dynamic `eval`/source calls are forbidden.
Alias, `shopt`, `enable` and hash-table mutation are forbidden as well, so
later exact command nodes cannot be rebound after parsing; quoted command names,
`command`/`builtin` options and repeated wrapper prefixes are normalized for
that check. Every command position (including a wrapped effective command) must
resolve statically from literal or quoted AST word parts; command names carrying
shell escapes and parameter expansions are rejected rather than normalized, so
they cannot hide a trap or teardown bypass.
The non-returning `fail` implementations are exact, as are the resource teardown
and bounded SSH/SCP wrapper bodies. Critical calls are matched as one exact
argument vector rather than pieced together from subsequences; unique install,
switch and foreground-QEMU actions must occur exactly once across the whole
AST. Quoted or unquoted path-qualified Podman/QEMU executables are normalized
for the same policy. The QEMU statement itself must be backgrounded and
immediately followed by its `$!` capture, and the EXIT trap must be one direct,
foreground parent-shell statement armed before the first disk/resource mutation
and disarmed only after one direct, fatal explicit cleanup. The fixture storage
paths, Podman argument array, QEMU PID and observed deployment-slot identities
become readonly after their separately fail-closed captures. Their assignment
sets are exact, so cleanup cannot be redirected to a different container store
or disk and update/rollback comparisons cannot be made self-referential. The
capture and matching readonly declaration sequences are contiguous top-level
statements; an indirect `read` or other mutation cannot be inserted into the
gap before protection.
Comparisons and
critical evidence calls must feed `|| fail`. Capability sorting is pinned
separately to the independently probed expected file and decoded broker file;
the expected and actual host-image normalizers likewise accept only their
respective `bootc status` and broker-response inputs. Every critical evidence
file has a complete, exact redirection-writer set and all of those writers must
use the reviewed command, file descriptor, operator and literal target, then
precede its comparison/scan; generic copy/move/truncate/stream writers and
every alternate sort are forbidden on the guest evidence path. Redirection-only
and compound-command statements are recorded too. The write-capable `<>` form
and every `<&`/`>&` descriptor duplication are covered alongside ordinary
output redirections. A critical writer statement must contain its one reviewed
redirection and no additional descriptor routing, so a later `1>&3` cannot
leave an empty evidence file behind. Every pathname target must be one reviewed
literal path, so descriptor changes, variable targets and `/./` path aliases
cannot evade the writer set. The broker query
helper's complete curl/output/status body is exact-pinned because its legitimate
destination is necessarily a parameter. The guest main path also has a closed
allowlist of effective command names; `stdbuf`, `chroot`, `awk`, shell
interpreters and any other unreviewed execution layer fail the guard before
they can hide a writer. Its only four command-prefix assignment sites are
closed as well: the two `LC_ALL=C` sorts and the two credential-to-`jq`
environment projections. Assignment-only statements have a closed name set,
with the constants, expected slot and work directory values pinned; a
standalone or prefixed `PATH=...` cannot alter how an otherwise reviewed
command resolves or make phase identity self-referential. Mutable tools on the
evidence path are narrowed further to their reviewed read-only argument vectors:
`journalctl`, `systemctl`, `bootc`, `rpm-ostree`, `systemd-sysext` and `sed`
cannot gain vacuum, service mutation, deployment mutation or file-writing
modes. The guest has exactly one direct `trap cleanup EXIT`, and both that
invocation and the cleanup body are exact, so an EXIT trap cannot replace a
validation failure with success. Its `log` helper is exact too, and Bash's
variable-writing `printf -v` extension is forbidden on the main path so a
reviewed assignment cannot be changed without appearing in the assignment
audit.
The two AVC scans use
`jq -Rse` predicates whose false result, read error or malformed execution all
feed the same fatal edge, leaving no mutable shell status variable. Both scans
and every core slot/SELinux/capability/host-image assertion or normalizer must
be direct foreground top-level fatal statements, so an unreachable outer branch
cannot preserve their AST while skipping them. The unique cursor
writer/decode/nonempty chain is ordered before both exact broker calls, and its
assignment set is closed, so moving or recapturing the cursor cannot exclude the
operations under test.
Successful top-level
shortcuts are rejected, and the runner's complete trap set is the one EXIT arm
plus its two reviewed disarms; an ERR/DEBUG/RETURN override cannot make a failed
command green. All three guest validation invocations must be direct, foreground
top-level statements, not calls parked behind a false conditional, and their
line ordering is anchored respectively before the switch, after the
staged-to-booted continuity proofs, and after the rollback continuity proofs.
The staged name/digest shape assertion and all four post-reboot deployment-slot
comparisons are likewise direct foreground fatal statements. The staged proof
is ordered after both status captures and before the first reboot; wrapping
continuity or cleanup in an unreachable branch cannot leave the recursive AST
looking valid. Direct fatal guards inspect both child statements for negation
and backgrounding, while the outer `||` cannot carry a redirection, so
`! cmp ... || fail` cannot invert an evidence oracle while retaining the
expected command node.
The runner's main path has closed sets for `guest_copy`, `guest_run` and
`guest_run_long`: it may transfer only the reviewed validator, credentials and
local update archive, then issue only the reviewed setup, archive removal,
local-image load, switch and rollback commands. The SSH-up, SSH-down,
broker-ready and reboot function bodies are exact alongside the copy/run
wrappers. An extra host-to-guest command therefore cannot replace the copied
validator while source guards continue inspecting the pristine repository
file.
Whole-file
negative policies include nested function bodies and recognize direct or
wrapped host Podman bypasses, wrapped/alternate push, any recursive removal,
extra bootc switches/QEMU processes and registry forms. It deliberately does
not search raw shell text: comments, string literals, no-op copies and commands
moved into an uncalled decoy function cannot satisfy the guards. The guest also
requires exactly one capability-response JSON document and binds `jq`, `sort`,
journal and AVC-filter failures directly to fatal edges so pipeline or
line-filter status semantics cannot turn malformed evidence or an inspection
error into a clean result.

**Still out of scope for this package.**

- **Reading real `.deb`/`.rpm` files.** Nothing in `packaging/` itself opens an
  artifact. The extractors that populate a `Model` from a built `.deb` and from
  a built `.rpm` both exist one directory down, in `packaging/extract`
  (tier (d) above), and the command that runs them and reports the resulting
  `Finding`s is `cmd/verify-packages` (see "The command" below). Neither is
  reachable from this package: `packaging/` imports neither, and the dependency
  runs the other way.
- **Building the packages.** `make package` builds them locally with goreleaser
  Pro, and the CI packaging job is **#72**'s; this package is exercised by
  `go test` alone.
- **On-disk state after a real install.** No Go code in this package installs
  anything. Package validation splits in two here. **Layer A** is the
  container-level install check: the shell script `packaging/verify-install.sh`
  ("Install validation" below), which this package's Go tests only read as
  text, run by `make verify-package-install` locally and by
  `.github/workflows/packaging.yml`'s `install` job in CI. **Layer B** — VM
  installs and booted-host verification, anything needing systemd as PID 1 or
  an enforcing SELinux policy — is **#67**'s; its harness lives
  in `test/vm` (it boots a guest, installs the package, starts both units,
  asserts the systemd-created directories, the broker socket's ownership and
  mode, that the broker is live, that PAM authenticates a real non-root
  administrator through the running stack, that the daemon reads a record
  it emitted itself back through the broker's journal query, and that the
  posture survives a real reboot) and `.github/workflows/packaging.yml`'s
  `vm-boot` job runs it; see M1 above
  for why an artifact cannot prove ownership and why `Entry.Owner`/
  `Entry.Group` therefore drive no assertion.

**Native build dependencies:** PAM (`libpam0g-dev`) and systemd
(`libsystemd-dev`) headers; `pilothoused` is built with `-tags sdjournal`. If
unavailable locally, use `make docker-build` / `make docker-test` /
`make docker-fmt` / `make docker-lint` / `make docker-generate`, which build
and reuse the repo's dev container image.

### Install validation (`packaging/verify-install.sh`)

`packaging/verify-install.sh` is **Layer A** of package validation: a single
POSIX `sh` script that runs **inside a target distro container image, as root**,
against a directory of built artifacts. It is not a packaged file — it is not
in `contract.go`'s `//go:embed` set, not named by any `packaging.Verify` table,
and not an nfpm content entry in `.goreleaser.yaml`. It exists because the
static artifact contract above reads bytes out of a `.deb`/`.rpm`; only a real
install can show what the package manager and the postinstall scriptlet
actually produce on disk.

**Container-only, by construction.** The script never requires systemd as PID 1,
never drives a service manager, never restarts a machine, and never depends on
an enforcing SELinux policy; anything needing a booted host is Layer B (#67).
It also never asserts `/run/pilothouse` or `/var/lib/pilothouse`: those are
deliberately unpackaged because systemd's `RuntimeDirectory=`/`StateDirectory=`
own them, and a container has no running systemd to create them.

**Shape.** `set -eu` is the first effective line, and `fail()` prints
`verify-install: <message>` to standard error and exits 1, so the **first**
failed assertion aborts the run. Usage is
`packaging/verify-install.sh <artifact-dir>` with no default: a missing or
non-directory operand is an actionable failure that prints the usage. The
package format is detected from the container's own package manager (`apt-get`
→ deb, `dnf` → rpm, neither → failure), never from an artifact's file name, and
exactly one amd64 artifact of that format (`*_amd64.deb` / `*.x86_64.rpm`) must
be present — zero or several is a failure naming what was found. arm64 install
validation is out of scope.

**What it checks at this commit** — all eight checks:

1. **Install through the distro package manager.** `apt-get update` then
   `apt-get install -y <artifact>` for deb, `dnf install -y <artifact>` for rpm.
   Never a bare `dpkg -i`/`rpm -i`: the point of this check is that the
   hand-written per-format `dependencies` lists in `.goreleaser.yaml` resolve
   against the distro's real repositories.
2. **`check_account()`** — the `pilothouse` user and group exist afterward and
   match the **installed** sysusers declaration. The account *name* is pinned:
   the script asserts the installed declaration declares exactly `pilothouse`,
   because the packaging contract is about that specific account and a file
   declaring some other valid system account must not pass. The account's
   *properties* are not pinned — home directory, shell and GECOS are parsed out
   of `/usr/lib/sysusers.d/pilothouse.conf` on the
   installed filesystem (the live source of truth, not a hardcoded copy of
   `packaging/pilothouse.sysusers`) and compared against
   `getent passwd pilothouse` / `getent group pilothouse`; the account's primary
   group must be `pilothouse` and its uid must be in the system range (< 1000).
3. **`check_owner_mode()`** — on-disk owner, group and mode read with
   `stat -c '%U %G %04a'` from the installed filesystem, never from package
   metadata, for `/etc/pilothouse` (`root:pilothouse 0750`),
   `/etc/pilothouse/storage/credentials` (`root:root 0700`), and both env files
   (`root:pilothouse 0640`).
4. **`check_pam()`** — every stack and every module the **installed**
   `/etc/pam.d/pilothouse` names exists on that distro. Both lists are parsed
   out of the installed policy at run time: the stacks are the operands of
   `@include` lines plus the files named by an `include`/`substack` control
   value, and the modules are every `pam_*.so` token (directory prefix
   stripped). Each stack must exist as `/etc/pam.d/<name>`, and each module
   must exist in one of the candidate module directories — `/lib/security`,
   `/lib64/security`, `/usr/lib/security`, `/usr/lib64/security` and
   `/usr/lib/*-linux-gnu/security` — which are **searched**, not selected by
   distro family. A policy that yields zero stacks or zero modules is an
   explicit failure, so a mis-parse cannot pass vacuously.
5. **`expect_unit()`** — `systemd-analyze verify` is run against both
   **installed** unit paths, `/usr/lib/systemd/system/pilothouse.service` and
   `/usr/lib/systemd/system/pilothoused.service`; a non-zero exit is a failure.
   `packaging/units_test.go` is the precedent for the invocation.
6. **`expect_linked()`** — `ldd /usr/bin/pilothoused` must succeed and no line
   of its output may contain `not found`. This is the check that proves the
   declared libpam and libsystemd dependencies actually satisfy the cgo-linked
   binary. `/usr/bin/pilothouse` is deliberately **not** checked:
   `.goreleaser.yaml` builds it with `CGO_ENABLED=0`, so it is static and `ldd`
   exits non-zero for it for a reason that has nothing to do with the
   dependency lists.
7. **Reinstall.** The **same** artifact is installed over the existing install
   — `apt-get install -y --reinstall <artifact>` for deb, `dnf reinstall -y
   <artifact>` for rpm — and then `check_account` and `check_owner_mode` are
   **re-invoked**. They are called a second time, not copied: the reinstalled
   state is held to exactly the assertions the first install was held to, so a
   postinstall that only repairs ownership on a fresh install fails here.
8. **Removal, asserted per format.** Each format runs its removal verbs in one
   uninterrupted sequence, so no reinstall is needed between dpkg's two verbs
   (`dpkg -P` operates on a removed-but-unpurged package). The account name is
   captured before the first verb, because the sysusers file it is read from
   goes away with the package. `check_removed_paths_gone`,
   `check_conffiles_present`, `check_conffiles_gone`, `check_no_rpmsave` and
   `check_account_survives_removal` are the named assertions, each taking the
   verb it follows so the failure message names it.

**The removal matrix, and why each cell reads the way it does.** dpkg and rpm
do not treat a `type: config` file alike on removal. The behaviour below was
measured in the two pinned images with a minimal package carrying one marked
config file, and the script asserts exactly it:

| | dpkg `remove` (`dpkg -r`) | dpkg `purge` (`dpkg -P`) | rpm `erase` (`rpm -e`) |
|---|---|---|---|
| unmodified config file | **survives** | removed | **removed** |
| modified config file | **survives** | removed | **saved as `.rpmsave`** |

- **Non-config payload, both formats.** `/usr/bin/pilothouse`,
  `/usr/bin/pilothoused`, both units under `/usr/lib/systemd/system` and
  `/usr/lib/sysusers.d/pilothouse.conf` must be **gone**. Neither manager keeps
  non-config payload, so a survivor means the package claimed ownership of a
  file it did not actually own.
- **Debian `dpkg -r`: the three conffiles survive.** `/etc/pam.d/pilothouse`
  and both env files are `type: config`, and a remove is not a purge — dpkg
  deliberately keeps local edits available for a reinstall. Asserting their
  **presence** is what pins that they really were registered as conffiles: a
  package that shipped them as ordinary files would delete them here and fail.
- **Debian `dpkg -P`: the same three are gone.** Covering both verbs rather
  than only the gentler one is the point; a purge that left a conffile behind
  would leave stale credentials-adjacent configuration on an uninstalled host.
- **Fedora `rpm -e`: the same three are gone, and no `.rpmsave` survives.**
  rpm erases an *unmodified* config file outright, so their absence is the
  correct expectation. The `.rpmsave` sweep over `/etc/pilothouse` and
  `/etc/pam.d` is the interesting half: rpm only writes a `.rpmsave` when the
  file's on-disk content diverged from what the package shipped, so a hit means
  the postinstall scriptlet modified a config file its own package shipped —
  a real defect, and invisible to every static check.
- **The account survives both managers.** `systemd-sysusers` creates the
  `pilothouse` user and group from the shipped sysusers file during the
  postinstall; neither dpkg nor rpm owns them, so neither removes them. The
  script asserts they still resolve through `getent` after every verb, so a
  future change that starts deleting the account — orphaning any file still
  owned by its uid — is noticed here rather than on a user's machine.
- **`/etc/pilothouse`'s own pruning is deliberately not pinned.** Whether an
  emptied directory that held surviving conffiles is removed varies between
  managers and versions and carries no user-visible consequence, so asserting
  it either way would be a flaky claim about implementation detail rather than
  about this package. The script asserts only about files *inside* it, and a
  guard test enforces that: no `[ ! -e /etc/pilothouse ]`-shaped assertion, and
  `/etc/pilothouse` may appear in neither expectation set.

**Why `systemd-analyze verify` is Layer A work.** It is a parser: it validates
unit *files* offline, resolving them under the filesystem it is pointed at, and
never contacts PID 1. Starting, enabling or querying a unit does need a running
service manager, so it stays Layer B (#67) — which is why `systemctl` appears
nowhere in the script and `systemd-analyze` does.

**Why the PAM expectations are derived, not tabulated.** The two formats ship
different policies (`packaging/pilothouse.pam` for deb,
`packaging/rpm/pilothouse.pam` for rpm), naming different stacks, and the
distro families keep their modules in different directories. A per-distro table
in the script would be a second copy of the packaging contract that could drift
from the shipped policy silently, and it would still say nothing about the
policy the package actually installed. Reading the installed file makes the
check follow whichever policy the format's override shipped, which is why the
script contains no stack name, no module name and no multiarch module path.

Checks 2 and 3 are shell **functions** rather than inline code, so each is a
single named unit that can be invoked more than once — which check 7 does.

**Expectation-line convention.** Every expected ownership value, every
verified unit path, the cgo-linked binary path and both removal sets are
written as one `expect_owner_mode <path> <owner> <group> <mode>`,
`expect_unit <path>`, `expect_linked <path>`, `expect_conffile <path>` or
`expect_removed <path>` call per line with literal arguments, so the Go guards
can parse the script's expectations deterministically instead of matching
free-form shell. `expect_conffile` and `expect_removed` *print* their path
rather than asserting on it, because the same set is asserted with opposite
polarity at different points of the removal sequence; the assertion functions
iterate over `conffile_paths` and `removed_paths` and decide the polarity.

**Go guards** (`packaging/verify_install_test.go`, tier (c) above). Per the
spec, no Go test executes the script — it needs a package manager and a
network. The tests read the script as text: the script is executable and starts
with `#!/bin/sh`; `set -eu` is its first effective line;
`shellcheck --shell=sh` is clean (skipped with an explanatory message naming
`.docker/Dockerfile` when `shellcheck` is absent, so it really runs under
`make docker-ci`); the install goes through the package manager and not a bare
installer; the account check reads the installed sysusers file and hardcodes
none of its values; ownership is read with `stat`; no booted-host command
(`systemctl`, `reboot`, `setenforce`, `getenforce`, `systemd-run`) appears; and
neither systemd-managed path is mentioned.
`TestVerifyInstallOwnerModeExpectationsMatchGoreleaserConfig` is a
**bidirectional** drift guard: it reads the live `../.goreleaser.yaml` and
requires set equality, in both directions and for **both** the deb and rpm
overrides, between the script's `expect_owner_mode` destinations and the
content entries carrying a `file_info` block — an unauthorized expectation line
and a dropped packaged entry both fail it.

Checks 4 through 6 add four more guards, all of them text-only:

- `TestVerifyInstallPAMCheckReadsTheInstalledPolicy` requires the script to
  name `/etc/pam.d/pilothouse` and to contain neither repository PAM source nor
  any per-distro literal (`common-auth`, `common-account`, `password-auth`,
  `pam_nologin.so`, a multiarch `…-linux-gnu/security` path), so the
  expectations can only be derived at run time.
- `TestVerifyInstallPAMCheckRejectsAnEmptyParse` pins the emptiness guards on
  the parsed stack list, module list and discovered module directories.
- `TestVerifyInstallVerifiesUnitsWithSystemdAnalyze` pins that check 5 calls
  `systemd-analyze verify` and that `systemctl` appears nowhere.
- `TestVerifyInstallUnitExpectationsMatchGoreleaserConfig` and
  `TestVerifyInstallLinkedBinariesMatchBuilds` are the two further
  **bidirectional** drift guards. The first requires set equality between the
  script's `expect_unit` paths and the live config's destinations under
  `/usr/lib/systemd/system`, for both overrides. The second requires set
  equality between the script's `expect_linked` paths and the `/usr/bin`
  destinations of the live `builds` entries whose `env` contains
  `CGO_ENABLED=1` — today exactly `/usr/bin/pilothoused` — and asserts the
  converse too: no `CGO_ENABLED=0` build may appear there. It declares its own
  local types decoding `builds[].binary` and `builds[].env`, because
  `drift_test.go`'s `buildTarget` decodes `binary` alone.

Checks 7 and 8 add five more guards, all still text-only:

- `TestVerifyInstallReinstallsAndRepeatsTheAccountAndOwnershipChecks` requires
  both reinstall verbs on the same `${artifact}` and counts the bare
  `check_account` / `check_owner_mode` invocation lines: each must appear more
  than once, so a chunk that re-asserted the ownership rules by copying them
  instead of re-invoking the function fails.
- `TestVerifyInstallConffilesMatchGoreleaserConfig` and
  `TestVerifyInstallRemovedPathsMatchGoreleaserConfig` are the two further
  **bidirectional** drift guards, run against both overrides. The first
  requires set equality between the script's `expect_conffile` paths and the
  live config's `type: config` destinations. The second requires set equality
  between the script's `expect_removed` paths and the live destinations that
  are neither `type: config` nor `type: dir`, plus the two `/usr/bin` build
  outputs. Directories are excluded because pruning is deliberately unpinned,
  and config files because their fate depends on the removal verb.
- `TestVerifyInstallRemovalMatrix` is the structural guard over check 8, and it
  enumerates the matrix as data — format × verb × expectation — rather than
  spot-checking one cell. It scopes itself to the lines after the
  `removal_account=` anchor so the earlier `deb)`/`rpm)` case labels cannot be
  mistaken for the removal block's, splits those lines into one segment per
  verb, and requires each verb to appear exactly once, inside its own format's
  branch, followed by exactly the assertions its row lists and by **none** of
  the assertions that would contradict them (so `check_conffiles_gone` after
  `dpkg -r`, or `check_conffiles_present` after `dpkg -P`/`rpm -e`, fails).
- `TestVerifyInstallRemovalChecksAssertTheRightPolarity` reads each of those
  check functions' bodies and pins what they actually assert (`[ -e … ]` versus
  `[ ! -e … ]`, the `.rpmsave` sweep, both `getent` calls), so a matrix cell
  cannot be satisfied by a correctly named function that asserts the opposite.
- `TestVerifyInstallNeverAssertsTheConfigDirectoryIsPruned` is the negative
  guard for the deliberate non-goal, and
  `TestVerifyInstallPackageNameMatchesGoreleaserConfig` keeps the name the
  removal verbs operate on equal to the live `package_name`.

**How the script is invoked.** `make verify-package-install` is the one thing
in the repository that runs it, as of this commit. The target takes two
variables: `INSTALL_IMAGE`, the container image reference, with **no default**,
and `ARTIFACT_DIR`, the directory of built artifacts, defaulting to `dist`. An
unset `INSTALL_IMAGE` is an actionable failure naming both digest-pinned
references (`debian:12@sha256:9344f8b8992482f80cba753f323adeaf17690076c095ccff6cc9536be98185dc`
and `fedora:42@sha256:99e203b80b1c3d8f7e161ec10a68fd02b081ef83a3963553e513c82846b97814`),
following `make package`'s precedent of failing with what was found and what is
required rather than silently picking a distro family; a missing
`ARTIFACT_DIR` fails the same way, naming `make package` as the local producer.

The recipe writes out an explicit `$(DOCKER) run --rm` and deliberately does
**not** reuse the `DOCKER_RUN` macro. That macro is wrong here in three
independent ways: it pins the host UID/GID (installs need root inside the
container), it bind-mounts the whole workspace (only `packaging/` and the
artifact directory should be visible, both read-only), and it hardcodes
`$(DOCKER_IMAGE)`, the development image, when the image under test is the
parameter. Network access is left enabled on purpose — check 1 exists to
resolve dependencies against the distro's real repositories.

Like `verify-packages` and `package`, the target is **deliberately absent from
`ci` and from `docker-ci`** (`make -n ci | grep verify-package-install` prints
nothing): it depends on artifacts a stock checkout does not have, and
additionally on Docker and the network.

**The CI job that runs it.** `.github/workflows/packaging.yml` gained a second
job, `install` ("Install packages on target distros"), as of this commit. It is
Layer A in CI, and it is a *consumer* of the `packages` job, not a second build:

- `needs: packages`, and **no `if:` of its own**. The fork skip lives once, on
  `packages` (a fork pull request never receives `GORELEASER_KEY`); a skipped
  job skips its dependents, so this job inherits the skip instead of carrying a
  second copy of the condition that could drift from the first.
- A `strategy.matrix` of exactly two entries — `debian:12@sha256:9344f8…` paired
  with the `packages-deb` artifact and `fedora:42@sha256:99e203…` paired with
  `packages-rpm` — with `fail-fast: false`, so one family's failure does not
  mask the other's result. The same digest pins the Makefile's unset-variable
  message names, for the same reason: a floating tag makes the gate's result
  depend on when it ran.
- Its steps are `actions/checkout@v7` (the script and the Makefile come from the
  tree, never from the artifact), `actions/download-artifact@v5` fetching the
  matrix row's artifact into `dist/`, then
  `make verify-package-install INSTALL_IMAGE=${{ matrix.image }}
  ARTIFACT_DIR=dist` — the *same* target a developer runs, so CI and a
  workstation cannot disagree about what installing the package proves.
- Reusing the uploaded artifacts is what keeps the bytes under test equal to
  the bytes `make verify-packages` already passed, and keeps the workflow to
  **exactly one** `goreleaser/goreleaser-action` step. Only the matching
  format's artifact is downloaded, so a format mis-detection inside the script
  cannot silently install the wrong file. The job needs no Go, no GoReleaser and
  no packaging build dependencies — only Docker, which `ubuntu-latest` provides.

`packaging/workflow_install_job_test.go` guards all of that by decoding the live
workflow with `gopkg.in/yaml.v3` and asserting the job's `needs`, the absence of
its own `if:`, both matrix rows verbatim, the `make verify-package-install`
invocation, the one-GoReleaser-step count, and that the token `cpio` appears
nowhere in the file. Its expectations are hand-written from the specification,
never derived from the workflow under test.

`make help` was added in the same commit. The Makefile has annotated targets
with `## description` since long before, but nothing printed them; `help` is a
minimal `awk` over `$(MAKEFILE_LIST)` that prints every such annotation, and
both `verify-package-install` and `help` are listed in `.PHONY`.

### Booted-VM harness: pinned image acquisition (`test/vm/images.env`, `test/vm/lib/images.sh`)

Layer B (#67) — installing the artifacts on a **booted** host with real
systemd — is complete as of this commit. **The harness boots a guest,
installs the package, activates both units, authenticates a real non-root
administrator through PAM, reads the daemon's own journal record back
through the broker and proves the posture that survives a real guest
reboot**, and `.github/workflows/packaging.yml`'s `vm-boot` job runs it in CI.
What exists is image acquisition (this section), the boot mechanism
("Booted-VM harness: credentials, seed, boot and SSH"), the orchestrator, the
guest scripts and the diagnostics that drive them ("Booted-VM harness: the
orchestrator, guest staging and diagnostics") and the CI job that invokes them
("Booted-VM harness: the CI job (`vm-boot`)").

This section covers the pinning table and the host-side fetcher:

- **`test/vm/images.env`** is the *single* pinning site. Per distro family it
  records three values — the image URL, the checksum algorithm and the digest:
  Debian 12 `genericcloud` amd64 from the dated
  `cloud.debian.org/images/cloud/bookworm/20260722-2547/` directory under
  **SHA-512**, and Fedora 42 `Cloud-Base-Generic` x86_64 from the
  `dl.fedoraproject.org/pub/archive/...` path under **SHA-256**. The algorithm
  differs per family because each digest is the distributor's own published
  value (Debian's `SHA512SUMS`, Fedora's `Fedora-Cloud-42-1.1-x86_64-CHECKSUM`);
  a digest computed here would pin nothing. Fedora 42 is archived — the
  non-archive `releases/42/...` path 404s and must not be "fixed" back. Both
  entries are amd64/x86_64 only, matching the two container images the install
  matrix already uses, and both paths are immutable: a `latest`-style URL would
  move out from under the digest.
- **`test/vm/lib/images.sh`** is a **sourced** bash library (no shebang, `set
  -euo pipefail`, committed non-executable) exposing
  `fetch_image <family> <cache-dir>`. It reads the family's row from
  `images.env`, downloads over HTTPS into the cache directory, and dispatches to
  `sha256sum`/`sha512sum` per the declared algorithm. A mismatch prints **both**
  the expected and the actual digest and exits non-zero; a file already in the
  cache is re-verified before it is reused, so a truncated or tampered copy
  cannot be handed to a caller. On success the verified path is the function's
  only standard output — progress and failures go to standard error.

**`packaging/vm_harness_test.go`** guards this from the existing `packaging` Go
package, alongside `verify_install_test.go`, because this is Layer B of the same
packaging verification chain. The guards are structural and **never execute the
harness**: the only process any of them spawns is `shellcheck` (`--shell=bash`,
skip-if-absent, the same runner shape `verify_install_test.go` uses). They parse
`images.env` as text rather than sourcing it, assert both families' rows and the
two literal image references, and assert the properties that make a pin worth
anything — HTTPS, no `latest` path segment, and a digest whose hex length matches
its declared algorithm (128 for SHA-512, 64 for SHA-256). They also open the
executed-versus-sourced mode discipline that later harness scripts extend:
`lib/images.sh` is sourced, so `Mode().Perm()&0o111` must be zero — the same
instrument `verify_install_test.go` uses in the opposite direction for the
executed install-validation script.

### Booted-VM harness: credentials, seed, boot and SSH (`test/vm/lib/cloudinit.sh`, `vm.sh`, `ssh.sh`)

The boot half of Layer B's host side: three sourced bash libraries that can
create a guest and talk to it. **They are mechanism only** — they assert nothing
about a running system. The orchestrator that calls these functions and the
guest-side script that installs the package are described in the next section;
the booted-host assertions and the CI job land later.

**`cloudinit.sh` — every credential is generated here, on the host, at run
time.** `create_run_workspace` makes the per-run directory with mode `0700`;
`generate_credentials` writes into it, and only into it, an ed25519 keypair
(`ssh-keygen -t ed25519 -N ''`) and two passwords (`openssl rand`, falling back
to `/dev/urandom`; never a literal), recording them in `creds.env` as
`PH_ADMIN_USER`, `PH_ADMIN_PASSWORD` and `PH_ROOT_PASSWORD`. No guest-side
command generates anything. Every function that touches a credential disables
shell tracing first, and nothing echoes a password or a private key, so no
generated value can reach a job log.

`write_cloud_init_seed` takes the **family** as an argument and emits the
NoCloud `meta-data`/`user-data`; `build_seed_iso` packs them into a `cidata`
volume. `create_seed` runs the three in order and is called **directly**, never
through a command substitution: it publishes `VM_SSH_KEY`, `VM_CREDS_ENV` and
`VM_SEED_ISO` as exported variables, and a subshell would discard all three.
The seed declares exactly **one** login account, the administrator,
with the generated public key, `sudo: ['ALL=(ALL) NOPASSWD:ALL']`, and the
administrator group its own package expects — `sudo` on Debian, `wheel` on
Fedora, the single token by which the two unit files differ. Both the
administrator's and root's generated passwords are set through cloud-init's
`chpasswd:` module with `expire: false`, so root carries a *valid* password
later checks can be run against.

Root gets **no** authorized key and SSH root login is not enabled: stock cloud
images restrict it, enabling it would make the guest less stock, and the
harness needs root's password to be set and later removed under its own
control. Everything that needs privilege escalates instead — which is why the
`NOPASSWD` grant is load-bearing rather than a convenience. Guest commands are
non-interactive with no TTY, so an escalation that prompted would hang; `sudo
-n` turns the same situation into an immediate, named failure.

The seed also builds the half of the serial-console channel that does not
depend on the guest's kernel command line: a
`/etc/systemd/journald.conf.d/99-console.conf` drop-in
(`ForwardToConsole=yes`, `MaxLevelConsole=info`, `TTYPath=/dev/ttyS0`) and
cloud-init's `output: {all: '| tee -a /dev/console'}`.

**`vm.sh` — boot, without touching the image.** `create_overlay` builds a qcow2
overlay in the run workspace with `qemu-img create -F qcow2 -b <base>`, so the
pinned base downloaded by `images.sh` is read-only input and is never rewritten;
there is deliberately no offline image-editing step, because rewriting the
guest's disk or kernel command line would make it no longer stock (Fedora
likewise stays SELinux-enforcing). `start_vm` runs `qemu-system-x86_64` with
`-accel kvm` (never software emulation), `-display none`, a file chardev bound
to `-serial`, and user-mode networking forwarding a loopback port to the guest's
sshd; it exports `QEMU_CONSOLE_LOG` and `QEMU_STDERR_LOG`, the only two things a
guest that never answers leaves behind.

`wait_for_console_boot` is what makes that channel a **gate** rather than a
hope: it polls the console log for a boot marker and, on `CONSOLE_BOOT_TIMEOUT`
expiry, exits non-zero naming the assertion and dumping both logs. `boot_guest`
calls it **before** `wait_for_ssh`, so a run whose serial log receives no output
cannot pass — the diagnostics channel is proven working on every green run
instead of being discovered broken on a failing one.

**`ssh.sh` — one identity, explicit escalation, bounded waits.** `guest_run`
and `guest_copy` address the guest as the administrator account read back from
`creds.env` (never `root@`), `guest_sudo` escalates with `sudo -n`, and
`wait_for_ssh` polls within the explicit `SSH_READY_TIMEOUT`. `guest_copy`
carries **both** directions through one function — staging a host file into the
guest, and retrieving a staged guest file back to the job workspace — and
requires **exactly one** of its two paths to be a guest path, recognised by its
leading `~`: that keeps a single audited site where a guest end of a copy is
constructed, and makes a host-to-host or guest-to-guest call fail by name
instead of copying locally. `reboot_guest`
issues the reboot through `guest_sudo` and then waits for the **pre-reboot**
sshd to stop answering (`SSH_GONE_TIMEOUT`) before waiting for sshd to return,
so a readiness check cannot be satisfied by the daemon that is about to die.

Three things keep that sequence from passing without proving a reboot, and each
closes a hole the SSH probes cannot close by themselves:

- **The reboot command's status is not discarded.** A successful reboot kills
  the connection, which ssh reports as 255; that one status is the expected
  symptom. Any other non-zero status means the command never dispatched — a
  rejected escalation being the likely cause — and `reboot_guest` fails
  immediately carrying the remote stderr. The blanket `>/dev/null 2>&1 || true`
  this replaced made a rejected escalation indistinguishable from the expected
  disconnect, and the wait loop then reported the real failure as a shutdown
  timeout.
- **Disappearance requires `SSH_GONE_CONFIRMATIONS` consecutive unanswered
  probes** (default 3), with the counter reset the moment the guest answers.
  A single transient refusal against a guest that never rebooted would
  otherwise end the wait, after which `wait_for_ssh` would be satisfied by the
  very sshd that was supposed to die.
- **The guest's `boot_id` is compared across the reboot.** `guest_boot_id`
  reads `/proc/sys/kernel/random/boot_id`, which the kernel regenerates on
  every boot; `reboot_guest` captures it before and after and fails if it is
  unchanged. This is the only assertion here that cannot be satisfied by an
  sshd that merely restarted, and it cannot fail on a genuine reboot, so it is
  immune to how the probes happen to fall.

The endpoint it dials, `VM_SSH_HOST`/`VM_SSH_PORT`, is declared with the same
`:-` default in both `ssh.sh` and `vm.sh` — the library that forwards it — so
the two can be sourced in either order; a guard test holds the two
declarations identical so the dialled and forwarded endpoints cannot drift.

`packaging/vm_harness_test.go` guards all three as text and by file mode, and
still executes nothing but `shellcheck`: every file under `test/vm/lib` must be
non-executable, both family branches must exist, the `NOPASSWD` grant must be
present *and* no password-prompting escalation form may appear anywhere in the
library directory, no file under `test/vm` may contain a key blob, a PEM block,
a literal password value or the string `root@`, and no file may disable KVM or
introduce an offline image-editing step. It also pins the three reboot
invariants above by text, so a regression to the suppressed form fails the unit
suite rather than waiting for a VM run to misreport it.

Two of those guards are worth calling out because their earlier forms passed
without proving anything. The private-key print check scans **line by line**: a
single regex anchored with `$` and no `(?m)` matches only at end of file, so it
silently permitted every occurrence that was not the last thing in the file.
It also matches both spellings of the key — the literal `id_ed25519` and the
`$VM_SSH_KEY` variable the scripts actually use — while exempting a reference
followed by `.pub`, since the public key is not a credential.

### Booted-VM harness: the orchestrator, guest staging and diagnostics (`test/vm/vm-boot-test.sh`, `test/vm/lib/diagnostics.sh`, `test/vm/guest/`)

The harness's first end-to-end runnable path. **A run ends once
the guest has come back from a real reboot with its posture proven** (see
"Reboot posture" below); `.github/workflows/packaging.yml`'s `vm-boot` job is
what invokes it (see "Booted-VM harness: the CI job" below).

**`test/vm/vm-boot-test.sh` is the one entry point**, taking
`--family debian|fedora` and `--artifact-dir <dir>`; any other family is
rejected by name rather than defaulted. It fetches and verifies the pinned
image, builds the seed, boots, gates on serial-console output, waits for sshd,
probes escalation, creates the staging directory, selects the artifact, stages
the artifact/guest scripts/`creds.env`, installs the credentials privileged,
runs the guest bootstrap and the activation, PAM and journal checks, and
finally captures the pre-reboot posture, reboots the guest and runs the
post-reboot check.

**One guest identity, staged files, explicit escalation.** The only SSH identity
in the guest is the administrator account cloud-init created. That account
cannot write `/root`, cannot install packages and cannot read a `0600`
root-owned file, so nothing is copied to a privileged destination directly:

- The orchestrator creates `~/vm-boot` (mode `0700`) and `~/vm-boot/guest/` **as
  that account, before anything is copied**, and every guest-bound destination
  is inside it. Nothing ever addresses the guest as root over SSH.
- `creds.env` is staged there and then placed with
  `sudo -n install -o root -g root -m 0600 ~/vm-boot/creds.env
  /root/.pilothouse-vm-creds`, after which the staged copy is removed with
  `rm -f` — the generated root credential must not linger in a file the
  unprivileged account can read. No credential is ever a command-line argument;
  only the path of the file holding it is.
- Every guest script is invoked as **`sudo -n sh ~/vm-boot/guest/<name>.sh`** —
  explicit interpreter *and* explicit escalation. Package installation,
  `systemctl enable --now`, reading that credentials file, opening the `0660`
  `root:pilothouse` broker socket and `journalctl -u` all require root.
- Immediately after the guest is reachable, the orchestrator probes
  `sudo -n true` and fails with a named assertion. A broken `NOPASSWD` grant is
  then reported once, at the top of the run, instead of surfacing obscurely
  three scripts deep.

**Executed versus sourced is enforced by two mechanisms, not one.** Every script
invoked as a program — the orchestrator and every file under `test/vm/guest`
except `lib.sh` — is committed `100755` **and** is run through an explicit
interpreter (`bash test/vm/vm-boot-test.sh` for the orchestrator, whose caller
lands with the CI job; `sh <staged path>` for each guest script, which the
orchestrator already does today). `scp` does not preserve the executable bit
without `-p`, so a harness that trusted the copied mode would carry the same
defect one layer down.
Sourced libraries (`test/vm/lib/*.sh` and `test/vm/guest/lib.sh`) stay
non-executable and are guarded as such, so the two categories cannot blur in
either direction.

**Artifact selection is arch-qualified.** The `packages` job uploads both an
amd64 and an arm64 file per format, and the runner is x86_64 with KVM requiring
guest and host architectures to match, so the orchestrator globs
`"${artifact_dir}"/*_amd64.deb` for Debian and `"${artifact_dir}"/*.x86_64.rpm`
for Fedora, then requires **exactly one** match and otherwise fails naming the
count and the matched basenames. This is the same rule
`packaging/verify-install.sh` already applies for Layer A. The selected file is
staged under a fixed name, so the guest script has nothing to choose.

**`test/vm/guest/` is the single directory every guest-side assertion script
lives in.** They are POSIX `sh` (Debian's `/bin/sh` is dash, Fedora's is bash)
with `set -eu`, and they share `guest/lib.sh`: exactly one `fail()`, which
prints the failing assertion by name and exits non-zero so the script aborts on
its **first** failure, plus `require_root()`, `expect_owner_mode`, the
credentials loader and the `broker_curl`/`web_curl` wrappers. `require_root` is
each script's first effective statement and is the converse of the call form: a
call site that lost its `sudo -n` fails immediately and legibly instead of
producing a confusing permission error later. Because the script is already
root, the curl wrappers carry **no inner escalation** — one escalation boundary
is auditable, one per request is not, and `require_root` is what makes that
safe.

`guest/install-package.sh` installs `curl` and `jq` (test fixtures: `curl` makes
the `--unix-socket` requests, `jq` lets later checks match a JSON field rather
than a substring) and then the staged artifact, through the guest's own package
manager. It deliberately restates **nothing** from Layer A (#77) — dependency
resolution, postinstall repair, PAM policy resolution, unit validity, linkage,
reinstall and removal are asserted there, against these same artifacts. The
Fedora guest is SELinux-**enforcing** and stays that way: `setenforce` and
`permissive` appear nowhere in the harness, so an install that policy would
break fails the gate rather than being worked around.

`guest/check-activation.sh` runs next, and is the first check that asserts
anything about the running system. It **enables and starts** both units itself
(`systemctl enable --now`) rather than asserting they are already active:
`packaging/postinstall.sh` contains no `systemctl` call, so installing the
package deliberately starts nothing, and asserting otherwise would assert a
behaviour the packaging does not have. Each unit is then waited for under one
named constant, `UNIT_ACTIVATION_TIMEOUT_SECONDS`, and on expiry the script
prints **that unit's own** `systemctl status` and `journalctl -u` before failing
by name — both processes log to their own unit's journal, so naming the other
one would pass vacuously. With both units active it asserts `/run/pilothouse`
and `/var/lib/pilothouse` are `root:pilothouse` mode `0750` — the state
systemd's `RuntimeDirectory=`/`StateDirectory=` create, which is exactly the
check no container can make and therefore the one #77 does not attempt — and
`/run/pilothouse/broker.sock` is `root:pilothouse` mode `0660`. Liveness is
proved by an **unauthenticated** `POST` to
`/v1/queries/org.frostyard.pilothouse.capabilities.list` over that socket,
bounded by `BROKER_PROBE_TIMEOUT_SECONDS`, which must answer **exactly `401`**
with a JSON `error` body: every broker query calls `authorize()` first, so a
`401` can only come from a server that accepted the connection and parsed the
request, while a refused connection, a stale socket file, a hang, a `200` or
any other status fails. The authenticated capability list needs a session token
and is not attempted here.

`guest/check-pam.sh` runs next and is the reason this tier exists: no container
can run `pam_authenticate` against a live daemon. It **consumes** the
credentials cloud-init delivered — it sources `/root/.pilothouse-vm-creds` and
generates nothing; `openssl`, `/dev/urandom` and `pwgen` appear nowhere in it —
and starts by reading the `--admin-group` token out of the **installed**
`pilothoused.service` (through `systemctl cat`, so it is the file the package
put on *this* guest), asserting it is `sudo` on Debian and `wheel` on Fedora and
that the cloud-init-created administrator is a member of it. That single token
is the only difference between the two packages' broker units.

Then three logins against the web console on `127.0.0.1:8888`, in this order,
each asserting **one exact status**:

1. `GET /login` for the hidden `csrf` input value — `POST /login` rejects a
   missing `csrf` field with `403`, so a bare POST would fail for the wrong
   reason — then the form POST with `csrf`, username and password, expecting
   exactly **`303`**. There is no pre-login cookie to carry: the session cookie
   is set only after authentication succeeds.
2. The same administrator with a wrong password (derived from the real one so
   it is certainly wrong), expecting exactly **`401`**.
3. `root`, with its **valid** cloud-init-set password, expecting exactly
   **`401`**. A locked or password-less root would be refused by PAM before
   Pilothouse ever saw it, which would pass while proving nothing.

**A `429` is a failure of this check, never a pass.** One failed attempt arms a
per-`username`+`remote` lockout that answers `429` *before* `Authenticate` is
called, so the success comes first (it also clears the entry) and every
assertion names one expected status rather than accepting "any non-success".

Two of the claims cannot be carried by a status code at all, so each is proved
from the journal, bounded by a cursor captured **immediately before** the
request it is about, and matched on the record's **parsed JSON `msg` field**
rather than on a substring of the line:

- After the successful login, **no** record past that cursor in
  `journalctl -u pilothouse.service` may have `msg == "refresh capabilities"`.
  The web process runs the broker's capability query on the administrator's
  behalf right after login, but `refreshCapabilities` swallows its error and
  logs that warning, so the `303` alone proves nothing about the query. The
  record comes from `internal/web`, so this is the **web** unit's journal;
  searching the broker's would find nothing whatever happened.
- After the root attempt, a record past its cursor in
  `journalctl -u pilothoused.service` must have
  `msg == "authenticated account rejected"`, `user == "root"` and an `error`
  containing `direct root login is disabled`. Both PAM stacks run an *account*
  phase whose rejection produces the identical `401`, so the status cannot tell
  the UID-zero refusal apart from it; that message is emitted only on the
  `Resolve` path — reached only after `Authenticate` returned nil — and that
  error text is unique to the UID-zero branch. This is the **broker's** journal,
  the opposite unit from the check above.

The chunk also adds the reusable **authenticated direct-socket helper** in
`guest/lib.sh`: `broker_login` posts `/v1/login` with a JSON `username`,
`password` and `remote`, then `broker_query` posts `/v1/queries/{id}` with the
returned bearer token. Its `remote` is `vm-boot-harness`, deliberately **not**
the web process's `127.0.0.1`: the broker keys its lockout on
`lower(username) + NUL + remote`, so a shared value would let the wrong-password
attempt above throttle a direct login of the same account and turn a status
assertion into a statement about the limiter. The `0660 root:pilothouse` socket
needs privilege, and that privilege comes from the whole script running as root
under `sudo -n sh` (which `require_root` proves) — not from an inner `sudo` per
request. `check-pam.sh` uses the helper once, to assert the administrator's
`session.identity.admin` is `true`, which proves the family's administrator
group **functionally** rather than by string comparison, and then runs the
capability query as an authenticated caller. Credentials reach `curl` through
files (`--data-urlencode name@file`, `--header @file`, `--data-binary @file`)
built by `jq` from the environment, so no credential and no session token lands
in the guest's process table. Finally the generated root password is removed
(`passwd -d root`, `usermod -L root`), which succeeds because the script runs as
root; nothing in it assumes an SSH login as root, which the guest does not have.

`guest/check-journal.sh` runs last and proves the daemon built with the
`sdjournal` tag reads the journal **back** on a booted host. It reuses the
authenticated direct route — `broker_login`, then `broker_query` — to run the
broker's own journal-backed read surface, `QueryServicesJournal`
(`org.frostyard.pilothouse.services.journal` in `internal/broker/api.go`,
registered behind `Systemd` **and** `Journald`) with the `unit` parameter its
handler in `cmd/pilothoused/main.go` reads, for `pilothoused.service`.
`broker_query` fails by name on anything other than **exactly `200`**, and the
assertion is then made on the **response body**: the answer must be about that
unit, `entries` must be the array `services.Journal` declares and must be
non-empty (a `200` carrying nothing read back fails on its own name), and some
returned entry's `message` must contain `privileged broker listening` — the
line the *daemon itself* logs on a successful listen, emitted when
`check-activation.sh` started it and so comfortably inside the reader's
one-hour window. The matching entries' own `unit` field is checked too, so the
record's provenance comes from `_SYSTEMD_UNIT` rather than from the query's
parameter.

**Finding that line in `journalctl` output is explicitly not accepted as
evidence** — that would prove systemd logged it, not that the daemon can read
it back — so `check-journal.sh` reads no log for itself: it invokes neither
`journalctl` nor `systemctl`, and a guard in `packaging/vm_harness_test.go`
enforces that alongside grounding the query id, the parameter name and the
searched-for line against `internal/broker/api.go` and
`cmd/pilothoused/main.go`. The guards read the script as text and, as
everywhere else here, never execute it.

**Reboot posture: what a real reboot proves, and the one inode assertion the
harness makes.** `guest/capture-pre-reboot.sh` and
`guest/check-reboot-posture.sh` are the two halves of the run's last stage. The
capture reads the capability set through the **direct authenticated broker
route** (`broker_login` over the Unix socket, then `broker_query` for
`QueryCapabilities`) — never from the web console, which renders a *view* of the
set rather than the set — plants a **sentinel** file inside `/run/pilothouse`,
and records `/var/lib/pilothouse/audit.db`'s inode, both directories'
owner/group/mode and the boot id into `~/vm-boot/pre-reboot-state.env`. That
file is written as root into the administrator-writable staging directory and
`chown`ed to the one login account, so the orchestrator retrieves it with an
ordinary `guest_copy` in the retrieval direction and no escalation; it carries
**no credential** and is printed into the job log.

The orchestrator retrieves and prints that state and dumps `systemctl status`
and `journalctl` for both units (`dump_pre_reboot_diagnostics`) **before** it
issues the reboot: a guest that never comes back cannot be asked anything
afterwards, and this is the evidence that case is diagnosed from alongside the
continuous serial console log. `reboot_guest` then waits for the **pre-reboot
sshd to stop answering** before waiting for sshd to return, and compares boot
ids, so no post-reboot check can be answered by the sshd that was about to die;
the post-reboot script re-reads `/proc/sys/kernel/random/boot_id` and fails if
it matches the recorded one.

`check-reboot-posture.sh` then asserts four things and **enables or starts
nothing** — that both units returned *unaided* is the whole claim, so unit state
is read with `is-active` inside a bounded wait, the only other `systemctl` verb
in the file is the `status` of its own failure dump, and a guard scans the file
(comments included) for any enable-or-start form:

| Claim | How it is proven |
| --- | --- |
| Both units active again | `systemctl is-active --quiet`, broker first, bounded by `UNIT_ACTIVATION_TIMEOUT_SECONDS`, dumping that unit's status and journal on expiry |
| Capability set unchanged | the same authenticated broker route, sorted ids compared for equality; a query that fails **or answers an empty set** is a failure on **either** side, so an empty answer can never compare equal against another empty answer |
| `/run/pilothouse` **destroyed and recreated** | the pre-reboot **sentinel is absent** *and* the directory exists again as `root:pilothouse` `0750` — one assertion group, neither half accepted alone |
| `/var/lib/pilothouse` **persisted** | same owner/group/mode *and* `audit.db`'s inode **unchanged** |

The sentinel is the discriminator because a directory that survived a reboot
still carries its contents and one systemd recreated from `RuntimeDirectory=`
cannot. Absence alone would also hold for a directory that never came back, and
correct-looking metadata alone would also hold for a directory that was never
touched — hence one group.

**`/run/pilothouse`'s own inode is recorded and printed, and asserted nowhere.**
`/run` is a fresh tmpfs on every boot whose inode counter restarts, so correct
behaviour can legitimately reuse the same number: requiring the inode to
**differ** is an assertion that could fail on correct behaviour, and requiring it
to match would be wrong outright. It exists only as context for whichever half
of the group fails. The asymmetry with `audit.db` is principled rather than an
oversight: inode **equality** on the guest's persistent root filesystem, where an
inode number is a durable identity, is sound, and that is the harness's **only**
inode assertion. No pre-reboot audit *record* is required to be readable — the
database file's identity is the persistence proof.

`packaging/vm_harness_test.go` guards that shape in **both** directions: it fails
if the sentinel-absence proof or the recreation-metadata check is removed, fails
if `audit.db`'s inode-equality assertion is removed, and fails if an
inode-**inequality** assertion on `/run/pilothouse` ever appears — the last one by
scanning every file under `test/vm` for any line that names the runtime
directory's inode and takes part in a comparison, a `fail` or a `[` test. The
identifiers it looks for are **derived from the harness**, not listed in the
test: any variable assigned from a `stat`-`%i` read of the runtime directory, and
anything aliased from one, joins the set, so an inequality reintroduced under a
fresh name — or written inline with no variable at all — is still caught.

**At this commit the harness run ends there.** The guest's SELinux audit
posture and image-based hosts are #80's. Its image path installs the last
released x86_64 RPM while building an ephemeral uCore-derived image; `.deb`
layering and Snosi sysext delivery are explicitly outside that issue.
`.github/workflows/packaging.yml`'s `vm-boot` job is what invokes all of this
(see "Booted-VM harness: the CI job (`vm-boot`)" below).

**`test/vm/lib/diagnostics.sh` discriminates on whether the guest answers SSH at
the moment of failure**, not on whether it ever did. `install_failure_diagnostics`
arms an `ERR` and an `EXIT` trap; on a non-zero exit it probes reachability
*then* and branches: a reachable guest yields `systemctl status` and
`journalctl` for **both** `pilothoused.service` and `pilothouse.service` (two
processes, two journals, so dumping one would hide the other), through `sudo -n`
because both need privilege; an unreachable one yields the host-side
`QEMU_STDERR_LOG` and `QEMU_CONSOLE_LOG`. Neither branch is silent, a collection
command that does not complete is reported by name rather than swallowed, and
the guest is stopped only *after* the dump — a dump from a killed guest is not a
dump. Everything goes to the job log; nothing is uploaded.

The unreachable branch prints **both** host-side sections unconditionally, even
when the variables naming them are still unset. Diagnostics are armed *before*
the guest is created, so a failure during image verification, seed creation or
overlay creation reaches `dump_boot_diagnostics` before `start_vm` has exported
either path. Skipping an empty path — the obvious reading of "dump the log if
we have one" — left exactly those failures with no output at all, which is the
silent-dump failure mode this file exists to prevent. An absent log is itself
evidence, so it is reported by name (`not created: the run failed before
start_vm launched qemu`) instead of passed over. `stop_vm` is already a no-op
when `QEMU_PID` is unset, so the same early-failure path unwinds cleanly.

`packaging/vm_harness_test.go` grew the matching guards — including, for the PAM
check, the login order and the one exact status per attempt, the cursor captured
as the statement *immediately* before each POST it bounds, the parsed-field
journal matches on the unit that actually emitted the record, the absence of any
credential generator, and the direct route's `remote` differing from the web
process's `127.0.0.1` — still executing nothing but `shellcheck` (`--shell=bash`
for the orchestrator and the host libraries,
`--shell=sh` for the guest files). They discover the guest scripts **on disk**
rather than from a hand-kept list, so a script added later cannot escape the
mode, dialect, `require_root` and invocation-form checks; they enumerate every
guest-script invocation in the orchestrator and require the full
`guest_run sudo -n sh ~/vm-boot/guest/<name>.sh` form, in both directions (no
other form is permitted, and a guest script that is never invoked fails too);
they match the two selection globs against a synthetic listing holding *both*
architectures, which is the case an unqualified glob would silently get wrong;
and they pin the credential path's three steps in order. The `sudo -n` scan that
previously covered `test/vm/lib` now covers **every** file under `test/vm`,
since the orchestrator is where the escalations actually happen.

**Scope is the recurring defect in these guards, and it is invisible from the
test result** — a guard checking the wrong file set still passes, so nothing
fails until the gap is exploited. Two instances are worth remembering. Guest
scripts are enumerated with `filepath.WalkDir`, not `os.ReadDir`: the
single-level listing skipped directories, so `test/vm/guest/subdir/new.sh`
escaped the mode, shebang, `require_root`, shellcheck and invocation guards
simultaneously. And the staging-directory fence is applied to **every** file
under `test/vm`, not only `vm-boot-test.sh`: the invariant is phrased
harness-wide, and any library could call `guest_copy` or reach for `scp`
directly. `scp` is permitted at exactly one site — inside `guest_copy`, the one
place a guest destination is constructed — and that exemption is expressed as
the function's own body rather than as a whole-file pass.

Widening those scans required distinguishing a call from a mention: the fence
now matches `guest_copy`/`scp` only outside string literals, because
`ssh_fail "usage: guest_copy <local> <remote>"` names the function without
calling it. Without that, widening the scan produces a phantom violation, and
the tempting fix — re-narrowing the guard until it goes quiet — is what left
the fence covering one file in the first place.

### Booted-VM harness: the CI job (`vm-boot`)

The third job in `.github/workflows/packaging.yml`, added as of this commit,
is what makes the harness above a gate rather than a script nobody runs. It is
Layer B in CI, and like `install` it is a *consumer* of `packages`, never a
second build.

- `runs-on: ubuntu-latest`, `needs: packages`. The standard GitHub-hosted
  runner is deliberate: no self-hosted runner, no larger runner. Hardware
  acceleration is available there once the `99-kvm4all.rules` udev rule is
  installed (`KERNEL=="kvm", GROUP="kvm", MODE="0666",
  OPTIONS+="static_node=kvm"`, then `udevadm control --reload-rules` and
  `udevadm trigger --name-match=kvm`), which is the job's second step. There
  is **no** software-emulation fallback: `start_vm` passes `-accel kvm`, so a
  runner that cannot accelerate fails the job instead of quietly running a
  slower, different test.
- `strategy.fail-fast: false` with a two-row matrix — `debian` paired with the
  `packages-deb` artifact and `fedora` with `packages-rpm`. Unlike `install`'s
  matrix, no image reference appears here: the guest image is pinned by
  checksum in `test/vm/images.env`, which stays the single pinning site.
- Its `if:` is precisely
  `github.event_name != 'pull_request' || contains(github.event.pull_request.labels.*.name, 'vm-boot')`
  and nothing else. That is label **presence**, not the labeling event, so a
  later push to an already-labelled pull request runs the job again — a gate
  keyed to the `labeled` event alone would certify only the commit that
  happened to be at HEAD when the label was applied. The workflow's
  `pull_request.types` is widened to `[opened, synchronize, reopened,
  labeled]` for the same reason: without `labeled` the trigger cannot fire when
  the label goes on. Widening `types:` also lets `packages` start on a labeling
  event, which is expected and is not a second build. The fork skip is **not**
  restated: `needs: packages` inherits it.
- **No tag trigger was added.** `packaging.yml` is deliberately not
  tag-triggered; adding one so `needs: packages` could be satisfied on tags
  would create a third GoReleaser build alongside `release.yml` for no gain,
  since every tag points at a commit the `main` push trigger already covered.
- Its steps are `actions/checkout@v7` (the harness comes from the tree), the
  KVM rule, an `apt-get install` of `qemu-system-x86`, `qemu-utils` and
  `xorriso` (boot, overlay creation and the NoCloud seed ISO respectively),
  `actions/download-artifact@v5` for the matrix row's artifact **only**, and a
  single run step: `bash test/vm/vm-boot-test.sh --family ${{ matrix.family }}
  --artifact-dir dist`. The **explicit interpreter** is not decoration —
  `test/vm/vm-boot-test.sh` is also committed `100755`, so neither the file
  mode nor the `bash` prefix is a single point of failure for the step
  starting.
- It adds **no** repository secret (the workflow's one `secrets.` use remains
  `GORELEASER_KEY`, in `packages`), carries **no** `actions/upload-artifact`
  step and pushes to no registry: the overlay disk, the NoCloud seed and the
  run-time credentials never leave the job, and diagnostics go to the job log
  (see the orchestrator section). No OS image is built, derived or published.
- It is **not** a required check and is on no branch-protection list, and
  there is no local `make` target for it: it needs KVM, network access and the
  artifact `packages` builds, so it cannot run under `make ci` or
  `make docker-ci` any more than the rest of `packaging.yml` can.

`packaging/workflow_vm_job_test.go` guards all of that, reusing
`loadPackagingWorkflow` from `workflow_install_job_test.go` so both tiers are
asserted against the same live decode of the same file. Its expectations —
the matrix rows, the exact label expression, the `pull_request.types` list —
are hand-written from the specification, never derived from the workflow under
test, and it asserts the harness step's command *starts with* `bash
test/vm/vm-boot-test.sh` and passes `--family` and `--artifact-dir`. It also
asserts the absences that matter: no tag trigger, no fork-skip restatement, no
GoReleaser step in the job (the workflow-wide count stays exactly one), no
upload-artifact and no registry push, no `tcg` fallback, and no `secrets.`
reference outside `packages`. It executes nothing — the harness needs KVM and
a network.

**What this tier proves, and what it does not.** It proves what a container
cannot: units activating on a booted host, systemd itself creating
`/run/pilothouse` and `/var/lib/pilothouse` `root:pilothouse 0750`, a live
broker socket at `0660 root:pilothouse`, PAM authenticating a real non-root
administrator through the running stack, journald reachable through the
broker's own journal query, and the whole posture surviving a real reboot. It
does **not** cover image-based hosts (uCore), SELinux AVC assertion or
policy qualification — the Fedora guest stays enforcing, but nothing scans or
classifies the audit log — or bootc update/rollback of an ephemeral
uCore-derived image containing the last released x86_64 RPM. Those are
**#80**'s; `.deb` layering and Snosi sysext delivery are not.

### Artifact extraction (`packaging/extract`)

`packaging/extract` is a subpackage whose only job is to turn a real artifact on
disk into a `packaging.Model`. At this commit it holds exactly two backends,
`Deb` and `RPM`. The command that runs them, `cmd/verify-packages`, exists as of
this commit and is described under "The command" below, and as of this commit
`make verify-packages` is the target that runs it; that target is wired into
neither `ci` nor `docker-ci`, so nothing in the repository invokes the command
automatically.
Nothing here decides whether a model is acceptable — `packaging.Verify` remains
the sole source of `Finding`s, and moving one of its assertions down into an
extractor would be a defect, because that separation is what keeps every
contract assertion provable on a host with no packaging tool installed.

**Why a subpackage, not more files in `packaging/`.** Three structural reasons,
not stylistic ones:

- The `grep -lE 'os/exec|exec\.Command' packaging/*.go` guarantee above is a
  **non-recursive** glob, so every `exec.Command` this package adds leaves it
  exactly true. An extractor placed in `packaging/` would have falsified it,
  along with `model.go`'s "they are inert data" and `contract.go`'s "nothing
  here opens a file at run time".
- `requirements`, `contractDependencies`, `forbiddenRoots`, `sourceBytes` and
  `postinstallSource` are unexported. From another package they are not merely
  off limits, they are **invisible**, so contract logic cannot leak into an
  extractor by accident.
- `packaging/drift_test.go` already declares `usrBinDir` and `underUsrBin` in
  package `packaging`. Those are test-only and belong to the drift guard; they
  stay exactly where they are, and `packaging/extract` declares its own copies
  with a comment recording the deliberate duplication.

`packaging` itself cannot move the other way: a `//go:embed` pattern may not name
a parent directory, so only a package rooted at `packaging/` can embed
`packaging/*`.

**Shared helpers and the error-text contract** (`packaging/extract/extract.go`).
`ErrToolUnavailable` is wrapped into every error caused by a tool that is not on
`PATH`, so `errors.Is` separates an environmental fault from a definitive one: a
tool that ran and rejected the artifact does **not** report it. Two unexported
helpers do the work — `lookTool`, which resolves a tool, and `runTool`, which
runs one with `exec.CommandContext` under the caller's context and folds its
standard error into the returned error. Every error either one returns carries
the literal prefix `packaging/extract: <tool>: `, for example
`packaging/extract: dpkg-deb: packaging tool unavailable`. The prefix is produced
inside those two helpers and never at a call site, so no path can return a tool
error without it, and it is a token an artifact's filename cannot forge —
matching on the bare tool name would be satisfied by any file called `b.rpm`.
Folding standard error in is also what puts the offending artifact's path into
the message, since `dpkg-deb` names the file it could not read there.

**The deb backend** (`packaging/extract/deb.go`). `Deb(ctx, path)` runs
`dpkg-deb` three separate times under `ctx`, once per thing it needs, and caches
nothing between them:

- `dpkg-deb -x <deb> <dir>` extracts the payload into scratch space. That
  extraction was measured to reproduce recorded modes exactly (0640, 0700 and
  0750 under `umask 077`), which is why entry modes are read off the extracted
  tree rather than parsed out of the `drwxr-x---` strings `dpkg-deb -c` prints.
- `dpkg-deb -e <deb> <dir>` extracts the control members. It writes a
  **directory** and emits no tar stream, so a `-e … | tar -xO postinst` pipeline
  is not a valid way to read one of them.
- `dpkg-deb -f <deb> Depends` reads the dependency field. A package declaring no
  dependencies exits 0 with empty output, so absence is not a failure.

Neither `dpkg-deb -c` nor `-I` is used: the extracted tree already carries exact
modes, and `-f` gives the one field needed. `dpkg-deb -c` *is* used one package
over, in `internal/packagingtest`'s own tests, as an independent oracle for the
fixture builder — a different role in a different package.

Entries come from a `filepath.WalkDir` over the extracted payload with the tree
root itself skipped, so `/` is never an entry, while the intermediate
directories `dpkg-deb` synthesizes for a declared path **are** — a package
rooted at `/opt/phx/…` and `/usr/bin/…` archives `./opt/`, `./opt/phx/`,
`./usr/` and `./usr/bin/` too, and each becomes an entry. Directory entries
carry `Mode` and nil `Content`; regular files carry their bytes except those
under `/usr/bin`, whose nil `Content` means "deliberately not captured" rather
than "empty file"; symlinks, devices and fifos are not emitted at all, so a
required destination shipped in one of those shapes surfaces as a loud
`missing_path` rather than being silently accepted. `Config` is true for the
destinations the `conffiles` control member lists, and a `conffiles` line that is
not an absolute path is an error rather than a dropped row — an artifact read
wrong must not come back as a confident model. `Postinstall` is a non-nil
`Scriptlet` exactly when a `postinst` control member exists and `nil` when it does
not, keeping "ships none" distinct from "ships the wrong bytes". `Dependencies`
are split on commas only and trimmed, so alternatives (`a | b`) and version
constraints (`c (>= 1)`) pass through verbatim in declaration order; splitting on
`|` would make `Verify`'s rule against alternatives unfireable. `Owner` and
`Group` are left empty for a deb, because a `dpkg-deb`-extracted tree cannot
recover the archive's recorded ownership and nFPM's DEB tar records numeric
UID/GID 0 anyway — both fields are informational per M1.

**What failure means for `Deb`.** Everything in this paragraph is a statement
about the deb backend alone; the rpm backend's outcomes are stated separately
with that backend below, and none of this is a claim about extraction in
general. `Deb`
distinguishes three outcomes. (1) **Absent optional metadata is not a failure.**
A package with no `Depends` field returns no dependencies and a nil error,
because `dpkg-deb -f <deb> Depends` exits 0 with empty output there; a package
with no `conffiles` member marks no entry `Config`; a package with no `postinst`
member returns a nil `Postinstall`, which stays distinct from the non-nil empty
`Scriptlet` a zero-byte member returns. (2) **A definitive fault returns a
non-nil error and the zero-value `packaging.Model`** — never a confidently empty
model, which would otherwise verify as a pile of absent paths indistinguishable
from a genuinely broken package. A nonexistent path and a file that is not an
`ar` archive both land here, and both name the offending artifact, because
`dpkg-deb` writes the filename to standard error and `runTool` folds it in.
Metadata that is present but malformed lands here too: a `conffiles` line that
is not an absolute path is an error rather than a row silently defaulted to "not
a configuration file". (3) **An environmental fault also carries
`ErrToolUnavailable`**, so `errors.Is` separates "this host has no `dpkg-deb`"
from "`dpkg-deb` ran and rejected the artifact". Every error that comes from
*running* the tool — outcome (3), and the two archive faults in (2) — carries the
`packaging/extract: dpkg-deb: ` prefix, because `runTool` produces it. The
malformed-`conffiles` error does not: no tool failed there, so it reads
`packaging/extract: conffiles entry "…" is not an absolute path` and names the
offending line. Only (3) satisfies `errors.Is(err, ErrToolUnavailable)`.

**The deb backend's tests** (`packaging/extract/deb_test.go`,
`extract_test.go`). The happy
path builds a throwaway `.deb` with `packagingtest.BuildDeb` into a
`t.TempDir()` and asserts every model field against literals hand-written from
the `Spec` the test itself declares — never against a value the extractor,
`Verify` or a contract helper produced. The destination assertion is **set
equality in both directions**, including those synthesized parents and excluding
`/`: a one-directional membership check would pass while the extractor invented
or dropped entries. Around it sits one fixture-backed row per outcome above: a
fixture with no config file, no `Depends` and no postinstall; one whose
postinstall is a zero-byte member; a nonexistent path and a file of arbitrary
non-`ar` bytes, both required to return the zero-value model; a `conffiles`
member holding a relative path, which needs `packagingtest.BuildDebRaw` because
`dpkg-deb` will not `--build` such a tree; and `t.Setenv("PATH", "")` for the
`ErrToolUnavailable` row, which needs no packaging tool and therefore executes
on every host with no skip. `extract_test.go` is an **internal** test (package
`extract`) holding table-driven tests over the unexported `conffiles` and
`Depends` parsers, reaching the shapes a real `dpkg-deb` cannot be made to emit
(an empty comma-separated component, a member of nothing but whitespace); they
supplement the fixture-backed tests rather than replacing them. Every
fixture-backed test resolves `dpkg-deb` through `packagingtest.LookTool`, so on
a host without it they skip with an explicit reason while the parser tables and
the `ErrToolUnavailable` row still run.

**The rpm backend** (`packaging/extract/rpm.go`). `RPM(ctx, path)` runs four
`rpm -qp` metadata queries under `ctx` and extracts the payload once. Nothing is
cached between them:

- the **file table**, in one query:
  `rpm -qp --qf '[%{FILENAMES}\t%{FILEMODES:octal}\t%{FILEFLAGS}\t%{FILEUSERNAME}\t%{FILEGROUPNAME}\n]' <rpm>`.
  Destination, mode, config bit, owner and group all come from this one query,
  so there is no separate `-qpc` pass for the configuration-file designation.
- `rpm -qp --requires <rpm>` for the dependency text, paired index-wise with
  `rpm -qp --qf '[%{REQUIREFLAGS}\n]' <rpm>`, a second query carrying no
  dependency text at all.
- `rpm -qp --qf '%|POSTIN?{HAS\n%{POSTIN}}:{NONE\n}|' <rpm>` for the postinstall
  body behind a tag-presence marker.
- `rpm2archive -n -` reading the artifact on standard input, piped into
  `tar -x --no-same-owner` for the payload bytes. The pipe is wired **in Go**,
  so no shell is invoked and **both** exit statuses are the verdict — a shell
  pipeline would report only the last one. Standard error is folded into an
  error message and is read for nothing else.

  **Why `rpm2archive` and not the older `rpm2cpio`:** nfpm writes
  `RPMTAG_ARCHIVESIZE` as the sum of the file *content* bytes, while `rpmbuild`
  writes the size of the whole uncompressed archive. rpm 4.18's `rpm2cpio`
  validates the bytes it emitted against that tag and exits 1 when they
  disagree — *after* emitting a complete, perfectly valid stream. Every fixture
  here is built by `rpmbuild`, so the disagreement only ever appears against a
  real nfpm artifact, which is exactly what the packaging gate feeds it.
  `rpm2archive` reads both without complaint. Reverting to `rpm2cpio` turns
  that gate red immediately.

**The pipe uses `os.Pipe`, not `StdoutPipe`, and waits on both halves at
once.** This is a correctness requirement, not a style choice. `StdoutPipe`
leaves the *parent* holding the read end, and an earlier implementation waited
on the producer before the consumer: when the consumer exited early, the
producer kept writing
into a full pipe, and because this process still held a reader it never took
`SIGPIPE` — so the wait never returned and extraction hung forever with no
deadline. Both ends are now `*os.File`, handed straight to the children with no
copying goroutine in between, and **this process closes both of its copies as
soon as both children are running**: dropping the read end is what lets
`SIGPIPE` reach a producer whose consumer has gone, and dropping the write end
is what gives the consumer its end of input. The two `Wait` calls then run
concurrently so neither can block the other's reaping.

Error precedence follows from the same asymmetry: the producer's status is
reported first, because a producer that dies mid-stream makes the consumer fail
too and is the more useful root cause — **except** when the producer died of
`SIGPIPE`, which means the consumer went first and the consumer's status is the
real explanation.

**Why the file table is a tab-delimited `--qf` and not rpm's positional dump
alias.** The alias *pads* its columns — an `X` where a symlink target would go,
an all-zero digest for a directory — so its field count is stable at eleven; an
earlier claim that its columns can be empty was wrong and is corrected here. The
reason it is not parsed is different and measured: **a destination containing a
space** (`/opt/phx/with space.txt`) yields **twelve** whitespace fields, so no
whitespace-positional scheme is sound, while the five-field tab query parses the
same package correctly. Two lesser reasons stand: the alias's column set is a
formatting detail rpm documents nowhere and has changed before, and it
pre-digests the config designation into an `isconfig` column where
`%{FILEFLAGS}` gives the raw bit, read with a named `rpmfileConfig = 1` constant
citing rpm's `RPMFILE_CONFIG`. The parse uses `strings.Split(line, "\t")` and
requires exactly five fields, because `Split` preserves empty fields where a
whitespace split would collapse a row whose owner name is empty; the mode is
`strconv.ParseUint(field, 8, 32)` — base 8 **explicitly**, so both `0100644` and
`%{FILEMODES:octal}`'s `100644` read as the same file; the flags field must be
decimal. Empty output and the literal `(none)` mean zero entries; anything else
parses strictly, and a wrong field count, a non-octal mode or a non-decimal
flags field is an error rather than a defaulted row.

**Why the postinstall body comes from the `POSTIN` header tag and not from
rpm's scriptlet report.** That report is human-readable prose with no escaping,
no quoting and no length prefix, so a `%post` body containing the lines
`preuninstall scriptlet (using /bin/sh):` and `postinstall program: /bin/sh`
renders them at column 0 **byte-identically to real headers** (measured on rpm
4.18). Any header-anchored terminator therefore truncates such a body, and the
truncated bytes can still be exactly what the contract expects — a package
shipping *extra* postinstall code would verify clean. No smarter regexp fixes
that: the information needed to tell body from header is not in the output. The
tag-presence marker cannot be fooled, because rpm emits it from a tag-presence
test, before the body, at a fixed position: the parse splits on the **first**
newline and nothing else, `NONE` → `Postinstall = nil` (the measured verdict for
both a missing and an empty `%post`), `HAS` → the remaining bytes verbatim, and
any other prefix is an error. It also disambiguates a body that *is* the literal
text `(none)`, which comes back as `HAS\n(none)`.

**Trailing newlines are rpm's and stay rpm's.** Measured, rpm strips **all**
trailing newlines from a recorded scriptlet (`echo one\n\n\n` → `echo one`). The
extractor returns exactly what rpm recorded and **must not** append one:
re-adding a byte the package does not contain is the same normalization hazard
that forbids rewriting dependency expressions. It is also unnecessary, because
`packaging.Verify`'s scriptlet check already compares
`trimFinalNewline(scriptlet.Content)` against `trimFinalNewline` of the embedded
source, so a package differing only in a final newline produces no finding.

**Dependencies are filtered by provenance flags, never by name.** A requires
entry is dropped **iff** rpm marked it `RPMSENSE_RPMLIB` (`1 << 24`) or
`RPMSENSE_INTERP` (`1 << 8`), both declared as named constants in `rpm.go`.
Both classes are written by the *builder*, not by any packaging author:
`rpmlib` capabilities make an older rpm refuse the package, and the `INTERP`
requirement is what `rpmbuild` derives from a scriptlet's interpreter — measured,
**any** `%post` adds a `/bin/sh` requirement with flag word `1280` even under
`AutoReqProv: no`. Filtering on provenance rather than on a name is what keeps
the check honest in both directions: a package that genuinely declares
`Requires: /bin/sh` records a *second* entry with flag word `0`, and **that one
survives** while the generated one is dropped — a name filter would return
neither, and no filter would return both. Everything surviving passes through
verbatim, version constraints and spacing included. The two queries are
index-parallel and a line-count mismatch is an **error**, never a best-effort
merge. rpm does not preserve declaration order (measured: a dependency declared
third comes back first), so every assertion about rpm dependencies compares
**sorted clones** — which is what `Verify` does too. RPM has no `|` alternative
syntax, so the alternative-preservation rule is a deb-only concern and is tested
there.

**What failure means for `RPM`.** This paragraph is a statement about the rpm
backend alone. Absent optional metadata is not a failure: a package with no
requires table, no owned files or no `%post` yields no dependencies, no entries
and a nil `Postinstall` respectively, because rpm answers each of those with
empty output or the literal `(none)`. Everything else parses **strictly** — a
file-table line whose tab-separated field count is not five, a mode that is not
octal, a non-decimal flags word, a requires/flags line-count mismatch and an
unrecognised postinstall presence marker are all errors, and each returns the
zero-value `packaging.Model` rather than a confidently partial one, which would
otherwise verify as a pile of absent paths indistinguishable from a genuinely
broken package. A tool that is not on `PATH` yields an error wrapping
`ErrToolUnavailable`; a tool that ran and rejected the artifact yields one that
does not, so `errors.Is` still separates "this host has no rpm" from "this file
is not an rpm".

**Where the two backends deliberately differ.** Each row is a statement about
one backend; none of it may be restated as one sentence about "the extractors".

| | `Deb` | `RPM` |
| --- | --- | --- |
| mode source | read off the tree `dpkg-deb -x` extracted, measured to reproduce recorded modes exactly | `%{FILEMODES:octal}` from the header, masked to `0o777`; setuid/setgid/sticky are not mapped to Go's `ModeSetuid`/`ModeSetgid`/`ModeSticky`, since the contract compares `Mode.Perm()` only |
| owner/group | left empty — a `dpkg-deb`-extracted tree cannot recover them | populated per entry from `%{FILEUSERNAME}`/`%{FILEGROUPNAME}` |
| intermediate directories | present — `dpkg-deb` archives `./opt/`, `./opt/phx/`, `./usr/` and `./usr/bin/` for a package rooted at `/opt/phx/…` and `/usr/bin/…`, and each becomes an entry | absent — rpm owns only what `%files` lists, so a package rooted at the same paths yields **no** entry for `/opt`, `/opt/phx`, `/usr` or `/usr/bin` |
| empty scriptlet | constructible: a zero-byte `postinst` member yields a non-nil `Scriptlet` with empty `Content`, distinct from a nil one | not constructible: an empty `%post` builds but records no body, and its `POSTIN` presence marker reads `NONE`, so it is indistinguishable from shipping none |
| config designation | the `conffiles` control member | the `RPMFILE_CONFIG` bit of the same file-table query |
| dependency order | preserved, since it comes from splitting one field | not preserved by rpm, so callers sort |

What both share: directory entries carry `Mode` and nil `Content`; regular files
carry their bytes except under `/usr/bin`, whose nil `Content` means
"deliberately not captured"; symlinks, devices and fifos are not emitted at all,
so a required destination shipped in one of those shapes surfaces as a loud
`missing_path`; and every error from running a tool carries the
`packaging/extract: <tool>: ` prefix, with `ErrToolUnavailable` wrapped in only
when the tool is not on `PATH`.

**The rpm backend's tests** (`packaging/extract/rpm_test.go`). This file is an
**internal** test (package `extract`), because three of its tables drive
unexported parsers. The happy path builds a throwaway `.rpm` with
`packagingtest.BuildRPM` and asserts every model field against literals
hand-written from the `Spec` the test itself declares: destination **set
equality in both directions**, containing exactly the declared paths and **no**
synthesized parents; `Mode.Perm()` per entry including a `0750` directory, a
`0700` directory and a `0640` file; `fs.ModeDir` and nil `Content` on the
directories; `Config` true for exactly the `%config` file; byte-equal `Content`
for every regular file except the `/usr/bin` one, whose `Content` is nil; and
**three distinct owner/group pairs**, so an extractor that read one owner and
reused it cannot pass and no assertion could pass against a hardcoded `root`.
Its dependency assertion compares sorted clones against a hand-written literal
and adds two separate provenance assertions — no returned element carries the
`rpmlib` prefix, and none is `/bin/sh` — even though the fixture ships a
`%post`. A second fixture declares a `%post` body containing the two
header-shaped lines above and asserts `Postinstall.Content` byte-equal to it;
that test is the standing guard against reintroducing a report-anchored parse,
which returns a truncated body for exactly that fixture. A third pair of
fixtures pins the trailing-newline behaviour in both directions. The pipe helper
gets its own three rows, each required to surface as an error carrying the
failing half's `packaging/extract: <tool>: ` prefix: `rpm2archive` against a file
that is not an rpm (the source half exits non-zero), a real artifact extracted
into a directory that does not exist (the destination half never starts), and a
real artifact whose payload is deliberately larger than any pipe buffer piped
into a `tar` given an option it does not implement (**the destination half
exits before draining the pipe**). That last row is the deadlock guard: it is
the case a sequential `Wait` hangs on forever — measured, it does not return,
and the row's own 30-second deadline fails it rather than letting the test
binary time out — while the concurrent implementation returns in under a tenth
of a second. It is also the case a happy-path row cannot reach, because the
hang needs the *consumer* to leave first with more than a buffer still to
write. None of the three simulates a denial by `chmod`-ing a directory
unwritable: root and `CAP_DAC_OVERRIDE` write into one anyway, so such a row
asserts a property of whoever ran the test rather than of the code (see
`docs/agents/skills/dont-use-chmod-to-simulate-permission-denied-in-tests.md`);
a file that is not an rpm, a path that does not exist and an unimplemented
option get the same answer for every user. The happy path is the other side of
that contract, proving a producer's progress chatter on standard error is not
treated as a
failure. The four table-driven tables — the file-table line parser, the whole
file-table output, the dependency-pairing function with the *measured* flag words
`0`, `12`, `1280` and `16777226`, and the postinstall presence-marker parser —
need no tool and execute on **every** host. Every fixture-backed test resolves
*all four* of its tools through `packagingtest.LookTool` before it builds
anything — `rpmbuild`, `rpm`, `rpm2archive` and `tar` — so a host missing any one
of them skips with a message naming that tool and
`make docker-ci`, while inside the dev image — which sets
`PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1` — the same call **fails** instead, which
is what makes a green `make docker-ci` proof that they ran.

**The declared-versus-generated `/bin/sh` row** is the end-to-end proof that
rpm dependencies are reconciled by provenance and never by name, and it is a
statement about the rpm backend alone. One fixture **both** declares
`Requires: /bin/sh` **and** ships a `%post`. Measured, its raw
`rpm -qp --requires` output lists `/bin/sh` **twice**, with index-parallel flag
words `0` (declared) and `1280` (`INTERP|SCRIPT_POST`, which `rpmbuild` derives
from the scriptlet's interpreter).
`TestRPMKeepsADeclaredInterpreterAndDropsTheGeneratedOne` asserts that
`Dependencies` contains `/bin/sh` **exactly once**, which discriminates all
three candidate implementations: a name-based
filter returns **0** occurrences and silently disarms a genuinely declared
dependency, an unfiltered pass-through returns **2** and reports one the package
never declared, and only the flag-based filter returns **1**. The raw count of
two is asserted first, so that if `rpmbuild` ever stopped generating the
interpreter requirement the row fails loudly instead of letting a do-nothing
filter satisfy "exactly once" vacuously. The test also re-checks that `alpha`
and `gamma >= 1` survived and that no `rpmlib(` capability did, so exactly-once
cannot be bought by discarding the requires table wholesale.

**What the rpm backend's degenerate and error rows pin.** Every sentence here is
about `RPM`; none of it is a claim about `Deb` or about "the extractors".
`TestRPMOnFixtureWithoutOptionalMetadata` builds one fixture with no
`Requires:` line, nothing marked `%config` and no `%post` section, and makes
three separate assertions on it: `Postinstall` is **nil**; `Dependencies` is
empty **with a nil error**, asserted alongside a check that the raw
`rpm -qp --requires` output for the *same* artifact was **not** empty — measured,
`rpmbuild` writes three `rpmlib(...)` capabilities into every artifact, so that
second check is what proves the provenance filter removed them rather than the
assertion passing vacuously; and `Config` is false on **every** entry, including
the one whose name ends in `.conf`, over a destination set pinned in both
directions first so the sweep cannot run over an empty entry list.
`TestRPMOnUnreadableArtifact` covers a path that does not exist and a file of
arbitrary non-rpm bytes (a fixed literal, so a failure reproduces byte for
byte): each returns a non-nil error and the **zero-value** `packaging.Model`,
names the offending artifact path, carries the `packaging/extract: rpm: `
prefix, and does **not** satisfy `errors.Is(err, ErrToolUnavailable)`, because
`rpm` ran and rejected the file. `TestRPMWithoutToolWrapsErrToolUnavailable` is
the converse: `t.Setenv("PATH", "")` makes the first `rpm` lookup fail, and the
error both wraps `ErrToolUnavailable` and starts with that same prefix. That row
needs no packaging tool, so it executes on **every** host and never skips.

**The one rpm row that is narrowed rather than covered** is the non-nil-but-empty
`Scriptlet`. An empty `%post` builds but records no body at all — measured,
`rpm -qp --scripts` prints `postinstall program: /bin/sh` with no scriptlet
header, `%{POSTIN}` is `(none)`, and rpm's own presence marker
`%|POSTIN?{HAS}:{NONE}|` reads `NONE` — so the rpm backend correctly reports
such a package as `nil`, and the empty-but-present side is proven for the **deb**
backend only, by `TestDebOnFixtureWithEmptyPostinstall` in
`packaging/extract/deb_test.go`, where a zero-byte `postinst` member is
genuinely constructible. `packagingtest.BuildRPM`'s `t.Fatalf` on a non-nil
empty `Postinstall` is what makes the case impossible to reintroduce for rpm by
accident, and both the narrowing and its measurement are recorded in a comment
on `TestRPMOnFixtureWithoutOptionalMetadata` as well as here.

**The command** (`cmd/verify-packages/main.go`). This is what turns the two
backends and `packaging.Verify` into something a person can run. It sits under
`cmd/` because that is where every binary in this repository lives, and its
non-product name says what it is: a repository tool, deliberately **not** added
to `make build` and **not** given a `.goreleaser.yaml` `builds:` entry, so `bin/`
still holds only `pilothouse` and `pilothoused` and no development tool can leak
into a package. It is developer tooling in the strict sense used everywhere else
in this document — it performs no privileged operation, registers no broker query
or action, adds no capability, imports none of `internal/broker`,
`internal/platform` or `internal/capability`, and is unreachable from either
shipped binary. At this commit `make verify-packages` is the one thing that runs
it, and that target is wired into neither `ci` nor `docker-ci`, so nothing
invokes it automatically (see "The make target" below).

*Shape.* `main` is one line, `os.Exit(run(context.Background(), defaultDeps(),
os.Args[1:], os.Stdout, os.Stderr))`. `run` is the composing entry point and the
function whose return value becomes the exit status. It takes a `deps` — an
**ordered slice** of backends (`ext`, `label`, `extract`) plus the verification
function — and `defaultDeps()` is the only thing production code constructs:
`{".deb", "deb", extract.Deb}`, `{".rpm", "rpm", extract.RPM}`, and
`packaging.Verify`. The slice is ordered rather than keyed because discovery
order is a guarantee, not an accident; a map would leave it unspecified.
`packaging.Verify` is named exactly once in non-test code, inside `defaultDeps`,
so every verdict printed for a real artifact is that function's. The command
invents no finding code and never compares a `Finding.Message` — it only prints
one.

*Discovery.* With no positional arguments, `run` globs the `-dir` directory
(default `dist`) once per backend, **in backend order**, sorting each glob's
matches and concatenating them: every `*.deb` in sorted order, then every
`*.rpm` in sorted order. So `z.deb`, `a.deb` and `b.rpm` in one directory are
reported as `a.deb`, `z.deb`, `b.rpm`, and a file matching neither glob is not an
artifact. With positional arguments, those are the paths, in the order given, and
`-dir` is not consulted at all.

*Dispatch.* The format comes from the file extension, matched
case-insensitively against `deps.backends`: `.deb` to `extract.Deb`, `.rpm` to
`extract.RPM`. An extension no backend claims is an error naming the path and its
extension — never a silently skipped file.

*Output shape.* The report goes to standard output; standard error carries only
the reasons no report could be produced (a bad flag, an unreadable directory, no
artifacts). Per artifact, one header line naming the path, its format label and
its finding count — `dist/pilothouse_1.0_amd64.deb (deb): 3 findings` — followed
by one tab-indented line per finding carrying `Code`, `Path` and `Message`. A
finding whose `Path` is empty, which is what a dependency mismatch or a missing
scriptlet is, renders its path column as `-` rather than blank. An artifact whose
extraction fails prints a failure line in place of a header, carrying the path,
the format label and the extractor's error — including the
`packaging/extract: <tool>: ` prefix — folded onto one line, and processing
**continues** to the remaining artifacts, because one unreadable file must not
hide the others' findings. A summary line closes every run.

*Exit semantics.* Non-zero if any finding was reported or any artifact could not
be extracted; zero only when every artifact was extracted and verified with no
findings.

*The empty-`dist/` message*, which is the output this command produces on a
development host and inside the development image, since neither builds
packages by default. It names the directory searched and both globs, names the
three GoReleaser Pro workflows that are the CI producers
(`.github/workflows/release.yml` on a tag, `.github/workflows/snapshot.yml` on
`main`, and `.github/workflows/packaging.yml` on pushes and pull requests
targeting `main`), and states in one line that `make package` is the local producer that
builds them into `dist/` but requires goreleaser Pro, which is not installed on
a stock development host or in the development image, so it will not succeed
there. Every `make` target the message names is really defined in this
repository's Makefile — `TestRunWithNoArtifactsExplainsWhereTheyComeFrom`
checks each one against `Makefile` itself rather than against a second copy of
the expected list — so a reader is never pointed at something that does not
exist.

*The make target.* `make verify-packages` is a one-line target — `$(GO) run
./cmd/verify-packages`, with no prerequisite and no arguments, so it verifies
whatever `dist/` holds — carrying a `##` help comment like every other
non-obvious target and listed in `.PHONY` because it produces no file of its
own. It is **deliberately absent from `ci` and from `docker-ci`**, and that
absence is the point rather than an oversight: `dist/` is empty on this host and
in the development image, so the target fails there by design, printing the
empty-`dist/` message above — the searched directory and both globs, all three
GoReleaser Pro workflows, and, on one line, that `make package` is the local
producer that fills `dist/` but requires goreleaser Pro, absent here and in the
development image. Wiring it
into `ci` would make every local and containerized run of the full gate fail on
a clean checkout and would break the "`make ci` / `make docker-ci` runs every
CI gate that runs without credentials, and local green means the credential-free
gates will be green" promise that `AGENTS.md` and `README.md` both make. CI
*does* run a packaging gate now — `.github/workflows/packaging.yml`, which
builds the artifacts, runs `make verify-packages` against them, then
installs them on pinned Debian and Fedora containers with
`make verify-package-install`, and finally boots real Debian and Fedora VMs
under QEMU/KVM and validates the installed package on a host with systemd as
PID 1 — but that
gate needs the `GORELEASER_KEY` secret and the goreleaser Pro distribution, so
it cannot run locally at all (and its booted-VM tier needs KVM besides), and
it is the single named exception to the
mirror-CI promise rather than something `ci` could reproduce. An agent or developer who
runs `make verify-packages` on a checkout with nothing built should read the
failure as the expected outcome, not as a defect to fix; the target becomes
useful once a build has put real artifacts in `dist/`. The exclusion is meant to
stay checkable by `make -n ci | grep verify-packages` printing nothing, which is
why the same commit made the Makefile's `GOFILES` expand in the shell at recipe
time rather than in make at parse time: `format-check` otherwise inlined every
Go source path — including `cmd/verify-packages`' own files — into the dry-run
text, so the check reported wiring that does not exist. The set of files
formatted is unchanged, and a command-line `GOFILES=` override still wins, which
is what `scripts/bump_test.sh` relies on.

*The local producer.* `make package` is the target that fills `dist/`: after a
guard it runs exactly `goreleaser release --snapshot --clean`, which builds
snapshot `.deb` and `.rpm` artifacts, publishes nothing, needs no tag and needs
no `GITHUB_TOKEN`. It has no prerequisite — GoReleaser's own `before` hooks run
`go mod tidy` and `go tool templ generate` — carries a `##` help comment and is
listed in `.PHONY`. Like `verify-packages` it is **deliberately absent from
`ci` and from `docker-ci`**, and goreleaser is deliberately **not** installed
in the development image, so both gates stay green without a Pro key. The guard
resolves `goreleaser` through a `PATH` lookup **inside the recipe shell** — no
absolute path and no parse-time `$(shell …)` cache — so a per-invocation `PATH`
override reaches it, and it reads the binary's own `goreleaser --version`
banner, rendered by `github.com/caarlos0/go-version`'s `Info.String()`. The
distribution is decided from the banner's **app-name line** (`goreleaser-pro:
…` for Pro, `goreleaser: …` for OSS), never from the version token: the
published goreleaser-pro v2 binary reports `GitVersion:\t2.17.0` with no `-pro`
suffix, so a version-token rule would reject exactly the binary this target
must accept, while older Pro v1 tags did carry the suffix, which is why the
version parse tolerates one without requiring it. The major version is the
integer before the first `.` on the `GitVersion:` line, after an optional
leading `v` and tolerating any pre-release suffix, and it must be `2` — what
the repository's existing `~> v2` pin resolves to. Each of the four failure
modes — not on `PATH`, not the Pro distribution, wrong major version, version
undeterminable (a source build reports `devel`) — prints its own case-specific
reason alongside the required-version statement and
<https://goreleaser.com/pro/>, then exits non-zero **before** any goreleaser
subcommand runs. Failing that guard is the expected outcome on a development
host and in the development image.

*The CI packaging gate* is `.github/workflows/packaging.yml` (workflow name
`Packaging`). It triggers on `push` to `main` and on `pull_request` targeting
`main` with `types: [opened, synchronize, reopened, labeled]` — no tag
trigger, no `workflow_run` — holds `permissions: contents:
read` because it publishes nothing, and runs **three** jobs. The first,
`packages`, installs the packaging and cgo build dependencies (`rpm`
explicitly, since no ambient runner tool may be relied on, plus the
`libpam0g-dev`/`libsystemd-dev` headers and the `gcc-aarch64-linux-gnu` cross
toolchain the arm64 cgo `pilothoused` build needs — `cpio` is **not** among
them: nothing in the workflow uses it, and `packaging/extract`'s rpm backend
runs `rpm2archive` piped into `tar`), runs
`goreleaser/goreleaser-action@v7` with the Pro distribution and the existing
`~> v2` constraint on `release --snapshot --clean`, then asserts and verifies
what came out and uploads the two artifacts. The second, `install`, downloads
those artifacts and installs them on the two digest-pinned distro containers;
it is described in full under "How the script is invoked" above. The third,
`vm-boot`, downloads the same artifacts and boots real Debian and Fedora VMs
with them; it is described in full under "Booted-VM harness: the CI job
(`vm-boot`)" above. So the gate
does not stop at the artifact payload: it also proves the packages install,
and that the installed package works on a booted host.

It is a **separate file** from `.github/workflows/test.yml` because GitHub does
not hand repository secrets to a run triggered by a fork's pull request: a job
needing `GORELEASER_KEY` inside `test.yml` would fail for every fork
contributor, and `test.yml` must stay green for them and must gain no
`GORELEASER_KEY` dependency. Instead the `packages` job carries
`if: github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository`,
so a fork pull request **skips** the job — neutral, never failed — and never
receives the key. Neither `install` nor `vm-boot` restates that condition:
`needs: packages` means a skipped `packages` skips them too. (`vm-boot` does
carry an `if:` — the `vm-boot` label gate — but nothing about forks.)
`GORELEASER_KEY` is the only secret the
workflow takes; there is
no `GITHUB_TOKEN`, because `--snapshot` publishes nothing and needs no tag.

It is also a separate file from `.github/workflows/snapshot.yml`, which is a
publisher rather than a gate: snapshot.yml runs after `Tests` succeeds on
`main`, so it never sees a pull request, and it publishes the rolling `dev`
pre-release. The consequence is that a push to `main` **builds the packages
twice** — once here, verified and published nowhere, and once in snapshot.yml,
published. That double build is deliberate and must not be optimized away:
a gate chained to the publisher could not block a pull request, and a gate
sharing a run with the publisher could not fail without breaking publication.
Adding verification to the publishing path is a separate follow-up, not part of
this workflow.

Each format's presence is asserted by its **own** step — one `ls dist/*.deb`,
one `ls dist/*.rpm` — so a build that produced only one of them fails on its
own. A single `upload-artifact` step globbing both with
`if-no-files-found: error` would succeed with just one format present, which is
why the uploads are two steps as well, each with `if-no-files-found: error`.
Between the assertions and the uploads the job runs `make verify-packages`,
not `continue-on-error`, so any contract finding fails the job.

*Its tests* live in two files, split by what they need from the host, and every
test in both drives `run` itself, never a reporting or dispatch helper.
`main_test.go` holds the cells that execute on **every** host with no packaging
tool and no skip: discovery over a mixed directory, dispatch by the
`packaging/extract: <tool>: ` error prefix, the unsupported extension, the
missing artifact path, the empty-`dist/` message and the injected clean-path
table. `integration_test.go` holds the cells that need a real synthetic artifact
— an explicit `.deb`, an explicit `.rpm`, and one discovered directory holding
one of each beside a decoy — and those are **deep-gated**: they build their
fixtures with `internal/packagingtest`, which resolves every tool through
`packagingtest.LookTool`, so on a host without `rpmbuild`/`rpm`/`rpm2archive`/`tar`
the `.rpm` and mixed cells skip naming the missing tool, while `make docker-ci`
sets `PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1` and the same lookup **fails** there
instead. A green `docker-ci` is therefore proof those three cells ran. All of
them go through `run(ctx, defaultDeps(), …)` — real artifact in, `extract.Deb` or
`extract.RPM` out, `packaging.Verify` deciding — and none injects a backend or a
verification result, which is what keeps the hand-built `deps` below bounded to
the outcomes a real artifact cannot produce. A placeholder fixture cannot satisfy
the artifact contract, so each block carries real `Verify` findings; the
assertions are structural — the block names its file, carries its own format
label, lists at least one finding, shows no extraction-failure text, and (for the
`.rpm`) contains no `dpkg-deb` text — plus membership of every printed `Code` in
a hand-written literal of the nine codes, so an invented code fails. No test
there asserts the wording of a finding.

The artifacts `main_test.go` stages are arbitrary bytes, so extraction is
expected to fail there, and its dispatch proof reads the
`packaging/extract: <tool>: ` prefix rather than a bare tool name: the deb
block must contain `packaging/extract: dpkg-deb: ` and not
`packaging/extract: rpm`, and the rpm block the converse. That distinction is
load-bearing — a bare `rpm` check would be satisfied by the filename `b.rpm`
itself — and it holds in both environments, since a missing tool yields
`ErrToolUnavailable` and a present one yields a parse failure, both carrying the
prefix. Every substring assertion over a multi-artifact run is applied to the
slice of output belonging to the artifact under test, from its line through the
indented lines beneath it, never to the whole capture.

One test, `TestRunExitStatusIsDerivedFromResults`, is the only place a `deps` is
built by hand, and the reason is worth recording. Exit 0 is correct only for a
package that satisfies the artifact contract, and no such package can be built
here: a conforming synthetic fixture would have to restate `contract.go`'s tables
inside a test, and the repository cannot build a real project package locally or
in `make docker-ci` (`.goreleaser.yaml` uses a GoReleaser Pro block, and the dev
image deliberately has no goreleaser). Asserting the clean path one level down,
on a reporting helper, would leave `run`'s own return value asserted only on
failures — where a `run` that returned 1 unconditionally would pass everything
(`docs/agents/skills/test-the-composing-function-not-its-merge-helper.md`). So
that table injects an extraction result and a verification result, and only
those: a placeholder `Model` with a `verify` returning nothing gives exit 0 with
the zero-finding summary and no failure text, for a `.deb` row and an `.rpm` row;
two hand-written findings, one with an empty `Path`, give exit 1 with both
rendered and the empty path shown as `-`; and a backend erroring on the first of
two artifacts gives exit 1 with the second still processed. The injected backend
records the path it was handed, so the clean rows cannot pass against a `run` that
reports success without invoking a backend. The doubles carry no contract
knowledge — the findings use invented codes that are none of the nine, and the
models are the same `/opt/phx/…` placeholders the fixtures use. Everything a real
artifact *can* demonstrate — discovery, dispatch, extraction failure — is asserted
through `defaultDeps()`, which keeps the seam a seam and not a shim
(`docs/agents/skills/exercise-the-actual-boundary-not-a-precomputed-shim.md`).
The narrowing that remains, stated plainly: **no test asserts exit 0 for a real
artifact**, because no conforming one can be produced here.

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
  `CLAUDE.md` is a symlink to `AGENTS.md`, so the two are byte-identical by
  construction and can never drift.
- `AGENTS.md` (and therefore `CLAUDE.md`) is deliberately generic: it carries
  only repository-wide process, stack, build-target, templ, release, and
  skill-review instructions, with no per-module feature inventory and no
  module-specific claim anywhere in it. A change that adds or reshapes a
  module's surface therefore does not make any sentence in it stale — the
  per-module feature narrative lives here in `yeti/OVERVIEW.md`, in
  `docs/modules.md`, and in `README.md`'s "What works" list. Confirm this is
  still true when reviewing AGENTS.md's "update relevant documentation after
  any change to source code" invariant for a feature change, rather than
  assuming either that it must be edited or that it can be skipped
  unexamined. (#51's host-image series was reviewed against it on exactly
  these grounds and required no edit to either file.)
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
- `docs/autoupdate.md` — the automatic-update reporting surface end to end:
  response schema, the bootc and rpm-ostree policy normalizers, the daemon-side
  `AutoUpdateManager`, and the Maintenance page's read-only "Automatic updates"
  section (`queryAutoUpdate` → `autoUpdateSection`), which exposes no mutation.
- `docs/branding.md` — the neutral-branding rules: the canonical
  self-description sentence and where it may be used, the rule that `updex`/
  `sysext`/`systemd-sysext`/`bootc`/`rpm-ostree` are tool and capability
  identifiers rather than branding, and the allowlist of sites (test fixtures,
  `docs/capabilities.md` fixture prose, the `release.yml` dispatch, mock Fleet
  data, `yeti/` historical narrative) that naming sweeps must leave unchanged.
