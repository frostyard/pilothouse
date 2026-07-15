# Pilothouse

Pilothouse is a local web administration console for [Snosi](https://github.com/frostyard/snosi) installations. It starts with an attractive live system dashboard and complete sysext lifecycle management through Snosi's `updex` interface.

The application is bootstrapped from [housecat-inc/scratch](https://github.com/housecat-inc/scratch): Go and templ on the server, HTMX for focused page updates, an embedded design system, and no Node runtime or external frontend assets.

## What works

- Live CPU, memory, persistent storage, load, uptime, network totals, host, OS, and kernel metrics
- Automatic dashboard refresh every 15 seconds
- Discovery of both shared `/usr/lib/sysupdate.d` and component-scoped `/usr/lib/sysupdate.<name>.d` Snosi definitions
- Installed and merged state from `systemd-sysext`
- Install, remove, update-all, and merge-refresh actions through `updex` and `systemd-sysext`
- System Podman inventory for containers, pods, images, engine version, and reported image storage
- Administrator-only container start, stop, restart, and safe removal actions
- System Docker Engine inventory with the same container lifecycle controls and socket isolation
- Local Incus default-project inventory for containers, virtual machines, and images with lifecycle controls
- PAM authentication using Snow's users and account policy
- Opaque, idle-expiring broker sessions with per-session CSRF tokens
- An unprivileged web process and a root-only action broker connected through a protected Unix socket
- Group-based administration, POST-only mutations, origin checks, strict command arguments, and bounded command timeouts
- Responsive desktop and mobile layouts

## Develop

Go 1.26 or newer is required.

```bash
make test
make build
sudo ./bin/pilothoused --socket /tmp/pilothouse-broker.sock --socket-group "$(id -gn)"
./bin/pilothouse --broker-socket /tmp/pilothouse-broker.sock
```

Open `http://127.0.0.1:8888` and sign in with a non-root system account. Any authenticated account can view the dashboard. Members of the configured broker admin group (`sudo` by default) can perform sysext, Podman, and Docker mutations.

The default is intentionally loopback-only. Terminate TLS at a reverse proxy and add `--secure-cookie` to the web service before exposing it to another machine.

When a reverse proxy changes the upstream `Host`, configure the browser-visible origin explicitly. The option is repeatable; an HTTPS origin automatically enables secure cookies.

```bash
./bin/pilothouse --allowed-origin https://admin.example.test
```

The packaged service also reads comma-separated origins from `/etc/pilothouse/pilothouse.env`:

```ini
PILOTHOUSE_ALLOWED_ORIGINS=https://admin.example.test
```

## Module architecture

The central contract is deliberately small. Every management module provides:

- a manifest for navigation and ordering;
- zero or more cards for the landing dashboard;
- its own routes, handlers, domain service, actions, and templ views.

The shell knows only about `platform.Module`; it does not import concrete modules. The web composition root registers presentation modules. The broker composition root separately registers privileged queries and action implementations. Modules submit fixed query and action identifiers through `platform.Host`; they never execute privileged commands or connect to root-equivalent service sockets in the web process.

The Podman module intentionally manages the root/system store used for host services through the Podman 5.0 or newer Libpod API. Enable the rootful API socket with `sudo systemctl enable --now podman.socket`; use `--podman-socket` to select a different Unix socket. The Docker module targets the system Docker daemon. The Incus module uses the official SDK against `/var/lib/incus/unix.socket` and is fixed to the local daemon's `default` project; it never reads configured Incus remotes. Rootless and remote workloads remain isolated from this system administration surface.

See [docs/modules.md](docs/modules.md) for a worked module template and [docs/authentication.md](docs/authentication.md) for the trust model.

## Install on Snosi

```bash
make build
sudo systemd-sysusers packaging/pilothouse.sysusers
sudo install -Dm0755 bin/pilothouse /usr/local/bin/pilothouse
sudo install -Dm0755 bin/pilothoused /usr/local/libexec/pilothoused
sudo install -Dm0644 packaging/pilothouse.service /etc/systemd/system/pilothouse.service
sudo install -Dm0644 packaging/pilothoused.service /etc/systemd/system/pilothoused.service
sudo install -Dm0644 packaging/pilothouse.pam /etc/pam.d/pilothouse
sudo install -d -m0755 /etc/pilothouse
# Set PILOTHOUSE_ALLOWED_ORIGINS in /etc/pilothouse/pilothouse.env when using a reverse proxy.
sudo systemctl daemon-reload
sudo systemctl enable --now pilothouse.service
```

For an immutable production image, package the binary and unit in a dedicated sysext and keep mutable updex state under `/etc/sysupdate.d` and `/var/lib/extensions.d`.
