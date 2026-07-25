# Run acceptance-criteria shell commands against the repo before locking them in

**When it applies:** Writing or reviewing a spec/plan whose acceptance
criteria embed a shell command meant to prove a count or property (e.g.
"reproducible with `grep -c '^\tAction' internal/broker/api.go`, expecting
35").

**What to do:** Actually run the command against the current tree before
finalizing the criterion, not just eyeball its intent. A single-quoted
`\t` in most `grep`/shell contexts is the literal two characters backslash-t,
not a tab — it silently returns 0 matches even when the expected count is
otherwise correct. The same class of trap applies to any acceptance
criterion built from a copy-pasted command: verify it against the real repo
state (tabs vs spaces, quoting, tool flags like `grep -P`/`awk`/`perl` for
tab-matching) rather than trusting that the command "obviously" does what it
reads as. An acceptance criterion that is impossible to satisfy as written
blocks the whole chunk/plan on a technicality unrelated to the actual spec
intent.

**Learned from:** mill run for issue #50, plan revision round 3 — the plan's
acceptance criteria required `grep -c '^\tAction' internal/broker/api.go` and
`grep -c '^\tQuery' internal/broker/api.go` to return 35 and 15; both
commands return 0 in this repo because the pattern's `\t` is not a real tab,
even though the stated counts were otherwise correct.

**`git diff` without `--cached` proves nothing once the acceptance workflow
stages files before checking them.** If a chunk's own acceptance criteria
(or the plan's binding staged-file-discipline rule) requires staging
required files before verification runs, any criterion written as plain
`git diff -- <file>` compares the worktree to the index — after the file is
staged, that diff is empty by construction, even though the file
genuinely changed relative to the commit base. Write the criterion as
`git diff --cached -- <file>` (or `git diff <base>..HEAD -- <file>` against
a fixed base) so it still shows the real change after staging. Likewise,
`git diff --stat <file>` (staged or not) only proves a file's line churn
count, not *which* lines or sites changed — it cannot verify a criterion
like "only these two description sites changed" or "the canonical wording
now appears at both call sites." When a criterion needs to confirm the
content of a specific change, require inspecting `git diff --cached -- <file>`
(or grepping the post-change content directly) rather than a line-count
summary.

**Learned from:** mill run for issue #65 (neutral branding sweep), plan
revision round 3 — two criteria were rejected in the same round: c1's
`git diff -- .gitignore` produced no diff once `.gitignore` was staged
(needed `git diff --cached -- .gitignore`), and c4's
`git diff --stat .goreleaser.yaml` could not identify which of the file's
description sites had changed or confirm they were staged.
