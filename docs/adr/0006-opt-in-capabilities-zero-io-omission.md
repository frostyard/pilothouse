# 0006 — Capabilities are a closed vocabulary; unconfigured means zero I/O, unavailable means omitted

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

Pilothouse manages hosts whose optional tooling varies: some run Podman, some
Docker, some Incus, k3s, updex, bootc, rpm-ostree — most run only a few. The
broker must decide which privileged surfaces to offer on a given host. The
tempting defaults are dangerous in a privileged daemon: autodetecting a tool
by finding a binary on `PATH` (or a socket at a conventional path) means an
attacker who can drop a file at a well-known location gets a root-owned
daemon to exec it; and rendering "degraded" UI for absent tooling turns every
missing tool into error surface.

## Decision

Host capabilities are a **closed vocabulary of exactly twelve IDs** declared
in `internal/capability/capability.go:19-32` (`systemd`, `journald`,
`updex`, `sysext`, `bootc`, `rpm-ostree`, `autoupdate-rpm-ostree`,
`autoupdate-bootc`, `podman`, `docker`, `incus`, `k3s`); "there is no
mechanism to register additional IDs at runtime" (lines 14-16), and the wire
strings are pinned by `TestIDConstantsMatchCanonicalStrings`
(`capability_test.go:11-25`).

Optional integrations are **opt-in by explicit configuration, and an
unconfigured integration performs zero I/O**. The five optional-tooling
flags on `pilothoused` default to empty; each probe returns an empty set on
the zero value without running a command or dialling a socket
(`docs/modules.md:118-139`). `ProbeUpdex` is the canonical statement
(`internal/capability/probe_exec.go:50-64`): "there is deliberately no
PATH-lookup fallback: an unconfigured updex must never be exec'd, even if
some binary named 'updex' happens to be on PATH."
`TestProbeUpdexAbsentAndNeverRunsAnythingWhenUnconfigured`
(`probe_exec_test.go:78-90`) wires a *succeeding* runner and asserts zero
calls, so the test cannot pass by a probe merely failing; the packaged units
pass none of the five flags, so a stock install runs with all five off.

**Unavailable surfaces are omitted, never degraded.** Every `registerX`
function in `cmd/pilothoused/main.go` is guarded per registration by
`caps.Has` / `caps.HasAll` / `caps.HasAny`
(`internal/capability/capability.go:52,61,75`; call sites at
`main.go:542,576,735,877,922,1050,1077,1110,1167,1193,1219,1231`) and
returns nil before any `Register` call — the surface is "simply absent from
every registry (and from QueryCapabilities) rather than aborting startup"
(`main.go:1160-1165`). The one deliberate exception is
`registerCapabilities` itself (`main.go:519-529`): capability discovery
requires no capability, because it is what reports them. The web side
mirrors the rule: skipped modules render nothing — no card, no error
placeholder (`docs/modules.md:186-188`).

## Consequences

- A file dropped on `PATH` or a socket at a conventional path can never
  cause the root daemon to exec or dial it; enabling an integration is an
  administrator's explicit, auditable flag/env change.
- A stock install starts with every optional integration off — secure by
  default, at the cost that operators must configure each tool they want
  managed.
- The closed vocabulary keeps `docs/capabilities.md`'s binding table and the
  contract tests finite and enumerable (see ADR-0007); adding a capability
  is a reviewed vocabulary change, not a runtime event.
- Omission means a user cannot tell "not installed" from "not configured"
  in a module's UI; `QueryCapabilities` is the introspection point.
- Every new registration must choose and justify its guard (`Has` vs
  `HasAll` vs `HasAny`); the any-of rows exist so a host can still report
  an empty state when either of two sources would do.

## Alternatives considered

- **Autodetect via PATH lookup / conventional socket paths:** turns a
  writable well-known location into privileged execution; rejected in code
  comments (`probe_exec.go`, `probe_engines.go:100-103`) and by test.
- **Register everything and fail per request:** every absent tool becomes
  runtime error surface and degraded UI; absence handling scatters into
  every handler instead of one registration guard.
- **Runtime-extensible capability registration:** unbounded vocabulary
  breaks the enumerable contract table and its both-direction tests.
- **Config file listing enabled integrations:** pilothouse has no config
  file by decision (ADR-0008); flags/env are the configuration surface.

## References

- Shapes: [capabilities.md](../capabilities.md) (the binding table),
  [modules.md](../modules.md) (capability-guarded registration),
  [design/overview.md](../design/overview.md) (PR #193 splits this into
  `docs/design/capability-gating.md`)
- Implemented in: `internal/capability/`, `cmd/pilothoused/main.go`
- Enforced by: `internal/capability/capability_test.go`,
  `internal/capability/probe_exec_test.go`,
  `cmd/pilothoused/capability_contract_test.go`
- Related: [0007 — capabilities table contract](0007-capability-table-contract-tests.md),
  [0008 — flags and env configuration](0008-no-config-file-flags-env-precedence.md)
- Builds on: [core ADR-0016 — Reverse-DNS org.frostyard.* identifiers](https://github.com/frostyard/core/blob/main/docs/adr/0016-reverse-dns-org-frostyard-identifiers.md)
