# 0003 — Build privileged detail surfaces from allowlists, never passthrough

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

Detail queries surface configuration of privileged subsystems to the web UI.
The raw upstream objects mix shape/posture data with payload and secrets: an
Incus instance's expanded config carries `user.user-data` (cloud-init,
routinely containing credentials), `environment.*`, `raw.lxc`; a network's
config carries `bgp.*` peer passwords. Upstream releases add keys over time.
A detail surface that copies the upstream map wholesale — or filters by a
denylist — leaks every key nobody has reviewed yet, and each upstream
release silently widens the exposure.

## Decision

Privileged detail surfaces are constructed from explicit allowlists; unknown
keys are excluded until reviewed. The Incus module is the canonical
implementation (`internal/modules/incus/detail.go`):

- `configKeys` (lines 71-84) — twelve reviewed instance-config keys
  (`boot.autostart*`, `limits.*`, `security.*`, `volatile.base_image`);
  "Every key here is instance shape or posture, never payload" (lines 68-70).
- `configPrefixes` (line 91) — the single reviewed prefix `image.`.
- `deviceProperties` (lines 97-104) — a per-device-type property map; "a
  device kind added by a future Incus release exposes no properties until it
  is reviewed and added here" (lines 93-96).
- `networkConfigKeys` (`internal/modules/incus/network.go:74-97`) — the
  network-detail allowlist; `bgp.*`, `ovn.*`, `tunnel.*`, `user.*`, `raw.*`
  are absent by design (lines 69-73), and the list model reads values through
  the same gate (`allowedNetworkValue`, lines 117-124) "so the list model
  cannot become a bypass around the detail model's filter".

The construction functions iterate the *allowlist*, not the source map
(`allowedProperties`, `detail.go:181-191`), so passthrough of unknown keys is
structurally impossible, and profiles reuse the instance allowlists
(`internal/modules/incus/profile.go:20-27`). The posture is stated at the
surface's entry point (`detail.go:11-24`): "built by allowlist, never by
copying the instance's expanded configuration wholesale… a key added by a
future Incus release is excluded until it is reviewed and added here."

Tests enforce the posture, not just the current list:
`TestDetailExcludesSecretConfiguration` asserts against the serialized JSON
(catching nested leaks) using a secret-laden fixture
(`internal/modules/incus/detail_test.go:40-75`,
`manager_test.go:332-347`); `TestDetailDeviceAllowlistIsPerType` injects a
future device kind (`brand-new-kind`) and asserts its properties do not
appear (`detail_test.go:104-120`); network and profile variants at
`network_test.go:128` and `profile_test.go:38-60`; a live-daemon test
asserts every returned key passes `allowedConfigKey`
(`manager_live_test.go:57`).

The same posture applies wherever a module surfaces upstream-defined key
spaces (e.g. the updater-unit allowlist in
`internal/modules/maintenance/autoupdate_manager.go:53`): default-deny,
reviewed additions only.

## Consequences

- Upstream releases cannot widen what pilothouse exposes; new keys stay
  invisible until someone reviews and adds them. Detail pages may lag
  upstream features, and that lag is the accepted cost.
- Every allowlist addition is a small reviewable diff answering one
  question: is this key shape/posture, or payload?
- Fixture-based secret tests document *why* keys are excluded; the
  future-device-kind test keeps the forward-compatibility property itself
  under test, not just today's list.
- New detail surfaces must budget for building and testing an allowlist
  rather than serializing the upstream struct.

## Alternatives considered

- **Wholesale passthrough of upstream config:** every upstream release
  silently widens exposure; cloud-init payloads and BGP passwords reach the
  browser.
- **Denylist of known-secret keys:** fails open — a key nobody has heard of
  yet is exposed by default, the exact wrong default for a privileged
  surface.
- **Redaction by value pattern (entropy/keyword scanning):** heuristic,
  bypassable, and still fails open on structured payloads like cloud-init.

## References

- Shapes: [design/overview.md](../design/overview.md) (Incus module; PR #193
  splits this into `docs/design/incus.md`), [modules.md](../modules.md)
  (privileged reads)
- Implemented in: `internal/modules/incus/detail.go`,
  `internal/modules/incus/network.go`, `internal/modules/incus/profile.go`
- Enforced by: `internal/modules/incus/detail_test.go`,
  `network_test.go`, `profile_test.go`, `manager_live_test.go`
- Related: [0001 — broker wire surface](0001-versioned-broker-wire-surface.md)
