# 0004 — Keep the packaging contract hand-transcribed as an independent oracle

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

`packaging.Verify` checks built `.deb`/`.rpm` artifacts against a contract:
which files land where, with what modes and config flags, which per-format
runtime dependencies are declared, which scriptlet runs, and which roots are
forbidden. That contract has to come from somewhere. The obvious source is
`.goreleaser.yaml` itself — parse it and verify artifacts against whatever it
says. But a verifier that reads its expectations from the same config that
produced the artifacts can only detect packaging-tool bugs, never a wrong or
accidentally edited config: if someone changes a destination path or drops a
dependency in `.goreleaser.yaml`, artifacts and expectations move together and
nothing fails. The repo already had a near miss of this shape, documented in
`docs/agents/skills/completeness-tests-need-live-source-of-truth.md`.

## Decision

The packaging contract lives as hand-written Go tables in
`packaging/contract.go`, deliberately duplicating `.goreleaser.yaml` by hand:
embedded source names (`contract.go:19-36`), the postinstall scriptlet name
(`contract.go:46`), per-format runtime dependencies (`contract.go:88-111`),
forbidden systemd-owned roots (`contract.go:126`), and ten
destination/mode/config/source rows (`contract.go:168-198`). Nothing in
`contract.go` or `verify.go` reads the live config; the comment at
`contract.go:66-73` states that keeping the expectation hand-written "is what
makes it an independent statement of the contract rather than a restatement of
whatever the config happens to say."

Exactly one file ties the tables to the live config: `packaging/drift_test.go`,
whose guards `TestContractTablesMatchGoreleaserConfig` (line 76) and
`TestBinaryDestinationsMatchBuilds` (line 223 — the two `/usr/bin` binaries
are nFPM build outputs, invisible to the first guard's `contents` walk) parse
`.goreleaser.yaml` and fail on any divergence in either direction. A separate
suite, `packaging/goreleaser_config_test.go`, asserts the config against its
*own* hand-written tables — the opposite direction, acknowledged explicitly at
`drift_test.go:14-20` and `38-42`.

DRY refactors that would make one list the source for the other are rejected
on principle: the duplication *is* the check. Each layer keeps its own
hand-written list so neither becomes the other's oracle (`contract.go:76-77`,
`119-121`; `verify_test.go:584-587` — "that slice is the thing under test, so
it may not also be the oracle").

## Consequences

- An accidental or malicious edit to `.goreleaser.yaml` fails
  `packaging/drift_test.go` instead of silently shipping; an edit to the
  tables fails the same test from the other side. Changing the packaging
  contract therefore requires touching two files in the same change, which is
  the point.
- Contributors (and automated refactoring agents) will repeatedly be tempted
  to "deduplicate" the tables into the config or vice versa; every such
  refactor destroys the check. The intent comments in `contract.go` and
  `drift_test.go` exist to stop this, and this ADR is the durable record.
- Maintenance cost is real: every intentional packaging change is written
  twice, and the drift test's error output is the guide for keeping them
  aligned.

## Alternatives considered

- **Parse `.goreleaser.yaml` and verify artifacts against it:** detects only
  packaging-tool bugs; a wrong config verifies its own artifacts as correct.
- **Embed a YAML snapshot of the config as the fixture:** the fixture-vs-
  fixture failure mode `drift_test.go:22-42` names — snapshot and tables
  drift together, undetected.
- **Generate `contract.go` from the config:** same self-referential collapse
  with extra machinery.

## References

- Shapes: [design/overview.md](../design/overview.md) (packaging validation;
  PR #193 splits the artifact contract out to
  `docs/specs/artifact-contract.md` and the fixture layer to
  `docs/design/packaging-test-fixtures.md` — this ADR is their rationale)
- Enforced by: `packaging/drift_test.go`
  (`TestContractTablesMatchGoreleaserConfig`,
  `TestBinaryDestinationsMatchBuilds`)
- Related: [0010 — forbidden packaging roots](0010-forbidden-packaging-roots-two-unharmonized-layers.md),
  [docs/agents/skills/completeness-tests-need-live-source-of-truth.md](../agents/skills/completeness-tests-need-live-source-of-truth.md)
- Builds on: [core ADR-0022 — make ci is the canonical gate](https://github.com/frostyard/core/blob/main/docs/adr/0022-make-ci-gate-and-test-naming-filter.md)
