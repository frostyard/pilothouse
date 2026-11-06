<!-- readme-kind: service -->
# Pilothouse

[![Tests](https://github.com/frostyard/pilothouse/actions/workflows/ci.yml/badge.svg)](https://github.com/frostyard/pilothouse/actions/workflows/ci.yml)

Pilothouse is a local web administration console for image-based Linux systems,
for the people who run them. It shows a live system dashboard and manages
services, sysexts, containers, storage and files, while every privileged
operation goes through a separate root broker.

**Status:** pre-release. Releases are `v0.x`; behavior and formats can change between them.

**Documentation:** [docs/README.md](docs/README.md)

## What it does

- **Dashboard and attention.** Live CPU, memory, storage, load, network and host metrics, refreshed every 15 seconds, and one view of what needs attention. See [the feature list](docs/features.md).
- **Services and logs.** systemd service, socket and timer inventory with lifecycle controls for administrators, and bounded system-journal search. See [features](docs/features.md#what-works).
- **Extensions and host image.** sysext lifecycle through `updex` and `systemd-sysext`, reboot posture, and read-only `bootc` and `rpm-ostree` host-image and automatic-update status. See [autoupdate.md](docs/autoupdate.md) and [design/host-image.md](docs/design/host-image.md).
- **Containers.** System Podman, Docker Engine and Incus inventory and lifecycle controls, and read-only k3s health. Each engine is off until you configure it on the broker. See [optional surfaces](docs/install.md#optional-surfaces).
- **Storage and files.** Block devices, mounts and their health, managed NFS and SMB mounts, and file browsing, download and upload in configured roots. See [modules.md](docs/modules.md#managed-remote-mounts).
- **Safe by construction.** PAM sign-in, an unprivileged web process, and a root broker that runs only fixed queries and actions, with an audit trail. See [authentication.md](docs/authentication.md).
- **Degrades gracefully.** The broker probes host capabilities at startup and registers only what the host supports. See [capabilities.md](docs/capabilities.md).

## Interface

| Endpoint | What it does |
| --- | --- |
| `http://127.0.0.1:8888/` | The console; the default listener is loopback HTTP |
| `GET /healthz` | Liveness of the web process |
| `GET /readyz` | Readiness, which includes the broker |
| `/run/pilothouse/broker.sock` | The broker's Unix socket; only the web process uses it |

The broker's wire contract is in [ADR 0001](docs/adr/0001-versioned-broker-wire-surface.md) and [capabilities.md](docs/capabilities.md).

## Deploy

Pilothouse runs on the host it manages, as 2 systemd units: `pilothouse.service` (the web console, an unprivileged user) and `pilothoused.service` (the root broker). Install the `.deb` or `.rpm` from the [latest release](https://github.com/frostyard/pilothouse/releases/latest), then run `sudo systemctl enable --now pilothouse.service` and open `http://127.0.0.1:8888`. A change reaches hosts through a tagged release; frostyard/snosi ships it in its `pilothouse` sysext. [docs/install.md](docs/install.md) has the checksum-verified install, the install from source, the optional surfaces and remote access.

## Problem records

A release build records its own panics and 5 fixed internal errors as entries in the local systemd journal, and sends nothing over the network ([ADR 0013](docs/adr/0013-local-problem-records.md)). [docs/problem-records.md](docs/problem-records.md) lists the codes and the crash-file paths. To read them:

```sh
journalctl MESSAGE_ID=8915bdd215c84cbab0c31c05a1a33e7e
```

## Develop

```sh
mise install                  # the pinned tools
make ci BASE=origin/main      # what CI runs
make docker-ci BASE=origin/main  # the same, in the development image
```

The build, run, package-validation and release guide is [docs/development.md](docs/development.md).

## Where to go next

- [AGENTS.md](AGENTS.md): the rules for agents and contributors.
- [docs/README.md](docs/README.md): the decisions, designs, specs and plans.
- [SECURITY.md](SECURITY.md): report a vulnerability privately.
- [LICENSE](LICENSE): MIT.
