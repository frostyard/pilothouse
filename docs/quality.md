# Quality dashboard

This page is the repository's index of auditable quality signals. The current
status and run history for each automated signal are available in the
repository's **Actions** tab.

| Signal | Source | What it reports |
| --- | --- | --- |
| Pull request quality | [`test.yml`](../.github/workflows/test.yml) | Lint, vulnerability scanning, unit-test coverage, race detection, module tidiness, formatting, vetting, and builds |
| Repository policy | [`repository.yaml`](../policies/repository.yaml) | Versioned structural requirements for governance, review, workflow, and policy-enforcement assets |
| Nightly compliance | [`nightly-compliance.yml`](../.github/workflows/nightly-compliance.yml) | A scheduled run of the complete credential-free `make ci` contract plus an automation-secret drift check |
| Package quality | [`packaging.yml`](../.github/workflows/packaging.yml) | Package contract checks, installation checks, and opt-in booted-VM validation |
| Image quality | [`image-tier.yml`](../.github/workflows/image-tier.yml) | Opt-in uCore update and rollback validation under QEMU/KVM |
| Advisory AI review | [`claude-code-review.yml`](../.github/workflows/claude-code-review.yml) | Claude review findings for eligible same-repository pull requests; comments require human verification |

The pull request checks are the primary merge signal. Code coverage is
published from the unit-test job to Codecov, but upload failures are
non-blocking; the unit tests themselves remain blocking. The workflow disables
token permissions by default, grants every job read-only contents access, and
adds OIDC only to the unit-test job for the Codecov upload. Its vulnerability
scanner is pinned to a reviewed version. System telemetry has direct regression
coverage for zero-capacity percentage calculations, lower and upper saturation,
wrapped CPU counters, and rendering of the composed dashboard resource summary.

## Local verification

Run `make ci` before pushing. It checks module tidiness, vetting, formatting,
lint, known Go vulnerabilities, tests (including the repository policy), race
detection, and both binaries in the same order as the credential-free CI
contract. Use `make docker-ci` when the host lacks the required native
dependencies.

Packaging and image validation require credentials, artifacts, containers, or
KVM and therefore remain separate from the local CI mirror. See the workflow
files above for their trigger and environment requirements.

Claude review requires the `ANTHROPIC_API_KEY` repository secret and skips fork
pull requests. It is advisory evidence, not approval or a replacement for
deterministic checks and human review. See the [workflow trust and setup
notes](claude-code-review.md).
