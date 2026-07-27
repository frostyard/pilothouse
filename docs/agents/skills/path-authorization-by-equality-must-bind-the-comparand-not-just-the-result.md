# A path-authorization rule built on "resolve, then compare equal to a recorded value" must also constrain what counts as in-bounds — equality alone is not containment

**When it applies:** Writing or reviewing a spec/plan for any guarded
filesystem operation (test-fixture cleanup, cache eviction, extraction)
that authorizes acting on a `target` path by canonicalizing it and
comparing the result to a previously recorded value (e.g. "cleanup may act
on `target` iff its resolved form equals `$RECORDED_WORKSPACE`"), especially
when the record is a mutable variable and/or the target may be a symlink.

**What to do:** "Resolved target == recorded value" is not the same claim
as "resolved target is inside the authorized root," and plan reviewers in
this repo test both directions adversarially, round after round:
- If the *recorded value itself* can be repointed (e.g. reassigning the
  same variable used both to record and to authorize), an attacker path
  can be made to equal the record after the fact — the criterion needs an
  immutable/second record, not just "compare to the current variable."
- If the target may be a symlink, "resolves to the recorded value" accepts
  aliases from *anywhere*, including a symlink physically outside the
  authorized root whose target happens to match. Authorization must also
  check that the *supplied path itself* (not just its resolution) lies
  within the root, or the containment requirement is silently violated.
- Don't specify a canonicalization algorithm (e.g. "canonicalize dirname,
  re-append basename") and separately require that `.`-, `..`-, and
  symlink-spelled variants of the same path compare equal — verify by hand
  that the stated algorithm actually normalizes every listed variant before
  locking in the acceptance table; algorithms that skip `..`/symlink
  resolution won't satisfy a table that includes them.
- If the operation is destructive (e.g. `rm -rf -- "$target"`), check
  whether acting on the literal `$target` (which may be a symlink) versus
  the resolved path produces the effect the criteria actually describe
  (does it delete the link or the thing it points to?).

Work through at least one out-of-root symlink alias and one
record-repointing case by hand before finalizing the plan; these were
rejected on five consecutive rounds in one run before the harness gave up.

**Learned from:** mill run for issue #80 (image-workspace test-fixture
library), plan revision rounds 3, 5, and 6 — the guarded-cleanup
authorization for `image_workspace_cleanup` was repeatedly accepted with a
compare-to-recorded-value rule that a repointed variable or an
out-of-root symlink alias could satisfy, exhausting the plan-round budget
without ever reaching chunk execution.
