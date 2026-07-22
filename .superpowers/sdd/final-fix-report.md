# Final Bump Workflow Fix Report

## Files

- `scripts/bump.sh`: authoritative direct-tag parity preflight, strict version
  validation, and explicit remote-tag-conflict recovery message.
- `scripts/bump_test.sh`: isolated local-only and object-ID-mismatched tag tests,
  temporary-repository Docker absence test, and remote-conflict recovery test.
- `docs/superpowers/specs/2026-07-21-bump-workflow-design.md`: final secure
  architecture and verification contract.
- `docs/superpowers/plans/2026-07-21-bump-workflow.md`: final implementation
  plan without stale direct-workspace Docker recipes.

## Commits

- `2eb01e4 fix: harden bump release preflight`

## Finding Resolutions

1. Preflight now fetches before comparing sorted direct local tag records with
   direct `git ls-remote --tags origin` records. Peeled `^{}` records are
   excluded; local-only and object-ID-mismatched semver tag regressions pass.
2. The missing-Docker assertion now enters a `new_repo missing-docker`
   temporary repository before executing preflight.
3. `validate_version` now relies only on strict
   `^v[0-9]+\.[0-9]+\.[0-9]+$` validation; the redundant case arm is removed.
4. A non-empty remote tag resolving elsewhere now reports `remote tag conflict`
   and that the local tag was preserved; the harness covers this race.
5. The design and plan document the Git-less clean-clone verification input,
   sanitized read-only bare mirror version input, no host Git config in Docker,
   authoritative tag parity, and exact version-output verification.
6. The bare mirror was executed successfully and emitted exactly one version
   line: `v0.5.1`.

## Commands And Results

- `sh -n scripts/bump.sh && bash -n scripts/bump_test.sh && make test-bump`:
  PASS, 25 isolated harness assertions.
- `make docker-bump-verify`: PASS; generation, both builds, tests, formatting,
  and golangci-lint completed with `0 issues.`
- `make docker-lint`: PASS; `0 issues.`
- `make --silent docker-next-version`: PASS; stdout was exactly one line,
  `v0.5.1`, validated against `^v[0-9]+\.[0-9]+\.[0-9]+$`.
- `git tag --points-at HEAD`: no output after the implementation commit; no
  release command was run and no tag was created or pushed by this fix pass.
- `git diff --check`: PASS before committing the implementation.

## Self-Review

The tag comparison uses direct object IDs intentionally: annotated tag object
identity is part of authoritative ref parity, while peeled lines are excluded.
Fetch rejection caused by a tag mismatch is deferred until after direct remote
comparison so the user receives the tag-parity diagnostic. An unavailable
origin retains the existing synchronization diagnostic.

## Concerns

`git clone --mirror` reports existing non-remote branch refs while removing
`origin`; this is informational. The mirror has no origin configuration and is
mounted read-only, and `svu next` completed successfully.
