---
title: CLI reference
description: Flags for the web process and the privileged broker.
group: Reference
order: 20
---

pilothouse ships two binaries. `pilothouse` is the unprivileged web process; `pilothoused` is the root-only action broker. They connect through a protected Unix socket.

## pilothouse

| Flag | Default | Purpose |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8888` | HTTP listen address |
| `--broker-socket` | `/run/pilothouse/broker.sock` | Privileged broker Unix socket |
| `--allowed-origin` | — | Trusted public HTTP(S) origin when behind a reverse proxy; repeatable |
| `--secure-cookie` | `false` | Require HTTPS when sending the session cookie |

The web process reads extension state through the broker rather than running
`updex` or `systemd-sysext` itself, so it has no `--definitions-root` or
`--updex` flag. Those belong to `pilothoused` below, which is the process that
actually invokes the tools.

## pilothoused

| Flag | Default | Purpose |
| --- | --- | --- |
| `--socket` | `/run/pilothouse/broker.sock` | Unix socket path |
| `--socket-group` | `pilothouse` | Group allowed to connect to the broker |
| `--admin-group` | `sudo` | System group allowed to perform privileged actions |
| `--login-group` | — | Optional system group allowed to log in |
| `--pam-service` | `pilothouse` | PAM service name |
| `--audit-db` | `/var/lib/pilothouse/audit.db` | Durable action audit database |
| `--jobs-db` | `/var/lib/pilothouse/jobs.db` | Durable maintenance job database |
| `--backup-timer` | — | Exact systemd backup timer to monitor; repeatable |
| `--backup-max-age` | `48h` | Maximum acceptable age of a successful configured backup |
| `--definitions-root` | — | Custom root containing sysupdate definition directories; by default updex uses its standard layered search paths |
| `--podman-socket` | — | Podman API Unix socket path; Podman requires explicit configuration to enable |
| `--updex` | — | Path to the updex executable; updex requires explicit configuration to enable |

## Environment files

The packaged services read environment files under `/etc/pilothouse/`.

| File | Variable | Purpose |
| --- | --- | --- |
| `pilothouse.env` | `PILOTHOUSE_ALLOWED_ORIGINS` | Comma-separated trusted origins for the web process |
| `pilothoused.env` | `PILOTHOUSE_BACKUP_TIMERS` | Comma-separated exact backup timers to monitor |
