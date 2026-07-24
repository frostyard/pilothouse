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

Literal-phrase grep is not enough by itself: a stale claim about "feature X
has landed / not landed" often has *siblings* that are not textual restatements
of the same sentence at all — a hardcoded ID/query count ("52 IDs, 17
queries"), a hardcoded constant count ("35 `Action*` constants"), or a
worked-example list of per-module broker calls that's now missing the new
one. These siblings frequently live inside a narrative section written for a
*different, already-finished* feature/issue (e.g. a "landed end state" recap
for issue #51) rather than anywhere near the section you're editing for the
current change, so a search scoped to "the paragraph about my feature" misses
them entirely. Before resubmitting a chunk that adds a new broker
capability, `grep -n` the whole file (and any sibling context doc that
narrates the same module, e.g. `docs/modules.md` alongside
`yeti/OVERVIEW.md`) for: the literal objected phrase, the module/feature
name, and any number that looks like an ID/query/constant count — update
every hit in the same commit, not just the one the reviewer already found.

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

Recurred in mill run for issue #58, chunk 3 (`yeti/OVERVIEW.md`,
`docs/modules.md`), despite this skill already being in the repo. Round 1
flagged line 100's "has not landed" sentence; the agent fixed it. Round 2
flagged a *second* stale sentence 3 lines away at line 81 ("no
automatic-update reporting") plus a stale per-module worked-example table
in `docs/modules.md` line 293 that hadn't listed the new
`QueryAutoUpdateStatus` call; the agent fixed both. Round 3 flagged two
more spots in the same `yeti/OVERVIEW.md`: line 103's "no production code
path... gates on or reports them" claim (a third restatement of the same
underlying fact, in different words), and line 237's hardcoded "52 IDs, 17
queries" capability-table count, now stale at 53/18 — both embedded inside
a "Host-image status (#51) — landed end state" recap section for the
*previous* feature, not anywhere the agent had touched. The run exhausted
`review_rounds` (limit 2, 3 rounds used) with these still open and failed.
Each round's fix was correct but scoped to only the line quoted in that
round's objection, never a proactive whole-file sweep for every sibling
claim before resubmitting.
