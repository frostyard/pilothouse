# Reproducible Bump Workflow Design

## Objective

Make plain `make bump` work end-to-end on a clean, synchronized `main` checkout when the developer has only Docker and authenticated host Git available. The workflow must not require native Go build dependencies, `svu`, PAM or systemd headers, or Git credentials inside a container.

## Current Problems

The current target runs native build and test commands before tagging. On hosts without `libsystemd-dev`, the `sdjournal` build fails. The development image contains the required C headers but does not contain `svu`, and running the entire target in that image requires passing Git identity, SSH credentials, user records, and writable Go tool paths into the container. The current lint target also silently skips lint when `golangci-lint` is unavailable.

## Architecture

The host Make process will orchestrate the release while Docker supplies all build and versioning tools.

- `.docker/Dockerfile` will install a pinned `svu` version alongside the pinned golangci-lint version.
- `bump-preflight` will run on the host and validate repository state before expensive checks.
- `bump-verify` will run generation, build, tests, formatting verification, and lint inside one development container.
- `docker-next-version` will run pinned `svu next` in the development image and return only the proposed version.
- `bump` will compose those targets, validate the proposed version, and use host Git to create and push the annotated tag.

Docker will receive the repository as a bind mount for reads, generated ignored files, and build output. It will not receive SSH keys, Git identity, host passwd/group files, or responsibility for creating or pushing tags.

## Workflow

1. Confirm required host commands `docker` and `git` are available.
2. Require the current branch to be exactly `main` and the tracked worktree to be clean.
3. Fetch `origin main` and all tags. Fail clearly if the remote is missing or unreachable.
4. Require local `HEAD` to equal `origin/main`, rejecting ahead, behind, and diverged states.
5. Build the development image and run one containerized verification pass:
   - generate templ output;
   - build both binaries, including the `sdjournal` daemon;
   - run the complete Go test suite;
   - verify non-generated Go source is already gofmt-clean without rewriting it;
   - run golangci-lint and fail if it is unavailable or reports findings.
6. Run `svu next` in the development image.
7. Require the result to match `vMAJOR.MINOR.PATCH` and reject a version that already exists locally or on `origin`.
8. Create an annotated tag on the host with message `Version <version>` and push only that tag to `origin`.
9. If the push reports failure, query the remote tag directly:
   - if the remote tag resolves to the intended commit, report that publication succeeded despite the transport error;
   - if the remote tag is confirmed absent, delete only the local tag created by this invocation and return a clear retryable error;
   - if remote state cannot be determined, preserve the local tag and report the indeterminate state for manual reconciliation.
   Never alter an existing or remote tag during rollback.

The preflight and all verification must finish before tag creation. A failure before tagging creates no local or remote version tag; preflight fetches may update normal remote-tracking references.

## Make Targets

The public interface remains `make bump`. Supporting targets may be public Make targets for direct diagnosis, but they must have narrow responsibilities and avoid recursive host/container ambiguity.

- `bump-preflight`: host Git validation and synchronization.
- `bump-verify`: release checks intended to run inside the development image.
- `docker-bump-verify`: image build plus one invocation of `bump-verify`.
- `docker-next-version`: image build plus containerized `svu next`.
- `bump`: orchestration, version validation, host tag creation, and push.

The implementation may use a small script for Git-heavy preflight and tag logic if that makes error handling and tests clearer than multiline Make recipes. The Makefile remains the stable user entry point.

## Tooling And Versions

`SVU_VERSION` will be pinned as a Make build argument and Dockerfile argument, following `GOLANGCI_LINT_VERSION`. The binary must be installed in the image's normal executable `PATH`. Release verification must call golangci-lint directly rather than use the permissive native `lint` target that can skip execution.

No tool is installed dynamically during `make bump`; image construction is the only installation step. Docker layer caching keeps subsequent bumps fast.

## Error Handling

Each preflight failure will identify the violated condition and the corrective action. In particular:

- wrong branch: instruct the user to switch to `main`;
- dirty tree: instruct the user to commit or stash changes;
- fetch failure: report that `origin` could not be synchronized;
- branch mismatch: report whether local `main` is ahead, behind, or diverged;
- verification failure: stop before calculating or creating a version tag;
- malformed/existing version: stop before tag creation;
- push failure: inspect the remote before rollback, remove the new local tag only when remote absence is confirmed, and preserve it when publication state is indeterminate.

Commands must preserve the underlying failure status. Success messages must only appear after the corresponding operation succeeds.

## Testing

A shell test harness will exercise release orchestration against temporary repositories and fake external commands. It must not mutate the developer checkout or contact the real origin.

Coverage will include:

- missing required commands;
- wrong branch and dirty worktree;
- missing or unreachable origin;
- synchronized, ahead, behind, and diverged `main`;
- failed container verification;
- malformed and already-existing proposed versions;
- successful annotated tag creation and push to a temporary bare origin;
- push failure with confirmed-absence rollback, accepted-but-reported-failed recovery, and indeterminate-state preservation;
- preservation of pre-existing tags;
- Docker image smoke checks showing both `svu` and `golangci-lint` on `PATH`.

Final verification will run the release harness plus `make docker-build`, `make docker-test`, and `make docker-lint`. The bump tests will use a non-publishing mode or isolated fake origin so verification can never create a real release.

## Documentation

The README development section will document that `make bump` requires Docker, authenticated host Git, a clean synchronized `main`, and no native Go or `svu` installation. It will state that the target verifies, creates, and immediately pushes the next semantic-version tag.

## Non-Goals

- Changing semantic-version calculation or `.svu.yml` policy.
- Adding interactive confirmation.
- Moving release publication from tags to a manual GitHub workflow.
- Supporting releases from feature branches or detached HEADs.
- Passing host credentials into the development image.
