# Agent workflow tooling

Living document; extracted verbatim from [overview.md](overview.md). It
covers the repository's agent/automation surface: the mill configuration
(`.mill.toml`), the cross-agent instruction files, risk-tier classification,
the knowledge index, harvested skills, and the Claude automation
workflows.

- `.mill.toml` configures the [frostyard/mill](https://github.com/frostyard/mill)
  spec→PR harness for this repo: `[gates].chunk` (`make generate`, `gofmt`,
  `go vet`, `go test`) runs after every chunk, `[gates].deep` (`make
  docker-ci`) runs before the ship decision, and `[context].docs` lists
  `AGENTS.md`, `docs/design/overview.md`,
  `docs/design/capability-gating.md`, and `docs/modules.md` as required
  reading for every mill agent. The mill engine itself lives in the separate
  `frostyard/mill` repo; this repo carries only config, learned skills, and
  cross-agent surface links (`CLAUDE.md`, `GEMINI.md`,
  `.github/copilot-instructions.md`, all pointing back to `AGENTS.md`).
  `CLAUDE.md` is a symlink to `AGENTS.md`, so the two are byte-identical by
  construction and can never drift.
- `AGENTS.md` (and therefore `CLAUDE.md`) is deliberately generic: it carries
  only repository-wide process, stack, build-target, templ, release, and
  skill-review instructions, with no per-module feature inventory and no
  module-specific claim anywhere in it. A change that adds or reshapes a
  module's surface therefore does not make any sentence in it stale — the
  per-module feature narrative lives in `docs/design/overview.md`, in
  `docs/modules.md`, and in `README.md`'s "What works" list. Confirm this is
  still true when reviewing AGENTS.md's "update relevant documentation after
  any change to source code" invariant for a feature change, rather than
  assuming either that it must be edited or that it can be skipped
  unexamined. (#51's host-image series was reviewed against it on exactly
  these grounds and required no edit to either file.)
- `docs/risk-tiers.md` is the pull-request change-classification authority.
  Every pull request selects the highest tier present in its final diff:
  documentation-only changes are Tier 1, routine unprivileged implementation
  is Tier 2, operational/capability/dependency/concurrency changes are Tier 3,
  and authentication, broker/root/package/release/secret or untrusted-input
  boundaries are Tier 4. Safeguards inherit the protected behavior's tier.
  Tier 3 adds targeted failure-path and rollback evidence; Tier 4 adds explicit
  maintainer security review, trust-boundary/abuse analysis, and least-privilege
  confirmation. The pull request template records the decision, while
  `CONTRIBUTING.md` explains when it must be updated; classification never
  substitutes for an existing gate.
- `.knowledge/README.md` is the cross-session knowledge index. It points agents
  to the live owners of durable facts — `AGENTS.md`, the append-only
  `corrections.jsonl`, every file under `docs/agents/skills/`,
  `docs/design/overview.md`,
  and authoritative subsystem docs — rather than copying those facts into a
  second store that can drift. Its correction schema requires a date, scope,
  the prior belief plus verified reality, evidence, and an optional promotion
  target. Committed knowledge must contain no credentials, personal data,
  speculation, or transient worktree state.
- `docs/agents/skills/` holds durable lessons harvested from previous mill
  runs (e.g. `templ-generated-files.md` on gitignored `*_templ.go` output).
  `AGENTS.md` requires reading every file there before planning,
  implementing, or reviewing changes — treat them as binding guidance.
- The Copilot assignment flow (`ai-fix-requested.yml`,
  `copilot-review-apply.yml`) and the nightly drift job for its
  `COPILOT_ASSIGNMENT_TOKEN` secret were removed on 2026-10-22: the maintainer
  chose not to provision a user-scoped token. Do not reintroduce a workflow
  that needs that token without a new decision.
- `.github/workflows/claude-code-review.yml` provides advisory AI review for
  non-draft, same-repository pull requests. It uses the commit-pinned official
  Anthropic action when `ANTHROPIC_API_KEY` is configured; an explicit
  configuration step otherwise emits a warning and gates off both checkout and
  review. The job has only `contents: read` plus `pull-requests: write`;
  checkout credentials are not persisted and its tools can only inspect PR data
  and create comments. The ordinary `pull_request` trigger and explicit
  head-repository check skip forks rather than exposing the secret through
  `pull_request_target`. Model output remains untrusted and cannot replace
  deterministic gates or human review. See `docs/claude-code-review.md`.
- `workflows/` holds standalone [Conductor](https://github.com/microsoft/conductor)
  multi-agent workflow definitions unrelated to the mill: `test-triage.yaml`
  (gate chain, only escalates to an LLM on failure), `code-review.yaml`
  (parallel security/correctness reviewers plus a synthesizer), and
  `module-audit.yaml` (fans out one audit agent per `internal/modules/*`
  directory). See `workflows/README.md` for setup and schema gotchas.
- Tool versions are not yet centralized. `golangci-lint` and `svu` are pinned
  as Makefile variables and re-declared as `.docker/Dockerfile` build args, and
  the Go toolchain is pinned in `go.mod` (`1.26.3`), the Makefile (`1.26.6`),
  and every workflow (`1.26.6` or the moving `stable` alias) with values that
  already disagree. [ADR-0012](../adr/0012-adopt-mise-tool-pinning-and-verify-gate.md)
  adopts frostyard/core ADR-0043: `mise.toml` + `mise.lock` at the repository
  root, `go.mod` as the only Go pin, no optional tools, and the `make verify` /
  `check` / `ci` gate triad. Until the adoption change lands, the existing pins
  remain independent and `make lint` still skips when the linter is absent.
