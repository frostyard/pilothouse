# AGENTS

All AI-assisted work must follow `docs/security/SECURITY-AI.md`.

This project is derived from `housecat-inc/scratch` and follows its core stack:

- Idiomatic Go
- HTML via templ
- HTMX for focused interactivity
- Vanilla CSS and JavaScript sparingly

Keep management features isolated in `internal/modules/<name>`. Web modules may collect unprivileged read-only data locally. Privileged reads must use a fixed broker query, and mutations must use a fixed broker action. Register privileged implementations only in `cmd/pilothoused`; never add arbitrary command execution, filesystem access, or generic socket proxying to the broker protocol.

Run `make generate` after editing `*.templ`. Never hand-edit generated `*_templ.go` files.

When composing templ components with text, put the component invocation in its own template node. Do not embed calls such as `@web.Icon("chevron")` in a text node (`View all @web.Icon("chevron")` renders literally). For example:

```templ
<a class="card-link" href="/attention">
    View all
    @web.Icon("chevron")
</a>
```

For any new or changed templ component invocation, add or update a rendering test that asserts the rendered HTML contains the component output and does not contain the literal `@web.` call syntax.

Run `make build`, `make test`, `make fmt`, and `make lint` before handing off changes.

Process-level web E2E tests live in `test/e2e/`. They build and start the real `pilothouse` binary on an ephemeral local port (loopback, plus one wildcard bind exercising the self-signed TLS path), require no broker, and run as part of `make test`.

The web binary's listen address, TLS material, and plaintext acknowledgment resolve as explicit flag → `PILOTHOUSE_*` environment variable → default (`cmd/pilothouse/listen.go`). The loopback HTTP default must not change, and the non-loopback guardrail must stay fail-closed: a non-loopback bind without operator TLS material serves a self-signed certificate from `internal/tlscert` (persisted in `$STATE_DIRECTORY` or the XDG state home), refuses to start when none can be prepared, and serves plaintext only under the explicit `--allow-insecure-http` acknowledgment.

If native Go, PAM, or systemd build dependencies are unavailable, use the matching containerized targets: `make docker-build`, `make docker-test`, `make docker-fmt`, and `make docker-lint`. Use `make docker-generate` after templ changes. These targets build and reuse the repository's development image; do not assemble ad hoc build containers when they are available.

`make verify-packages` reports the packaging contract's findings for built `.deb` and `.rpm` artifacts in `dist/`; it sits outside `make ci` and `make docker-ci` on purpose, and it fails by design when `dist/` is empty, which is the normal state on a development host and in the development image. `make package` is the local producer that fills `dist/` — it runs `goreleaser release --snapshot --clean`, requires the goreleaser Pro distribution at major version 2, which is not installed on this host or in the development image, and it too sits outside `make ci` and `make docker-ci`. `make verify-package-install` is the install-side sibling: it runs `packaging/verify-install.sh` inside the container image named by `INSTALL_IMAGE` (no default; an unset value fails with a message naming the two digest-pinned images) against `ARTIFACT_DIR` (default `dist`), and it sits outside `make ci` and `make docker-ci` too, because it needs built artifacts, Docker and network access. `.github/workflows/packaging.yml` now runs that same target in CI, installing the built artifacts on pinned Debian and Fedora containers after the contract check passes; that gate stays outside `make ci` and `make docker-ci`. That workflow also carries a third tier beyond the contract check and the container install matrix: the `vm-boot` job, which boots a stock Debian and a stock Fedora cloud image under QEMU/KVM with `bash test/vm/vm-boot-test.sh` and validates the installed package on a booted host — activation, the systemd-created directories, a live broker socket, real PAM authentication, journald read-back and reboot posture. It runs on every push to `main` and on a pull request only while the `vm-boot` label is on it, it is **not** a required check, and — like the rest of `packaging.yml` — it cannot run under `make ci` or `make docker-ci`, here because it needs KVM, network access and the artifact the `packages` job builds, so there is no local `make` target for it. `make help` prints every target carrying a `##` description.

`go run ./test/image/releaserpm --workspace ABSOLUTE_PATH` is the test-only
producer for #80's released-RPM fixture. It resolves GitHub's latest stable
semantic-version release once, accepts exactly the tag-correlated
`frostyard-pilothouse-<version>-1.x86_64.rpm`, verifies its recorded size and
SHA-256 while downloading, and creates a fresh
`fixture-release-rpm/fixture.json` plus the RPM inside the caller-owned
workspace. It never publishes, uploads, installs or retains anything on its
own; `test/image/ucore-image-test.sh`, the landed lifecycle owner, owns
workspace cleanup. The workspace must be private to one invocation and not
concurrently mutated. Do not point it at a reused fixture directory or
substitute a branch artifact.

`test/image/compose-ucore.sh --workspace ABSOLUTE_PATH --bin-dir
ABSOLUTE_PATH --run-id LOWERCASE_ID`
consumes that released-RPM fixture from the same private, non-concurrently
mutated workspace. It resolves `ghcr.io/ublue-os/ucore:latest` once, verifies
both the index and its sole linux/amd64 member with the reviewed key in
`test/image/ucore/cosign.pub`, then uses only the member digest. It rechecks
the RPM size and SHA-256 before building distinct `baseline` and `update`
derivatives with repositories and build networking disabled. It also requires
the checked-out `pilothouse` and `pilothoused` executables from `--bin-dir`,
digest-rechecks them in the private context and resulting image, and records
their digests. The released RPM remains the packaging substrate; overlaying
the checked-out executables makes the pull-request gate exercise its own code
without requiring a branch package. The derivative enables the packaged
broker and web units so each bootc transition starts them; this is fixture
composition, while the guest validator still does not repeat #67's activation
assertions. The `v0.6.0` RPM carries the Debian PAM
service file, so the composer applies the reviewed Fedora policy only for
immutable release ID `358276825`, asset ID `486354638`, tag `v0.6.0`, and
matching RPM basename; it verifies both the installed bad-file digest and
replacement digest, and applies no override to any other release identity.
The consumer enforces that exact identity if and only if the override is
selected. Its private build context contains only verified copies of that RPM,
reviewed policy and the two checked-out executables. Podman graph, image, run and both
temporary-storage roots are below `fixture-ucore-images`. Explicit
general/storage configuration selectors disable normal system and per-user
Podman configuration: general configuration
uses the explicit empty `/dev/null` file, while a generated private
`storage.conf` repeats the graph, image and run paths plus driver; the libpod
and download temporary paths are pinned separately by `--tmpdir` and `TMPDIR`.
Storage environment and late general-configuration overrides are cleared,
file events are disabled, and remote Podman selection is disabled. Never
substitute the host store, push either image, or remove that directory
independently of the enclosing workspace.
For a VM run, invoke composition as root. Its manifest records the producer
UID, and the VM consumer rejects a store not produced by UID 0; compose,
consume, exact-store reset and workspace removal must stay in that one
rootful ownership domain.
The composer retains its store and does not claim to clean up detached tool
helpers or bound caller-owned stdout/stderr storage.

`sudo test/image/ucore-vm-test.sh --workspace ABSOLUTE_PATH` consumes that
composed fixture. It checksum-verifies the official Fedora CoreOS stable QEMU
image selected by the stream metadata, boots a private qcow2 overlay with an
ephemeral Ignition key for the unprivileged `core` account, and attaches one
read-only ext4 disk containing one shared local OCI layout with both fixtures.
It requires both loaded Podman image IDs to equal the fixture manifest exactly
and reaches uCore with
`bootc switch --transport containers-storage`. This is the path uCore's own VM
harness recommends: `bootc install to-disk` on FCOS/uCore omits the
`LABEL=boot` partition that its GRUB configuration searches for. Do not
reinstate that known-unbootable installer path or weaken either FCOS checksum.
It boots under QEMU/KVM with OVMF and checks the image-host deltas that #67
does not:
enforcing SELinux without unexpected or Pilothouse-related AVC denials (the
only classified exceptions are uCore's explicitly permissive
`coreos_boot_mount_generator_t` boot-domain records and at most two exact
enforcing `chcon`/`mac_admin` capability-probe records from `bootc status`), an exact
broker capability set derived from independent guest probes, a usable bootc
host-image report, staged-to-booted update continuity, rollback-slot
continuity, and the reverse transition after `bootc rollback`. The consumer
uses Skopeo to export both refs into one OCI layout with shared compressed
blobs, matches both config digests to the manifest, empties the isolated
private host image store and proves it empty before downloading FCOS. Broker
readiness is checked with the fixed non-interactive sudo channel because
`core` cannot traverse the correctly protected mode-0750 runtime directory.
The compressed FCOS input is capped;
its declared uncompressed size may not exceed 4 GiB, and the verified copy is
removed after decompression. After the private store is emptied, the capped OCI
layout is copied into a sparse no-journal ext4 carrier and the standalone
layout is recursively removed from its one fixed transient path before the
FCOS download. The guest mounts that disk read-only and Skopeo streams both OCI
refs into containers-storage, avoiding compressed Docker-archive loading's
full uncompressed temporary tar. The source layout and sparse carrier
temporarily coexist only while the carrier is populated. This ordering is
load-bearing on the standard runner, and the outer lifecycle refuses to start
with less than 10 GiB free. The update is switched from the already-verified
guest-local containers-storage; never add a registry or external push. Privileged guest
operations go only through `sudo -n` from `core`. The runner owns and waits for
QEMU, but it does not recursively delete the VM directory or perform the
owner's final image-store reset; its only recursive deletion is the fixed
standalone OCI layout after carrier construction.

`test/image/ucore-image-test.sh --run-id LOWERCASE_ID` is the root-only owner
of that complete lifecycle. It creates one unpredictable mode-0700 workspace
below `/var/tmp`, runs acquisition, composition and the VM consumer synchronously
with both wall-clock and 4 MiB retained-log limits, resets the exact private
Podman store synchronously, and only then recursively removes the workspace.
The output cap is applied by a streaming collector below the same timeout-owned
process group and never as a process file-size limit, so it cannot constrain
phase artifacts or outlive the deadline. Each bounded phase records the group
PID, waits for group readiness, forwards INT/TERM to the group, and rejects and
terminates descendants that survive their direct command before cleanup. The
collector ignores soft INT/TERM to drain diagnostics after producer shutdown;
the bounded KILL escalation still terminates it if it cannot drain.
Once a signal handler begins, reentrant INT/TERM is deliberately ignored so
bounded exact-store reset and workspace removal cannot be interrupted. Its
EXIT/INT/TERM path performs the same reset-then-remove sequence.
If the reset group survives bounded TERM/KILL escalation, removal is refused
and the workspace is preserved rather than deleted beneath a live process.
`.github/workflows/image-tier.yml` invokes it on `ubuntu-26.04`, whose Podman 5
provides the required `--imagestore` option. The job runs on every push to
`main` and on a pull request only while the `vm-boot` label is present. It is
not a required check, uses the last released RPM rather than a branch package,
overlays locally built checked-out executables, and never uploads or publishes
a package, image, disk or log artifact.

Run releases with `make bump` from a clean, synchronized `main`. The target
uses the development image for build dependencies, lint, and `svu`, then uses
authenticated host Git to create and push the tag. Do not run the full bump
target inside an ad hoc container or pass Git credentials into the image.
Preflight treats `origin` as authoritative for moved and remote-only tags, but
preserves and rejects local-only tags.

`.github/workflows/copilot-review-apply.yml` hands actionable submitted reviews
on non-draft, same-repository pull requests to the Copilot coding agent and
supports manual replay by pull request and review ID. It re-fetches both
objects, ignores approvals and empty reviews, and uses a review-ID marker to
prevent duplicate handoffs. The workflow requires the user-scoped
`COPILOT_ASSIGNMENT_TOKEN` secret because an installation `GITHUB_TOKEN`
comment cannot invoke the agent. A missing secret must produce a notice and
skip the handoff job; the nightly compliance workflow reports the persistent
configuration drift. Keep default permissions empty, never expose the token to
fork pull requests, and never execute or copy review text into the handoff
command.

## Documentation

**update documentation** After any change to source code, update
relevant documentation in this file (`AGENTS.md`, which `CLAUDE.md`
symlinks), README.md, and `docs/`. A task is not complete without
reviewing and updating relevant documentation.

**docs/ directory** All documentation lives in one tree, `docs/`, in
frostyard/core's four-category shape per
[core ADR-0025](https://github.com/frostyard/core/blob/main/docs/adr/0025-consolidate-repository-docs-into-docs.md):
`docs/adr/` (why — decisions), `docs/design/` (how it fits together),
`docs/specs/` (exact contracts), `docs/plans/` (order of work), indexed in
[docs/README.md](docs/README.md), whose table and conventions are binding.
The entry point for codebase context is
[docs/design/overview.md](docs/design/overview.md) (formerly
`yeti/OVERVIEW.md`) — read it and the docs it links before performing
tasks. New docs start from their category's `TEMPLATE.md` and get indexed.
Repo-local decisions get an ADR in `docs/adr/` (next free number);
org-wide decisions go to frostyard/core, with a back-link recorded in
[docs/org-adrs.md](docs/org-adrs.md). Write every doc to be maximally
useful to an agent's context window — dense, factual, exact paths and
constants, decision rationale rather than user-facing guides.
Inbox-style notes (corrections, `.memory/`-style learnings) are seeds,
not archives: fold them into the right `docs/` page (or this file, for
rules of engagement) and delete the entry.

**.knowledge/ directory** is the cross-session entry point for committed agent
knowledge. Read `.knowledge/README.md` before planning changes, then follow its
links to this file, `corrections.jsonl`, every learned skill, the design
overview, and authoritative subsystem docs. Append only verified corrections,
promote stable rules to their durable owner, and never commit credentials,
personal data, speculation, or transient worktree state.

## One command mirrors CI

**make ci / make docker-ci** runs every CI gate that runs without
credentials — tidy check, vet, format check, lint, govulncheck, tests, race,
build — in CI's order. Run it before pushing; if it is green locally, the
credential-free gates will be green in CI. `docker-ci` is the containerized
equivalent for hosts without Go/PAM/systemd headers or golangci-lint.
`.github/workflows/test.yml` disables token permissions by default, grants
each job read-only contents access, and grants OIDC only to the Codecov job.
Automated harnesses (the mill's deep gate) use this same target, so agents
and CI can never disagree about what "passing" means for the credential-free
gates. `.github/workflows/nightly-compliance.yml` reruns that same `make ci`
contract every day at 04:23 UTC and on manual dispatch with read-only contents
access, commit-pinned actions, and non-cancelling concurrency. Its compliance
job is credential-free; a separate job asserts that the
`COPILOT_ASSIGNMENT_TOKEN` automation secret is configured. It installs the
native headers and pinned linter required to prevent a gate from silently
degrading on a fresh runner.

Two workflow gates sit outside that one-command mirror. First,
`.github/workflows/packaging.yml` carries three tiers: it builds the `.deb`
and `.rpm` artifacts and verifies them against the artifact contract, installs
those same artifacts on pinned Debian and Fedora containers, and — in the
`vm-boot` job — boots a real Debian and a real Fedora VM under QEMU/KVM and
validates the installed package on a host with systemd as PID 1. It cannot
run locally because it needs the `GORELEASER_KEY` secret and the goreleaser
Pro distribution, neither of which is available on a development host or in
the development image. The install half additionally needs Docker and
network access, and the `vm-boot` half needs KVM, network access and the
artifact the `packages` job builds, so the whole gate stays outside
`make ci` and `make docker-ci` by construction. Once artifacts exist in
`dist/`, `make verify-packages` and `make verify-package-install` are the
local tools for the first two tiers' contracts; there is deliberately **no**
local `make` target for the `vm-boot` tier.

Second, `.github/workflows/image-tier.yml` acquires the last released x86_64
RPM as its packaging substrate, builds and overlays the checked-out
executables, composes signed ephemeral uCore derivatives and validates their
update/rollback lifecycle under QEMU/KVM. It needs root, live KVM, network
access, Podman 5, cosign and up to 180 minutes on the GitHub-hosted
`ubuntu-26.04` image. It cannot run inside the development container or the
credential-free local CI mirror, and deliberately has no local `make` target.

## Learned agent skills

**docs/agents/skills/** Read every file in `docs/agents/skills/` before
planning, implementing, or reviewing changes. Each file is a durable lesson
distilled from a previous automated run of [the mill](https://github.com/frostyard/mill)
(the spec→PR harness, configured here via `.mill.toml`); they are binding
guidance, not suggestions. New skills are added by the mill's harvest step
and reviewed like any other change in the PR that carries them.

## Org-wide decisions

Org-level conventions this repo follows are recorded as ADRs in
frostyard/core — see [docs/org-adrs.md](docs/org-adrs.md) for the list that
binds this repo. Change the ADR (in core) before changing behavior it covers.
