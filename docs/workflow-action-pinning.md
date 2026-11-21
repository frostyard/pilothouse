# Workflow action pinning

Every external `uses:` reference under `.github/workflows/` (and
`.github/actions/`, if the repository adds it) is an executable supply-chain
dependency and must have an immutable identity
([ADR-0021](https://github.com/frostyard/core/blob/main/docs/adr/0021-sha-pinned-actions-and-least-privilege-ci.md)):

- GitHub actions and reusable workflows use a full 40-character commit SHA.
- Container actions use a `sha256` digest.
- Repository-local actions beginning with `./` are already bound to the checked
  out repository and are exempt.

Keep the reviewed release in a trailing comment so updates remain readable,
for example:

```yaml
uses: actions/checkout@0123456789abcdef0123456789abcdef01234567 # v7.0.1
```

(The SHA above is a placeholder; the live pins are in `.github/workflows/`.)

Resolve and review a new upstream commit before changing a pin; never replace a
pin with a tag, branch, short SHA, expression, or container tag for convenience.
Run `make ci` (or `make docker-ci`) after any workflow action update.

## What the tests check

`internal/workflowcheck/actions_test.go` parses every `.yml` and `.yaml` file
below `.github/workflows/` and `.github/actions/` (a missing directory is
skipped) and checks every `uses:` value, repository-wide:

- **Immutable reference (ADR-0021):** a full commit SHA, a `docker://` image
  digest or a `./` local path; a tag, branch, short SHA or expression fails.
- **Version comment
  ([ADR-0090](https://github.com/frostyard/core/blob/main/docs/adr/0090-scope-action-pins-and-check-them-repository-wide.md)):**
  each `OWNER/REPO[/PATH]@SHA` carries `# vX.Y.Z`, or `# vX.Y` when the
  upstream's release tags have 2 parts. A major-only `# v7` is not a version
  comment and fails. A frostyard/core reusable workflow
  (`frostyard/core/.github/workflows/FILE@SHA`) is exempt, because core has no
  release tags: its comment names the core commit on `main`, for example
  `# core#246 (frostyard/core main, 2026-11-14; core has no release tags)`, and
  the test asserts only that a comment is there.
- **1 SHA for each action:** each `OWNER/REPO[/PATH]`, core workflows included,
  has the same SHA in every file. A Dependabot bump moves every copy at once;
  a hand edit that moves only 1 job fails and names each SHA with its file and
  line.
- The test fails when it finds no workflow file, no `uses:` key, or no
  `OWNER/REPO@SHA` pin, so it cannot pass on an empty walk. `TestPinCheckSelfTest`
  runs the same check on synthetic fixtures (placeholder SHAs) for each rule.

Job tests (`internal/workflowcheck/claude_review_test.go`,
`packaging/workflow_*_test.go`) check an action by its name and the 40-hex form
of its ref, and never write a live SHA
([ADR-0088](https://github.com/frostyard/core/blob/main/docs/adr/0088-read-the-action-pin-in-workflow-tests-never-hard-code-it.md)),
so a Dependabot bump that keeps these rules passes without a test edit. The
`claude-code-review.yml` test asserts exactly 1 `anthropics/claude-code-action`
step. Container image digests in a matrix, such as `wantInstallMatrix` in
`packaging/workflow_install_job_test.go`, are not action pins and stay literal.

## Reviewing a bump

The tests do not decide whether a new commit is safe. A pull request that
changes `.github/workflows/**` is level L2 and `risk:high`, so it goes to the
owner's review. Before the merge, read the upstream changes between the old and
the new SHA, above all for `anthropics/claude-code-action`, which runs with the
`ANTHROPIC_API_KEY` secret and a `pull-requests: write` token.

To check each comment against its upstream tag (and the core pin against core's
`main`), run the online script of ADR-0090 from the repository root, with an
authenticated `gh`; it is read-only and not part of `make ci`, which is offline.
