# Public metrics

Pilothouse publishes repository-quality and automation signals from public,
auditable sources. This page is the stable index for those signals and defines
how the pull-request acceptance metric is calculated. It contains no secrets,
private telemetry, or host data from Pilothouse installations.

## Signal index

| Signal | Public source | Interpretation |
| --- | --- | --- |
| Pull-request acceptance | [Closed pull requests](https://github.com/frostyard/pilothouse/pulls?q=is%3Apr+is%3Aclosed) | Ratio defined below; review with rejection reasons and feedback. |
| CI health and history | [GitHub Actions](https://github.com/frostyard/pilothouse/actions) | Per-commit results for tests, security scanning, packaging, compliance, and opt-in image validation. |
| Go test coverage | [Codecov](https://app.codecov.io/gh/frostyard/pilothouse) | Line coverage uploaded by the blocking unit-test job; upload itself is non-blocking. |
| Open work and outcomes | [Issues](https://github.com/frostyard/pilothouse/issues) and [pull requests](https://github.com/frostyard/pilothouse/pulls) | Public proposal, review, closure, and merge history, including agent-authored work. |
| Quality-gate definitions | [Quality dashboard](../quality.md) | Canonical map from each quality signal to its workflow and local verification contract. |

These links expose source evidence rather than a copied snapshot that can go
stale. A red or missing signal should be investigated at its source; it is not
silently converted into a passing value here.

## Pull-request acceptance rate

Pilothouse tracks the percentage of pull requests that are accepted:

```text
PR acceptance rate = merged PRs / closed PRs × 100
```

Count a pull request in the reporting period by its `closed_at` timestamp.
The numerator includes those pull requests whose `merged_at` timestamp is
present; the denominator includes all pull requests closed in the same period,
including merged pull requests. Open pull requests are not counted.

Report the metric over a rolling 90-day window. If no pull requests closed
during that window, report the rate as `N/A` rather than zero. Include the
window end date and the merged and closed counts with the percentage so changes
in a small sample are visible and the result is reproducible from the public
pull-request history.

This metric measures whether proposed changes reach the repository. Review it
alongside rejection reasons and review feedback; the rate alone does not
distinguish quality problems from superseded or intentionally withdrawn work.

## Publication and privacy contract

Only repository-level metadata already public through GitHub or Codecov belongs
in this index. Do not publish credentials, workflow secrets, prompts containing
private data, security-sensitive findings, or telemetry from managed hosts.
Automation output remains untrusted until reviewed, and public metrics are
audit evidence rather than approval or a substitute for the required quality
gates.
