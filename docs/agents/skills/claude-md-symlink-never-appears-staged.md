# CLAUDE.md is a tracked symlink to AGENTS.md — it can never appear as a changed path in a staged diff

**When it applies:** Writing or reviewing a spec/chunk acceptance criterion
that requires a specific file to appear in `git diff --cached --name-status`
(or any similar "staged files must include `<path>`" check), where that
path is `CLAUDE.md`.

**What to do:** `CLAUDE.md` in this repo is a git symlink (mode `120000`)
pointing at `AGENTS.md`, not an independent file with its own content.
Editing the shared prose changes only the blob `AGENTS.md` points to; the
symlink object itself is untouched and will never show up in a staged diff
unless something needlessly retargets or replaces the symlink. Any
acceptance criterion or gate that lists `CLAUDE.md` alongside `AGENTS.md` as
files that must both appear staged is unsatisfiable through normal content
edits. When writing or reviewing such a spec, drop `CLAUDE.md` from
staged-file lists and instead verify shared-doc content through the symlink
target (`AGENTS.md`) or by reading through `CLAUDE.md` (which will
transparently show `AGENTS.md`'s content) without expecting it to appear as
a changed path.

**Learned from:** mill run for issue #77, plan review round 1. The initial
plan required chunks c4 and c5 to have both `CLAUDE.md` and `AGENTS.md` in
`git diff --cached --name-status`; the plan reviewer rejected this because
`CLAUDE.md` is a tracked symlink to `AGENTS.md` and cannot appear staged
without an unnecessary symlink-target change. The plan was revised to drop
`CLAUDE.md` from the required staged-file lists before proceeding.
