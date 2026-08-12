# 0007 — Keep docs/capabilities.md a binding table, mirrored in tests and diffed against live code

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

Every broker ID's required host capability is a security-relevant fact: it
decides which surfaces exist on which hosts. Those facts need to live
somewhere reviewers and agents read — a document — but documents drift from
code unless something fails when they do. With 64 registered IDs across four
registries, hand-checking the mapping at review time does not scale.

## Decision

[docs/capabilities.md](../capabilities.md) is the binding reference mapping
every registered broker ID to its required capability, and it carries the
invariant that it covers "every broker ID registered today" — so **the
document is updated in the same chunk that registers a new ID**
(`docs/capabilities.md:49-52`; `docs/modules.md:105-117` binds registrations
to the table).

The mechanical guard is a mirror-plus-live-diff, in
`cmd/pilothoused/capability_contract_test.go`:

- `capabilityTable` (line 111) is a hand-maintained Go table mirroring the
  document row for row — 64 rows, count asserted by
  `TestCapabilityTableHasExactlySixtyFourRows` (lines 430-431).
- `declaredBrokerIDs` (lines 238-274) parses `internal/broker/api.go` with
  `go/ast` — the live file, not a snapshot, "so a constant added, removed,
  or renamed in api.go changes this result immediately and without anyone
  remembering to update a mirror" (lines 232-237).
- `TestCapabilityTableMirrorsBrokerAPIConstants` (lines 284-320) diffs the
  two **in both directions**: a declared `Action*`/`Query*` constant with no
  table row fails (line 308), and a table row naming an ID the code no
  longer declares fails (lines 317-319). It also pins the 40/24/64 totals
  against the parsed source and checks each constant is registered in a
  registry of the matching kind (lines 310-315).
- The full table is additionally enforced behaviorally across a fixture
  matrix of capability sets (`docs/capabilities.md:15-16`), and the web
  binary carries its own transcribed tables covering all 64 IDs
  (`cmd/pilothouse/capability_contract_test.go:262-267, 2725`).

The Markdown file itself is deliberately **not** machine-parsed: the doc is
prose-plus-table written for readers, mirrored by the Go table
(`docs/capabilities.md:52-55`). The human-maintained link is doc↔table; the
machine-enforced link is table↔code. An ID can therefore only drift out of
the document by someone editing the Go mirror without the doc in the same
change — a same-file, same-review diff.

## Consequences

- Registering a broker ID without touching the contract test fails the
  build; the failure message names the doc, so the same change updates
  `docs/capabilities.md`, the Go mirror, and the registration together.
- The doc keeps its narrative value (exception rationales, any-of
  explanations, history) because no parser constrains its format.
- The residual gap is doc↔mirror: a reviewer must still check that a
  `capabilityTable` edit carries the matching doc row. The invariant
  statement inside the doc and this ADR are the instruction to do so.
- Two transcribed tables (daemon test, web test) plus the doc means three
  restatements per ID — deliberate, same independent-oracle posture as
  ADR-0004, at a real maintenance cost per new ID.

## Alternatives considered

- **Parse the Markdown table in the test:** couples the doc's prose format
  to a parser, and the doc would degrade into machine-first rows; the Go
  mirror keeps enforcement while the doc stays a document.
- **Generate the doc from code:** inverts authority — the doc is where the
  *intended* mapping is reviewed; generation would bless whatever the code
  says (the drift direction ADR-0004 exists to catch).
- **No doc, code comments only:** scatters the security mapping across 64
  registration sites; reviewers and agents lose the single binding table.

## References

- Shapes: [capabilities.md](../capabilities.md) (the table this ADR makes
  binding), [modules.md](../modules.md)
- Enforced by: `cmd/pilothoused/capability_contract_test.go`
  (`TestCapabilityTableMirrorsBrokerAPIConstants`,
  `TestCapabilityTableHasExactlySixtyFourRows`),
  `cmd/pilothouse/capability_contract_test.go`
- Related: [0006 — opt-in capabilities](0006-opt-in-capabilities-zero-io-omission.md),
  [0004 — duplication-as-oracle](0004-hand-transcribed-packaging-contract-oracle.md)
- Builds on: [core ADR-0016 — Reverse-DNS org.frostyard.* identifiers](https://github.com/frostyard/core/blob/main/docs/adr/0016-reverse-dns-org-frostyard-identifiers.md)
