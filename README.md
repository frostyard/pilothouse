# Pilothouse

Pilothouse is a local web administration console for image-based Linux systems. It starts with an attractive live system dashboard and, once the broker is pointed at `updex`, complete sysext lifecycle management through the `updex` interface.

The application is bootstrapped from [housecat-inc/scratch](https://github.com/housecat-inc/scratch): Go and templ on the server, HTMX for focused page updates, an embedded design system, and no Node runtime or external frontend assets.

## What works

- Live CPU, memory, persistent storage, load, uptime, network totals, host, OS, and kernel metrics
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
- Local Incus project inventory for containers, virtual machines, and images with lifecycle controls
- Administrator-only browsing, download, and atomic upload within configured host file roots
- PAM authentication using Snow's users and account policy
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

Pilothouse-managed SMB mounts can optionally map file ownership to a local
numeric UID/GID. Both IDs are required together; leaving both fields blank
preserves default ownership. Names and free-form mount options are not
accepted.

## Develop

Before pushing, run `make ci` (or `make docker-ci` on hosts without the
native toolchain) — it runs every CI gate that runs without credentials, in
the same order. Local green means the credential-free gates will be green in
CI. The single exception is `.github/workflows/packaging.yml`, the packaging
gate, which cannot run locally because it needs the `GORELEASER_KEY` secret
and the goreleaser Pro distribution; `make verify-packages` is the local tool
for the package contracts it checks, once artifacts exist in `dist/`.

Go 1.26 or newer is required.

```bash
make test
make race
make build
sudo ./bin/pilothoused --socket /tmp/pilothouse-broker.sock --socket-group "$(id -gn)"
./bin/pilothouse --broker-socket /tmp/pilothouse-broker.sock
```

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
`main`. The target then fails with a message naming that directory, all three
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
artifacts and runs this same verification in CI; it cannot run locally because
it needs the `GORELEASER_KEY` secret and the goreleaser Pro distribution.

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
