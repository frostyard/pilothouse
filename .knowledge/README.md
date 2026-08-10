# Cross-session knowledge

This directory is the entry point for durable knowledge that Pilothouse agents
must carry between sessions. Read this index before changing the repository,
then follow the linked source that matches the task instead of relying on a
prior conversation.

## Knowledge sources

- [`../AGENTS.md`](../AGENTS.md) — binding repository-wide invariants, build
  gates, and contribution instructions.
- [`../corrections.jsonl`](../corrections.jsonl) — append-only records of
  verified differences between an agent's prior belief and repository reality.
- [`../docs/agents/skills/`](../docs/agents/skills/) — durable implementation
  and review lessons harvested from previous mill runs. Every skill is binding.
- [`../yeti/OVERVIEW.md`](../yeti/OVERVIEW.md) — current architecture,
  subsystem contracts, and rationale written for agent context.
- [`../docs/`](../docs/) — authoritative subsystem documentation and historical
  design records.

## Recording knowledge

Record a correction only after a repository file, command, issue, pull request,
or maintainer response verifies it. Append one JSON object per line to
`corrections.jsonl` with this shape:

```json
{"date":"YYYY-MM-DD","scope":"repo-relative path or subsystem","correction":"prior belief and verified reality","evidence":"file, command, issue, or pull request","promoted_to":null}
```

Use `promoted_to` to name the durable document that now carries the rule. Put
stable repository-wide instructions in `AGENTS.md`, reusable implementation
lessons in `docs/agents/skills/`, architecture and rationale in
`yeti/OVERVIEW.md`, and subsystem contracts in the relevant `docs/` file.
Do not duplicate those facts in this index.

Never record credentials, tokens, personal data, unverified speculation, or
transient worktree state in committed knowledge artifacts.
