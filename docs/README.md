# Documentation

Docs are split by the question they answer (the shape defined by
[frostyard/core ADR-0025](https://github.com/frostyard/core/blob/main/docs/adr/0025-consolidate-repository-docs-into-docs.md)):

| Directory | Question | Contents |
| --- | --- | --- |
| [adr/](adr/) | **Why** did we choose this? | Repo-local Architecture Decision Records — immutable once accepted; superseded, never edited. Org-wide decisions live in frostyard/core (see [org-adrs.md](org-adrs.md)) |
| [design/](design/) | **How** does it fit together? | Living documents describing the current architecture; [design/overview.md](design/overview.md) is the entry point |
| [specs/](specs/) | **What exactly** is the contract? | Precise, testable interface definitions, changed only alongside implementing code |
| [plans/](plans/) | **When/in what order** do we build? | Roadmaps and phase plans; updated as work lands |

## Index

### Decisions (ADRs)

- [org-adrs.md](org-adrs.md) — the frostyard/core ADRs that bind this repo
- [adr/0001](adr/0001-versioned-broker-wire-surface.md) — version the broker
  wire surface as four ID-dispatched registry routes; no proxy/exec escape
  hatch
- [adr/0002](adr/0002-audited-resource-key-doubles-as-lock-key.md) — one
  canonical resource key per action: the audited resource is the default
  serialization-lock key
- [adr/0003](adr/0003-allowlist-built-detail-surfaces.md) — privileged detail
  surfaces are allowlist-built, never passthrough
- [adr/0004](adr/0004-hand-transcribed-packaging-contract-oracle.md) — the
  packaging contract stays hand-transcribed as an independent oracle
- [adr/0005](adr/0005-tiered-package-validation-and-vm-boot-label.md) —
  tiered package validation (contract → container → booted VM → image host);
  heavy tiers gated on `vm-boot` label presence
- [adr/0006](adr/0006-opt-in-capabilities-zero-io-omission.md) — capabilities
  are a closed vocabulary; unconfigured means zero I/O, unavailable means
  omitted
- [adr/0007](adr/0007-capability-table-contract-tests.md) — docs/capabilities.md
  is a binding table, mirrored in tests and diffed against live code
- [adr/0008](adr/0008-no-config-file-flags-env-precedence.md) — no config
  file: flags plus `PILOTHOUSE_*` env, explicit flag wins
- [adr/0009](adr/0009-fail-closed-non-loopback-bind.md) — fail closed on
  non-loopback binds: TLS, persisted self-signed, or refuse
- [adr/0010](adr/0010-forbidden-packaging-roots-two-unharmonized-layers.md) —
  packages never install under systemd-owned roots; two enforcement layers
  stay unharmonized
- [adr/0011](adr/0011-digest-pinned-test-images-no-default-install-image.md) —
  test images pinned per tier at one site each; `INSTALL_IMAGE` has no
  default
- [adr/0012](adr/0012-adopt-mise-tool-pinning-and-verify-gate.md) — adopt
  frostyard/core ADR-0043: `mise.toml` + `mise.lock` at the repo root, `go.mod`
  as the only Go pin, and the `make verify` / `check` / `ci` gate triad
- [adr/0013](adr/0013-local-problem-records.md) — record panics and 5 fixed
  internal error codes with `frostyard/std/problems`, to the local journal
  only, in release builds only
- [adr/0014](adr/0014-adopt-the-repository-baseline.md) — adopt frostyard/core's
  repository baseline (ADR-0071, rows B1 to B16): `.agents/skills/`,
  `.memory/corrections.jsonl`, the template gate, risk paths, `ci.yml`; the
  mill and superpowers trees are retired

### Design

- [design/overview.md](design/overview.md) — architecture, module inventory,
  broker contract, patterns, and configuration; the entry point for agents and
  contributors (formerly `yeti/OVERVIEW.md`)
- [design/host-image.md](design/host-image.md) — host-image and
  automatic-update reporting (#51/#58/#60): parsers, manager, queries,
  degrade rules
- [design/capability-gating.md](design/capability-gating.md) — capability
  probing, guarded registration, web-side fetch/cache and gating, per-module
  adoption, optional-tooling opt-in (#50/#54/#64)
- [design/incus.md](design/incus.md) — the Incus module's depth phases:
  allowlisted detail, snapshots/force stop, networks/profiles, creation
- [design/packaging-test-fixtures.md](design/packaging-test-fixtures.md) —
  `internal/packagingtest`: the tool gate and `.deb`/`.rpm` fixture builders
- [design/artifact-extraction.md](design/artifact-extraction.md) —
  `packaging/extract` backends, `cmd/verify-packages`, and the CI packaging
  gate
- [design/install-validation.md](design/install-validation.md) — Layer A:
  `packaging/verify-install.sh` container install checks and the `install`
  CI job
- [design/vm-harness.md](design/vm-harness.md) — Layer B: the `test/vm`
  booted-VM harness and the `vm-boot` CI job
- [design/image-tier.md](design/image-tier.md) — the #80 image-tier
  validation on a uCore/bootc host and `image-tier.yml`
- [design/agent-workflows.md](design/agent-workflows.md) — agent
  instruction surfaces, risk tiers and risk paths, knowledge index, automation
  workflows
- [design/engineering-lessons.md](design/engineering-lessons.md) — the rules
  learned from earlier defects and reviews (tests, fixtures, packaging, shell
  harness, docs); code comments link its anchors

### Specs

- [specs/artifact-contract.md](specs/artifact-contract.md) — the exact
  packaging artifact contract: model, finding codes, required destinations,
  dependency lists, `packaging.Verify` semantics, drift guards
- [specs/files.md](specs/files.md) — the Files module contract: root flags,
  descriptor-based `openat2` resolution, list limits, download, `O_TMPFILE`
  upload, streaming broker operations, status mapping
- [specs/logs.md](specs/logs.md) — the Logs module's journal query: filter
  grammar, scan and size limits, record rules, polling
- [specs/storage.md](specs/storage.md) — the Storage module contract:
  snapshot limits, health rules, remote-mount actions and grammar, artifacts
  and modes, target rules, lifecycle, anchors

### Plans

- *(none yet — start from [plans/TEMPLATE.md](plans/TEMPLATE.md))*

### Subsystem docs (uncategorized, indexed in place)

- [features.md](features.md) — the full feature list ("What works") and the
  module architecture; the root README links it
- [install.md](install.md) — install a release package or from source, turn
  on the optional surfaces, remote access and runtime configuration
- [development.md](development.md) — gates and review, build and test, run
  locally, package and booted-VM validation, releases
- [problem-records.md](problem-records.md) — what the binaries record to the
  local journal (ADR 0013): codes, crash files, what is never sent
- [authentication.md](authentication.md) — login, session, authorization,
  audit, PAM policy, deployment rules
- [autoupdate.md](autoupdate.md) — the automatic-update reporting surface:
  response schema, policy normalizers, daemon-side manager, Maintenance UI
- [capabilities.md](capabilities.md) — binding table mapping every broker ID
  to its required host capability
- [modules.md](modules.md) — how to add a management module: contract, file
  layout, action/query rules
- [branding.md](branding.md) — neutral-branding rules, the canonical
  self-description sentence, and the naming-sweep allowlist

### Process and governance

- [quality.md](quality.md) — index of auditable quality signals
- [risk-tiers.md](risk-tiers.md) — change risk tiers for pull requests
- [review-rubric.md](review-rubric.md) — pull request review rubric
- [security/SECURITY-AI.md](security/SECURITY-AI.md) — AI security policy
- [claude-code-review.md](claude-code-review.md) — advisory AI review workflow
- [workflow-action-pinning.md](workflow-action-pinning.md) — SHA-pinned
  actions contract and the workflowcheck tests enforcing it
- [metrics/README.md](metrics/README.md) — public repository metrics
- [prompts/README.md](prompts/README.md) — reusable prompts catalog

### Agent surfaces

Outside `docs/`: `AGENTS.md` (the canonical instructions; `CLAUDE.md`,
`GEMINI.md`, `.cursorrules` and `.github/copilot-instructions.md` link to it),
`.agents/skills/` (skills synced from frostyard/core; `.claude/skills` links
to it), `.memory/corrections.jsonl` (the corrections inbox) and
`.knowledge/README.md` (the knowledge index). See
[design/agent-workflows.md](design/agent-workflows.md).

## Conventions

- **New docs start from their category's `TEMPLATE.md`** (in each directory).
- New repo-local decision → new ADR in [adr/](adr/) with the next number; if
  it reverses an old one, mark the old one `Superseded by NNNN` rather than
  editing it. Org-wide decisions go to frostyard/core instead, with a
  back-link added to [org-adrs.md](org-adrs.md).
- Design docs are updated in place to always reflect reality.
- Specs change only alongside the code that implements them.
- Cross-links between categories are mandatory in both directions.
- Adding a doc means adding it to the index above.
- All docs are written for a reader with a context window: dense, factual,
  structured; exact paths, commands, and constants; name the test or guard
  that enforces each pinned fact.
