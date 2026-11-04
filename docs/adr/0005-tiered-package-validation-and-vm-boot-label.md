# 0005 — Tier package validation from contract to booted host; gate heavy tiers on label presence

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

"The package works" spans claims of very different cost: the artifact matches
its contract (seconds, hermetic), it installs in a distro userland (a
container pull), the installed system actually runs under systemd as PID 1
with enforcing SELinux (a booted VM under QEMU/KVM), and the released RPM
composes into a bootc image host and survives update/rollback (network +
KVM + released artifacts). Running everything on every push is unaffordable
and unnecessary; running only the cheap checks misses exactly the failures
(PAM, SELinux, unit ordering, image composition) that only a booted host
shows. Local `make ci` mirrors the credential-free CI gate (core ADR-0022),
so anything needing KVM or network cannot live behind it.

## Decision

Package validation is tiered by cost, across two workflow gates that sit
outside the `make ci` mirror (`AGENTS.md:224-246`):

1. **Contract** — `packaging.Verify` checks built `.deb`/`.rpm` artifacts
   against the hand-written contract (the `packages` job,
   `.github/workflows/packaging.yml:49`; locally `make verify-packages`).
2. **Container install (Layer A)** — the same artifacts install on pinned
   Debian and Fedora containers via `packaging/verify-install.sh` (the
   `install` job, `packaging.yml:125-143`; locally
   `make verify-package-install`).
3. **Booted VM (Layer B)** — the same artifacts install on real booted
   Debian and Fedora VMs under QEMU/KVM, with systemd as PID 1 (the
   `vm-boot` job, `packaging.yml:171-191`).
4. **Image host** — a separate gate, `.github/workflows/image-tier.yml`
   (`ucore-vm` job), composes the last released RPM into signed ephemeral
   uCore/bootc derivatives and validates update/rollback under QEMU/KVM.

The "Layer A / Layer B" split is the container-vs-booted-host boundary
inside package validation (`packaging.yml:125`, `:171`;
`docs/design/overview.md:3220-3238`), and each layer refuses to re-assert
the other's checks (`packaging/vm_harness_test.go:1587-1595`,
`packaging/verify_install_test.go:29`).

The two heavy tiers deliberately have **no local make target**
(`AGENTS.md:236-238`, `:246`): they need KVM, network, and (for the image
tier) released artifacts, so they cannot run under `make ci`/`make
docker-ci`, and a make target would imply they can. On pull requests they
are gated by the `vm-boot` label, checked by **presence** —
`contains(github.event.pull_request.labels.*.name, 'vm-boot')`
(`packaging.yml:196`, `image-tier.yml:26`) — not by the `labeled` event.
`labeled` *is* in the trigger types, but only so applying the label can
start a run; the gate re-fires on every push to a labelled PR, because "a
label that certified only the commit at HEAD when it was applied would be a
worse guarantee than not running at all" (`packaging.yml:38-43`,
`image-tier.yml:13-15`). Both expressions and trigger lists are test-pinned
(`packaging/workflow_vm_job_test.go:36-40,131`,
`packaging/workflow_image_tier_test.go:19,85`). The heavy tiers are not
required status checks (`packaging.yml:182-183`,
`docs/design/overview.md:4118`): they always run on pushes to `main`, and on
PRs they are reviewer-invoked evidence, per the tier rules in
[risk-tiers.md](../risk-tiers.md).

(Naming note: `docs/design/overview.md`'s "artifact-contract portability:
four tiers" is an unrelated taxonomy of Go source layering inside
`packaging/`; the VM and image harnesses are explicitly "not a fifth
artifact-contract tier".)

## Consequences

- Every push pays only for the hermetic tiers; booted-host evidence is
  available on demand by labelling a PR, and stays honest across subsequent
  pushes because the gate checks presence, not the labelling event.
- A PR can merge without VM/image validation; `main` always gets it
  post-merge. That window is accepted, and `risk-tiers.md` tells reviewers
  when to demand the label before merge.
- No local make target for the heavy tiers means contributors cannot
  discover them by `make help`; the workflows and AGENTS.md are the
  documentation. The flip side: nothing in the local gate can hang on
  missing KVM.
- Removing the label mid-review silently stops re-validating later pushes —
  the presence gate cuts both ways.

## Alternatives considered

- **Run VM/image tiers on every PR:** minutes-scale KVM jobs on every push,
  mostly re-proving what Layer A already proved.
- **Gate on the `labeled` event:** validates only the commit that was HEAD
  when labelled; later pushes ride an inapplicable green check — worse than
  not running.
- **Make the heavy tiers required checks:** every PR would need the label
  (or an admin bypass), collapsing the tiering back to "run everything
  always".
- **Local make targets that shell to the VM harness:** implies `make ci`
  parity that cannot hold without KVM/network; rejected explicitly in
  AGENTS.md.

## References

- Shapes: [design/overview.md](../design/overview.md) (validation layers; PR
  #193 splits these into `docs/design/install-validation.md`,
  `docs/design/vm-harness.md`, `docs/design/image-tier.md`),
  [quality.md](../quality.md), [risk-tiers.md](../risk-tiers.md)
- Implemented in: `.github/workflows/packaging.yml`,
  `.github/workflows/image-tier.yml`, `packaging/verify-install.sh`,
  `test/vm/`, `test/image/`
- Enforced by: `packaging/workflow_vm_job_test.go`,
  `packaging/workflow_image_tier_test.go`,
  `packaging/workflow_install_job_test.go`, `packaging/vm_harness_test.go`,
  `packaging/verify_install_test.go`
- Related: [0011 — pinned test images](0011-digest-pinned-test-images-no-default-install-image.md),
  [0004 — packaging contract oracle](0004-hand-transcribed-packaging-contract-oracle.md)
- Builds on: [core ADR-0022 — make ci is the canonical gate](https://github.com/frostyard/core/blob/main/docs/adr/0022-make-ci-gate-and-test-naming-filter.md),
  [core ADR-0021 — SHA-pinned actions and least-privilege CI](https://github.com/frostyard/core/blob/main/docs/adr/0021-sha-pinned-actions-and-least-privilege-ci.md)
  (see [org-adrs.md](../org-adrs.md))
