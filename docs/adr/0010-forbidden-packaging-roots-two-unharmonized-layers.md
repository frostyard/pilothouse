# 0010 — Packages never install under systemd-owned roots; two enforcement layers stay unharmonized

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

Core ADR-0004 gives pilothouse product-namespaced filesystem roots split by
lifetime tier and establishes that systemd owns the runtime and state tiers.
Concretely, the packaged units declare `RuntimeDirectory=pilothouse` and
`StateDirectory=pilothouse` (`packaging/deb/pilothoused.service:14-17`,
`packaging/rpm/pilothoused.service:14-17`; the web unit adds
`StateDirectory=pilothouse/web` in `packaging/pilothouse.service:14-15`), so
systemd creates, owns, and garbage-collects `/run/pilothouse` and
`/var/lib/pilothouse`. A package that ships files under those roots fights
systemd for ownership: package-owned paths survive unit removal, get wrong
modes, and break `RuntimeDirectory` recreation semantics.
`.goreleaser.yaml:164-166` records the packaging side of the rule: those
directories are deliberately not packaged.

A single check could enforce this, but the repo's packaging validation has two
different vantage points — the build *configuration* and the built
*artifacts* — and a shared implementation (or shared list) would let one
layer's bug or edit blind both.

## Decision

Packages must never install anything under `/run/pilothouse` or
`/var/lib/pilothouse`; systemd's `RuntimeDirectory=`/`StateDirectory=`
directives are the sole owners of those roots. Two independent layers enforce
this, with deliberately different matching semantics, and they must not be
harmonized:

- **Config-level (plain prefix, stricter):**
  `checkNoSystemdManagedPaths` in `packaging/goreleaser_config_test.go:164-174`
  walks `.goreleaser.yaml`'s contents and rejects any destination matching
  `strings.HasPrefix(dst, managed)` against the `systemdManagedPaths` list
  (line 23). The plain prefix is on purpose (comment at lines 160-163): it
  rejects the root itself, anything nested under it, *and* near-miss siblings
  such as `/run/pilothouse-helper` — at config-review time a sibling name that
  close to a systemd-owned root deserves a human look, not silent acceptance.
  Exercised by `TestOverridesNeverPackageSystemdManagedPaths` (line 410) and
  `TestCheckNoSystemdManagedPathsRejectsMutations` (line 425).
- **Artifact-level (component-aware, narrower):** `packaging.Verify` checks
  each artifact entry with `entry.Dest != root && !strings.HasPrefix(entry.Dest, root+"/")`
  (`packaging/verify.go:281-312`, match at line 291), emitting the
  `forbidden_path` finding (`packaging/finding.go:40`). This accepts
  `/run/pilothouse-helper` as a path a package may legitimately own — the
  artifact check judges what was actually built, not what looks suspicious.

The non-harmonization is contractual, not accidental: `verify.go:255-273`
("checkNoSystemdManagedPaths must NOT be 'harmonized' with the rule here")
and `TestVerifyForbiddenPathContainmentIsComponentAware`
(`packaging/verify_test.go:1142`, fixtures for roots, descendants, and
near-miss siblings at lines 584-610) pin the divergence. Each layer also keeps
its own hand-written root list (`packaging/contract.go:126` vs
`goreleaser_config_test.go:23`) so neither list is the other's oracle
(`contract.go:119-121`), and `packaging/verify-install.sh:59` restates the
rule a third time at install-tier, guarded by
`packaging/verify_install_test.go:238`.

## Consequences

- Shipping a file under a systemd-owned root fails at two independent stages
  (config test, artifact verification) plus the container install tier; a bug
  or edit in one layer does not blind the others.
- The layers disagree on near-miss siblings by design: a future
  `/run/pilothouse-helper` entry passes artifact verification but fails the
  config-level test, forcing a human decision (rename it, or consciously
  amend `systemdManagedPaths` and its rationale). That friction is the
  feature.
- Anyone "cleaning up" the duplicate lists or unifying the two matchers
  destroys the independence; the O4 comments and this ADR are the guard
  rails.
- Adding a new systemd-owned directory (as `StateDirectory=pilothouse/web`
  did) means updating unit files and, where it introduces a new *root*, both
  hand-written lists.

## Alternatives considered

- **One shared check/list used by both layers:** a single edit (or bug)
  disables the rule everywhere; the two vantage points collapse into one.
- **Harmonizing both matchers on the component-aware rule:** loses the
  config-level tripwire for suspicious sibling names next to systemd-owned
  roots.
- **Packaging the directories with matching modes:** fights
  `RuntimeDirectory` lifecycle semantics and leaves orphaned package-owned
  state after unit removal; rejected by `.goreleaser.yaml:164-166`.

## References

- Shapes: [design/overview.md](../design/overview.md) (packaging validation;
  the artifact contract is being split out to `docs/specs/artifact-contract.md`
  by PR #193)
- Enforced by: `packaging/goreleaser_config_test.go`
  (`TestOverridesNeverPackageSystemdManagedPaths`,
  `TestCheckNoSystemdManagedPathsRejectsMutations`), `packaging/verify_test.go`
  (`TestVerifyForbiddenPathContainmentIsComponentAware`),
  `packaging/verify_install_test.go`
- Related: [0004 — hand-transcribed packaging contract](0004-hand-transcribed-packaging-contract-oracle.md)
- Builds on: [core ADR-0004 — Product-namespaced filesystem paths, split by lifetime tier](https://github.com/frostyard/core/blob/main/docs/adr/0004-product-namespaced-filesystem-tiers.md)
  (see [org-adrs.md](../org-adrs.md))
