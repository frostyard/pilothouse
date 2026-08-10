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
| `--dev` | `false` | Register in-development preview modules that are not backed by real functionality |

`--dev` currently gates exactly one module: the Fleet preview, a static mock
with no real multi-system transport or enrollment behind it. Without the flag
it is not registered at all, so it has no navigation entry, no sidebar
system-picker link, and no routes — `/fleet` and its sub-paths return 404. The
packaged unit does not pass `--dev`, so a normal installation runs without it.

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
| `--docker` | — | Docker endpoint such as `unix:///var/run/docker.sock`; Docker requires explicit configuration to enable |
| `--incus` | `false` | Enable Incus inventory against the local socket `/var/lib/incus/unix.socket`; Incus requires this explicit opt-in to enable, and the socket path is fixed rather than configurable |
| `--k3s` | — | Path to the k3s executable; enables read-only node and namespace health through the fixed `/etc/rancher/k3s/k3s.yaml` kubeconfig |
| `--podman-socket` | — | Podman API Unix socket path; Podman requires explicit configuration to enable |
| `--updex` | — | Path to the updex executable; updex requires explicit configuration to enable |

The last five tooling flags are the broker's only optional dependencies, and all
five are off by default. Leaving one unset does not merely hide a surface:
the corresponding probe reports the capability absent without any I/O — no
command is run and no socket is dialled — so `updex` on `PATH`,
a live `/run/podman/podman.sock`, an exported `DOCKER_HOST`, a responding
`/var/lib/incus/unix.socket`, or `k3s` on `PATH` never enables anything on its own.
The broker then registers no query or action for that tool, and the console
shows no navigation entry, no dashboard card, and 404s its routes.

The packaged `pilothoused.service` passes none of the five, and it declares
no `Wants=` on `podman.socket` or `incus.socket` — only `After=`, which
orders the broker behind those units if something else starts them. Add the
flags you want to `ExecStart`. Every other capability (systemd, journald,
`systemd-sysext`, bootc, rpm-ostree, and the automatic-update timer pairs)
is detected by presence and has no flag.

## Environment files

The packaged services read environment files under `/etc/pilothouse/`.

| File | Variable | Purpose |
| --- | --- | --- |
| `pilothouse.env` | `PILOTHOUSE_ALLOWED_ORIGINS` | Comma-separated trusted origins for the web process |
| `pilothoused.env` | `PILOTHOUSE_BACKUP_TIMERS` | Comma-separated exact backup timers to monitor |
