# Map every spec requirement to a chunk or an explicit deferral before submitting a plan

**When it applies:** Writing or reviewing a plan that decomposes a spec into
chunks, especially when a spec requirement is phrased as one sentence that
actually bundles several distinct, separately-checkable asks — e.g. "X must
be surfaced end to end in the broker response, the state struct, and the
UI," or "source A and source B must both report availability/errors."

**What to do:** Before submitting the plan, walk the spec requirement by
requirement and, for each discrete clause, name the specific chunk whose
acceptance criteria implement it, or state explicitly that the clause is
out of scope/deferred and why. Partial coverage of a bundled sentence reads
as "handled" to the plan's author but is not: implementing 2 of 3 clauses
(e.g. threading a value into one struct field but never adding the
corresponding field to the query/state type it's supposed to also appear
in) or covering one of two named sources (e.g. specifying availability/error
fields for source A's response shape but leaving source B's shape
unspecified) leaves a requirement with no chunk and no acceptance test that
could catch its absence. This is a different check from enumerating a test
matrix inside one chunk (see
`enumerate-the-full-test-matrix-for-multi-axis-criteria.md`) — it's a
plan-level requirement-coverage pass across the whole chunk list, done
before any chunk is written, not test-coverage inside a single chunk.
Also check plan-level chokepoints that a chunk changes but doesn't finish
propagating: if a chunk changes what interface/mechanism a module uses for
whole-module availability, confirm some chunk in the series updates every
place that currently special-cases the old mechanism (e.g. a web shell's
central availability helper), not just the module's own routes.

**A spec clause that says a capability "remains owned by module Y" after
being removed from module X needs a chunk with acceptance criteria that
actually add Y's rendered replacement, not just a chunk that deletes X's
copy.** Recurred in mill run for issue #52, plan review round 2 (splitting
Extensions from Maintenance). The spec required "extension inventory,
update availability, and extension jobs remain owned by Extensions" — a
relocation, not a deletion — but the plan's Maintenance-removal chunk
dropped `State.Updates`, the Health finding, dashboard rendering, and
README wording, while the Extensions-module chunk's acceptance criteria
covered only inventory, source errors, and action gating — no criterion
required update availability to render anywhere. Every listed acceptance
criterion could pass while the user-visible feature vanished from the app
between the two chunks. When a plan relocates a feature's ownership from
one module to another, write the destination module's acceptance criteria
first and check they name the specific rendered output (a field on a page,
a section, a table) — not just "consumes the shared aggregate" or "the data
is available via the query" — before treating the source module's removal
chunk as complete.

**Learned from:** mill run for issue #51 (Maintenance host-image status).
Plan review rejected the plan on reject-severity grounds across three
consecutive rounds because discrete spec requirements had no implementing
chunk: soft-reboot eligibility was never added as a field to
`QueryMaintenanceState`/`maintenance.State` even though other chunks
covered parsing the source data and rendering the UI section it was meant
to feed; the plan introduced a new whole-module `CapabilityGateAny`
gate but no chunk updated `internal/web/server.go`'s `moduleAvailable`
(the actual nav/dashboard/route choke point) to understand it, so real
gating would have silently kept treating the module as available; and the
response shape specified `BootcAvailable`/`BootcError` fields for bootc but
never specified the equivalent rpm-ostree fields, leaving that half of a
"both sources report availability/errors" requirement unspecified and
therefore untestable in any chunk.
