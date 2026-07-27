# A "never does X" negative property needs a behavioral test, not an enumerated syntax denylist

**When it applies:** Writing or reviewing acceptance criteria for a shell
(or similarly dynamic) helper that must prove a negative — it never
inspects a candidate path's filesystem state, never exports variables,
never mutates the filesystem outside a scratch root — by listing forbidden
syntax forms (`grep`-for-`export`, a denylist of `[[ -e ]]`/`[[ -f ]]`/
`stat`/`file` calls, a count of allowed executable lines).

**What to do:** Syntax denylists are enumerable and therefore always
incomplete: reviewers in this repo will find the omitted form on the next
round (`[[ -p ]]`, `[[ -b ]]`, `file "$candidate"`, a export happening
inside a command-substitution subshell that a caller-side check can't see,
etc.), and each patch to close one gap tends to open or leave another,
burning plan-revision rounds. Prefer specifying a criterion that observes
the *actual* runtime property instead of the source text:
- "Never inspects the candidate" → assert the function produces the
  identical result whether the candidate path exists, is a directory, is a
  dangling symlink, or is entirely absent from the filesystem (a
  before/after existence-invariance test), not a grep over the function
  body.
- "Never exports variables" → diff `compgen -e` (or equivalent) captured
  immediately before and after calling the helper *in the current shell*
  (not via `$(...)`, which already hides exports — see the companion
  skill on command substitution), not a source-line count.
- "Never mutates outside a scratch root" → snapshot the filesystem tree
  outside the root before and after, and diff it, rather than banning
  specific commands (`rm`, `mv`, `chmod`) by name.
Reserve structural/textual checks for properties that are inherently
syntactic (e.g. "no `eval`"), and use behavioral tests for anything that
is actually a claim about runtime effects.

**Learned from:** mill run for issue #80 (image-workspace test-fixture
library), plan revision rounds 2 and 3 — `image_workspace_destination`'s
"never inspects the candidate" and "never exports state via command
substitution" criteria were both written as enumerated structural
denylists and both were rejected for omitting a form (`[[ -p ]]`/`[[ -b ]]`/
`file`, and export-inside-a-subshell) that the same rule should have
caught.
