# Features

What the Pilothouse console does today, in full. The root `README.md` gives
the short list and links here. How to install and configure it is in
[install.md](install.md); the design is in
[design/overview.md](design/overview.md).

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
- Read-only k3s visibility with node readiness and aggregate pod health totals per namespace; unrecognized future pod phases conservatively count as not ready instead of hiding the whole view, while individual pod details and cluster mutations are intentionally excluded
- Administrator-only browsing, download, and atomic upload within configured host file roots
- PAM authentication using host users and account policy, with bounded per-user/per-address exponential login backoff
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
- Startup-time host capability probing (systemd, journald, updex, `systemd-sysext`, bootc, rpm-ostree, automatic-update pairs, Podman, Docker, Incus, k3s), advertised over an authenticated broker query; the daemon starts and registers only the privileged operations whose required capability is actually present, degrading gracefully instead of failing to start when a dependency is absent or unreachable. Five of those capabilities — updex, Podman, Docker, Incus, and k3s — are additionally opt-in, so "present" means "explicitly configured *and* reachable" rather than "detected on the host" (next bullet)
- Explicit opt-in for every optional dependency: `updex`, Podman, Docker, Incus, and k3s are probed only when `--updex`, `--podman-socket`, `--docker`, `--incus`, or `--k3s` is configured on `pilothoused`, and an unconfigured one is reported absent without any I/O — no command is run and no socket is dialled — so a binary on `PATH`, a socket at a conventional path, or an exported `DOCKER_HOST` never enables anything by itself, and the packaged unit passes none of the five. Every feature bullet above that names one of these dependencies therefore describes a surface that appears only once its flag is set. The remaining capabilities (systemd, journald, `systemd-sysext`, bootc, rpm-ostree, automatic-update pairs) stay presence-probed, and the broker also no longer declares `Wants=` on any engine socket
- Machine-readable command probes parse stdout only. A successful command's stderr diagnostics do not corrupt its JSON contract; when a command fails, stderr is retained in the returned diagnostic while stdout remains separately available.

Pilothouse-managed SMB mounts can optionally map file ownership to a local
numeric UID/GID. Both IDs are required together; leaving both fields blank
preserves default ownership. Names and free-form mount options are not
accepted.

## Module architecture

The central contract is deliberately small. Every management module provides:

- a manifest for navigation and ordering;
- zero or more cards for the landing dashboard;
- its own routes, handlers, domain service, actions, and templ views.

The shell knows only about `platform.Module`; it does not import concrete modules. The web composition root registers presentation modules. The broker composition root separately registers privileged queries and action implementations. Modules submit fixed query and action identifiers through `platform.Host`; they never execute privileged commands or connect to root-equivalent service sockets in the web process.

See [modules.md](modules.md) for a worked module template and
[authentication.md](authentication.md) for the trust model.
