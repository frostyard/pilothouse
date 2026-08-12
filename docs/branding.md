# Branding and naming

Pilothouse ships as a neutral product: its generic UI, systemd unit
descriptions, package metadata, and user-facing documentation carry no
third-party product identity. This document records the wording rules that
keep it that way, and — just as importantly — the explicit allowlist of
places where a `cayo`/`snosi` occurrence is **correct** and must not be
swept.

It exists so the boundary is reviewable in-repo and future sweeps do not
re-litigate it. A blanket find-and-replace over `cayo`/`snosi` is wrong: it
would rename pinned test fixtures and break the release pipeline.

## The canonical self-description

Pilothouse describes itself as:

> a local web administration console for image-based Linux systems

Use that sentence verbatim. It is accurate to what the software actually
does — bootc / rpm-ostree host-image reporting, `systemd-sysext` extension
lifecycle, immutable EROFS mount handling — and carries no product identity.
It currently appears in `README.md`, `site/content/getting-started/overview.md`,
`.goreleaser.yaml` (both the short package `description` and the long one),
`docs/design/overview.md`'s Purpose paragraph, and `workflows/code-review.yaml`'s
reviewer prompt.

### When to use it, and when not to

The canonical sentence is for **self-description prose only** — the
"Pilothouse is …" positioning sentence at the top of a document, a package
description, or a prompt preamble that has to say what this software is. Do
not paste it into headings or link text. For those, apply the contextual
rules instead:

- **Headings** — an "install on \<product\>" heading becomes `## Install`
  (or `## Install on a host`), matching the heading style already used in
  that file.
- **Host references in body text and link text** — "install pilothouse on a
  \<product\> host" becomes "…on a host"; a page's front-matter
  `description:` likewise drops the product name rather than growing the
  full canonical sentence.
- **Product links** — remove a `https://github.com/frostyard/snosi`-style
  markdown link from a generic self-description rather than relabeling it.

## Tool and capability identifiers are not branding

`updex`, `sysext`, `systemd-sysext`, `bootc`, and `rpm-ostree` are real
binaries, capability IDs, and on-disk concepts. They are **never** renamed,
removed, or genericized by a branding sweep. Only the possessive framing
around them gets rewritten:

| Before | After |
| --- | --- |
| "Snosi's `updex` interface" | "the `updex` interface" |
| "Snosi `updex` definitions" | "`updex` definitions" |
| "Snosi `updex` definition/install state" | "`updex` definition/install state" |

The same applies to capability IDs themselves (`updex`, `sysext`, `bootc`,
`rpm-ostree` in `docs/capabilities.md`'s binding table): they are contract
identifiers shared with the broker, not prose.

## Generic host prose stays product-neutral

Describe host accounts, validation scope, and extension delivery without
attributing them to an operating-system product:

| Before | After |
| --- | --- |
| "PAM authentication using Snow's users and account policy" | "PAM authentication using host users and account policy" |
| "Snosi-built sysext delivery" | "system-extension delivery" |

## The allowlist — do not sweep these

Every site below intentionally retains a `cayo`/`snosi` occurrence. A
branding sweep must leave each one byte-for-byte unchanged.

### `*_test.go` fixture names, fixture IDs, and test-function names

Includes the capability-contract fixture ID `snosi-without-bootc` (used as a
subtest name and in the fixture tables of
`cmd/pilothouse/capability_contract_test.go` and
`cmd/pilothoused/capability_contract_test.go`, backed by the
`snosiWithoutBootcCapabilitySet()` helper) and the test function
`TestCapabilityContractBootcSnosiFixture`. Other occurrences live in
`cmd/pilothoused/main_test.go`, `internal/modules/maintenance/*_test.go`, and
`internal/modules/fleet/*_test.go`.

**Rationale:** these are internal identifiers, not product surfaces. They
name a *capability shape* the contract pins — a host with `updex`/`sysext`
and every engine but no `bootc` — and renaming them churns the capability
contract for zero user-visible benefit while risking a break in the pinned
contract the deep gate enforces.

### `docs/capabilities.md` prose naming those fixtures

Lines 301, 549, 568-569, 801, and 866 name `snosi-without-bootc` and
`TestCapabilityContractBootcSnosiFixture` directly.

**Rationale:** this documentation must keep matching the fixture IDs in
code. Renaming the prose without renaming the fixtures (which the entry
above forbids) would simply make the documentation wrong.

### `.github/workflows/release.yml`'s `frostyard/snosi` dispatch

The "Kickoff snosi" step's `repository: frostyard/snosi`.

**Rationale:** functional release wiring — a cross-repo `repository_dispatch`
target, not wording. Editing it breaks the release pipeline.

### `internal/modules/fleet/*` mock demo data

`"cayo 2026.07"`, the host ID `"cayo-01"`, and the `"cayo-03"` placeholder in
the enrollment view.

**Rationale:** the Fleet module is a preview built entirely on canned mock
data, and #64 placed it behind the `--dev` flag — without `--dev`,
`fleet.New()` is never constructed and never reaches the registry. This is
developer-only fixture data, not a production surface.

### `docs/design/` and `docs/specs/` historical and phase narrative (formerly `yeti/`)

Where `docs/design/overview.md` and the subsystem docs split out of it
(`docs/design/*.md`, `docs/specs/artifact-contract.md` — all formerly
`yeti/OVERVIEW.md`) record what a past phase actually did, a product name in
that record is a historical fact.

**Rationale:** the phase narrative is history rather than a live product
description; rewriting it would make the record inaccurate. This exemption
covers narrative only — the doc's *current-state* prose (its Purpose
self-description and the module table's `sysext` row) is live product
description and was neutralized by this sweep like any other generic
surface. As of that sweep no `cayo`/`snosi` occurrence remained anywhere in
the then-single `docs/design/overview.md`; narrative added later (e.g. the
booted-VM harness scope notes, now in `docs/design/vm-harness.md`) names
`Snosi` under this same historical-narrative rule.

## Checking a sweep

Every remaining `cayo`/`snosi` occurrence in the tree should be an
allowlisted site (or this file's own description of the allowlist), and the
product possessive `Snow's` should not appear outside this rule's example.
That is what the complement grep asserts — it should print nothing:

```sh
git grep -i -E "snosi|cayo|snow's" \
  | grep -v '_test.go' \
  | grep -v 'docs/capabilities.md' \
  | grep -v '.github/workflows/release.yml' \
  | grep -v 'internal/modules/fleet/' \
  | grep -v 'docs/design/' \
  | grep -v 'docs/specs/artifact-contract.md' \
  | grep -v 'docs/branding.md'
```

Because the allowlist is defined by "unchanged," a sweep should also confirm
the allowlisted paths carry no diff at all against the pre-sweep tree, e.g.
`git diff --exit-code <base> -- docs/capabilities.md .github/workflows/release.yml internal/modules/fleet/ '*_test.go'`,
and that `make docker-ci` passes — the capability contract tests are the
check that the fixtures really did survive the sweep unrenamed.

## Build artifacts

Do not commit built binaries. The Makefile's `build` target writes to
`bin/`, which is gitignored, and `/pilothouse` is gitignored too so an
accidentally-built root binary cannot be re-added. A tracked binary embeds
whatever strings the source carried when it was built, which no source edit
can reach.
