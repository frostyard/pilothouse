# 0012 — Adopt mise tool pinning and the verify gate

<!--
Filename: NNNN-kebab-case-title.md (next free number).
ADRs are immutable once Accepted. To reverse one, write a new ADR and set the
old one's Status to "Superseded by NNNN".
-->

- **Status:** Accepted
- **Date:** 2026-09-07

## Context

frostyard/core [ADR-0043](https://github.com/frostyard/core/blob/main/docs/adr/0043-pin-repository-tools-in-mise-and-name-the-verify-gate.md)
makes `mise.toml` plus a committed `mise.lock` at the repository root the
standard tool-pinning mechanism for every Go repository in the fleet, with
`go.mod` as the only Go pin (a full `1.x.y`, never a floating `1.x`), no
optional tools, and a named gate triad — `make verify` (non-mutating,
read-only safe), `make check` (developer gate), `make ci` (ADR-0022's
credential-free gate). Core's organization gate checks only that an enabled Go
repository has `mise.toml`, `mise.lock`, and those three targets; it never
reads or compares a version.

Pilothouse does none of this today:

- `golangci-lint` (`v2.11.4`) and `svu` (`v3.4.1`) are pinned only as Makefile
  variables and re-declared as `--build-arg`s in `.docker/Dockerfile`. Nothing
  can install a tool from a Makefile variable.
- The Go toolchain is pinned in at least four places that already disagree:
  `go.mod` declares `go 1.26.3`, the Makefile sets `GO_VERSION ?= 1.26.6`,
  `test.yml` and `nightly-compliance.yml` hard-code `1.26.6`, and
  `release.yml`, `snapshot.yml`, `packaging.yml`, and `image-tier.yml` request
  the moving `stable` alias.
- `make lint` prints `golangci-lint not installed, skipping` and exits `0`
  when the binary is absent — the "gate passed but lint never ran" failure
  class ADR-0043 closes. `make ci` reaches lint the same way.
- There is no non-mutating gate. `make ci` exists (and calls the non-mutating
  `format-check`), but there is no `make verify` or `make check` target, and
  `make fmt` rewrites files. A read-only reviewer has no named command that is
  safe to run.

Pull request [#206](https://github.com/frostyard/pilothouse/pull/206)
proposed a repository-local alternative — a `.go-version` file plus a
`make update-go` drift guard — that addressed only the Go-toolchain slice of
this problem and diverged from the org mechanism core already checks for. It
is closed unmerged in favor of this ADR.

## Decision

Pilothouse adopts core ADR-0043 as written.

- **`mise.toml` plus a committed `mise.lock` at the repository root** pin
  every executable a gate invokes beyond the development image's baseline — at
  minimum `golangci-lint` and `svu`. `mise.lock` is the ADR-0023 checksum
  registry for those tools, and `mise outdated` (or Renovate's mise manager)
  is their update check. `templ` stays a `go.mod` `tool` directive — Go module
  tooling the Go toolchain installs — not a mise pin.
- **`go.mod` is the only Go pin**, written as a full `1.x.y`. mise reads it
  through `idiomatic_version_file_enable_tools = ["go"]`. The Makefile
  `GO_VERSION` variable, every hard-coded `go-version:` input, and every
  `go-version: stable` are removed; workflows install Go through mise or with
  `go-version-file: go.mod`. Raising the pin stays a deliberate `go.mod` edit
  with its own compatibility review.
- **No tool is optional.** `make lint` and every gate that invokes a tool fail
  with the install command (`mise install`) when the tool is absent; the
  "skipping" branch is deleted.
- **The gate triad is named:**
  - `make verify` — credential-free and non-mutating: `go mod tidy -diff`,
    `gofmt -l`, `go vet`, the pinned linter, `govulncheck`, and tests. This is
    what a read-only reviewer runs. `make verify` on a clean checkout leaves
    `git status --porcelain` empty.
  - `make check` — the developer gate; may format (`make fmt` then `verify`).
  - `make ci` — ADR-0022's gate: it calls `verify` and adds coverage, the
    race detector, and the sdjournal-tagged and cross-arch builds.
    `make docker-ci` stays the containerized equivalent.
- **The development image installs from the pinned files.** `.docker/Dockerfile`
  and `.devcontainer/Dockerfile` install mise and run `mise install`; they no
  longer take `GO_VERSION`, `GOLANGCI_LINT_VERSION`, or `SVU_VERSION` build
  args. Pilothouse keeps a standalone image rather than descending from
  `ghcr.io/frostyard/snowcat-worker-base` because it needs libpam and
  libsystemd headers and a packaging toolchain that mise cannot install;
  ADR-0043 allows a repository that needs system packages to extend the base
  with its own image, and the same reasoning covers a standalone image that
  installs the same pinned tools.
- **Bump the Go/lint pair in order, as one change.** A `golangci-lint` release
  built with Go N lands in `mise.toml` before `go.mod` moves to N; that pull
  request's CI is the compatibility proof.
- **Core checks presence, not versions** — only that `mise.toml`, `mise.lock`,
  and the three targets exist.

Implementation is a single follow-up change, noted in
[docs/design/agent-workflows.md](../design/agent-workflows.md). This ADR
records the target contract.

## Consequences

- A tool bump becomes a one-file change with `mise.lock` proving the checksum,
  and CI proves the Go/lint pair before the mill or any executor runs it.
- Read-only review — AI and human — gains a named gate (`make verify`) that
  cannot mutate the checkout. A mutating `verify` is a mechanical conformance
  failure a future check can detect.
- The four-way Go-version disagreement collapses to `go.mod`. `stable` stops
  silently pulling new Go releases into the release and packaging workflows;
  adoption of a new toolchain is again a reviewed diff.
- Every developer and CI job gains a `mise install` step. Contributors must
  install mise, or use the development image, which does it for them.
- Direct `docker build` of `.docker/Dockerfile` no longer needs the three
  `--build-arg`s, and the Make targets stop passing them.
- The adoption change is large but mechanical: `mise.toml`, `mise.lock`, the
  Makefile targets, every workflow's Go setup, both Dockerfiles, and
  `scripts/bump.sh` (`svu` now resolves through mise). It lands once.
- `go.mod`'s `go` directive rises from `1.26.3` to the real toolchain version
  as part of adoption. That is a minimum-supported-version change and is
  called out in the adoption pull request's review.

## Alternatives considered

- **The `.go-version` plus `make update-go` proposal (PR #206):** solved only
  the Go-toolchain pins, added a repo-specific drift guard and a generated
  devcontainer mirror, and diverged from the org mechanism core already checks
  for. Rejected in favor of the shared convention.
- **Keep the Makefile-variable pins:** nothing installs from them, they have
  already diverged from `.docker/Dockerfile` and `go.mod`, and they leave the
  "lint skipped" gate hole open.
- **Descend the development image from `snowcat-worker-base`:** the base cannot
  carry the libpam and libsystemd headers or the `.deb`/`.rpm` toolchain
  pilothouse builds against; a standalone image that installs the same pinned
  tools meets the contract.
- **A Go `tool` directive for `golangci-lint`:** Go-only, and golangci-lint
  discourages that install path because module merging changes lint results.
  `svu` and any future non-Go tool would still need mise.

## References

- Builds on:
  [frostyard/core ADR-0043](https://github.com/frostyard/core/blob/main/docs/adr/0043-pin-repository-tools-in-mise-and-name-the-verify-gate.md)
  (org decision this repo follows — see [org-adrs.md](../org-adrs.md)),
  [frostyard/core ADR-0022](https://github.com/frostyard/core/blob/main/docs/adr/0022-make-ci-gate-and-test-naming-filter.md),
  [frostyard/core ADR-0023](https://github.com/frostyard/core/blob/main/docs/adr/0023-verified-pinned-downloads.md)
- Shapes: [design/agent-workflows.md](../design/agent-workflows.md),
  [`Makefile`](../../Makefile),
  [development Dockerfile](../../.docker/Dockerfile),
  [devcontainer Dockerfile](../../.devcontainer/Dockerfile),
  [GitHub Actions workflows](../../.github/workflows/)
- Replaces the abandoned proposal in pull request
  [#206](https://github.com/frostyard/pilothouse/pull/206)
