# Conductor workflows

Multi-agent workflows for [microsoft/conductor](https://github.com/microsoft/conductor),
run against this repo. All use the `claude-agent-sdk` provider, which
authenticates via your existing `claude login` (no API key needed) and gives
agents real file/Bash access with the repo root as their working directory —
so always run `conductor` from the repo root.

## Setup (one-time)

```bash
curl -sSfL https://aka.ms/conductor/install.sh | sh
uv tool install --force 'conductor-cli[claude-agent-sdk] @ git+https://github.com/microsoft/conductor.git@v0.1.25'
conductor doctor -p claude-agent-sdk   # should show Installed ✓
```

> Note: do **not** `uv tool install conductor-cli` from PyPI — that is an
> unrelated package that shadows the real Conductor. Install from the git URL
> (pin the tag to the release you want).

## Workflows

### test-triage.yaml
Deterministic quality gates (`gofmt` → `go vet` → `go test`) chained by exit
code. The green path costs **zero** LLM tokens; only on a failure does a
sonnet agent spin up, read the repo, and produce a structured root-cause
diagnosis.

```bash
conductor run workflows/test-triage.yaml
```

### code-review.yaml
Captures a git diff with a script step, then runs two reviewers **in
parallel** (security lens + correctness lens, each with repo access), and a
synthesizer merges their findings into one report with an
approve / approve-with-nits / request-changes verdict.

```bash
conductor run workflows/code-review.yaml                       # last commit
conductor run workflows/code-review.yaml -i range=main...HEAD  # branch diff
```

### module-audit.yaml
Dynamic `for_each` fan-out: a script step lists `internal/modules/*` as JSON,
a cheap haiku agent audits each module concurrently (tests, registration,
AGENTS.md conventions), and a sonnet aggregator merges everything into a
scorecard. Full 14-module sweep runs ~3 min / ~$0.20.

```bash
conductor run workflows/module-audit.yaml
conductor run workflows/module-audit.yaml -i filter=podman   # subset
```

## Useful flags

- `--web` — live DAG dashboard in the browser
- `--dry-run` — show the execution plan without running
- `conductor validate <file>` — schema check
- `conductor resume <file>` — resume the latest checkpoint after a failure
  (completed agents are cached; only remaining steps run)
- Event logs and checkpoints land in `/tmp/conductor/`

## Schema gotchas (v0.1.25)

- Script-step `env:` values are **not** Jinja-templated (inputs arrive as the
  literal `{{ ... }}` string). `args` are templated — pass untrusted inputs as
  extra argv entries after the `bash -c` script (`"$1"`), never interpolated
  into the shell source (shell injection) and never via `env`.
- Per-agent `provider:`/`model:` overrides work — e.g. one reviewer on
  `provider: copilot` with `model: gpt-5.5` while the rest of the workflow
  runs on `claude-agent-sdk` (cross-model adversarial review).
- In `bash -c` pipelines, remember the exit code is the last command's: use
  `set -o pipefail` on gates or failures get swallowed, and wrap `grep` in
  `{ grep ... || true; }` so no-match doesn't turn pipefail into a false red.

- `input:` declarations nest under `workflow:`, not at top level.
- Inline `for_each` agents require a `name:`.
- Outputs of a `parallel` group are read as
  `{{ group.outputs.agent_name.field }}`, not `{{ agent_name.output.field }}`.
- The `claude-agent-sdk` provider ignores `working_dir` and per-agent `tools:`
  allowlists, and rejects `reasoning.effort` and `mcp_servers` (experimental
  carve-outs).
