# Don't describe end-state behavior in docs before the chunk that finishes it

**When it applies:** Any plan that decomposes a cross-cutting behavior change
(e.g. "the daemon degrades gracefully when optional tooling X/Y/Z is
absent") across multiple chunks, where an early/transitional chunk updates
user- or agent-facing docs (README.md, yeti/OVERVIEW.md, docs/*.md).

**What to do:** Write documentation claims that are true of the repo *after
this chunk's commit*, not the behavior the plan promises once every chunk in
the series has landed. If chunk N only capability-gates a subset of the
registrations covered by the overall goal (e.g. gates container-engine
registration but leaves systemd-dependent manager construction
unconditional), any doc line asserting the full end-state ("starts on a host
missing any combination of optional tooling") is false at that commit and
will be rejected — correctly — as ungrounded. Either (a) word the doc update
narrowly to describe only what this chunk actually changed, (b) explicitly
mark the behavior as partial/in-progress, or (c) move the user/agent-facing
doc update to the final chunk in the series where the full claim becomes
true, and say so in the plan. Also treat a binding reference table (e.g. one
that claims to map "every currently registered broker ID") as invalidated by
*any* chunk that adds a new ID — update the table in the same chunk that
registers the new ID, not a later one.

If the same doc-grounding objection recurs on a chunk across multiple
revision rounds with no substantive change between resubmissions, that is a
signal the fix belongs in chunk decomposition (split the chunk, or move the
doc update to a later/different chunk) rather than in rewording the same
doc — rewording without changing what is actually built will keep failing
the same objection until `review_rounds` is exhausted and the run fails.

**Learned from:** mill run for issue #50, chunk 5 — three consecutive
revision rounds got the identical objection (docs/capabilities.md still
claimed the pre-chunk 15-query/50-ID count and omitted the newly-registered
QueryCapabilities; README.md and yeti/OVERVIEW.md claimed the daemon starts
with any optional tooling missing, while systemd-dependent manager
construction outside the gated registrations still aborted startup). The
objection never changed because each resubmission reworded the same
overreaching claims instead of narrowing them to match the chunk's actual
partial implementation, and the run ultimately failed with the chunk stuck
unresolved.
