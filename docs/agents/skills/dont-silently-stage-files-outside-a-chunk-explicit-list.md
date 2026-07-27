# Don't silently stage a file outside the chunk's explicit file list

**When it applies:** A chunk's plan/description enumerates a specific,
closed list of files it is allowed to touch (e.g. "these five files"), and
implementing the chunk's behavior seems to require also creating or editing
one more file that isn't on that list (a new top-level test/entrypoint
script, a helper, a fixture).

**What to do:** Do not just add the extra file and resubmit — a reviewer
gate in this repo treats any staged path outside the chunk's declared file
list as scope expansion and will reject it as a `high`-severity objection,
regardless of how necessary the file seems. If the chunk truly cannot be
completed within its stated file list, stop and reconcile the plan first
(fold the new content into one of the already-listed files, or get the file
list itself amended) rather than shipping the extra file and hoping the
objection goes away on resubmission — repeating the same out-of-list path
across multiple revision rounds without addressing it burns review rounds
for no benefit, since the objection will not change until the extra path is
actually removed or the chunk's file list is actually updated.

**Learned from:** mill run for issue #67, chunk 7 — `test/vm/vm-boot-test.sh`
was staged as a sixth path against an explicit five-file chunk list. The
identical "outside the chunk's explicit file list" objection was raised in
three consecutive `chunk_revise` rounds without the extra file being removed
or the list being reconciled; the chunk exhausted its review rounds and
soft-passed with the objection still open.
