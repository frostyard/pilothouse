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
