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
- *(no repo-local ADRs yet — start from [adr/TEMPLATE.md](adr/TEMPLATE.md))*

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
- [design/agent-workflows.md](design/agent-workflows.md) — mill
  configuration, risk tiers, knowledge index, automation workflows

### Specs

- [specs/artifact-contract.md](specs/artifact-contract.md) — the exact
  packaging artifact contract: model, finding codes, required destinations,
  dependency lists, `packaging.Verify` semantics, drift guards

### Plans

- *(none yet — start from [plans/TEMPLATE.md](plans/TEMPLATE.md))*

### Subsystem docs (uncategorized, indexed in place)

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
- [ai-fix-workflow.md](ai-fix-workflow.md) — the AI fix requested workflow
- [claude-code-review.md](claude-code-review.md) — advisory AI review workflow
- [workflow-action-pinning.md](workflow-action-pinning.md) — SHA-pinned
  actions contract and the workflowcheck tests enforcing it
- [metrics/README.md](metrics/README.md) — public repository metrics
- [prompts/README.md](prompts/README.md) — reusable prompts catalog

### Agent surfaces

- [agents/skills/](agents/skills/) — durable lessons harvested from mill runs
  plus org-synced skills (`.agents` and `skills` symlink here)

### Superpowers (historical)

Design-doc/plan pairs from the July 2026 superpowers effort. These are
historical records of what was planned and built at the time — they are not
the new [specs/](specs/) and [plans/](plans/) categories, and they retain
pre-migration paths (e.g. `yeti/OVERVIEW.md`, now `design/overview.md`).

- [superpowers/specs/](superpowers/specs/) — per-feature design specs
- [superpowers/plans/](superpowers/plans/) — the matching phased plans

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
