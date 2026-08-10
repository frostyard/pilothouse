# Claude code review workflow

`.github/workflows/claude-code-review.yml` provides an advisory AI review for
non-draft pull requests whose branch is in this repository. It runs when such a
pull request is opened, reopened, synchronized, or marked ready for review.
Fork pull requests are deliberately skipped because the `pull_request` event
does not expose repository secrets to forks, and switching to
`pull_request_target` would put a secret-bearing job in the trust path of an
untrusted diff.

## Repository setup

A maintainer must configure `ANTHROPIC_API_KEY` as a GitHub Actions repository
secret. The workflow passes it only to the commit-pinned official
`anthropics/claude-code-action`. If the secret is absent or invalid, the review
job fails without changing repository or host state.

The workflow has only `contents: read` and `pull-requests: write`. Checkout does
not persist credentials. Claude's tools are limited to reading pull-request
metadata and diffs and creating review comments; it cannot change contents, run
repository commands, merge, deploy, publish, or access Pilothouse hosts. A
per-pull-request concurrency group cancels stale review runs after a new push.

## Trust and review model

Pull-request text and files are untrusted input. The prompt explicitly tells
Claude not to follow instructions from that data. This is defense in depth, not
a claim that model output is trusted: comments are advisory and require human
verification under `docs/review-rubric.md`. Existing deterministic gates,
security-focused review, and maintainer approval remain authoritative.

Rotate or remove `ANTHROPIC_API_KEY` to disable API access immediately. To
disable the integration while retaining its history, disable the workflow in
GitHub Actions or revert the workflow commit. No broker protocol, host
privilege, authentication rule, package, release, or deployment configuration
is affected.
