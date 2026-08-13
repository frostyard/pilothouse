# AI fix requested workflow

The [AI fix requested workflow](../.github/workflows/ai-fix-requested.yml)
assigns an open issue to the GitHub Copilot coding agent when a maintainer adds
the `ai-fix-requested` label. It revalidates the issue's open state and label,
rejects pull requests, and does nothing when Copilot is already assigned.

The workflow checks out no code, grants the default `GITHUB_TOKEN` no
permissions, and never interpolates issue text into a command or prompt. A
gate checks whether `COPILOT_ASSIGNMENT_TOKEN` is configured before starting
the assignment job. When it is absent, the gate emits a notice and the
assignment job is reported as skipped rather than failed.

The [review feedback workflow](../.github/workflows/copilot-review-apply.yml)
uses the same token-presence gate before handing actionable reviews to
Copilot. A missing token likewise produces a notice and a skipped handoff job.

## Repository setup

GitHub's agent-assignment API requires a user-to-server token. The workflow's
default `GITHUB_TOKEN` is an installation token and cannot start the agent. A
repository administrator must:

1. Create a fine-grained personal access token owned by an account with access
   to Copilot coding agent and this repository.
2. Grant the token metadata read access and read/write access to Actions,
   Contents, Issues, and Pull requests, as required by GitHub's
   agent-assignment API.
3. Store it as an Actions repository secret named
   `COPILOT_ASSIGNMENT_TOKEN`.

Give the token access only to this repository, set an expiration, and rotate it
before it expires. See GitHub's
[Copilot cloud agent API documentation](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-via-the-api)
for the current authentication and permission requirements.

The scheduled [nightly compliance workflow](../.github/workflows/nightly-compliance.yml)
fails its separate automation-secret drift job when this token is absent. This
keeps label-triggered and submitted-review runs neutral while making
configuration drift visible once per day.

## Manual replay

If assignment fails after the label is applied, open **Actions**, select
**AI fix requested**, choose **Run workflow**, and enter the issue number. The
manual path applies the same issue-number, type, open-state, label, and
duplicate-assignment checks as the label event.
