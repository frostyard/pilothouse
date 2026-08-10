# Pilothouse

Pilothouse is a local web administration console for image-based Linux systems. It starts with an attractive live system dashboard and, once the broker is pointed at `updex`, complete sysext lifecycle management through the `updex` interface.

The application is bootstrapped from [housecat-inc/scratch](https://github.com/housecat-inc/scratch): Go and templ on the server, HTMX for focused page updates, an embedded design system, and no Node runtime or external frontend assets.

## What works

- Live CPU, memory, persistent storage, load, uptime, network totals, host, OS, and kernel metrics, with utilization gauges safely bounded from 0–100% even for zero-capacity or wrapped-counter inputs
- Automatic dashboard refresh every 15 seconds
- Live attention view for disk, memory, load, failed systemd units, and unavailable status sources
- Storage health that distinguishes expected immutable EROFS mounts from unexpected read-only or capacity-exhausted writable filesystems
- Storage Attention and topology deep links that land on the matching inventory, mount, or finding row
- Systemd service, socket, and timer inventory with administrator-only lifecycle and enablement controls
- Layered discovery of shared `sysupdate.d` and component-scoped `sysupdate.<name>.d` `updex` definitions
- Installed and merged state from `systemd-sysext`
- Extension update availability on the Extensions page — the pending component updates for each extension, an "Update available" badge on every affected extension row, and the aggregate count on the dashboard card
- Install, remove, update-all, and merge-refresh actions through `updex` and `systemd-sysext`
- System Podman inventory for containers, pods, images, engine version, reported image storage, and bounded log viewing
- Administrator-only container start, stop, restart, and safe removal actions
- System Docker Engine inventory with container lifecycle controls, bounded log viewing, and socket isolation
- Local Incus project inventory for containers, virtual machines, and images with lifecycle controls, live per-instance addresses and memory, a per-instance detail page (allowlisted configuration, devices, interfaces, snapshots), bounded console and supervisor logs, non-stateful snapshot create/restore/delete, read-only network and profile inventory with DHCP leases, and instance creation from the public image server as a background job
- Administrator-only browsing, download, and atomic upload within configured host file roots
- PAM authentication using host users and account policy
- Opaque, idle-expiring broker sessions with per-session CSRF tokens
- An unprivileged web process and a root-only action broker connected through a protected Unix socket
- Group-based administration, POST-only mutations, origin checks, strict command arguments, and bounded command timeouts
- Durable privileged-action history, destructive confirmations, and per-resource action serialization
- Durable background jobs for extension update and refresh operations
- Reboot-required posture, confirmed host reboot, and read-only host-image status — booted, staged, and rollback deployments with the image references and manifest digests `bootc` reports as the authoritative source, supplemented but never overridden by `rpm-ostree` version and checksum detail where it is present, plus soft-reboot eligibility when bootc exposes it
- Read-only automatic-update reporting on the Maintenance page for `bootc` and `rpm-ostree` — each updater's timer enablement and active state, next scheduled run, service state and last result, normalized policy, and whether local drop-ins customize its service or timer, or an explicit "not configured" statement when that updater is not set up; the section appears on any host reporting `bootc` or `rpm-ostree` and is omitted entirely on a host with neither, and Pilothouse never enables, disables, triggers, or reconfigures either updater
- Exact systemd backup timer monitoring with freshness and last-result health
- Liveness and broker-aware readiness endpoints at `/healthz` and `/readyz`
- Optional numeric local UID/GID ownership mapping for Pilothouse-managed SMB mounts
- Responsive desktop and mobile layouts
- Startup-time host capability probing (systemd, journald, updex, `systemd-sysext`, bootc, rpm-ostree, automatic-update pairs, Podman, Docker, Incus), advertised over an authenticated broker query; the daemon starts and registers only the privileged operations whose required capability is actually present, degrading gracefully instead of failing to start when systemd, journald, updex, `systemd-sysext`, or a container engine is absent or unreachable. Four of those capabilities — updex, Podman, Docker, Incus — are additionally opt-in, so "present" means "explicitly configured *and* reachable" rather than "detected on the host" (next bullet)
- Explicit opt-in for every optional dependency: `updex`, Podman, Docker, and Incus are probed only when `--updex`, `--podman-socket`, `--docker`, or `--incus` is configured on `pilothoused`, and an unconfigured one is reported absent without any I/O — no command is run and no socket is dialled — so a binary on `PATH`, a socket at a conventional path, or an exported `DOCKER_HOST` never enables anything by itself, and the packaged unit passes none of the four. Every feature bullet above that names updex, Podman, Docker, or Incus therefore describes a surface that appears only once its flag is set. The remaining capabilities (systemd, journald, `systemd-sysext`, bootc, rpm-ostree, automatic-update pairs) stay presence-probed, and the broker also no longer declares `Wants=` on any engine socket
- Machine-readable command probes parse stdout only. A successful command's stderr diagnostics do not corrupt its JSON contract; when a command fails, stderr is retained in the returned diagnostic while stdout remains separately available.

Pilothouse-managed SMB mounts can optionally map file ownership to a local
numeric UID/GID. Both IDs are required together; leaving both fields blank
preserves default ownership. Names and free-form mount options are not
accepted.

## Develop

Classify each pull request using [`docs/risk-tiers.md`](docs/risk-tiers.md),
selecting the highest tier represented anywhere in the final diff. Higher tiers
add targeted validation and review requirements; they do not replace CI. The
[public metrics index](docs/metrics/) links the auditable repository signals
and defines the rolling pull-request acceptance metric.

For work that spans sessions, start with [`.knowledge/README.md`](.knowledge/README.md).
It indexes the repository's binding instructions, verified corrections, learned
skills, architecture context, and subsystem documentation without duplicating
them.

Before pushing, run `make ci` (or `make docker-ci` on hosts without the
native toolchain) — it runs every CI gate that runs without credentials, in
the same order. Local green means the credential-free gates will be green in
CI. The pull-request test workflow starts from no token permissions, grants
only read-only contents access per job, and adds OIDC only to the unit-test job
for Codecov; its `govulncheck` installation is version-pinned. The read-only
`nightly-compliance.yml` workflow reruns the same `make ci` contract daily at
04:23 UTC and on manual dispatch. Its compliance job remains
credential-free; a separate job reports drift when the Copilot assignment
secret is absent.
Two workflow gates are exceptions. `.github/workflows/packaging.yml`, the
packaging gate, cannot run locally because it needs the `GORELEASER_KEY` secret
and the goreleaser Pro distribution. That gate does more than read the
artifacts: it installs them on pinned Debian and Fedora containers, so it
needs Docker and network access on top of the credentials, and its `vm-boot`
job boots real Debian and Fedora VMs under QEMU/KVM, so that tier needs KVM
too. The whole workflow stays outside `make ci` and `make docker-ci` by
construction. `make verify-packages` and `make verify-package-install` are the
local tools for the contracts it checks, once artifacts exist in `dist/`;
the booted-VM tier has no local `make` target.
`.github/workflows/image-tier.yml` separately needs root, live KVM, network
access, Podman 5, cosign and the GitHub-hosted `ubuntu-26.04` environment to
exercise released packaging plus checked-out executables on ephemeral uCore
derivatives, so it has no local make target either.

With the user-scoped token from the [Copilot workflow setup](docs/ai-fix-workflow.md#repository-setup)
configured, submitted review feedback on an open, non-draft pull request whose
branch is in this repository is handed to Copilot by
`.github/workflows/copilot-review-apply.yml`. Empty and approval-only reviews
are ignored, reruns are deduplicated by review ID, and every resulting change
must still pass the normal pull-request gates and human review.

With the `ANTHROPIC_API_KEY` repository secret configured, non-draft pull
requests whose branch is in this repository also receive an advisory,
least-privilege [Claude code review](docs/claude-code-review.md). Fork pull
requests are deliberately skipped, comments remain untrusted until a human
verifies them, and the workflow cannot approve, merge, deploy, or publish.

Go 1.26 or newer is required.

```bash
make test
make race
make build
sudo ./bin/pilothoused --socket /tmp/pilothouse-broker.sock --socket-group "$(id -gn)"
./bin/pilothouse --broker-socket /tmp/pilothouse-broker.sock
```

`make test` includes the process-level web checks in `test/e2e/`; they build
and start the real `pilothouse` binary on a loopback port and need no broker.

That broker runs with no optional tooling configured, so Podman, Docker,
Incus, and every `updex`-backed extension operation are absent from the
console; add `--podman-socket`, `--docker`, `--incus`, or `--updex` to the
`pilothoused` line to work on those surfaces, and `--dev` to the
`pilothouse` line to see the static Fleet preview.

Docker equivalents are available when the host does not have Go, PAM headers, or systemd headers installed:

```bash
make docker-generate
make docker-fmt
make docker-build
make docker-test
make docker-race
make docker-lint
```

Each target checks the reusable development image through Docker's build cache and uses persistent Docker volumes for Go and linter caches. Container commands run as the host user, so generated files and build output remain writable. `make docker-run` uses host networking and starts the web process, but broker-backed operations require separately mounting a broker socket into the container.

If local sign-in is unavailable, verify the privileged broker before debugging
the browser: `systemctl status pilothoused` and `journalctl -u pilothoused`.
The broker validates fixed storage-tool paths at startup; distro-provided
symlinks such as `pvs -> lvm` are accepted only when the resolved executable is
a safe root-owned regular file.

### Verify built packages

`make verify-packages` reads every `.deb` and `.rpm` in `dist/`, turns each one
into the packaging contract model, and prints the contract findings for it. It
exits non-zero if any artifact carries a finding or cannot be extracted.

An empty or missing `dist/` is the normal state on a development host, because
nothing here builds a package by default: GoReleaser Pro does that in CI,
through `.github/workflows/release.yml` on a tag,
`.github/workflows/snapshot.yml` on `main`, and
`.github/workflows/packaging.yml` on every push and pull request targeting
`main` (that workflow's booted-VM job additionally runs only on `main` or on
a pull request carrying the `vm-boot` label). The target then fails with a
message naming that directory, all three
workflows, and `make package` as the local producer along with its goreleaser
Pro requirement. That failure is the expected local outcome, not a defect to
fix.

```bash
make verify-packages
```

The target is deliberately not part of `make ci` or `make docker-ci`: those
gates must stay green on a checkout with no built artifacts, so that local
green still means the credential-free CI gates will be green. The one CI gate
they do not mirror is `.github/workflows/packaging.yml`, which builds the
artifacts, runs this same verification in CI, installs those same
artifacts on pinned Debian and Fedora containers with
`make verify-package-install`, and then boots real Debian and Fedora VMs and
validates the installed package on a host with systemd as PID 1. It cannot
run locally because it needs the `GORELEASER_KEY` secret and the goreleaser
Pro distribution — and its booted-VM tier needs KVM as well.

`make package` is the local producer: it runs `goreleaser release --snapshot
--clean`, which builds snapshot `.deb` and `.rpm` artifacts into `dist/`,
publishes nothing and needs no tag. It requires the goreleaser Pro
distribution at major version 2, which is deliberately not installed on this
host or in the development image, so on a stock checkout it fails with an
actionable message naming what was found, what is required, and
<https://goreleaser.com/pro/>. Once artifacts exist in `dist/`, `make
verify-packages` is the thing to run against them.

```bash
make package
```

### Verify package installation

`make verify-package-install` is the install-side sibling of `make
verify-packages`: instead of reading bytes out of an artifact, it installs the
artifact inside a real distro userland and asserts what the package manager and
the postinstall scriptlet actually produced. It runs
`packaging/verify-install.sh` inside a container image, as root, with only
`packaging/` and the artifact directory bind-mounted read-only.

Two variables drive it:

- `INSTALL_IMAGE` — the container image reference. There is no default; an
  unset value fails with a message naming the two digest-pinned images this
  validation targets,
  `debian:12@sha256:9344f8b8992482f80cba753f323adeaf17690076c095ccff6cc9536be98185dc`
  and
  `fedora:42@sha256:99e203b80b1c3d8f7e161ec10a68fd02b081ef83a3963553e513c82846b97814`.
- `ARTIFACT_DIR` — the directory of built artifacts, defaulting to `dist`.

```bash
make verify-package-install \
  INSTALL_IMAGE=debian:12@sha256:9344f8b8992482f80cba753f323adeaf17690076c095ccff6cc9536be98185dc
```

The script performs eight checks, each of them something the static payload
contract cannot see: that the hand-written per-format `dependencies` lists
resolve against the distro's real repositories; that the `pilothouse` user and
group exist afterward and match the installed sysusers declaration; that the
on-disk owner, group and mode of the configuration directories and env files
are right on the installed filesystem rather than in package metadata; that
every stack and module the installed PAM policy names exists on that distro;
that the distro's own `systemd-analyze verify` accepts both installed units;
that the cgo-linked binary's dynamic dependencies all resolve; that reinstalling
the same artifact succeeds with the account and ownership assertions still
holding; and that removal leaves the per-format state each manager promises
(conffiles surviving `dpkg -r` and gone after `dpkg -P`, no `.rpmsave` after
`rpm -e`, the account surviving both).

Like `make verify-packages`, it is deliberately outside `make ci` and `make
docker-ci`, and for a stronger version of the same reason: it needs artifacts
that a stock checkout does not have, plus Docker and network access — check 1's
whole point is dependency resolution against the distro's real repositories.

CI runs this exact target. `.github/workflows/packaging.yml`'s `install` job
depends on the build-and-verify job, downloads the artifacts it uploaded, and
runs `make verify-package-install` once per image across a two-entry matrix —
the pinned Debian image with the `.deb`, the pinned Fedora image with the
`.rpm`. That is why the packaging gate is not a payload-only check, and why it
stays outside `make ci` / `make docker-ci`. The same workflow's `vm-boot` job
carries the check one layer further, onto a booted host; it is described next.

### Booted-VM validation

A container shares the host's kernel and does not run systemd as PID 1, so the
install validation above can prove what a package *put on disk* but not what it
*does when the host boots*. `.github/workflows/packaging.yml`'s `vm-boot` job
closes that gap. It downloads the same verified artifact, boots a stock Debian
12 or Fedora 42 cloud image — pinned by checksum in `test/vm/images.env` —
under QEMU/KVM on the standard runner, installs the package the ordinary way,
and runs the assertions in `test/vm/` against the running guest:

- both units activate on a booted host, and systemd creates the runtime and
  state directories with the ownership and mode the units declare;
- the privileged broker's Unix socket is live, with the right owner, group and
  mode, and answers over that socket;
- PAM authenticates a real non-root administrator end to end through the
  running stack, and both negatives hold — a wrong password is rejected with
  a 401 by PAM itself, and a direct root login is refused;
- journald is reachable: the daemon reads a record it emitted itself back
  through the broker's own journal query;
- the posture survives a real reboot — both units come back active unaided,
  the capability set is unchanged, `/run/pilothouse` is destroyed and
  recreated, and `/var/lib/pilothouse` persists.

The job runs on every push to `main` and, on a pull request, only while the
`vm-boot` label is present. It is **not** a required check, it builds no
packages of its own, and it publishes nothing: the throwaway overlay disk, the
cloud-init seed and the run-time credentials never leave the job, and
diagnostics (QEMU stderr and the guest serial console) go to the job log. There
is no local `make` target for it — it needs KVM, network access and the
artifact the build job produced.

What it deliberately does **not** cover, all of which is
[#80](https://github.com/frostyard/pilothouse/issues/80): image-based hosts
(uCore), SELinux AVC assertions and policy qualification — the Fedora
guest stays enforcing, but the job neither scans the audit log nor classifies
denials — and bootc update/rollback of an ephemeral uCore-derived image
containing the last released x86_64 RPM. The issue does not test `.deb`
layering or system-extension delivery.

The released-RPM fixture producer lives at `test/image/releaserpm`. It is test
infrastructure, not a shipped binary. Ordinary repository test, vet and lint
gates analyze this package:

```bash
go run ./test/image/releaserpm --workspace /absolute/path/to/ephemeral-workspace
```

It queries this repository's latest stable semantic-version GitHub release,
requires the exactly tag-correlated Pilothouse x86_64 RPM, and verifies the
release asset's recorded byte size and SHA-256 while downloading. It creates a new
`fixture-release-rpm/fixture.json` and the verified RPM below the supplied
workspace; an existing fixture directory is an error. Supply a private
workspace that no other process mutates during the invocation. Failures,
including an inability to report the manifest path, roll back only entries
created by the invocation and never recursively delete unknown entries. The
caller owns cleanup after success, and the helper never pushes or uploads an
artifact or image.

The next image-test step consumes that fixture and builds two local uCore
derivatives:

```bash
sudo test/image/compose-ucore.sh \
    --workspace /absolute/path/to/ephemeral-workspace \
    --bin-dir /absolute/path/to/checkout/bin \
    --run-id local-run
```

The workspace must be private and non-concurrently mutated. The command
resolves `ghcr.io/ublue-os/ucore:latest` once, verifies the signed multi-arch
index and its sole linux/amd64 member, and pins every later operation to that
member digest. It rechecks the released RPM's size and SHA-256, installs it
with package repositories and build networking disabled, enables the packaged
broker and web units in the ephemeral image so they start after each bootc
transition, then overlays `pilothouse` and `pilothoused` built from the
checked-out head. Their SHA-256 values are rechecked in the private context,
passed into the offline build, verified again after installation, and recorded
in the fixture manifest. This split is intentional: the immutable release RPM
tests the selected packaging/image-delivery substrate, while the checked-out
executables make a pull request exercise the capability and host-image queries
it is gating; v0.6.0 predates both queries. The composer runs
`bootc container lint` and creates distinct `baseline` and `update` fixtures
with Podman's graph, image, run and temporary storage rooted entirely below
`fixture-ucore-images/`. Release `v0.6.0` also predates the
per-format packaging correction and its RPM contains the Debian PAM service
file. Because #80 assumes #67's PAM proof and needs authentication only to
reach its image-specific read-only queries, composition replaces that one
known-bad file with the reviewed Fedora policy only when the immutable release
ID `358276825`, asset ID `486354638`, tag `v0.6.0`, and RPM basename all
match. The installed bad-file digest and replacement policy digest are checked
first; every other release identity gets no compatibility override. The
fixture manifest records all four compatibility selector inputs, and its consumer requires
that exact identity if and only if the override is selected. The private build
context contains only verified copies of the released RPM, that reviewed
policy, and the two checked-out executables, never the container store.
Explicit general/storage configuration selectors disable
normal system and per-user Podman configuration: general
configuration uses the explicit empty `/dev/null` file, while a generated
private `storage.conf` repeats the graph, image and run paths plus driver; the
two temporary paths are pinned separately by `--tmpdir` and `TMPDIR`. Storage
environment and late general-configuration overrides are cleared, file events
are disabled, and remote Podman selection is disabled. It never pushes an
image or deletes its store. Its manifest records the effective UID that owns
the store. A fixture intended for the VM consumer must be composed as UID 0,
and compose, consume, exact-store reset and workspace removal must remain in
that one rootful ownership domain. Tool
progress remains on caller-owned standard output and standard error.

Once composition succeeds, the root-only VM consumer starts from the official
Fedora CoreOS stable QEMU image and switches that booted host to the local
baseline fixture:

```bash
sudo test/image/ucore-vm-test.sh \
    --workspace /absolute/path/to/ephemeral-workspace
```

It verifies both compressed and uncompressed checksums from Fedora CoreOS
stream metadata, boots a private qcow2 overlay under QEMU/KVM with OVMF and an
ephemeral Ignition key for the unprivileged `core` account, and attaches one
read-only ext4 disk containing one shared OCI layout with both fixtures. Both
loaded Podman image IDs must equal the fixture manifest exactly before the
baseline is staged with
`bootc switch --transport containers-storage`. uCore's own VM harness
documents why this bootstrap is necessary: `bootc install to-disk` on
FCOS/uCore omits the `LABEL=boot` partition that its GRUB configuration
requires. After the baseline reboot, the harness validates enforcing SELinux
and the exact broker-advertised capability set against independent host probes
(rejecting non-canonical or line-bearing capability IDs before comparison),
and requires Pilothouse's booted, staged and rollback image/digest pairs to
match `bootc status` exactly. The controlled broker-query window must produce
no AVC denial except uCore's delayed, explicitly permissive
`coreos_boot_mount_generator_t` boot records and at most two exact enforcing
`chcon`/`mac_admin` probe records that `bootc status` deliberately generates.
No current-boot AVC denial may name Pilothouse. A failure prints at most the
first 20 rejected records so the CI log identifies the denial without making
output unbounded. Privileged
guest work uses only non-interactive sudo from `core`; that includes broker
readiness because the correctly protected mode-0750 runtime directory is not
traversable by `core`. Both fixtures are exported into one job-local OCI
layout whose compressed blobs are shared, then Skopeo streams its two refs
into the guest's own container storage; the update is later staged with
`bootc switch --transport containers-storage`; no registry is
started or contacted for the derived images. Reboots prove staged-to-booted
digest continuity and placement of the old deployment in the rollback slot,
then `bootc rollback` proves the slots reverse and the baseline `/usr` marker
returns. The guest uses PAM only as the prerequisite for its authenticated
capability reads; it does not repeat #67's activation, directory, negative
login, journald-readback, or plain-reboot assertions.

The VM consumer streams QEMU output to the caller-owned sink and bounds every
other long-running child. On success and failure it stops and waits for QEMU.
The outer lifecycle first requires at least 10 GiB free in its workspace. To
stay within the standard runner's disk, the consumer empties the isolated
private Podman image store after export and proves it empty before downloading
FCOS. It rejects an FCOS archive declaring more than 4 GiB uncompressed and
deletes the verified compressed copy after decompression. After both OCI refs'
config digests are matched to the fixture manifest, the private store is
emptied and proved empty. The OCI layout is capped at 3 GiB, copied into a
sparse 3.5 GiB no-journal ext4 carrier, and recursively removed from its one
fixed transient path before FCOS is downloaded. The standalone layout and
carrier temporarily coexist only during that population step. FCOS mounts the
carrier read-only and Skopeo copies both OCI refs directly into
containers-storage, avoiding the full uncompressed temporary tar required by
compressed Docker-archive loading. The carrier is unmounted and both guest
image IDs are rechecked before the first switch. It
retains `fixture-ucore-vm`; the enclosing owner
still performs the final exact-store reset and workspace removal.

The enclosing production entry point owns the entire lifecycle:

```bash
sudo -n env "PATH=$PATH" \
    bash test/image/ucore-image-test.sh --run-id local-run
```

It creates one private workspace and invokes acquisition, composition and VM
validation synchronously with wall-clock and 4 MiB retained-log bounds. Each
phase command and its tail collector run below the same timeout-owned process
group, so the cap does not constrain artifacts and the deadline still covers
output collection. The owner waits until the group exists before acting on a
pending INT/TERM, forwards to the group, and rejects and terminates descendants
that survive their direct command before cleanup. The collector ignores soft
INT/TERM only long enough to drain the stopped producer and retain diagnostics;
the owner's bounded KILL escalation remains authoritative.
Once a signal handler begins, reentrant INT/TERM is deliberately ignored while
cleanup resets the exact workspace-local Podman store synchronously and removes
the workspace. Signal and failure paths use the same reset-then-remove ordering.
If TERM/KILL cannot quiesce the reset group, cleanup fails while preserving the
workspace instead of removing storage underneath a live process.
`.github/workflows/image-tier.yml` runs this on `ubuntu-26.04`
because Podman 5's `--imagestore` support is part of the isolation contract. It
runs on every push to `main` and, for pull requests, only while the `vm-boot`
label is present. The job uses the last released x86_64 RPM, uploads nothing
and never publishes either derived image.

### Create a release

`make bump` verifies the project in the development container, calculates the
next semantic version with the container's pinned `svu`, creates an annotated
tag, and immediately pushes that tag to `origin`.

The host needs only Docker, Make, and authenticated Git access. Run it from a
clean `main` checkout that exactly matches `origin/main`; the target rejects
dirty, ahead, behind, divergent, feature-branch, and detached-HEAD states
before creating a tag.

Before verification, preflight force-updates local tag refs from authoritative
`origin` values, so moved and remote-only tags are reconciled automatically.
Local-only tags are preserved and rejected rather than silently deleted.

```bash
make bump
```

Open `http://127.0.0.1:8888` and sign in with a non-root system account. Any authenticated account can view the dashboard. Members of the configured broker admin group can perform sysext, Podman, and Docker mutations. The packaged broker unit configures that group per distro family — `sudo` on Debian-family hosts, `wheel` on Fedora-family hosts.

The default is intentionally loopback-only. Terminate TLS at a reverse proxy and add `--secure-cookie` to the web service before exposing it to another machine.

When a reverse proxy changes the upstream `Host`, configure the browser-visible origin explicitly. The option is repeatable; an HTTPS origin automatically enables secure cookies.

```bash
./bin/pilothouse --allowed-origin https://admin.example.test
```

The packaged service also reads comma-separated origins from `/etc/pilothouse/pilothouse.env`:

```ini
PILOTHOUSE_ALLOWED_ORIGINS=https://admin.example.test
```

Configure exact backup timers for the privileged broker in `/etc/pilothouse/pilothoused.env`. Pilothouse deliberately does not infer backups from unit names.

```ini
PILOTHOUSE_BACKUP_TIMERS=restic.timer,borg.timer
```

Configure Files only on the privileged broker. `--files-root` adds a read-only
root and `--files-write-root` adds a writable root; each flag is repeatable and
uses `id=absolute-path`. The unprivileged web process never receives root paths.

```bash
sudo ./bin/pilothoused \
  --files-root logs=/var/log \
  --files-write-root imports=/var/lib/pilothouse/imports
```

The filesystem root (`/`) is rejected. Symlinks are displayed but never
followed. Downloads and uploads are limited to 256 MiB each. Uploads are
available only in writable roots, are atomically published as `root:root` mode
`0640`, and reject existing destination names rather than overwriting them.

## Module architecture

The central contract is deliberately small. Every management module provides:

- a manifest for navigation and ordering;
- zero or more cards for the landing dashboard;
- its own routes, handlers, domain service, actions, and templ views.

The shell knows only about `platform.Module`; it does not import concrete modules. The web composition root registers presentation modules. The broker composition root separately registers privileged queries and action implementations. Modules submit fixed query and action identifiers through `platform.Host`; they never execute privileged commands or connect to root-equivalent service sockets in the web process.

The Podman module intentionally manages the root/system store used for host services through the Podman 5.0 or newer Libpod API. Enable the rootful API socket with `sudo systemctl enable --now podman.socket`, then point `pilothoused` at it with `--podman-socket` (for example `--podman-socket /run/podman/podman.sock`); the flag defaults to empty and Podman stays disabled until it is set, so a host that merely has a socket present never enables the engine on its own. The Docker module targets the system Docker daemon; point `pilothoused` at it with `--docker` (for example `--docker unix:///var/run/docker.sock`). That flag defaults to empty and Docker stays disabled until it is set — an exported `DOCKER_HOST` or a socket at the SDK's default path never enables the engine on its own, because the endpoint you configure is the only input the Docker client is built from. The Incus module uses the official SDK against `/var/lib/incus/unix.socket` and allows selection from projects reported by that local daemon; it never reads configured Incus remotes. Opt in with `pilothoused --incus`; that flag defaults to `false` and Incus stays disabled until it is set, so a host that merely answers on that socket never enables the engine on its own. The socket path is fixed rather than configurable — the flag decides only whether it is probed. Rootless and remote workloads remain isolated from this system administration surface.

See [docs/modules.md](docs/modules.md) for a worked module template and [docs/authentication.md](docs/authentication.md) for the trust model.

## Install

Start with the steps that are the same everywhere:

```bash
make build
sudo systemd-sysusers packaging/pilothouse.sysusers
sudo install -Dm0755 bin/pilothouse /usr/local/bin/pilothouse
sudo install -Dm0755 bin/pilothoused /usr/local/libexec/pilothoused
sudo install -Dm0644 packaging/pilothouse.service /etc/systemd/system/pilothouse.service
```

The PAM policy and the broker unit are distro-specific, so run **only** the
block matching your host. Debian-family hosts use the
`common-auth`/`common-account` PAM stacks and the `sudo` admin group;
Fedora-family hosts use the `password-auth` stack and the `wheel` admin group.

Debian-family (Debian, Ubuntu, …):

```bash
sudo install -Dm0644 packaging/deb/pilothoused.service /etc/systemd/system/pilothoused.service
sudo install -Dm0644 packaging/pilothouse.pam /etc/pam.d/pilothouse
```

Fedora-family (Fedora, uCore, RHEL, …):

```bash
sudo install -Dm0644 packaging/rpm/pilothoused.service /etc/systemd/system/pilothoused.service
sudo install -Dm0644 packaging/rpm/pilothouse.pam /etc/pam.d/pilothouse
```

Then finish on either family:

```bash
sudo install -d -m0750 -o root -g pilothouse /etc/pilothouse
sudo install -d -m0700 -o root -g root /etc/pilothouse/storage/credentials
sudo install -Dm0640 -o root -g pilothouse packaging/pilothouse.env /etc/pilothouse/pilothouse.env
sudo install -Dm0640 -o root -g pilothouse packaging/pilothoused.env /etc/pilothouse/pilothoused.env
sudo systemctl daemon-reload
sudo systemctl enable --now pilothouse.service
```

`/etc/pilothouse` is `root:pilothouse` mode `0750` so the units can read their
`EnvironmentFile=` as the `pilothouse` group without exposing it to every
account on the host. `/etc/pilothouse/storage/credentials` is deliberately
stricter — `root:root` mode `0700` — because it holds remote-mount secrets
that only the root broker ever reads. The two env files ship with every
setting commented out, so copying them changes no behavior: uncomment
`PILOTHOUSE_ALLOWED_ORIGINS` in `/etc/pilothouse/pilothouse.env` when a
reverse proxy is in front of the console, and `PILOTHOUSE_BACKUP_TIMERS` in
`/etc/pilothouse/pilothoused.env` to name the backup timers to monitor. The
`.deb` and `.rpm` packages create the same two directories and install the
same two files as configuration files, and declare their PAM and systemd
runtime dependencies per format.

Both packaged units are deliberately minimal. `pilothoused.service`'s
`ExecStart` passes no optional-tooling flag, and the unit declares no
`Wants=` on `podman.socket` or `incus.socket` (only `After=`, which orders
the broker behind those units without pulling them in), so a stock install
enables no container engine and no `updex`-backed extension operation. Add
the flags for the surfaces you want to that `ExecStart`:
`--updex /usr/bin/updex` (adjust to your host's path),
`--podman-socket /run/podman/podman.sock`,
`--docker unix:///var/run/docker.sock`, and `--incus`.
`systemd-sysext`, systemd, journald, bootc, and rpm-ostree need no flag;
they are still detected by presence. `pilothouse.service` likewise omits
`--dev`, so the static Fleet preview is not registered in a normal
installation.

For an immutable production image, package the binary and unit in a dedicated sysext and keep mutable updex state under `/etc/sysupdate.d` and `/var/lib/extensions.d`.
