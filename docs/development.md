# Development

How to build, test, run and release Pilothouse from a checkout. The rules for
agents and contributors are in [`AGENTS.md`](../AGENTS.md); the
contribution steps are in [`CONTRIBUTING.md`](../CONTRIBUTING.md).

## Gates and review

Classify each pull request using [`docs/risk-tiers.md`](risk-tiers.md),
selecting the highest tier represented anywhere in the final diff. Higher tiers
add targeted validation and review requirements; they do not replace CI. The
[public metrics index](metrics/) links the auditable repository signals
and defines the rolling pull-request acceptance metric.

For work that spans sessions, start with [`.knowledge/README.md`](../.knowledge/README.md).
It indexes the repository's binding instructions, verified corrections,
engineering lessons, architecture context, and subsystem documentation without duplicating
them.

Before pushing, run `mise install` once and then `make ci BASE=origin/main`
(or `make docker-ci` on hosts without the native toolchain) — it runs every CI
gate that runs without credentials, in the same order. `make verify` is the
non-mutating subset and `make check` formats first. It runs all Go packages and the tagged sdjournal journal tests;
the lint recipe fails instead of skipping when `golangci-lint` is absent or
is not the `mise.toml` pin. Local green means the credential-free gates will be green in
CI. The pull-request test workflow starts from no token permissions, grants
only read-only contents access per job, and adds OIDC only to the unit-test job
for Codecov; its `govulncheck` installation is version-pinned. The read-only
`nightly-compliance.yml` workflow reruns the same `make ci` contract daily at
04:23 UTC and on manual dispatch. Its single compliance job is
credential-free.
Two workflow gates are exceptions. `.github/workflows/packaging.yml`, the
packaging gate, cannot run locally because it needs the `GORELEASER_KEY` secret
and the goreleaser Pro distribution. That gate does more than read the
artifacts: it installs them on pinned Debian and Fedora containers, so it
needs Docker and network access on top of the credentials, and its `vm-boot`
job boots real Debian and Fedora VMs under QEMU/KVM, so that tier needs KVM
too. The whole workflow stays outside `make ci` and `make docker-ci` by
construction. `make verify-packages` and `make verify-package-install` are the
local tools for the contracts it checks, once artifacts exist in `dist/`;
the booted-VM tier has no local `make` target.
`.github/workflows/image-tier.yml` separately needs root, live KVM, network
access, Podman 5, cosign and the GitHub-hosted `ubuntu-26.04` environment to
exercise released packaging plus checked-out executables on ephemeral uCore
derivatives, so it has no local make target either.

With the `ANTHROPIC_API_KEY` repository secret configured, non-draft pull
requests whose branch is in this repository also receive an advisory,
least-privilege [Claude code review](claude-code-review.md). Fork pull
requests are deliberately skipped, comments remain untrusted until a human
verifies them, and the workflow cannot approve, merge, deploy, or publish. If
the key is absent, the workflow reports a warning and skips the review.

## Build and test

Go 1.26 or newer is required.

```bash
make test
make race
make build
```

`make test` runs `go test ./...`, plus the tagged sdjournal journal tests.
It also includes the process-level web checks in `test/e2e/`; they build and
start the real `pilothouse` binary on an ephemeral local port and need no broker.

Docker equivalents are available when the host does not have Go, PAM headers, or systemd headers installed:

```bash
make docker-generate
make docker-fmt
make docker-build
make docker-test
make docker-race
make docker-lint
```

Each target checks the reusable development image through Docker's build cache and uses persistent Docker volumes for Go and linter caches. Container commands run as the host user, so generated files and build output remain writable. `make docker-run` uses host networking and starts the web process, but broker-backed operations require separately mounting a broker socket into the container.

## Run locally

Start the privileged broker, then the web process:

```bash
sudo ./bin/pilothoused --socket /tmp/pilothouse-broker.sock --socket-group "$(id -gn)"
./bin/pilothouse --broker-socket /tmp/pilothouse-broker.sock
```

That broker runs with no optional tooling configured, so Podman, Docker,
Incus, k3s, and every `updex`-backed extension operation are absent from the
console; add `--podman-socket`, `--docker`, `--incus`, `--k3s`, or `--updex` to the
`pilothoused` line to work on those surfaces, and `--dev` to the
`pilothouse` line to see the static Fleet preview.

Open `http://127.0.0.1:8888` and sign in with a non-root system account. Any
authenticated account can view the dashboard. Members of the configured
broker admin group can perform sysext mutations, and Podman, Docker, and Incus
mutations for whichever engines were configured above. The packaged broker
unit uses `sudo` on Debian-family hosts and `wheel` on Fedora-family hosts.

## Verify built packages

`make verify-packages` reads every `.deb` and `.rpm` in `dist/`, turns each one
into the packaging contract model, and prints the contract findings for it. It
exits non-zero if any artifact carries a finding or cannot be extracted.

An empty or missing `dist/` is the normal state on a development host, because
nothing here builds a package by default: GoReleaser Pro does that in CI,
through `.github/workflows/release.yml` on a tag,
`.github/workflows/snapshot.yml` on `main`, and
`.github/workflows/packaging.yml` on every push and pull request targeting
`main` (that workflow's booted-VM job additionally runs only on `main` or on
a pull request carrying the `vm-boot` label). The target then fails with a
message naming that directory, all three
workflows, and `make package` as the local producer along with its goreleaser
Pro requirement. That failure is the expected local outcome, not a defect to
fix.

```bash
make verify-packages
```

The target is deliberately not part of `make ci` or `make docker-ci`: those
gates must stay green on a checkout with no built artifacts, so that local
green still means the credential-free CI gates will be green. The one CI gate
they do not mirror is `.github/workflows/packaging.yml`, which builds the
artifacts, runs this same verification in CI, installs those same
artifacts on pinned Debian and Fedora containers with
`make verify-package-install`, and then boots real Debian and Fedora VMs and
validates the installed package on a host with systemd as PID 1. It cannot
run locally because it needs the `GORELEASER_KEY` secret and the goreleaser
Pro distribution — and its booted-VM tier needs KVM as well.

`make package` is the local producer: it runs `goreleaser release --snapshot
--clean`, which builds snapshot `.deb` and `.rpm` artifacts into `dist/`,
publishes nothing and needs no tag. It requires the goreleaser Pro
distribution at major version 2, which is deliberately not installed on this
host or in the development image, so on a stock checkout it fails with an
actionable message naming what was found, what is required, and
<https://goreleaser.com/pro/>. Once artifacts exist in `dist/`, `make
verify-packages` is the thing to run against them.

```bash
make package
```

## Verify package installation

`make verify-package-install` is the install-side sibling of `make
verify-packages`: instead of reading bytes out of an artifact, it installs the
artifact inside a real distro userland and asserts what the package manager and
the postinstall scriptlet actually produced. It runs
`packaging/verify-install.sh` inside a container image, as root, with only
`packaging/` and the artifact directory bind-mounted read-only.

Two variables drive it:

- `INSTALL_IMAGE` — the container image reference. There is no default; an
  unset value fails with a message naming the two digest-pinned images this
  validation targets,
  `debian:12@sha256:9344f8b8992482f80cba753f323adeaf17690076c095ccff6cc9536be98185dc`
  and
  `fedora:42@sha256:99e203b80b1c3d8f7e161ec10a68fd02b081ef83a3963553e513c82846b97814`.
- `ARTIFACT_DIR` — the directory of built artifacts, defaulting to `dist`.

```bash
make verify-package-install \
  INSTALL_IMAGE=debian:12@sha256:9344f8b8992482f80cba753f323adeaf17690076c095ccff6cc9536be98185dc
```

The script performs eight checks, each of them something the static payload
contract cannot see: that the hand-written per-format `dependencies` lists
resolve against the distro's real repositories; that the `pilothouse` user and
group exist afterward and match the installed sysusers declaration; that the
on-disk owner, group and mode of the configuration directories and env files
are right on the installed filesystem rather than in package metadata; that
every stack and module the installed PAM policy names exists on that distro;
that the distro's own `systemd-analyze verify` accepts both installed units;
that the cgo-linked binary's dynamic dependencies all resolve; that reinstalling
the same artifact succeeds with the account and ownership assertions still
holding; and that removal leaves the per-format state each manager promises
(conffiles surviving `dpkg -r` and gone after `dpkg -P`, no `.rpmsave` after
`rpm -e`, the account surviving both).

Like `make verify-packages`, it is deliberately outside `make ci` and `make
docker-ci`, and for a stronger version of the same reason: it needs artifacts
that a stock checkout does not have, plus Docker and network access — check 1's
whole point is dependency resolution against the distro's real repositories.

CI runs this exact target. `.github/workflows/packaging.yml`'s `install` job
depends on the build-and-verify job, downloads the artifacts it uploaded, and
runs `make verify-package-install` once per image across a two-entry matrix —
the pinned Debian image with the `.deb`, the pinned Fedora image with the
`.rpm`. That is why the packaging gate is not a payload-only check, and why it
stays outside `make ci` / `make docker-ci`. The same workflow's `vm-boot` job
carries the check one layer further, onto a booted host; it is described next.

## Booted-VM validation

A container shares the host's kernel and does not run systemd as PID 1, so the
install validation above can prove what a package *put on disk* but not what it
*does when the host boots*. `.github/workflows/packaging.yml`'s `vm-boot` job
closes that gap. It downloads the same verified artifact, boots a stock Debian
12 or Fedora 42 cloud image — pinned by checksum in `test/vm/images.env` —
under QEMU/KVM on the standard runner, installs the package the ordinary way,
and runs the assertions in `test/vm/` against the running guest:

- both units activate on a booted host, and systemd creates the runtime and
  state directories with the ownership and mode the units declare;
- the privileged broker's Unix socket is live, with the right owner, group and
  mode, and answers over that socket;
- PAM authenticates a real non-root administrator end to end through the
  running stack, and both negatives hold — a wrong password is rejected with
  a 401 by PAM itself, and a direct root login is refused;
- journald is reachable: the daemon reads a record it emitted itself back
  through the broker's own journal query;
- the posture survives a real reboot — both units come back active unaided,
  the capability set is unchanged, `/run/pilothouse` is destroyed and
  recreated, and `/var/lib/pilothouse` persists.

The job runs on every push to `main` and, on a pull request, only while the
`vm-boot` label is present. It is **not** a required check, it builds no
packages of its own, and it publishes nothing: the throwaway overlay disk, the
cloud-init seed and the run-time credentials never leave the job, and
diagnostics (QEMU stderr and the guest serial console) go to the job log. There
is no local `make` target for it — it needs KVM, network access and the
artifact the build job produced.

What it deliberately does **not** cover, all of which is
[#80](https://github.com/frostyard/pilothouse/issues/80): image-based hosts
(uCore), SELinux AVC assertions and policy qualification — the Fedora
guest stays enforcing, but the job neither scans the audit log nor classifies
denials — and bootc update/rollback of an ephemeral uCore-derived image
containing the last released x86_64 RPM. The issue does not test `.deb`
layering or system-extension delivery.

The released-RPM fixture producer lives at `test/image/releaserpm`. It is test
infrastructure, not a shipped binary. Ordinary repository test, vet and lint
gates analyze this package:

```bash
go run ./test/image/releaserpm --workspace /absolute/path/to/ephemeral-workspace
```

It queries this repository's latest stable semantic-version GitHub release,
requires the exactly tag-correlated Pilothouse x86_64 RPM, and verifies the
release asset's recorded byte size and SHA-256 while downloading. It creates a new
`fixture-release-rpm/fixture.json` and the verified RPM below the supplied
workspace; an existing fixture directory is an error. Supply a private
workspace that no other process mutates during the invocation. Failures,
including an inability to report the manifest path, roll back only entries
created by the invocation and never recursively delete unknown entries. The
caller owns cleanup after success, and the helper never pushes or uploads an
artifact or image.

The next image-test step consumes that fixture and builds two local uCore
derivatives:

```bash
sudo test/image/compose-ucore.sh \
    --workspace /absolute/path/to/ephemeral-workspace \
    --bin-dir /absolute/path/to/checkout/bin \
    --run-id local-run
```

The workspace must be private and non-concurrently mutated. The command
resolves `ghcr.io/ublue-os/ucore:latest` once, verifies the signed multi-arch
index and its sole linux/amd64 member, and pins every later operation to that
member digest. It rechecks the released RPM's size and SHA-256, installs it
with package repositories and build networking disabled, enables the packaged
broker and web units in the ephemeral image so they start after each bootc
transition, then overlays `pilothouse` and `pilothoused` built from the
checked-out head. Their SHA-256 values are rechecked in the private context,
passed into the offline build, verified again after installation, and recorded
in the fixture manifest. This split is intentional: the immutable release RPM
tests the selected packaging/image-delivery substrate, while the checked-out
executables make a pull request exercise the capability and host-image queries
it is gating; v0.6.0 predates both queries. The composer runs
`bootc container lint` and creates distinct `baseline` and `update` fixtures
with Podman's graph, image, run and temporary storage rooted entirely below
`fixture-ucore-images/`. Release `v0.6.0` also predates the
per-format packaging correction and its RPM contains the Debian PAM service
file. Because #80 assumes #67's PAM proof and needs authentication only to
reach its image-specific read-only queries, composition replaces that one
known-bad file with the reviewed Fedora policy only when the immutable release
ID `358276825`, asset ID `486354638`, tag `v0.6.0`, and RPM basename all
match. The installed bad-file digest and replacement policy digest are checked
first; every other release identity gets no compatibility override. The
fixture manifest records all four compatibility selector inputs, and its consumer requires
that exact identity if and only if the override is selected. The private build
context contains only verified copies of the released RPM, that reviewed
policy, and the two checked-out executables, never the container store.
Explicit general/storage configuration selectors disable
normal system and per-user Podman configuration: general
configuration uses the explicit empty `/dev/null` file, while a generated
private `storage.conf` repeats the graph, image and run paths plus driver; the
two temporary paths are pinned separately by `--tmpdir` and `TMPDIR`. Storage
environment and late general-configuration overrides are cleared, file events
are disabled, and remote Podman selection is disabled. It never pushes an
image or deletes its store. Its manifest records the effective UID that owns
the store. A fixture intended for the VM consumer must be composed as UID 0,
and compose, consume, exact-store reset and workspace removal must remain in
that one rootful ownership domain. Tool
progress remains on caller-owned standard output and standard error.

Once composition succeeds, the root-only VM consumer starts from the official
Fedora CoreOS stable QEMU image and switches that booted host to the local
baseline fixture:

```bash
sudo test/image/ucore-vm-test.sh \
    --workspace /absolute/path/to/ephemeral-workspace
```

It verifies both compressed and uncompressed checksums from Fedora CoreOS
stream metadata, boots a private qcow2 overlay under QEMU/KVM with OVMF and an
ephemeral Ignition key for the unprivileged `core` account, and attaches one
read-only ext4 disk containing one shared OCI layout with both fixtures. Both
loaded Podman image IDs must equal the fixture manifest exactly before the
baseline is staged with
`bootc switch --transport containers-storage`. uCore's own VM harness
documents why this bootstrap is necessary: `bootc install to-disk` on
FCOS/uCore omits the `LABEL=boot` partition that its GRUB configuration
requires. After the baseline reboot, the harness validates enforcing SELinux
and the exact broker-advertised capability set against independent host probes
(rejecting non-canonical or line-bearing capability IDs before comparison),
and requires Pilothouse's booted, staged and rollback image/digest pairs to
match `bootc status` exactly. The controlled broker-query window must produce
no AVC denial except uCore's delayed, explicitly permissive
`coreos_boot_mount_generator_t` boot records and at most two exact enforcing
`chcon`/`mac_admin` probe records that `bootc status` deliberately generates.
No current-boot AVC denial may name Pilothouse. A failure prints at most the
first 20 rejected records so the CI log identifies the denial without making
output unbounded. Privileged
guest work uses only non-interactive sudo from `core`; that includes broker
readiness because the correctly protected mode-0750 runtime directory is not
traversable by `core`. Both fixtures are exported into one job-local OCI
layout whose compressed blobs are shared, then Skopeo streams its two refs
into the guest's own container storage; the update is later staged with
`bootc switch --transport containers-storage`; no registry is
started or contacted for the derived images. Reboots prove staged-to-booted
digest continuity and placement of the old deployment in the rollback slot,
then `bootc rollback` proves the slots reverse and the baseline `/usr` marker
returns. The guest uses PAM only as the prerequisite for its authenticated
capability reads; it does not repeat #67's activation, directory, negative
login, journald-readback, or plain-reboot assertions.

The VM consumer streams QEMU output to the caller-owned sink and bounds every
other long-running child. On success and failure it stops and waits for QEMU.
The outer lifecycle first requires at least 10 GiB free in its workspace. To
stay within the standard runner's disk, the consumer empties the isolated
private Podman image store after export and proves it empty before downloading
FCOS. It rejects an FCOS archive declaring more than 4 GiB uncompressed and
deletes the verified compressed copy after decompression. After both OCI refs'
config digests are matched to the fixture manifest, the private store is
emptied and proved empty. The OCI layout is capped at 3 GiB, copied into a
sparse 3.5 GiB no-journal ext4 carrier, and recursively removed from its one
fixed transient path before FCOS is downloaded. The standalone layout and
carrier temporarily coexist only during that population step. FCOS mounts the
carrier read-only and Skopeo copies both OCI refs directly into
containers-storage, avoiding the full uncompressed temporary tar required by
compressed Docker-archive loading. The carrier is unmounted and both guest
image IDs are rechecked before the first switch. It
retains `fixture-ucore-vm`; the enclosing owner
still performs the final exact-store reset and workspace removal.

The enclosing production entry point owns the entire lifecycle:

```bash
sudo -n env "PATH=$PATH" \
    bash test/image/ucore-image-test.sh --run-id local-run
```

It creates one private workspace and invokes acquisition, composition and VM
validation synchronously with wall-clock and 4 MiB retained-log bounds. Each
phase command and its tail collector run below the same timeout-owned process
group, so the cap does not constrain artifacts and the deadline still covers
output collection. The owner waits until the group exists before acting on a
pending INT/TERM, forwards to the group, and rejects and terminates descendants
that survive their direct command before cleanup. The collector ignores soft
INT/TERM only long enough to drain the stopped producer and retain diagnostics;
the owner's bounded KILL escalation remains authoritative.
Once a signal handler begins, reentrant INT/TERM is deliberately ignored while
cleanup resets the exact workspace-local Podman store synchronously and removes
the workspace. Signal and failure paths use the same reset-then-remove ordering.
If TERM/KILL cannot quiesce the reset group, cleanup fails while preserving the
workspace instead of removing storage underneath a live process.
`.github/workflows/image-tier.yml` runs this on `ubuntu-26.04`
because Podman 5's `--imagestore` support is part of the isolation contract. It
runs on every push to `main` and, for pull requests, only while the `vm-boot`
label is present. The job uses the last released x86_64 RPM, uploads nothing
and never publishes either derived image.

## Create a release

`make bump` verifies the project in the development container, calculates the
next semantic version with the container's pinned `svu`, creates an annotated
tag, and immediately pushes that tag to `origin`.

The host needs only Docker, Make, and authenticated Git access. Run it from a
clean `main` checkout that exactly matches `origin/main`; the target rejects
dirty, ahead, behind, divergent, feature-branch, and detached-HEAD states
before creating a tag.

Before verification, preflight force-updates local tag refs from authoritative
`origin` values, so moved and remote-only tags are reconciled automatically.
Local-only tags are preserved and rejected rather than silently deleted.

```bash
make bump
```
