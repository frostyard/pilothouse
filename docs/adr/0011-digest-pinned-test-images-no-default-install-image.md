# 0011 — Pin test images per tier at one site each; INSTALL_IMAGE has no default

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

The heavy validation tiers run built packages against real distro
environments: the container install tier runs inside a distro container image,
and the booted-VM tier boots distributor cloud images. Unpinned image
references (`debian:12`, a `latest` cloud-image URL) make those tiers
non-reproducible — a distro-side push changes what CI tests with no diff in
this repo — and scattering pins across workflow YAML, Makefile, and shell
harness invites the copies to drift apart. Separately, `make
verify-package-install` runs an arbitrary image with `--user 0:0` on the
developer's machine, so letting the Makefile silently default the image is
also a trust decision (see
`docs/agents/skills/docker-run-must-not-trust-a-user-supplied-image-default.md`).

## Decision

Each heavy tier has exactly one in-repo pinning site, and pins are immutable
references with distributor-published integrity:

- **Booted-VM tier:** `test/vm/images.env` is the single pinning site for the
  guest cloud images (stated at `images.env:3-6`; the workflow and harness
  scripts carry no URL or digest of their own, enforced by
  `packaging/vm_harness_test.go:293-296` and
  `packaging/workflow_vm_job_test.go:22-25`). Rules recorded in the file:
  immutable, dated or archived URL paths only — a `latest`-style URL would
  defeat the pin (`images.env:11-13`) — and the checksum is the distributor's
  own published value, never a digest computed here (`images.env:14-15`).
  The harness parses the file as inert data, never sources it
  (`test/vm/lib/images.sh`).
- **Container install tier:** the OCI-digest pins (`debian:12@sha256:…`,
  `fedora:42@sha256:…`) live in the install job's matrix in
  `.github/workflows/packaging.yml:151-154`, with a hand-written oracle table
  in `packaging/workflow_install_job_test.go:78-83` guarding them
  (deliberately not read back from the workflow, per lines 69-74 — the same
  independent-oracle posture as ADR-0004).
- **`INSTALL_IMAGE` has no default:** `Makefile:18-21` leaves
  `INSTALL_IMAGE ?=` empty on purpose, and `verify-package-install`
  (`Makefile:216-220`) refuses to run when it is unset, printing the two
  digest-pinned references instead of guessing a distro family. The
  unset-default is itself asserted by
  `packaging/workflow_install_job_test.go:155-158`.

The image-host tier is the deliberate exception: `test/image/compose-ucore.sh`
tracks `ghcr.io/ublue-os/ucore:latest`, resolving the digest at runtime via
`skopeo inspect` and verifying provenance with `cosign verify`
(`compose-ucore.sh:6-7`, `161-170`) — that tier exists to catch upstream image
drift, so pinning it would defeat its purpose.

## Consequences

- A tier's image changes only via a reviewable diff at its single pinning
  site; distro-side pushes cannot silently change what CI validated
  (except on the image-host tier, where reacting to upstream movement is the
  job and cosign supplies the trust anchor).
- `make verify-package-install` never runs an assumed image as root on a
  developer machine; the cost is one extra `INSTALL_IMAGE=` argument, paid on
  every local invocation.
- Refreshing a VM image means finding the distributor's new dated/archived
  URL and published checksum — more work than bumping a tag, by design.
  Debian's dated paths and Fedora's `pub/archive` layout make old pins
  disappear eventually, forcing periodic refresh.
- The Makefile error message, README, and design doc restate the container
  pins as copy; only `packaging.yml` and its test oracle are load-bearing.

## Alternatives considered

- **Tag references (`debian:12`, `latest` cloud URLs):** non-reproducible;
  upstream pushes change CI behavior with no repo diff.
- **One global pinning file for all tiers:** the tiers pin different kinds of
  references (OCI digest vs URL+checksum vs cosign-verified moving tag) with
  different consumers; forcing one file couples unrelated refresh cadences.
- **Defaulting `INSTALL_IMAGE` to the Debian pin:** convenient, but a
  silently chosen root-running container image is a trust decision the
  Makefile must not make for the developer.
- **Computing checksums locally when pinning VM images:** self-referential —
  it would pin whatever was downloaded, not what the distributor published.

## References

- Shapes: [design/overview.md](../design/overview.md) (validation tiers; PR
  #193 splits these into `docs/design/install-validation.md`,
  `docs/design/vm-harness.md`, and `docs/design/image-tier.md`)
- Enforced by: `packaging/vm_harness_test.go`,
  `packaging/workflow_vm_job_test.go`, `packaging/workflow_install_job_test.go`,
  the `verify-package-install` guard in `Makefile`
- Related: [0005 — tiered package validation](0005-tiered-package-validation-and-vm-boot-label.md),
  [0004 — hand-transcribed packaging contract](0004-hand-transcribed-packaging-contract-oracle.md),
  [docs/agents/skills/docker-run-must-not-trust-a-user-supplied-image-default.md](../agents/skills/docker-run-must-not-trust-a-user-supplied-image-default.md)
