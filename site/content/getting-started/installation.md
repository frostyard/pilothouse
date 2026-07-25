---
title: Installation
description: Build pilothouse and install it on a host.
group: Getting started
order: 2
---

## Build

Go 1.26 or newer is required.

```bash
make test
make build
```

Docker equivalents are available when the host does not have Go, PAM headers, or systemd headers installed:

```bash
make docker-generate
make docker-fmt
make docker-build
make docker-test
make docker-lint
```

Each target checks the reusable development image through Docker's build cache and uses persistent Docker volumes for Go and linter caches. Container commands run as the host user, so generated files and build output remain writable. `make docker-run` uses host networking and starts the web process, but broker-backed operations require separately mounting a broker socket into the container.

## Run locally

Start the privileged broker, then the web process:

```bash
sudo ./bin/pilothoused --socket /tmp/pilothouse-broker.sock --socket-group "$(id -gn)"
./bin/pilothouse --broker-socket /tmp/pilothouse-broker.sock
```

That broker line configures none of the optional tooling, so Podman, Docker,
Incus, and every `updex`-backed extension operation are absent from the
console. Add `--podman-socket`, `--docker`, `--incus`, or `--updex` to the
`pilothoused` line for the surfaces you want, and `--dev` to the `pilothouse`
line for the static Fleet preview. The [CLI reference](/reference/cli/) lists
the exact flags and defaults.

Open `http://127.0.0.1:8888` and sign in with a non-root system account. Any authenticated account can view the dashboard. Members of the configured broker admin group can perform sysext mutations, and Podman, Docker, and Incus mutations for whichever of those engines was configured above. The packaged broker unit configures that group per distro family — `sudo` on Debian-family hosts, `wheel` on Fedora-family hosts.

## Install

Start with the steps that are the same everywhere:

```bash
make build
sudo systemd-sysusers packaging/pilothouse.sysusers
sudo install -Dm0755 bin/pilothouse /usr/local/bin/pilothouse
sudo install -Dm0755 bin/pilothoused /usr/local/libexec/pilothoused
sudo install -Dm0644 packaging/pilothouse.service /etc/systemd/system/pilothouse.service
```

The PAM policy and the broker unit are distro-specific, so run **only** the block matching your host. Debian-family hosts use the `common-auth`/`common-account` PAM stacks and the `sudo` admin group; Fedora-family hosts use the `password-auth` stack and the `wheel` admin group.

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

`/etc/pilothouse` is `root:pilothouse` mode `0750` so the units can read their `EnvironmentFile=` as the `pilothouse` group without exposing it to every account on the host. `/etc/pilothouse/storage/credentials` is stricter — `root:root` mode `0700` — because it holds remote-mount secrets only the root broker reads. Both env files ship with every setting commented out, so copying them changes no behavior: uncomment `PILOTHOUSE_ALLOWED_ORIGINS` in `/etc/pilothouse/pilothouse.env` for a reverse-proxy deployment (see [Expose beyond loopback](#expose-beyond-loopback)) and `PILOTHOUSE_BACKUP_TIMERS` in `/etc/pilothouse/pilothoused.env` to name the backup timers to monitor (see [Backup monitoring](#backup-monitoring)). The `.deb` and `.rpm` packages create the same two directories and install the same two files as configuration files, and declare their PAM and systemd runtime dependencies per format.

The packaged units are deliberately minimal: `pilothoused.service` passes none of the four optional-tooling flags and declares no `Wants=` on `podman.socket` or `incus.socket`, and `pilothouse.service` does not pass `--dev`. A stock install therefore enables no container engine, no `updex`-backed extension operation, and no Fleet preview until you add the flags to the relevant `ExecStart`.

For an immutable production image, package the binary and unit in a dedicated sysext and keep mutable updex state under `/etc/sysupdate.d` and `/var/lib/extensions.d`.

## Expose beyond loopback

The default is intentionally loopback-only. Terminate TLS at a reverse proxy and add `--secure-cookie` to the web service before exposing it to another machine.

When a reverse proxy changes the upstream `Host`, configure the browser-visible origin explicitly. The option is repeatable; an HTTPS origin automatically enables secure cookies.

```bash
./bin/pilothouse --allowed-origin https://admin.example.test
```

The packaged service also reads comma-separated origins from `/etc/pilothouse/pilothouse.env`:

```ini
PILOTHOUSE_ALLOWED_ORIGINS=https://admin.example.test
```

## Backup monitoring

Configure exact backup timers for the privileged broker in `/etc/pilothouse/pilothoused.env`. pilothouse deliberately does not infer backups from unit names.

```ini
PILOTHOUSE_BACKUP_TIMERS=restic.timer,borg.timer
```
