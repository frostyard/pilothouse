# Pull request metrics

## Acceptance rate

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
merged and closed counts with the percentage so changes in a small sample are
visible.

This metric measures whether proposed changes reach the repository. Review it
alongside rejection reasons and review feedback; the rate alone does not
distinguish quality problems from superseded or intentionally withdrawn work.
