# Spec: Packaging artifact contract

Extracted verbatim from [../design/overview.md](../design/overview.md)
(formerly `yeti/OVERVIEW.md`). This is the exact contract a built
`.deb`/`.rpm` artifact must satisfy — the model and finding vocabulary, the
required destinations with pinned modes and config designations, the
per-format dependency lists, the forbidden systemd-managed roots, the
postinstall-scriptlet rule, and `packaging.Verify`'s check semantics — plus
the packaging configuration it is transcribed from and the drift guards that
keep the transcription honest. It changes only alongside the code in
`packaging/` that implements it (`packaging/finding_test.go` pins the finding
codes' string values).

How a `Model` is populated from a real artifact is
[../design/artifact-extraction.md](../design/artifact-extraction.md); the
fixture builders the tests use are
[../design/packaging-test-fixtures.md](../design/packaging-test-fixtures.md);
on-disk validation after a real install is
[../design/install-validation.md](../design/install-validation.md) (Layer A)
and [../design/vm-harness.md](../design/vm-harness.md) /
[../design/image-tier.md](../design/image-tier.md) (Layer B).

The `packaging/` directory's test files split by role: `units_test.go`,
`postinstall_test.go`, `verify_install_test.go` and
`goreleaser_config_test.go` are its configuration-level tests — the first
runs the real `systemd-analyze verify` against both broker units and asserts
they differ in exactly one line, the second runs the real `shellcheck`
against `postinstall.sh` and exercises it against a temporary root, the
third guards `verify-install.sh` (the install-validation shell script, see
[../design/install-validation.md](../design/install-validation.md)) without
ever executing it, and the fourth parses `../.goreleaser.yaml` and asserts
the nfpms packaging contract. `finding_test.go` pins the finding codes'
string values and `verify_test.go` holds the artifact-contract behavioral
tests; `drift_test.go` holds the two guards tying `contract.go`'s
hand-written tables to the live `../.goreleaser.yaml`.

## Packaging configuration (`.goreleaser.yaml`, `packaging/postinstall.sh`)

**Package-owned configuration directories and runtime dependencies.**
`.goreleaser.yaml`'s `nfpms[0].overrides.<format>.contents` declares two
`type: dir` entries in both formats — `/etc/pilothouse` (`root:pilothouse`,
mode `0750`, group-readable so the units' `EnvironmentFile=` works) and
`/etc/pilothouse/storage/credentials` (`root:root`, mode `0700`, stricter than
its parent because only the root broker reads remote-mount secrets).
`/run/pilothouse` and `/var/lib/pilothouse` are deliberately not packaged;
the broker unit's `RuntimeDirectory=`/`StateDirectory=` own them. Runtime
dependencies are declared per format, naming the direct provider of each
role rather than relying on transitive requires — deb: `libc6`, `libpam0g`,
`libpam-modules`, `libpam-runtime`, `libsystemd0`, `systemd`; rpm: `glibc`,
`pam-libs`, `pam`, `authselect-libs`, `systemd-libs`, `systemd`. The six
roles line up one-to-one across the platforms: linked C library, PAM shared
library, PAM modules providing `pam_nologin.so`, provider of the PAM stacks
the policy includes, libsystemd shared library, and systemd itself.

**Postinstall scriptlet.** nfpm's static owner/group metadata alone does not
produce correct install-time ownership on either format: the payload is
extracted before the `pilothouse` account exists, and nfpm's DEB tar metadata
leaves numeric UID/GID at zero. `packaging/postinstall.sh` is wired once as
`nfpms[0].scripts.postinstall` (nfpm's `scripts` key is not per-format, so the
same script is the deb `postinst` and the rpm `%post`). It creates the account
by invoking `systemd-sysusers` scoped to this package's own
`/usr/lib/sysusers.d/pilothouse.conf` — never bare — falling back to a
guarded `groupadd`/`useradd` pair reproducing the sysusers declaration
(system account, primary group `pilothouse`, home `/nonexistent`, shell
`/usr/sbin/nologin`) when `systemd-sysusers` is absent. It then re-applies
`root:pilothouse` 0750 to `/etc/pilothouse`, `root:root` 0700 to
`/etc/pilothouse/storage/credentials`, and `root:pilothouse` 0640 to each env
file *that still exists* (both are `type: config`, so a deliberate deletion
must survive an upgrade rather than fail it). `set -e` is its first effective
line, so any failing repair exits non-zero — on Debian that leaves the package
not-configured; on RPM the payload stays installed but the scriptlet error is
reported, since a `%post` runs after extraction and cannot roll back its
transaction. `PILOTHOUSE_ROOT` is a test-only seam: it defaults to empty (so
production operands are exactly the paths above) and prefixes *only* the three
filesystem operands the script hands to `chown`/`chmod`/`[ -e ]`, letting
`packaging/postinstall_test.go` run the real script against a `t.TempDir()`
with PATH-injected `chown`/`chmod`/`systemd-sysusers` fakes. The
sysusers positional config path (relocated by `--root` instead) and the
fallback account's home and shell values stay unprefixed. Nothing here runs
privileged Go code or touches the broker socket — it is package-manager
shell, invoked outside the running daemon.

**Configuration-assertion test.** `packaging/goreleaser_config_test.go` parses
`.goreleaser.yaml` with `gopkg.in/yaml.v3` — a direct module requirement as of
this test — into a minimal set of structs covering only the `nfpms` shape this
repository owns. It deliberately does **not** decode into GoReleaser's own
`config.Project` type and performs no schema validation: that type cannot
represent this repo's GoReleaser Pro `nightly` block, and the goal is to pin
*this repository's* packaging contract, not GoReleaser's schema. Unknown keys
are ignored, so `builds`, `archives`, `changelog`, `release`, and `nightly`
parse and are discarded. What it pins: each format's override contains exactly
one `/etc/pam.d/pilothouse` entry and exactly one
`/usr/lib/systemd/system/pilothoused.service` entry, sourced from that
format's own file, with the *other* format's PAM policy and unit asserted
absent from the same list; both dependency lists match their six expected
elements exactly when sorted (so an extra, missing, or duplicated element
fails); both configuration directories and both env files carry the
type/mode/owner/group above in both formats; `scripts.postinstall` is
`./packaging/postinstall.sh`; `formats` is exactly `[deb, rpm]`; and no
content entry in either format installs to `/run/pilothouse` or
`/var/lib/pilothouse` or anything under them — a plain-prefix check that also
rejects a near-miss sibling like `/run/pilothouse-helper` on purpose, and that
is deliberately *broader* than the artifact-level rule below; see "The
forbidden roots" for why the two must not be harmonized. The comparison helpers
(`checkDependencies`, `checkNoSrc`, `checkNoSystemdManagedPaths`) return errors
rather than asserting inline, so companion tests can mutate a *test-local deep
copy* of the parsed data and prove each check actually fires — the real file is
never written. The test needs no container: it only reads a YAML file from
disk, so it runs under plain `make test` as well as `make docker-ci`.


## Artifact contract model (`packaging/model.go`, `packaging/finding.go`, `packaging/contract.go`, `packaging/verify.go`)

The `packaging` directory is a real Go package, not only a home for tests. It
holds the data types an artifact is described with, the codes a violation is
named by, the destinations the contract requires, the two roots it forbids, and
`Verify`, which reports every violation the contract names.

**Model types** (`packaging/model.go`). `Format` is a string type with exactly
two values, `FormatDeb` (`"deb"`) and `FormatRPM` (`"rpm"`). `Entry` describes
one installed file: `Dest`, `Mode` (`fs.FileMode`), `Config` (whether the
packaging metadata designates it a configuration file), `Content`, plus `Owner`
and `Group`. `Scriptlet` holds a maintainer script's `Content`. `Model` ties
them together: `Format`, `Entries`, `Dependencies`, and `Postinstall`, a
`*Scriptlet` whose nil value is the representation of "this package ships no
postinstall scriptlet" — a state the model keeps distinct from shipping a
scriptlet with unexpected bytes. Populating a `Model` from a real artifact is
out of scope for this package: both extractors live in the `packaging/extract`
subpackage ([../design/artifact-extraction.md](../design/artifact-extraction.md)).

**M1 — modes are asserted from the payload; ownership is proved by the
scriptlet; owner and group are never asserted.** `Entry.Mode` drives a real
assertion (see `wrong_mode` below). `Entry.Owner` and `Entry.Group` do not:
they exist for the convenience of an extractor that happens to surface them and
no code in this package reads them. Whether an extractor fills them in is a
per-backend detail, never one claim about "the extractors": the **deb** backend
leaves both empty, because a `dpkg-deb`-extracted tree cannot recover the
archive's recorded ownership, while the **rpm** backend populates both per entry
from the `%{FILEUSERNAME}`/`%{FILEGROUPNAME}` header tags. Neither changes what
this package asserts, because it reads neither field.
An artifact cannot prove installed ownership: nfpm's
DEB payload records numeric UID/GID 0 by construction (which is why #66 added
the postinstall scriptlet), and the RPM equivalent is unconfirmed against a
real nfpm build.
`grep -nE '\.Owner|\.Group' packaging/contract.go packaging/verify.go` printing
nothing is the mechanical form of that half of the rule.

The positive half is the scriptlet check below. Correct install-time ownership
is produced by `packaging/postinstall.sh` and by nothing else — the **Postinstall
scriptlet** paragraph above describes what that script does and
`packaging/postinstall_test.go` proves it, by running the real script. So this
package proves ownership the only way an artifact can: by asserting that the
package **ships exactly that script**, present and byte-for-byte. A package
whose payload claimed `root:pilothouse` would prove nothing; a package that
ships the scriptlet whose behavior is already pinned proves everything the
artifact is capable of proving. On-disk ownership after a real install is
asserted outside this Go package, by `packaging/verify-install.sh`
([../design/install-validation.md](../design/install-validation.md));
ownership on a booted host is Layer B's, verified
by the booted-VM harness in `test/vm` and run in CI by
`.github/workflows/packaging.yml`'s `vm-boot` job.

**Finding shape and code vocabulary** (`packaging/finding.go`). `Finding` has a
`Code`, a `Path`, and a `Message`. `Code` is a stable exported string — this
package's tests and `cmd/verify-packages` both key off it, so its value is part of
the contract, and `packaging/finding_test.go` pins each value literally and
asserts the nine are pairwise distinct. `Path` is the destination a finding
concerns and is empty for findings that are not path-scoped. `Message` is
human-readable and deliberately unstable; nothing may match on its wording.

The nine codes are `missing_path`, `wrong_mode`, `wrong_content`,
`missing_config_flag`, `dependency_mismatch`, `forbidden_path`,
`duplicate_entry`, `missing_scriptlet`, and `unknown_format`. The last two are
additions to the explicitly-minimal list in the issue: a missing scriptlet is
not path-scoped, so reporting it as `missing_path` would require inventing a
fake `Path`, and a code for an unrecognised `Format` is what keeps a zero-value
`Model` from ever being accepted as satisfying the contract.

**Embedded repository sources** (`packaging/contract.go`). The nine files in
this directory that the packages ship — `pilothouse.service`,
`pilothouse.pam`, `pilothouse.sysusers`, `pilothouse.env`, `pilothoused.env`,
`postinstall.sh`, `deb/pilothoused.service`, `rpm/pilothouse.pam` and
`rpm/pilothoused.service` — are compiled into the package with `//go:embed` and
read through the unexported `sourceBytes(name)`, which panics on a name that is
not embedded (the set is fixed at compile time, so a miss is a programming
error, not runtime input). This embed is why the artifact-contract code lives
in `packaging/` rather than under `internal/`: a `//go:embed` pattern may not
contain `..`, and `Verify`'s signature is fixed, so there is no seam through
which an `fs.FS` could be injected instead. The embedded bytes are consumed by
`Verify`'s content comparison (below) and by the test fixture, which builds a
contract-satisfying `Model` from the same real repository sources — neither
reads the working tree.

**The requirement table** (`packaging/contract.go`). `requirements(format)`
returns the contract table for a format, and `false` for a format this package
does not know. Both `deb` and `rpm` require the same ten destinations:
`/usr/lib/systemd/system/pilothouse.service`,
`/usr/lib/systemd/system/pilothoused.service`, `/etc/pam.d/pilothouse`,
`/usr/lib/sysusers.d/pilothouse.conf`, `/etc/pilothouse`,
`/etc/pilothouse/storage/credentials`, `/etc/pilothouse/pilothouse.env`,
`/etc/pilothouse/pilothoused.env`, `/usr/bin/pilothouse` and
`/usr/bin/pilothoused`. A row also carries the mode the contract pins for the
destination, whether the packaging metadata must designate it a configuration
file, and the embedded repository source whose bytes the entry must equal. The
table's rows carry only the fields the checks that exist actually read; a field
is added by the change that first asserts on it, so no field is ever dead.

A row's `mode` is zero for every destination `.goreleaser.yaml` gives no
`file_info`, and zero means "the contract states no mode, assert nothing" —
inventing a default would pin something the source of truth does not state. The
four destinations that do carry one are `/etc/pilothouse` (**0750**),
`/etc/pilothouse/storage/credentials` (**0700**),
`/etc/pilothouse/pilothouse.env` (**0640**) and
`/etc/pilothouse/pilothoused.env` (**0640**). The three config-designated
destinations are `/etc/pam.d/pilothouse`, `/etc/pilothouse/pilothouse.env` and
`/etc/pilothouse/pilothoused.env`. Both sets are identical in `deb` and `rpm`.

**Provenance: which entries are byte-compared, and which are deliberately
not.** A row's `source` names the embedded repository source the destination is
built from, and the empty string means "the contract compares no content here".
**Six** destinations are byte-compared, and the `deb` and `rpm` tables differ in
exactly the two rows whose source is per-format:

| Destination | `deb` source | `rpm` source |
| --- | --- | --- |
| `/usr/lib/systemd/system/pilothouse.service` | `pilothouse.service` | `pilothouse.service` |
| `/usr/lib/systemd/system/pilothoused.service` | `deb/pilothoused.service` | `rpm/pilothoused.service` |
| `/etc/pam.d/pilothouse` | `pilothouse.pam` | `rpm/pilothouse.pam` |
| `/usr/lib/sysusers.d/pilothouse.conf` | `pilothouse.sysusers` | `pilothouse.sysusers` |
| `/etc/pilothouse/pilothouse.env` | `pilothouse.env` | `pilothouse.env` |
| `/etc/pilothouse/pilothoused.env` | `pilothoused.env` | `pilothoused.env` |

The **four** destinations that are deliberately *not* content-compared are
`/usr/bin/pilothouse` and `/usr/bin/pilothoused` — a binary's bytes differ per
build (version stamps, build IDs, toolchain), so only its destination and
multiplicity are contract-relevant — and the two directory entries
`/etc/pilothouse` and `/etc/pilothouse/storage/credentials`, which have no
content at all. Whatever an extractor records at those four, including nothing,
is not a finding.

**The forbidden roots, and the deliberate divergence from the
configuration-level check** (`packaging/contract.go`, `packaging/verify.go`).
`forbiddenRoots` is `/run/pilothouse` and `/var/lib/pilothouse` — the two
directories the broker unit owns through `RuntimeDirectory=` and
`StateDirectory=`. systemd creates and removes them at unit start and stop with
the ownership the broker needs, so a package-owned copy would fight it and the
package must install **nothing** there. The slice is named `forbiddenRoots`
rather than the obvious `systemdManagedPaths` because
`goreleaser_config_test.go` already declares that name in this same package,
the same collision `contractDependencies` avoids.

Containment is **component-aware**: a destination `d` violates a root `m` when
`d == m` or `strings.HasPrefix(d, m+"/")`. Whole path components are compared,
so both the bare root and a nested descendant (`/run/pilothouse/broker.sock`,
`/var/lib/pilothouse/storage/mounts`) are reported, and a sibling that merely
shares a textual prefix — `/run/pilothouse-helper` — is not.

That rule is **intentionally narrower** than
`goreleaser_config_test.go`'s configuration-level `checkNoSystemdManagedPaths`,
whose plain `strings.HasPrefix` over the same two roots *does* reject
`/run/pilothouse-helper`, on purpose and as its own comment states: a
destination written into `.goreleaser.yaml` that merely looks like a managed
root is far likelier to be a typo for it than a deliberate path, and this
repository configures none. The two are **not in conflict** — anything
genuinely nested under a root is rejected by both, and they differ only on
names sharing a textual prefix without sharing a path component, where the
configuration check is the stricter of the two. The narrower rule belongs in
`Verify` because it judges a real artifact's payload, where an entry at
`/run/pilothouse-helper` is a path the package genuinely owns and no
systemd-managed directory is being fought over. **Do not "harmonize" the two**:
`checkNoSystemdManagedPaths` and the `systemdManagedPaths` slice it reads stay
exactly as they are, and `packaging/verify.go` carries a comment saying so, so
that neither side is "fixed" into the other later.

**The scriptlet source** (`packaging/contract.go`). The postinstall scriptlet's
expectation is the lone constant `postinstallSource = "postinstall.sh"` rather
than a row in the requirement table, because the scriptlet is not path-scoped:
nfpm's `scripts` key is a single value with no destination and no per-format
variant, so every field a row carries — `dest`, `mode`, `config` — is
meaningless for it. Its bytes come from the same `//go:embed` as the payload
sources, so the scriptlet assertions read nothing at run time. (Contrast
`packaging/postinstall_test.go`, which deliberately `os.ReadFile`s the same
script and execs `shellcheck` and `/bin/sh` against it: that file proves the
script's *behavior*, this package proves the artifact *ships* it.)

**The dependency lists** (`packaging/contract.go`). `contractDependencies(format)`
returns the runtime dependency list the contract requires for a format, and
`false` for a format this package does not know. It is named
`contractDependencies` and not the more obvious `wantDependencies` because
`goreleaser_config_test.go` already declares `wantDependencies` in this same
package for the configuration-level assertion. Each list names the **direct**
provider of the same six runtime roles on both platforms — the linked C
library, the PAM shared library `pilothoused` links via cgo, the package
providing the PAM modules the policy loads, the package providing the PAM
stacks the policy includes, the libsystemd shared library, and systemd itself:

| Role | `deb` | `rpm` |
| --- | --- | --- |
| C library | `libc6` | `glibc` |
| PAM shared library | `libpam0g` | `pam-libs` |
| PAM modules | `libpam-modules` | `pam` |
| PAM stacks the policy includes | `libpam-runtime` | `authselect-libs` |
| libsystemd shared library | `libsystemd0` | `systemd-libs` |
| systemd | `systemd` | `systemd` |

**Provenance:** both lists are hand-written constants transcribed from
`.goreleaser.yaml`'s per-format `overrides.<format>.dependencies` at
`b1294e1`. No non-test file in this package reads that file — keeping the
expectation hand-written is what makes it an independent statement of the
contract rather than a restatement of whatever the config happens to say. Tying
the two together mechanically is the job of the drift guards in
`packaging/drift_test.go` ("The drift guards" below), which do read the live
config.

**Comparison, order-independent and multiplicity-sensitive.** `Verify` compares
*sorted clones* of the declared and the expected list with `slices.Equal`,
never mutating the model. Nothing in the contract fixes the order the packaging
metadata lists dependencies in, so order is not asserted; but the comparison is
on slices, not set membership, so a missing, extra, **duplicated** or misspelled
element all fail — including a list that repeats one name and omits another,
whose set of names would still be a subset of the contract's.

**The alternatives rule (N2).** Debian permits alternatives
(`libc6 | libc6-udeb`), which satisfy a requirement only by accident of which
alternative the resolver picks. The contract requires plain package names, so
**any declared expression containing `|` is reported on its own**, independently
of the sorted comparison and once per offending expression. A list carrying both
faults — an otherwise-correct list with one element rewritten as an alternative
— therefore produces two findings: the whole-list mismatch, and one naming that
expression.

**`Verify`** (`packaging/verify.go`). The signature is
`func Verify(m Model) []Finding`, fixed by the issue. **`Verify` accumulates:**
no check returns early and no check is skipped because an earlier one produced
a finding, so the result holds every independent violation the model exhibits,
and a nil or empty result means the model satisfies the contract — at this
commit every assertion the contract calls for is implemented, so a clean result
is a complete verdict rather than a partial one.

`Verify` performs exactly ten checks at this commit:

- **`unknown_format`** when `Format` is neither `deb` nor `rpm`, including the
  zero-value `Model`. The finding is not path-scoped, so its `Path` is empty.
  An unknown format produces no other finding: the contract for such a package
  is unknown, not violated.
- **`missing_path`**, with `Path` set to the destination, for each of the ten
  required destinations no entry installs to. A file installed somewhere else —
  a binary under `/usr/local/bin`, say — leaves its contract destination
  uninstalled and is reported this way.
- **`duplicate_entry`**, with `Path` set to the destination, when two or more
  entries install to the same required destination — the multiplicity half of
  "exactly one entry at `/usr/bin/pilothouse` and one at
  `/usr/bin/pilothoused`", applied uniformly to all ten. Exactly **one**
  finding is emitted per duplicated destination however many copies there are:
  findings are identified by their `Code`/`Path` pair, so the N−1 further
  identical findings a per-copy rule would emit carry no information.
- **`wrong_mode`**, with `Path` set to the destination and a `Message` naming
  want and got in octal, when the entry's mode differs from the one the
  contract pins. Only the four modes listed above are asserted.
- **`missing_config_flag`**, with `Path` set to the destination, when an entry
  at one of the three config-designated destinations has `Config` false.
- **`wrong_content`**, with `Path` set to the destination and a `Message`
  naming the expected `packaging/<source>`, when the entry's bytes are not
  exactly the bytes of the source the table pins for that destination in that
  format. Only the six byte-compared destinations above are checked. This is
  the check that makes "the `rpm` shipped the `deb`'s PAM file" detectable at
  all: such a package installs a valid file at the right destination with the
  right mode and config designation, and nothing but the bytes gives it away.
- **`dependency_mismatch`**, with `Path` **empty** — a dependency concerns no
  destination — in the two independent shapes described above: one finding
  naming got and want when the sorted declared list differs from the format's,
  and one further finding per declared expression containing `|`, naming that
  expression. The check is skipped entirely for an unknown format, for the same
  reason the destination checks are: the contract for such a package is
  unknown, not violated.
- **`missing_scriptlet`**, with `Path` **empty**, when `Postinstall` is nil —
  the model's representation of "this package ships no postinstall scriptlet".
  The distinct code (rather than `missing_path`) exists because the scriptlet
  has no destination to name and because `cmd/verify-packages` has to tell "no
  scriptlet" apart from "wrong scriptlet".
- **`wrong_content`**, with `Path` **also empty** and a `Message` naming
  `packaging/postinstall.sh`, when the scriptlet's bytes are not that embedded
  source's. This is the one `wrong_content` finding that is not path-scoped;
  the empty `Path` is exactly what distinguishes it from a payload entry's.
  Both scriptlet checks are gated on a known format for the same reason
  everything else is.
- **`forbidden_path`**, with `Path` set to the *offending entry's own*
  destination (not the root it violates), for every entry installed to a
  systemd-managed root or to anything nested under one — see the forbidden
  roots below. Exactly **one** finding per offending destination however many
  entries install there, for the same (`Code`, `Path`) reason
  `duplicate_entry` is reported once per destination, and gated on a known
  format like everything else. An entry at a destination the contract neither
  requires nor forbids is *not* a finding: a real `.deb`/`.rpm` also carries
  `/usr/share/doc` and similar tooling artifacts, so the contract is "these
  files, correct, and never the forbidden roots", not "exactly these files and
  nothing else".

Three rules keep the mode, config and content checks honest:

- **Permission bits only.** Modes are compared as `Entry.Mode.Perm()`, so an
  extractor that sets `fs.ModeDir` on the two directory entries is not falsely
  reported; type bits are not part of the contract.
- **An unexpected config designation is not a finding.** The contract pins a
  minimum and the vocabulary has no `unexpected_config_flag`, so
  `missing_config_flag` is the only asserted direction — designating, say, the
  sysusers file a config file passes.
- **The *payload* content comparison is exact and normalizes nothing.** It is a
  plain `bytes.Equal` against the embedded source: a payload entry extracted
  from a real archive carries the shipped bytes verbatim, so a single added
  newline is reported like any other difference. A byte-compared entry whose
  `Content` is `nil` is reported too, since every embedded source is non-empty —
  an extractor that failed to capture the bytes must not verify clean, or
  `packaging/extract`'s own bugs become invisible.

A duplicate does not suppress the other checks for its destination: `Verify`
evaluates the first entry installing there for mode, config designation and
content, and still accumulates.

**The scriptlet rule: exact bytes, with at most one trailing newline
normalized.** The scriptlet comparison is the single place any normalization
happens, and it is deliberately as narrow as it can be: `trimFinalNewline`
applies `bytes.TrimSuffix(x, []byte("\n"))` — **at most one** trailing `"\n"` —
to each side, and then compares with the same exact `bytes.Equal`.
`packaging/postinstall.sh` ends with exactly one newline, so the rule has
exactly three consequences, all intended:

| Scriptlet `Content` | Result |
| --- | --- |
| the script with its single trailing newline removed | clean |
| the script exactly as the repository holds it | clean |
| the script with one further newline appended | `wrong_content` |

That asymmetry is the design, not an oversight. The normalization exists for
one reason only — whether a script is shipped with or without a *final* newline
is not a contract violation, and an extractor may present it either way. The
rejected alternative, `bytes.TrimRight(x, "\n")`, strips *all* trailing
newlines and would silently accept a script padded with arbitrary blank lines:
a real byte difference in a file whose entire contract is "ships exactly these
bytes".

**No shell parsing.** Nothing in the scriptlet check tokenizes shell, applies a
regular expression to the script, or looks for any individual command inside
it — `grep -nE 'chown|chmod|systemd-sysusers' packaging/verify.go` prints
nothing. That is not a gap to be filled later. The script's fail-fast,
idempotent, presence-guarded repairs are already proven by
`packaging/postinstall_test.go`, which runs the real script; all this package
has to add is that the artifact ships exactly those bytes (M1 above).

**The accumulate guarantee, demonstrated end to end.** Those ten checks are the
whole of the contract `Verify` asserts — every assertion the artifact contract
calls for is implemented. What the drift guards below add is not a further
`Verify` check: they judge the contract *tables* against `.goreleaser.yaml`,
never a `Model`.
Because the check list is complete, the accumulate guarantee is now proven
rather than merely stated: `TestVerifyAccumulatesEveryFaultAtOnce` seeds **one**
model with eight unrelated faults spanning **seven** distinct codes — a missing
directory, a wrong env-file mode, a cleared PAM config designation, a perturbed
unit file, a duplicated binary, a dropped dependency, a mutated scriptlet and an
entry under a forbidden root — and matches the whole result as a **set** of
(`Code`, `Path`) pairs with `require.ElementsMatch`, independent of order and
index. No check may return early or be skipped because an earlier one fired, or
that assertion loses pairs. `TestVerifyProducesEveryFindingCode` closes the
other half: each of the nine codes declared in `packaging/finding.go` is
produced by a model that breaks exactly the thing that code names.

`packaging/verify_test.go` holds the behavioral tests: the shared
`contractModel(t, format)` fixture that every mutation starts from, a
`findingsFor(findings, code, path)` matcher (findings are matched by the
`Code`/`Path` pair; a `Message` is read in only two places, each matching a
required substring and never the surrounding wording), and table-driven
mutations covering
each required destination in each format (missing, then duplicated), a
relocated binary in each format, a wrong mode on each of the four pinned
destinations, a cleared config designation on each of the three, a perturbed
and then a `nil` `Content` on each of the six byte-compared destinations, and
pairwise combinations of unrelated faults (a duplicate with a missing path, a
wrong content with a missing path, a dependency or scriptlet fault with a
missing path) on top of the whole-model accumulation proof described above.
The dependency table is N2's five
faults in each format, ten cases in all: a missing, an extra, a duplicated and
a misspelled element, and an element rewritten as an alternative
(`libc6 | libc6-udeb` for `deb`, `glibc | glibc-minimal-langpack` for `rpm`),
with three companion tests pinning the rules those cases rest on — the
fixture's list reversed verifies clean (order-independence), replacing one
element with a duplicate of another is reported even though the list keeps its
length and every declared name remains one the contract expects
(multiplicity), and the alternative case reports two findings, one of which
names the offending expression (independence from the sorted comparison) — the
first of the two `Message` reads.

The scriptlet cases run in each format too: `Postinstall` set to nil yields
`missing_scriptlet` with an empty `Path`, and two byte mutations — a line
dropped and a command appended — each yield `wrong_content` with an empty
`Path` and a `Message` naming `packaging/postinstall.sh`, which is the second
`Message` read. Three further cases pin the newline rule exactly as implemented
(final newline removed → clean, shipped unchanged → clean, one extra newline
appended → `wrong_content`); nothing asserts that an appended newline verifies
clean, and the test first asserts the premise that `packaging/postinstall.sh`
ends with exactly one newline. Both scriptlet faults are also shown coexisting
with an unrelated `missing_path`, and a model with a broken scriptlet *and* a
broken payload entry produces two `wrong_content` findings told apart by `Path`
alone. The scriptlet mutations name their target positionally, never by what
the affected line does, which is the same posture the production check takes.

The forbidden-path cases use `withExtraEntry`, which adds an entry *alongside*
everything the contract requires (and fails if the fixture already installs
there) — a package owning a systemd-managed path ships an extra entry, it does
not relocate a contract one. Eight mutation cases run the two roots and the two
nested descendants in each format, each asserting `forbidden_path` at the
entry's own destination and nothing else; a further test pins one finding per
destination when two entries install to the same forbidden path. A dedicated
test then pins the deliberate divergence described above — an entry at
`/run/pilothouse-helper` or `/var/lib/pilothouse-helper` yields **no**
`forbidden_path` finding here, while `checkNoSystemdManagedPaths` rejects that
sibling at configuration level on purpose — and its comment, like
`packaging/verify.go`'s, records that the configuration test must not be
changed to match.

Two cross-format tests are the point of the payload
content check: an `rpm` model carrying `packaging/pilothouse.pam`'s
bytes and a `deb` model carrying `packaging/rpm/pilothouse.pam`'s are each
reported at `/etc/pam.d/pilothouse`, and the same pair of substitutions is made
for the broker unit at `/usr/lib/systemd/system/pilothoused.service`. Four
tests pin the deliberate silences: an `fs.ModeDir`-bearing directory entry
verifies clean, changing the mode of a unit file or a binary produces nothing,
a config designation on the sysusers file produces nothing, and arbitrary or
`nil` content on either binary or either directory produces nothing. Its
expected destinations, modes, config designations, byte-compared set,
dependency names and forbidden roots are written out by hand rather than read
back from `requirements`, `contractDependencies` or `forbiddenRoots`, since
those tables are the thing
under test and may not also be the oracle — `postinstallSource` included, whose
value is written out as a literal in the scriptlet tests. The mutation helpers'
vacuity guards are load-bearing here: `withContent` fails if the bytes it
installs are the ones the entry already carried, so the cross-format tests
cannot silently degrade to no-ops if the two formats' sources ever converge,
and `withScriptlet`/`withoutScriptlet` fail the same way if the fixture ever
stops shipping a scriptlet or already carried the mutated bytes.

**The drift guards** (`packaging/drift_test.go`). Everything above rests on
tables that were *transcribed by hand* from `.goreleaser.yaml`. Two guards keep
the transcription honest, and they live in
their own file because that file is the one *artifact-contract* file that reads
the working tree — see the four tiers below, which are what keep that
statement from being confused with a claim about the whole package.

They are **not** a restatement of `goreleaser_config_test.go`. That test asserts
*the config* matches hand-written expectations; these assert *`Verify`'s tables*
match the live config. Opposite direction, and the only thing standing between
`contract.go` and silent divergence
(`docs/agents/skills/completeness-tests-need-live-source-of-truth.md` is why an
embedded snapshot of the YAML would not do: the snapshot and the tables would
drift together, undetected).

**Guard 1 — `TestContractTablesMatchGoreleaserConfig`.** It parses
`../.goreleaser.yaml` through `goreleaser_config_test.go`'s existing
`goreleaserConfigPath` constant and its `loadNFPMEntry`, `loadOverride`,
`normalizeSrc` and `entriesWithDst` helpers, so it holds no second copy of the
configuration. Per format it asserts:

- **Converse direction.** Every `dst` a format's override actually packages
  appears in `requirements(format)`, so adding a packaged file to
  `.goreleaser.yaml` without updating `contract.go` fails here.
- **Forward direction.** Each of the **eight** requirements whose `dest` is not
  under `/usr/bin` is installed by exactly one live entry, and the row's three
  metadata fields agree with it. Two of the three comparisons are conditional,
  because the config has an absent case for each:
  - **source** — when the row's `source` is empty the entry must carry no `src`
    *and* be `type: dir`; otherwise `normalizeSrc(entry.Src)` must equal
    `"packaging/" + source`. The split is forced: the two directory entries
    (`/etc/pilothouse`, `/etc/pilothouse/storage/credentials`) are the four
    `type: dir` entries in the file — two per format — and none carries a `src`
    key, and `normalizeSrc("")` is `""`, which can never equal `"packaging/"`.
    The `type: dir` half is what keeps the directory branch from degenerating
    into "assert nothing".
  - **mode** — when the entry has `file_info` the row's mode must equal
    `fs.FileMode(entry.FileInfo.Mode)` (the field is an `int` holding the YAML
    octal literal, so `0750` arrives as 488); when it has none the row's mode
    must be `0`, which is the "the contract states no mode" encoding.
  - **config** — the total comparison `requirement.config == (entry.Type ==
    "config")`, so both an unrequired designation and a missing one fail.
- **Dependencies.** `contractDependencies(format)` equals the live
  `overrides.<format>.dependencies`, both sorted.
- **Scriptlet source.** `scripts.postinstall` is `./packaging/postinstall.sh` —
  the file `postinstallSource` names and whose bytes the scriptlet check
  compares against.

**What guard 1 deliberately does not cover.** The two binary destinations
`/usr/bin/pilothouse` and `/usr/bin/pilothoused` appear in no assertion above.
They are nFPM **build outputs**, not override contents: goreleaser installs each
`builds[]` entry's binary itself, so neither destination is written into
`overrides.<format>.contents` and neither exists in the config this guard walks.
That is a gap in what the config can be asked, not a hole to be closed by
widening the guard — guard 2 is their cover, and the guard's doc comment says
so.

**Guard 2 — `TestBinaryDestinationsMatchBuilds`.** The existing parser discards
the `builds` section, so this guard declares its own minimal local types
(`binaryProvenanceConfig`, `buildTarget`, `bindirEntry` — names chosen not to
collide with `goreleaserConfig`, `nfpmEntry`, `contentEntry`, `fileInfo`,
`formatOverride` or `nfpmScripts`) and parses the same path through the same
reused `goreleaserConfigPath` constant. It asserts the whole chain that makes
`/usr/bin/<binary>` a contract destination in the first place: `builds[].binary`
is exactly the two-element set `{pilothouse, pilothoused}`; `nfpms[0].bindir` is
unset, so nFPM's `/usr/bin` default applies; the **complete multiset** of
`requirements(format)` destinations under `/usr/bin` equals the set the builds
produce, in **both** formats; and no override content entry in either format
installs to `/usr/bin` or anything under it — which is what confirms the
binaries are genuinely outside guard 1's reach rather than merely overlooked by
it.

The multiset comparison is deliberate and the guard is wrong without it. Merely
asserting that each expected binary has exactly one requirement leaves the check
one-directional: guard 1 skips every `/usr/bin` row, so `contract.go` could gain
an unsupported row such as `/usr/bin/extra` and pass both guards. Comparing the
whole sorted set rejects a missing destination, a duplicate, and an extra one
alike.

Neither guard has a companion mutation test, and that is deliberate: the thing
they guard is `contract.go` itself, so the way to demonstrate they fire is a
*temporary local edit to its tables* (a changed source name, mode, config flag,
dependency or binary destination, or a deleted row), never a checked-in mutation
of `.goreleaser.yaml`.

**Artifact-contract portability: four tiers, and why they must not be
blurred.** The one claim
that holds without qualification across all of this work is: **no file added by
the artifact-contract phase executes an external command.** That phase is #70's,
and it added only files in `packaging/` itself; the extractors added since are a
*separate package* and are tier (d) below, and `cmd/verify-packages` — which
runs them — is not under `packaging/` at all. Below that, the four tiers
differ, and a sentence true of one is false of another.

- **(a) The contract model, `Verify`, and their behavioral tests** —
  `packaging/model.go`, `finding.go`, `contract.go`, `verify.go`,
  `finding_test.go` and `verify_test.go`. Pure Go over bytes embedded at compile
  time by `//go:embed`. They read **no file** and run **no command** at run
  time; every expected byte comes from the FS compiled into the test binary.
  `grep -nE 'os\.ReadFile|os\.Open|os\.Stat|os/exec|exec\.Command'` over those
  six files prints nothing.
- **(b) The drift guards** — `packaging/drift_test.go`, plus
  `goreleaser_config_test.go` (which declares the `goreleaserConfigPath`
  constant and the loader they share) and the drift guard in
  `verify_install_test.go`. These files **do** read the live
  `../.goreleaser.yaml` from the working tree, deliberately: a guard
  compared against an embedded snapshot could not detect drift at all. Reading
  the live config is the named exception, they read that one path plus, in
  `verify_install_test.go`'s case, `verify-install.sh` itself, and
  `drift_test.go` and `goreleaser_config_test.go` still run **no external
  command** — so they run under a plain `go test ./packaging/` on any machine
  with the repository checked out.
- **(c) The pre-existing configuration-level tests and the install-script
  guards** — `packaging/units_test.go`, `packaging/postinstall_test.go` and
  `packaging/verify_install_test.go`. These **do** exec external tools:
  `systemd-analyze` (`units_test.go`), `shellcheck`
  plus `/bin/sh` (`postinstall_test.go`, which runs the real scriptlet against a
  `t.TempDir()`), and `shellcheck` again (`verify_install_test.go`, which only
  lints `verify-install.sh` — it never executes it, because that script needs a
  package manager and a network). The optional tools are resolved with
  `exec.LookPath` and
  their tests **skip with an explanatory message** when the tool is absent from
  `PATH`; `/bin/sh` is invoked directly, as a POSIX shell is assumed present.
  The skipping is exactly why **`make docker-ci` remains the full gate** — the
  dev image installs the systemd package and `shellcheck`, so those checks
  actually run there and quietly skip elsewhere.
- **(d) The extractors** — `packaging/extract/`. A **different package**,
  and the only tier here whose *non-test* code runs an external command: `Deb`
  invokes `dpkg-deb` three times, and `RPM` runs four `rpm -qp` queries plus one
  `rpm2archive`-into-`tar` pipe. It is a subpackage precisely so the
  guarantee below stays scoped to `packaging/*.go` — the glob is **not**
  recursive, so `packaging/extract/*.go` is outside it and tier (d) added
  nothing to that listing. Its tool-dependent tests resolve every tool through
  `internal/packagingtest.LookTool`, so they skip with an explicit reason on a
  host without it and **fail** rather than skip inside `make docker-ci`; the deb
  missing-tool test drives `PATH=""`, and the internal tables over the
  unexported `conffiles`/`Depends` parsers and over the rpm file-table,
  dependency-pairing and postinstall parsers read bytes the test itself staged,
  so none of them needs a tool on any host.

Because of (c), no sentence anywhere may claim that the `packaging` package's
*whole test suite* runs no external command, nor that the contract tests *as a
group* operate only over embedded bytes — the drift guards do not. Claims have
to name the tier they describe. At this commit,
`grep -lE 'os/exec|exec\.Command' packaging/*.go` lists exactly
`postinstall_test.go`, `units_test.go`, `verify_install_test.go` and
`vm_harness_test.go`. The extractors in tier (d) and the image-test subpackage
remain outside that non-recursive listing. Within `packaging/imagetest`,
`image_child_test.go` imports `os/exec`, while `image_process_test.go` is a
textual match only because its adversarial source fixtures contain both search
terms. The VM and image harnesses are later test-infrastructure layers, not a
fifth artifact-contract tier, and these greps are source inventories rather
than proof of which files execute children.


**Still out of scope for this package.**

- **Reading real `.deb`/`.rpm` files.** Nothing in `packaging/` itself opens an
  artifact. The extractors that populate a `Model` from a built `.deb` and from
  a built `.rpm` both exist one directory down, in `packaging/extract`
  (tier (d) above), and the command that runs them and reports the resulting
  `Finding`s is `cmd/verify-packages` (see "The command" in
  [../design/artifact-extraction.md](../design/artifact-extraction.md)). Neither is
  reachable from this package: `packaging/` imports neither, and the dependency
  runs the other way.
- **Building the packages.** `make package` builds them locally with goreleaser
  Pro, and the CI packaging job is **#72**'s; this package is exercised by
  `go test` alone.
- **On-disk state after a real install.** No Go code in this package installs
  anything. Package validation splits in two here. **Layer A** is the
  container-level install check: the shell script `packaging/verify-install.sh`
  ([../design/install-validation.md](../design/install-validation.md)), which
  this package's Go tests only read as
  text, run by `make verify-package-install` locally and by
  `.github/workflows/packaging.yml`'s `install` job in CI. **Layer B** — VM
  installs and booted-host verification, anything needing systemd as PID 1 or
  an enforcing SELinux policy — is **#67**'s; its harness lives
  in `test/vm` (it boots a guest, installs the package, starts both units,
  asserts the systemd-created directories, the broker socket's ownership and
  mode, that the broker is live, that PAM authenticates a real non-root
  administrator through the running stack (with each login-evidence cursor
  captured from the same unit-filtered journal stream that consumes it), that
  the daemon reads a record it emitted itself back through the broker's
  journal query, and that the
  posture survives a real reboot) and `.github/workflows/packaging.yml`'s
  `vm-boot` job runs it; see M1 above
  for why an artifact cannot prove ownership and why `Entry.Owner`/
  `Entry.Group` therefore drive no assertion.

