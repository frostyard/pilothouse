# Check `git diff --cached --name-status` against the acceptance file list before submitting a chunk

**When it applies:** Submitting any chunk whose acceptance criteria name a
specific file that must change in that commit (a README bullet, a doc note,
a generated file) — especially when that file was already edited earlier in
the run (by a previous chunk, a prior revision round, or manually in the
worktree) and its current contents already look correct.

**What to do:** "The file already reads correctly in the worktree" is not
the same claim as "this commit changes the file." Before submitting, run
`git diff --cached --name-status` (or equivalent) and confirm every
file the chunk's acceptance criteria requires is actually in the staged
set — not just present and accurate on disk. It's easy to edit a file,
decide the edit already satisfies a requirement from an earlier pass, and
forget to `git add` it, especially when several other files in the same
chunk genuinely do need staging. A reviewer checking `git diff --cached`
will flag the omission every round until the file is actually staged,
even if re-reading the objection makes it look like nothing changed —
because from the diff's perspective, nothing did.

**Learned from:** mill run for issue #51, chunk 11 (final documentation
chunk). Round 1 and round 2 both rejected the chunk because
`git diff --cached --name-status` showed only `docs/modules.md` and
`yeti/OVERVIEW.md` staged, omitting the `README.md` "What works" bullet
update the chunk's acceptance criteria explicitly required in the same
commit — even though the README text in the worktree already read
correctly by round 1. The fix (`git add README.md`) only landed on the
third attempt.
