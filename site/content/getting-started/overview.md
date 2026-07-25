---
title: Overview
description: What pilothouse is and where to start.
group: Getting started
order: 1
---

pilothouse is a local web administration console for [snosi](https://github.com/frostyard/snosi) installations. It starts with a live system dashboard and, once the broker is pointed at `updex`, complete sysext lifecycle management through the `updex` interface.

The application is built from Go and templ on the server, HTMX for focused page updates, an embedded design system, and no Node runtime or external frontend assets.

## What it does

- Live CPU, memory, persistent storage, load, uptime, network, host, OS, and kernel metrics, refreshed every 15 seconds
- An attention view for disk, memory, load, failed systemd units, and unavailable status sources
- Systemd service, socket, and timer inventory with administrator-only lifecycle and enablement controls
- Layered discovery of shared `sysupdate.d` and component-scoped `sysupdate.<name>.d` `updex` definitions
- Install, remove, update-all, and merge-refresh actions through `updex` and `systemd-sysext`
- Extension update availability on the Extensions page, per extension and in aggregate
- System Podman, Docker Engine, and local Incus inventories with lifecycle controls and bounded log viewing
- Reboot-required posture and confirmed host reboot
- Exact systemd backup timer monitoring with freshness and last-result health

## What is optional

`updex` and the three container engines are off unless you configure them.
The broker probes `updex`, Podman, Docker, and Incus only when `--updex`,
`--podman-socket`, `--docker`, or `--incus` is passed to `pilothoused`; an
unconfigured one is reported absent without running a command or contacting
a socket, so an engine socket that merely happens to be running on the host
never enables anything. The packaged unit passes none of the four, which
means a stock install shows no Podman, Docker, or Incus surface and no
`updex`-backed extension operations until an operator adds the flags. See
the [CLI reference](/reference/cli/) for the exact flags and defaults.

Everything else is detected by presence and needs no flag: systemd,
journald, `systemd-sysext`, bootc, rpm-ostree, and the automatic-update
timer pairs. A surface whose dependency is absent is omitted — no navigation
entry, no dashboard card, and its routes return 404 — rather than shown
broken.

## How it is built

Two processes share the work. An unprivileged web process serves the console; a root-only action broker performs privileged operations. They connect through a protected Unix socket.

Authentication uses PAM against the host's users and account policy. Sessions are opaque and idle-expiring, with per-session CSRF tokens. Members of the configured broker admin group (`sudo` by default) can perform sysext and container mutations; every other authenticated account can view the dashboard.

Privileged actions are durable: the broker records action history, requires destructive confirmations, and serializes actions per resource. Extension update and refresh operations run as durable background jobs.

Liveness and broker-aware readiness endpoints live at `/healthz` and `/readyz`.

## Where to start

- Read [installation](/getting-started/installation/) to build and install pilothouse on a snosi host.
- The [CLI reference](/reference/cli/) lists the flags for both the web process and the broker.
