---
title: Overview
description: What pilothouse is and where to start.
group: Getting started
order: 1
---

pilothouse is a local web administration console for [snosi](https://github.com/frostyard/snosi) installations. It starts with a live system dashboard and complete sysext lifecycle management through snosi's `updex` interface.

The application is built from Go and templ on the server, HTMX for focused page updates, an embedded design system, and no Node runtime or external frontend assets.

## What it does

- Live CPU, memory, persistent storage, load, uptime, network, host, OS, and kernel metrics, refreshed every 15 seconds
- An attention view for disk, memory, load, failed systemd units, and unavailable status sources
- Systemd service, socket, and timer inventory with administrator-only lifecycle and enablement controls
- Discovery of shared `/usr/lib/sysupdate.d` and component-scoped `/usr/lib/sysupdate.<name>.d` snosi definitions
- Install, remove, update-all, and merge-refresh actions through `updex` and `systemd-sysext`
- System Podman, Docker Engine, and local Incus inventories with lifecycle controls and bounded log viewing
- Extension update availability, reboot-required posture, and confirmed host reboot
- Exact systemd backup timer monitoring with freshness and last-result health

## How it is built

Two processes share the work. An unprivileged web process serves the console; a root-only action broker performs privileged operations. They connect through a protected Unix socket.

Authentication uses PAM against the host's users and account policy. Sessions are opaque and idle-expiring, with per-session CSRF tokens. Members of the configured broker admin group (`sudo` by default) can perform sysext and container mutations; every other authenticated account can view the dashboard.

Privileged actions are durable: the broker records action history, requires destructive confirmations, and serializes actions per resource. Extension update and refresh operations run as durable background jobs.

Liveness and broker-aware readiness endpoints live at `/healthz` and `/readyz`.

## Where to start

- Read [installation](/getting-started/installation/) to build and install pilothouse on a snosi host.
- The [CLI reference](/reference/cli/) lists the flags for both the web process and the broker.
