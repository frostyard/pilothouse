# 0012 — Use one canonical Go toolchain version

- **Status:** Accepted
- **Date:** 2026-09-07

## Context

Pilothouse selects its Go toolchain in the Makefile, the development and
devcontainer Dockerfiles, and several GitHub Actions workflows. Some workflows
carry an exact patch release while release-oriented workflows request
`stable`. Updating the toolchain therefore requires a manual search across
unrelated files, and a missed copy can make local development, validation, and
release builds use different compilers. This happened during the Go 1.26.6
security update: the intended patch release had to be changed in nine active
locations.

The `go` directive in `go.mod` is not the same decision. It declares the
minimum Go version required by the module and controls language and module
semantics. Security patch selection changes more frequently and must not raise
the module's minimum version as an incidental side effect.

GitHub's `actions/setup-go` can read a `.go-version` file directly. Make can
read the same file and pass the value to Docker builds. Dockerfile `FROM`
arguments and the devcontainer configuration cannot all read an arbitrary
repository file at image-resolution time, so at least one consumer may need a
checked mirror rather than direct file consumption.

## Decision

The root `.go-version` file is the sole authority for the exact Go toolchain
patch release used to develop, validate, package, and release Pilothouse. It
contains one stable `major.minor.patch` version with no prefix or range.

Consumers use that authority as follows:

- Every GitHub Actions job that installs Go uses `go-version-file:
  .go-version`. Moving release, snapshot, packaging, and image-tier workflows
  away from `stable` is intentional: toolchain updates become reviewed
  repository changes instead of ambient changes to a moving alias.
- The Makefile reads `GO_VERSION` from `.go-version`. Development Docker
  targets pass it to `.docker/Dockerfile`; that Dockerfile has no independent
  default and fails when a caller omits the build argument.
- The devcontainer base-image version is a generated mirror because the
  Dockerfile `FROM` instruction cannot derive an argument from `.go-version`.
  It is never edited by hand.
- `go.mod` remains the module compatibility authority. The update workflow
  does not modify its `go` directive or add a `toolchain` directive.

`make update-go VERSION=<major.minor.patch>` is the supported update interface.
It validates the exact stable-version syntax, updates `.go-version` and every
unavoidable generated mirror, then runs a deterministic drift check. The drift
check also rejects new active `go-version:` values, `stable` aliases, and
literal Go image versions outside the declared mirror set. Historical records
under `docs/superpowers/` are not active consumers and are excluded.

The drift check runs in `make ci`, so a hand-edited mirror, a newly introduced
pin, or an incomplete update fails locally and in pull-request CI. A toolchain
update pull request still runs the normal validation and security scan; the
update target does not claim that rewriting pins proves the new compiler works.

## Consequences

- A Go patch update has one command, one authoritative value, and a reviewable
  diff. Local Docker builds, CI, packaging, and release automation converge on
  the same compiler.
- Release workflows stop following new Go releases without a repository
  change. That delays adoption until a maintainer updates `.go-version`, but it
  also prevents an unreviewed toolchain change during a release.
- Direct builds of `.docker/Dockerfile` must supply `--build-arg GO_VERSION=…`.
  The supported Make targets do this automatically.
- The devcontainer keeps one duplicated version string. The update command and
  CI drift check make that duplication mechanical and fail closed, but both
  must be maintained if the devcontainer layout changes.
- Raising the module's minimum supported Go version remains a separate,
  deliberate change to `go.mod` with its own compatibility review.
- The Go 1.26.6 security update landed separately before this decision. Future
  urgent patch updates do not need to wait for canonicalization implementation.

## Alternatives considered

- **Use `go.mod` as the version file:** this conflates the module's minimum
  compatible language/toolchain semantics with the exact patched compiler used
  by this repository's automation.
- **Use the Makefile as the authority:** GitHub Actions cannot consume a Make
  variable directly before Go is installed, which would require ad hoc shell
  parsing in every workflow.
- **Keep exact versions copied everywhere:** a search-and-replace update has no
  complete consumer list and already allowed the active pins to diverge.
- **Use `stable` everywhere:** the selected compiler can change without a
  repository diff, making builds less reproducible and security updates harder
  to audit.
- **Require every consumer to read `.go-version` directly:** Docker image
  resolution cannot consistently do so, especially for the devcontainer. A
  narrow generated mirror with a drift guard is simpler and explicit.

## References

- Shapes: [agent workflow tooling](../design/agent-workflows.md)
- Current consumers: [`Makefile`](../../Makefile),
  [development Dockerfile](../../.docker/Dockerfile),
  [devcontainer Dockerfile](../../.devcontainer/Dockerfile), and
  [GitHub Actions workflows](../../.github/workflows/)
- External contracts:
  [`actions/setup-go` version-file support](https://github.com/actions/setup-go/blob/main/docs/advanced-usage.md#using-the-go-version-file-input),
  [Go module `go` directive](https://go.dev/ref/mod#go-mod-file-go)
