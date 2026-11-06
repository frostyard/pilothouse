# Agent workflow tooling

Living document; extracted from [overview.md](overview.md). It covers the
repository's agent and automation surface: the cross-agent instruction files,
risk-tier classification and risk paths, the knowledge index and corrections
inbox, the engineering lessons, the synced skills, and the Claude automation
workflows. The repository baseline it follows is recorded in
[ADR-0014](../adr/0014-adopt-the-repository-baseline.md).

- `AGENTS.md` is the one canonical instruction file. `CLAUDE.md`, `GEMINI.md`,
  `.cursorrules` and `.github/copilot-instructions.md` are committed relative
  symlinks to it, so they are byte-identical by construction and can never
  drift; edit `AGENTS.md`, never an alias. `make verify-template`
  (`scripts/check-template.mjs`, check `agent-symlinks`) fails when an alias
  stops resolving to it.
- `.agents/skills/` holds the skills that frostyard/core syncs into this
  repository (core ADR-0026: `golang-*` and `frostyard-*`). They are
  byte-identical copies: change them in core, never here. `.claude/skills` is
  a relative symlink to `../.agents/skills`.
- The archived [frostyard/mill](https://github.com/frostyard/mill) spec→PR
  harness and the superpowers plan/spec workflow are retired. Their
  configuration (`.mill.toml`), their 50 lesson files (once in
  `docs/agents/skills/`) and their finished plans (once in
  `docs/superpowers/`) were removed by ADR-0014; the lessons that still hold
  are sections of [engineering-lessons.md](engineering-lessons.md), and Git
  history keeps the rest.
- `AGENTS.md` (and therefore `CLAUDE.md`) is deliberately generic: it carries
  only repository-wide process, stack, build-target, templ, release, and
  skill-review instructions, with no per-module feature inventory and no
  module-specific claim anywhere in it. A change that adds or reshapes a
  module's surface therefore does not make any sentence in it stale — the
  per-module feature narrative lives in `docs/design/overview.md`, in
  `docs/modules.md`, and in `docs/features.md`'s "What works" list (the
  root `README.md` keeps only a short summary that links it). Confirm this is
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
- `.github/risk-paths.json` lists the high-risk paths (core ADR-0068): the
  gate and governance files, the release files (`.svu.yml`,
  `.goreleaser.yaml`, `scripts/bump.sh`, every workflow), the installed
  packaging files (`packaging/**`) and the privileged code
  (`cmd/pilothoused/**`, `internal/broker/**`, `internal/auth/**`).
  `.github/workflows/risk-paths.yml` calls core's reusable workflow on
  `pull_request_target`; it checks out nothing, and adds the label
  `risk:high` to a pull request that touches a listed path. It is not a
  required check. `make verify-template` validates the list.
- `.knowledge/README.md` is the cross-session knowledge index. It points agents
  to the live owners of durable facts — `AGENTS.md`,
  `.memory/corrections.jsonl`, [engineering-lessons.md](engineering-lessons.md),
  `docs/design/overview.md`, and authoritative subsystem docs — rather than
  copying those facts into a second store that can drift.
- `.memory/corrections.jsonl` is the append-only corrections inbox (core
  ADR-0018). Each line is one JSON object with exactly `date`, `scope`,
  `correction`, `evidence` and `promoted_to` (a string: `""` until the rule
  moves to a durable owner). `make verify` validates every line
  (`scripts/check-corrections.mjs`); `make ci BASE=<ref>` and the Verify job
  also refuse a changed or removed line. A blocking review point adds 1 line
  in the pull request that fixes it. Committed knowledge must contain no
  credentials, personal data, speculation, or transient worktree state.
- [engineering-lessons.md](engineering-lessons.md) holds the rules learned
  from earlier defects and review findings. Code comments cite its anchors
  (`docs/design/engineering-lessons.md#<anchor>`), so keep anchors stable.
  Read it before planning, implementing or reviewing a change.
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
  multi-agent workflow definitions: `test-triage.yaml`
  (gate chain, only escalates to an LLM on failure), `code-review.yaml`
  (parallel security/correctness reviewers plus a synthesizer), and
  `module-audit.yaml` (fans out one audit agent per `internal/modules/*`
  directory). See `workflows/README.md` for setup and schema gotchas.
- Tool pins: `mise.toml` and `mise.lock` at the repository root pin
  `golangci-lint` (2.15.0) and Node (24.22.2, for the template gate); Go comes
  from `go.mod` (`1.27.3`, `idiomatic_version_file_enable_tools`). The
  Makefile reads its `GOLANGCI_LINT_VERSION` from `mise.toml`, and
  `make lint` fails unless the installed release is that pin. The CI Lint and
  Verify jobs and the nightly compliance job install the tools with
  `jdx/mise-action`. `make verify`, `make check` and `make ci` are the gate
  triad of [ADR-0012](../adr/0012-adopt-mise-tool-pinning-and-verify-gate.md).
  Not done yet from ADR-0012: the Makefile `GO_VERSION` variable, the
  hard-coded `go-version: "1.27.3"` inputs and the `stable` alias in the
  release, snapshot, packaging and image-tier workflows remain separate Go
  pins (all `1.27.3` or `stable` today); `svu` is still pinned only in the
  Makefile and `.docker/Dockerfile`; and both development Dockerfiles still
  take versions as build arguments instead of running `mise install`.
  Go 1.27.3 is the repository build and CI pin.
