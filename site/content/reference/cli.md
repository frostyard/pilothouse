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
| `--definitions-root` | `/usr/lib` | Directory containing sysupdate definition directories |
| `--allowed-origin` | — | Trusted public HTTP(S) origin when behind a reverse proxy; repeatable |
| `--secure-cookie` | `false` | Require HTTPS when sending the session cookie |
| `--updex` | `updex` | Path to the updex executable |

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
| `--definitions-root` | `/usr/lib` | Directory containing sysupdate definition directories |
| `--podman-socket` | `/run/podman/podman.sock` | Podman API Unix socket path |
| `--updex` | `updex` | Path to the updex executable |

## Environment files

The packaged services read environment files under `/etc/pilothouse/`.

| File | Variable | Purpose |
| --- | --- | --- |
| `pilothouse.env` | `PILOTHOUSE_ALLOWED_ORIGINS` | Comma-separated trusted origins for the web process |
| `pilothoused.env` | `PILOTHOUSE_BACKUP_TIMERS` | Comma-separated exact backup timers to monitor |
