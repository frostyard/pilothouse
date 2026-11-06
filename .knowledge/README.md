# Cross-session knowledge

This directory is the entry point for durable knowledge that Pilothouse agents
must carry between sessions. Read this index before changing the repository,
then follow the linked source that matches the task instead of relying on a
prior conversation.

## Knowledge sources

- [`../AGENTS.md`](../AGENTS.md) — binding repository-wide invariants, build
  gates, and contribution instructions.
- [`../.memory/corrections.jsonl`](../.memory/corrections.jsonl) — append-only
  records of verified differences between an agent's prior belief and
  repository reality (contract: [`../.memory/README.md`](../.memory/README.md)).
- [`../docs/design/engineering-lessons.md`](../docs/design/engineering-lessons.md)
  — durable implementation and review lessons, each checked against the code.
  Every lesson is binding.
- [`../.agents/skills/`](../.agents/skills/) — skills synced from
  frostyard/core; change them in core, never here.
- [`../docs/design/overview.md`](../docs/design/overview.md) — current
  architecture, subsystem contracts, and rationale written for agent context
  (formerly `yeti/OVERVIEW.md`).
- [`../docs/`](../docs/) — all documentation in frostyard/core's four-category
  shape (see [`../docs/README.md`](../docs/README.md)): repo-local ADRs,
  design docs, specs, plans, authoritative subsystem documentation, and
  historical design records.

## Recording knowledge

Record a correction only after a repository file, command, issue, pull request,
or maintainer response verifies it. Append one JSON object per line to
`.memory/corrections.jsonl` with this shape (all 5 fields are required
strings; `make verify` validates each line):

```json
{"date":"YYYY-MM-DD","scope":"repo-relative path or subsystem","correction":"prior belief and verified reality","evidence":"file, command, issue, or pull request","promoted_to":""}
```

Lines are append-only. Set an empty `promoted_to` to the durable document that
now carries the rule; never change a line otherwise. Put
stable repository-wide instructions in `AGENTS.md`, reusable implementation
lessons in `docs/design/engineering-lessons.md`, architecture and rationale in
`docs/design/overview.md`, and subsystem contracts in the relevant `docs/` file.
Do not duplicate those facts in this index.

Never record credentials, tokens, personal data, unverified speculation, or
transient worktree state in committed knowledge artifacts.
