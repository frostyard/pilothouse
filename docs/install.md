# Install and run

How to install Pilothouse on a host, turn on its optional surfaces, and reach
it from another machine. The feature list is in [features.md](features.md);
building and testing from source is in [development.md](development.md).

## Install a release package

Download the package for your architecture and `checksums.txt` from the
[latest release](https://github.com/frostyard/pilothouse/releases/latest).
The current release uses `frostyard-pilothouse_<version>_amd64.deb` or
`_arm64.deb` on Debian-family hosts, and
`frostyard-pilothouse-<version>-1.x86_64.rpm` or `-1.aarch64.rpm` on
Fedora-family hosts.

For example, on Debian or Ubuntu:

```bash
version=0.9.1
artifact="frostyard-pilothouse_${version}_$(dpkg --print-architecture).deb"
base="https://github.com/frostyard/pilothouse/releases/download/v${version}"
curl -fLO "${base}/checksums.txt"
curl -fLO "${base}/${artifact}"
grep -F "  ${artifact}" checksums.txt | sha256sum --check -
sudo apt install "./${artifact}"
```

On Fedora, RHEL, or uCore:

```bash
version=0.9.1
artifact="frostyard-pilothouse-${version}-1.$(uname -m).rpm"
base="https://github.com/frostyard/pilothouse/releases/download/v${version}"
curl -fLO "${base}/checksums.txt"
curl -fLO "${base}/${artifact}"
grep -F "  ${artifact}" checksums.txt | sha256sum --check -
sudo dnf install "./${artifact}"
```

Set `version` to the release you selected. The examples support the published
`amd64`/`arm64` Debian architectures and `x86_64`/`aarch64` RPM architectures.
After installation, start the web service; it requires and starts the broker:

```bash
sudo systemctl enable --now pilothouse.service
```

Open `http://127.0.0.1:8888` and sign in with a non-root system account. Any
authenticated account can view the dashboard. Members of the configured
broker admin group can perform sysext mutations and mutations for configured
container engines; that group is `sudo` on Debian-family hosts and `wheel` on
Fedora-family hosts. The service listens only on loopback by default; to
reach it from another machine, set `PILOTHOUSE_LISTEN` in
`/etc/pilothouse/pilothouse.env` and see [Remote access](#remote-access) for
the SSH, overlay-network, native-TLS, and reverse-proxy options.

## Install from source

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

After starting the source installation, use the same console address, account
roles, and runtime configuration described for the package installation above.

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
`--docker unix:///var/run/docker.sock`, `--incus`, and
`--k3s /usr/local/bin/k3s`.
`systemd-sysext`, systemd, journald, bootc, and rpm-ostree need no flag;
they are still detected by presence. `pilothouse.service` likewise omits
`--dev`, so the static Fleet preview is not registered in a normal
installation.

For an immutable production image, package the binary and unit in a dedicated sysext and keep mutable updex state under `/etc/sysupdate.d` and `/var/lib/extensions.d`.

## Optional surfaces

The Podman module intentionally manages the root/system store used for host services through the Podman 5.0 or newer Libpod API. Enable the rootful API socket with `sudo systemctl enable --now podman.socket`, then point `pilothoused` at it with `--podman-socket` (for example `--podman-socket /run/podman/podman.sock`); the flag defaults to empty and Podman stays disabled until it is set, so a host that merely has a socket present never enables the engine on its own. The Docker module targets the system Docker daemon; point `pilothoused` at it with `--docker` (for example `--docker unix:///var/run/docker.sock`). That flag defaults to empty and Docker stays disabled until it is set — an exported `DOCKER_HOST` or a socket at the SDK's default path never enables the engine on its own, because the endpoint you configure is the only input the Docker client is built from. The Incus module uses the official SDK against `/var/lib/incus/unix.socket` and allows selection from projects reported by that local daemon; it never reads configured Incus remotes. Opt in with `pilothoused --incus`; that flag defaults to `false` and Incus stays disabled until it is set, so a host that merely answers on that socket never enables the engine on its own. The socket path is fixed rather than configurable — the flag decides only whether it is probed. The k3s module is read-only and opt-in through `--k3s /usr/local/bin/k3s`; the broker runs only fixed `kubectl get` commands against `/etc/rancher/k3s/k3s.yaml`, returning node readiness and namespace-level pod totals without exposing the kubeconfig, Kubernetes API, individual pod details, or any mutation. Rootless and remote workloads remain isolated from this system administration surface.

## Remote access

The default is intentionally loopback-only. To reach the console from another
machine, pick one of these, in order of preference:

1. **SSH port forwarding** — zero configuration, zero new exposure:

   ```bash
   ssh -L 8888:127.0.0.1:8888 user@host
   ```

   Then open `http://127.0.0.1:8888` locally.

2. **An overlay network (Tailscale, WireGuard)** — bind the console to the
   overlay address, or let `tailscale serve` terminate real HTTPS in front of
   the loopback listener:

   ```bash
   ./bin/pilothouse --listen 100.64.0.5:8888    # tailnet address
   # or, keeping the loopback default:
   tailscale serve https / http://127.0.0.1:8888
   ```

3. **Direct LAN bind with TLS** — binding a non-loopback address without TLS
   material auto-generates a persistent self-signed certificate (under
   `$STATE_DIRECTORY` for the packaged unit, `~/.local/state/pilothouse`
   otherwise) and serves HTTPS with it; supply `--tls-cert`/`--tls-key` to
   use your own certificate instead. The session cookie's `Secure` attribute
   engages automatically under TLS.

   ```bash
   ./bin/pilothouse --listen 0.0.0.0:8888                     # self-signed
   ./bin/pilothouse --listen 0.0.0.0:8888 \
     --tls-cert /etc/pilothouse/tls/console.crt \
     --tls-key /etc/pilothouse/tls/console.key                # your cert
   ```

   On a packaged install, set `PILOTHOUSE_LISTEN` (and optionally
   `PILOTHOUSE_TLS_CERT`/`PILOTHOUSE_TLS_KEY`) in
   `/etc/pilothouse/pilothouse.env`; flags passed in `ExecStart` would
   override the environment, and the packaged unit deliberately passes no
   `--listen` flag.

4. **A TLS-terminating reverse proxy** — keep the listener on loopback, add
   `--secure-cookie`, and configure the browser-visible origin as below.

Serving plaintext HTTP on a non-loopback address requires the explicit
`--allow-insecure-http` flag (or `PILOTHOUSE_ALLOW_INSECURE_HTTP=1`); without
it the console falls back to the self-signed certificate, and refuses to
start if none can be prepared. If you previously ran `--listen 0.0.0.0:...`
over plaintext, add that flag or switch to one of the TLS options.

When a reverse proxy changes the upstream `Host`, configure the browser-visible
origin explicitly. The option is repeatable; an HTTPS origin automatically
enables secure cookies.

```bash
./bin/pilothouse --allowed-origin https://admin.example.test
```

The packaged service also reads comma-separated origins from
`/etc/pilothouse/pilothouse.env`:

```ini
PILOTHOUSE_ALLOWED_ORIGINS=https://admin.example.test
```

Configure exact backup timers for the privileged broker in
`/etc/pilothouse/pilothoused.env`. Pilothouse deliberately does not infer
backups from unit names.

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

If local sign-in is unavailable, verify the privileged broker before debugging
the browser: `systemctl status pilothoused` and `journalctl -u pilothoused`.
The broker validates fixed storage-tool paths at startup; distro-provided
symlinks such as `pvs -> lvm` are accepted only when the resolved executable is
a safe root-owned regular file.
