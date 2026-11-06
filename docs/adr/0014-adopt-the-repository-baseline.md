# 0014 — Adopt the frostyard repository baseline

- **Status:** Accepted
- **Date:** 2026-11-06

## Context

frostyard/core holds every code repository to one baseline
([core ADR-0071](https://github.com/frostyard/core/blob/main/docs/adr/0071-hold-every-code-repository-to-one-baseline.md),
rows B1 to B16 of `docs/specs/repository-baseline.md`, and row B17, the
README standard of core ADR-0072). Pilothouse already met
several rows: the instruction-file aliases of `AGENTS.md`, the docs shape,
SHA-pinned actions, `.svu.yml` with `make bump`, the MIT license, and no
`CODEOWNERS`. It did not have `make verify` or `make check`, `mise.toml` and
`mise.lock` ([ADR 0012](0012-adopt-mise-tool-pinning-and-verify-gate.md)
decided them, but the adoption change had not landed), `.memory/` (its
corrections file was `corrections.jsonl` at the root), a risk-path list, a
Dependabot configuration, a `SECURITY.md`, a `Risk tier` heading in the PR
template, or a CI workflow named `ci.yml`.

The repository also kept the output of agent frameworks that no longer run:
`.mill.toml` and 50 lesson files in `docs/agents/skills/` from the archived
frostyard/mill harness, a stray `scripts/__pycache__/mill_state.cpython-313.pyc`,
and 18 finished superpowers specs and plans in `docs/superpowers/` (every one
of their features is in the code, and their current design is in
[design/overview.md](../design/overview.md), [modules.md](../modules.md) and
`README.md`). `.agents` and a root `skills` entry were symlinks into
`docs/agents/`, so the skills that core syncs (core ADR-0026) lived under
`docs/`.

## Decision

- **Template gate.** Pilothouse takes the gate of `bketelsen/agentic-template`
  at `05e8f5d`: `scripts/check-template.mjs`, the vendored core files
  `scripts/check-corrections.mjs` and `scripts/lib/risk-paths.mjs`, and
  `test/check-template.test.mjs`, all byte-identical copies. The gate checks
  the instruction aliases and `.claude/skills`, that every doc under `docs/`
  is indexed in `docs/README.md`, the risk-path list,
  `.memory/corrections.jsonl`, and the vendored hashes. (In those copies,
  "ADR-0002" and "ADR-0003" name the template's ADRs; in Pilothouse this ADR
  records the same decision.)
- **Make triad and tool pins** (core ADR-0043, ADR-0044; the gate half of
  ADR 0012). `make verify` runs the non-mutating checks, the template gate,
  govulncheck and the tests; `make check` formats and then runs `verify`;
  `make ci` runs `verify`, then the corrections check against `BASE`, the
  race detector and the build. `mise.toml` pins golangci-lint at the release
  already in use (2.11.4) and Node; `mise.lock` holds their checksums;
  go.mod's `go` line stays the only Go pin mise reads. The Makefile reads its
  golangci-lint version from `mise.toml`, and `make lint` refuses another
  release. The development image copies in the pinned Node so
  `make docker-ci` still runs the whole gate.
- **CI entry.** `.github/workflows/test.yml` becomes `ci.yml`. The workflow
  name (`Tests`, which `snapshot.yml` follows), every job name and every job's
  permissions stay. The 6 jobs together run every target of `make ci`; the
  Verify job adds the template gate and the corrections check.
- **Corrections inbox** (core ADR-0018). `corrections.jsonl` moves to
  `.memory/corrections.jsonl`, with the template's `.memory/README.md`.
  `promoted_to` is a string (`""` until promotion), never `null`.
- **Risk paths** (core ADR-0068). `.github/risk-paths.json` lists the
  governance files and tool pins, the release files (`.svu.yml`,
  `.goreleaser.yaml`, `scripts/bump.sh`, every workflow), the installed
  packaging files, and the privileged code (`cmd/pilothoused/**`,
  `internal/broker/**`, `internal/auth/**`).
- **Skills and lessons.** `.agents/` is a real directory; core's synced skills
  live in `.agents/skills/`, and `.claude/skills` is a symlink to it. The root
  `skills` symlink is removed. The mill lessons that still hold, each checked
  against the code, move to
  [design/engineering-lessons.md](../design/engineering-lessons.md) under
  anchors named after the old file names; code comments cite those anchors.
  The lessons about the mill's plan-chunk process are deleted.
- **README** (core ADR-0072, row B17, added to the baseline while this change
  was open). `README.md` follows core's README spec for the kind `service`.
  Its operator and developer guides move to `docs/features.md`,
  `docs/install.md`, `docs/development.md` and `docs/problem-records.md`,
  each with a short introduction. The moved text keeps its facts; the
  changes are link paths, the example release version, and the gate
  instructions this ADR changes (`mise install`, `make ci BASE=...`, the
  gate triad, the lint pin).
- **Framework trees.** `.mill.toml`, `docs/agents/skills/`,
  `docs/superpowers/` and the `.pyc` file are deleted. The one superpowers
  fact that a current doc still pointed to (the `make bump` design) is now in
  [design/overview.md](../design/overview.md), checked against
  `scripts/bump.sh` and the `Makefile`. Git history keeps the rest.

## Consequences

- `make verify` and `make ci` need Node.js 22 or later and the pinned
  golangci-lint (`mise install`); CI installs both with `jdx/mise-action`.
- A new doc under `docs/` fails the gate until `docs/README.md` links it.
- A pull request that touches a listed risk path gets the `risk:high` label.
- A golangci-lint upgrade is a change to `mise.toml` and `mise.lock`; the CI
  Lint job, the nightly job and `make lint` follow it.
- Core's skill sync writes to `.agents/skills/`; it supports a directory and a
  symlink alike.
- Lessons live in 1 design doc, not in 50 files that every agent had to read.
- Still open from ADR 0012: the Makefile `GO_VERSION` variable, the
  hard-coded and `stable` `go-version` inputs in the workflows, `svu` outside
  mise, and development images that take versions as build arguments.
- Not in this change (separate pull requests, core baseline rows): B9, a
  ProperDocs and MaterialX docs site (core ADR-0070) to replace the Astro
  site under `site/` that was never deployed; B11, Go 1.27 and golangci-lint
  2.15.0.

## Alternatives considered

- **Keep `.agents` as a symlink into `docs/`:** rejected. The docs-index check
  would require `docs/README.md` to link each of about 150 skill files, and
  skills are not documentation in the four categories.
- **Keep `test.yml`:** allowed by the baseline as a low-severity finding, but
  this change already edits the workflow, so the rename comes with it.
- **One Verify job that runs `make ci`:** rejected. The 6 jobs already run
  every gate in parallel; core accepts separate jobs that cover every
  `make ci` target, and the nightly job still runs `make ci` itself.
- **Keep the mill lessons as files:** rejected. The mill is archived, and part
  of the lessons are about its plan-chunk process, which no longer runs.

## References

- Shapes: [design/engineering-lessons.md](../design/engineering-lessons.md),
  [design/agent-workflows.md](../design/agent-workflows.md),
  [design/overview.md](../design/overview.md)
- Builds on: [ADR 0012](0012-adopt-mise-tool-pinning-and-verify-gate.md)
- Org: core ADR-0018, ADR-0021, ADR-0025, ADR-0026, ADR-0029, ADR-0043,
  ADR-0044, ADR-0068, ADR-0071 (see [org-adrs.md](../org-adrs.md))
