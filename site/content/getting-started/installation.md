---
title: Installation
description: Build pilothouse and install it on a snosi host.
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

Open `http://127.0.0.1:8888` and sign in with a non-root system account. Any authenticated account can view the dashboard. Members of the configured broker admin group (`sudo` by default) can perform sysext, Podman, and Docker mutations.

## Install on snosi

```bash
make build
sudo systemd-sysusers packaging/pilothouse.sysusers
sudo install -Dm0755 bin/pilothouse /usr/local/bin/pilothouse
sudo install -Dm0755 bin/pilothoused /usr/local/libexec/pilothoused
sudo install -Dm0644 packaging/pilothouse.service /etc/systemd/system/pilothouse.service
sudo install -Dm0644 packaging/pilothoused.service /etc/systemd/system/pilothoused.service
sudo install -Dm0644 packaging/pilothouse.pam /etc/pam.d/pilothouse
sudo install -d -m0755 /etc/pilothouse
sudo systemctl daemon-reload
sudo systemctl enable --now pilothouse.service
```

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
