# Pull request review rubric

Review the final diff, not only the pull request description. Use the highest
applicable [risk tier](risk-tiers.md) to set the required depth of review.

## Review criteria

- **Scope:** The change is focused, the stated problem is addressed completely,
  and unrelated behavior is unchanged.
- **Correctness:** Success, failure, and edge cases behave as intended. Tests
  exercise changed behavior at the real boundary and use expectations that are
  independent of the implementation.
- **Security:** Inputs, permissions, and side effects remain least-privilege.
  The unprivileged web process does not gain direct privileged access;
  privileged reads and mutations use fixed broker queries and actions. Apply
  the additional requirements in the
  [AI Security Policy](security/SECURITY-AI.md) when AI assisted the change.
- **Maintainability:** The implementation follows existing patterns, avoids
  unnecessary dependencies or mechanisms, and updates relevant user and agent
  documentation.
- **Validation:** The pull request records the checks that ran. `make ci` or
  `make docker-ci` passes, along with any specialized checks required by the
  affected subsystem and risk tier.

## Review decision

Approve only when every applicable criterion is satisfied and the declared
risk tier matches the final diff. Request changes for correctness or security
gaps, missing required evidence, unexplained validation failures, or an
understated risk tier. Clearly distinguish optional suggestions from blocking
findings.

Tier 3 changes require targeted failure-path evidence and review by someone
familiar with the affected area. Tier 4 changes additionally require explicit
maintainer security review, trust-boundary or abuse-case analysis, a rollback
plan, and confirmation that privileged surfaces remain least-privilege.

AI-assisted review is supplemental evidence and never replaces accountable
human review.
