# Don't describe end-state behavior in docs before the chunk that finishes it

**When it applies:** Any plan that decomposes a cross-cutting behavior change
(e.g. "the daemon degrades gracefully when optional tooling X/Y/Z is
absent") across multiple chunks, where an early/transitional chunk updates
user- or agent-facing docs (README.md, yeti/OVERVIEW.md, docs/*.md). Also
applies to a *dedicated documentation chunk* at the end of a series (e.g.
"write the end-state architecture doc"), where the risk shifts from
temporal overclaiming to plain factual inaccuracy about already-finished
code.

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

Even in a dedicated final documentation chunk, where every chunk in the
series is already merged and there is no "ahead of the chunk" timing risk,
each sentence describing a mechanism is still a factual claim that must be
checked against the actual code, not written from memory of the intended
design. Before finalizing an architecture/overview doc: (1) for every "X
reads/calls Y" claim, open Y and confirm its signature actually takes/reads
what the sentence says (e.g. "both the route gate and the availability
helper read capabilities through `Host.Capabilities()`" is false if the
helper's signature takes an already-computed `capability.Set` and never
calls `Host` at all); (2) for every claim about *when* a cache/state
refreshes ("never stale indefinitely"), trace the actual refresh trigger in
code rather than asserting the intended invalidation policy; (3) for every
claim that a specific named module/surface participates in a new mechanism,
grep that module for the new primitive (its gate type, registration call,
etc.) and drop or narrow the claim if the module was explicitly out of
scope and never touched by the change.

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

Also mill run for issue #54, chunk 11 (final "web-side capability gating
end state" documentation chunk, after all mechanism chunks had landed) —
round 1 got three factual-accuracy objections in the same commit:
`docs/modules.md`/`yeti/OVERVIEW.md` claimed `platform.Available` reads
capabilities through `Host.Capabilities()` when its actual signature takes
a `capability.Set` parameter and never calls `Host`; `yeti/OVERVIEW.md`
claimed the capability cache is "never cached indefinitely" when the
implementation only refreshes on login or after an observed
`broker.ErrUnavailable`, so an unnoticed capability change with no failed
call could go stale forever; and `docs/capabilities.md` claimed the web
registry itself is derived from the capability table when in fact every
module is registered unconditionally and only nav/dashboard/routes are
filtered. Round 2 then hit the original temporal-overclaim failure mode too
(claiming `sysext` was capability-gated when that module was explicitly
out of scope and untouched) — confirming both failure modes show up in the
same kind of chunk and both need checking before submitting a doc chunk.

**This check is not just for dedicated end-of-series doc chunks — every
ordinary code chunk's own inline doc edit needs the same clause-by-clause
verification against its own landed diff.** Mill run for issue #64 (default-
off container engines + dev-gated mock Fleet) got a doc-accuracy objection
on nearly every chunk's `yeti/OVERVIEW.md` (or `README.md`) touch —
5 of 6 chunks — each a different shape of the same underlying mistake
(writing the doc sentence from the *intended* design rather than the
*landed* code):

- **Stale pre-change text left untouched.** Chunk 1 made Podman opt-in
  (empty default, no ambient socket probe), but `yeti/OVERVIEW.md:68` still
  described the old behavior — a running Podman socket being probed and
  enabled with zero configuration — for the paragraph covering the exact
  engine the chunk had just changed.
- **Overclaiming an unwritten log line.** Chunk 2 added an explicit
  `--docker` flag; the doc said an unreachable *configured* Docker endpoint
  "is logged as a warning." The actual code (`ProbeDocker` on ping failure,
  `connectDocker` only warning on client-construction failure) silently
  degrades to absent with no warning in the ping-failure path — a specific,
  checkable claim about logging behavior that nobody re-read against the
  function it described.
- **Order-of-operations claim contradicted by the code.** Chunk 3 said
  "disabled `ProbeIncus` avoids constructing a client," but the function
  actually calls `newIncusProbeClient()` unconditionally *before* checking
  the enabled guard — the opt-in gate skips using the client, not building
  it.
- **A new flag omitted from an existing enumerated table.** Chunk 4 added
  `--dev`; the pilothouse configuration/flag list in `yeti/OVERVIEW.md`
  wasn't appended to include it, its default, or the Fleet-preview
  consequence — the same "table siblings" trap
  `grep-the-whole-doc-for-every-restatement-of-a-stale-claim.md` names for
  counts, recurring here for a flag list instead of a count.
- **A worked example asserting a consequence its own shown command doesn't
  produce.** Chunk 5's `site/content/getting-started/installation.md`
  walkthrough said administrators "can perform Podman and Docker mutations"
  right after a local-run command example that passed none of the new
  opt-in flags — under that literal command, both engines are disabled and
  their modules aren't even registered. Ground a walkthrough's claims in
  the exact command shown a few lines above, not in what the feature can do
  in general.

Every one of these was a single-clause factual error, not a wholesale
false paragraph, which is exactly why re-reading only for "does this doc
mention the new behavior" isn't enough — read each clause (what gets
logged, what order two calls happen in, whether this exact flag appears in
every table that lists its siblings, whether the example command shown
actually enables what the next sentence claims) against the diff of the
same chunk, even when that chunk is not a dedicated documentation chunk.
