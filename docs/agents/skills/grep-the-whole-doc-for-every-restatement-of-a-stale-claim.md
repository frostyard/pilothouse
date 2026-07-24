# When a doc-staleness objection names a claim, grep the whole file for every restatement of it

**When it applies:** A reviewer objection says a doc (`docs/*.md`,
`yeti/OVERVIEW.md`, etc.) still contains a stale claim — e.g. "this doc
still says X is not yet landed" — and the doc has more than one place where
that same fact is asserted in prose (a summary count near the top, a
per-phase status aside in the middle, a closing note). It's tempting to
find *a* sentence that matches the gist of the objection, rewrite it, and
resubmit.

**What to do:** Objections in this repo's review gate are anchored to a
specific file and line. Before resubmitting, re-read that exact line (or
search the whole file for the literal phrase quoted in the objection, e.g.
`grep -n "not yet landed"`) and confirm it actually changed — do not assume
that editing a different paragraph which discusses the same feature
satisfies the objection. A doc that mentions one landing fact in two
separate places (a "no consumer yet" aside in one section and a "not yet
landed" aside in another) needs *both* edited in the same commit; fixing
one and leaving the other means the next review round repeats what looks
like "the same objection" verbatim, because it is — just pointing at the
line you didn't touch. If a doc-grounding objection's file/line and quoted
text are byte-for-byte identical across two consecutive rounds, that is a
hard signal the previous edit missed the actual flagged sentence, not that
the reviewer is being repetitive; stop and diff the flagged line specifically
rather than re-reading the whole objection for new details.

**Learned from:** mill run for issue #51, chunk 9 (`docs/capabilities.md`).
Round 1 objected that the doc still said web-side rendering of host-image
status "is not yet landed and is not described here" (line 149, inside the
Phase 1b sysext-gating paragraph). The agent's revision rewrote a
different, similarly-themed sentence elsewhere in the doc ("No web-side
code calls this query yet: ... it is a registered, capability-guarded
daemon surface with no web consumer," ~line 259, inside the
`QueryHostImageStatus` section) and added a large new "Phase 2" section
describing the landed rendering. Round 2 and round 3 both re-quoted the
identical line-149 sentence, unchanged, as the objection — the actual
flagged text was never edited across three rounds. The run failed with
this exact objection as the last recorded event, having exhausted
`review_rounds` without the one-line fix ever landing.
