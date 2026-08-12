# Artifact extraction and `verify-packages` (`packaging/extract`, `cmd/verify-packages`)

Living document; extracted verbatim from [overview.md](overview.md) (formerly
`yeti/OVERVIEW.md`). It covers how a real `.deb`/`.rpm` on disk becomes a
`packaging.Model` — the two extractor backends and their measured tool
behaviors — plus the `cmd/verify-packages` command, the `make
verify-packages`/`make package` targets, and the CI packaging gate that runs
them. The contract the resulting model is judged against is
[../specs/artifact-contract.md](../specs/artifact-contract.md); the fixture
builders its tests use are
[packaging-test-fixtures.md](packaging-test-fixtures.md).

## The extractors

`packaging/extract` is a subpackage whose only job is to turn a real artifact on
disk into a `packaging.Model`. At this commit it holds exactly two backends,
`Deb` and `RPM`. The command that runs them, `cmd/verify-packages`, exists as of
this commit and is described under "The command" below, and as of this commit
`make verify-packages` is the target that runs it; that target is wired into
neither `ci` nor `docker-ci`, so nothing in the repository invokes the command
automatically.
Nothing here decides whether a model is acceptable — `packaging.Verify` remains
the sole source of `Finding`s, and moving one of its assertions down into an
extractor would be a defect, because that separation is what keeps every
contract assertion provable on a host with no packaging tool installed.

**Why a subpackage, not more files in `packaging/`.** Three structural reasons,
not stylistic ones:

- The `grep -lE 'os/exec|exec\.Command' packaging/*.go` guarantee (pinned in
  [../specs/artifact-contract.md](../specs/artifact-contract.md)) is a
  **non-recursive** glob, so every `exec.Command` this package adds leaves it
  exactly true. An extractor placed in `packaging/` would have falsified it,
  along with `model.go`'s "they are inert data" and `contract.go`'s "nothing
  here opens a file at run time".
- `requirements`, `contractDependencies`, `forbiddenRoots`, `sourceBytes` and
  `postinstallSource` are unexported. From another package they are not merely
  off limits, they are **invisible**, so contract logic cannot leak into an
  extractor by accident.
- `packaging/drift_test.go` already declares `usrBinDir` and `underUsrBin` in
  package `packaging`. Those are test-only and belong to the drift guard; they
  stay exactly where they are, and `packaging/extract` declares its own copies
  with a comment recording the deliberate duplication.

`packaging` itself cannot move the other way: a `//go:embed` pattern may not name
a parent directory, so only a package rooted at `packaging/` can embed
`packaging/*`.

**Shared helpers and the error-text contract** (`packaging/extract/extract.go`).
`ErrToolUnavailable` is wrapped into every error caused by a tool that is not on
`PATH`, so `errors.Is` separates an environmental fault from a definitive one: a
tool that ran and rejected the artifact does **not** report it. Two unexported
helpers do the work — `lookTool`, which resolves a tool, and `runTool`, which
runs one with `exec.CommandContext` under the caller's context and folds its
standard error into the returned error. Every error either one returns carries
the literal prefix `packaging/extract: <tool>: `, for example
`packaging/extract: dpkg-deb: packaging tool unavailable`. The prefix is produced
inside those two helpers and never at a call site, so no path can return a tool
error without it, and it is a token an artifact's filename cannot forge —
matching on the bare tool name would be satisfied by any file called `b.rpm`.
Folding standard error in is also what puts the offending artifact's path into
the message, since `dpkg-deb` names the file it could not read there.

**The deb backend** (`packaging/extract/deb.go`). `Deb(ctx, path)` runs
`dpkg-deb` three separate times under `ctx`, once per thing it needs, and caches
nothing between them:

- `dpkg-deb -x <deb> <dir>` extracts the payload into scratch space. That
  extraction was measured to reproduce recorded modes exactly (0640, 0700 and
  0750 under `umask 077`), which is why entry modes are read off the extracted
  tree rather than parsed out of the `drwxr-x---` strings `dpkg-deb -c` prints.
- `dpkg-deb -e <deb> <dir>` extracts the control members. It writes a
  **directory** and emits no tar stream, so a `-e … | tar -xO postinst` pipeline
  is not a valid way to read one of them.
- `dpkg-deb -f <deb> Depends` reads the dependency field. A package declaring no
  dependencies exits 0 with empty output, so absence is not a failure.

Neither `dpkg-deb -c` nor `-I` is used: the extracted tree already carries exact
modes, and `-f` gives the one field needed. `dpkg-deb -c` *is* used one package
over, in `internal/packagingtest`'s own tests, as an independent oracle for the
fixture builder — a different role in a different package.

Entries come from a `filepath.WalkDir` over the extracted payload with the tree
root itself skipped, so `/` is never an entry, while the intermediate
directories `dpkg-deb` synthesizes for a declared path **are** — a package
rooted at `/opt/phx/…` and `/usr/bin/…` archives `./opt/`, `./opt/phx/`,
`./usr/` and `./usr/bin/` too, and each becomes an entry. Directory entries
carry `Mode` and nil `Content`; regular files carry their bytes except those
under `/usr/bin`, whose nil `Content` means "deliberately not captured" rather
than "empty file"; symlinks, devices and fifos are not emitted at all, so a
required destination shipped in one of those shapes surfaces as a loud
`missing_path` rather than being silently accepted. `Config` is true for the
destinations the `conffiles` control member lists, and a `conffiles` line that is
not an absolute path is an error rather than a dropped row — an artifact read
wrong must not come back as a confident model. `Postinstall` is a non-nil
`Scriptlet` exactly when a `postinst` control member exists and `nil` when it does
not, keeping "ships none" distinct from "ships the wrong bytes". `Dependencies`
are split on commas only and trimmed, so alternatives (`a | b`) and version
constraints (`c (>= 1)`) pass through verbatim in declaration order; splitting on
`|` would make `Verify`'s rule against alternatives unfireable. `Owner` and
`Group` are left empty for a deb, because a `dpkg-deb`-extracted tree cannot
recover the archive's recorded ownership and nFPM's DEB tar records numeric
UID/GID 0 anyway — both fields are informational per M1.

**What failure means for `Deb`.** Everything in this paragraph is a statement
about the deb backend alone; the rpm backend's outcomes are stated separately
with that backend below, and none of this is a claim about extraction in
general. `Deb`
distinguishes three outcomes. (1) **Absent optional metadata is not a failure.**
A package with no `Depends` field returns no dependencies and a nil error,
because `dpkg-deb -f <deb> Depends` exits 0 with empty output there; a package
with no `conffiles` member marks no entry `Config`; a package with no `postinst`
member returns a nil `Postinstall`, which stays distinct from the non-nil empty
`Scriptlet` a zero-byte member returns. (2) **A definitive fault returns a
non-nil error and the zero-value `packaging.Model`** — never a confidently empty
model, which would otherwise verify as a pile of absent paths indistinguishable
from a genuinely broken package. A nonexistent path and a file that is not an
`ar` archive both land here, and both name the offending artifact, because
`dpkg-deb` writes the filename to standard error and `runTool` folds it in.
Metadata that is present but malformed lands here too: a `conffiles` line that
is not an absolute path is an error rather than a row silently defaulted to "not
a configuration file". (3) **An environmental fault also carries
`ErrToolUnavailable`**, so `errors.Is` separates "this host has no `dpkg-deb`"
from "`dpkg-deb` ran and rejected the artifact". Every error that comes from
*running* the tool — outcome (3), and the two archive faults in (2) — carries the
`packaging/extract: dpkg-deb: ` prefix, because `runTool` produces it. The
malformed-`conffiles` error does not: no tool failed there, so it reads
`packaging/extract: conffiles entry "…" is not an absolute path` and names the
offending line. Only (3) satisfies `errors.Is(err, ErrToolUnavailable)`.

**The deb backend's tests** (`packaging/extract/deb_test.go`,
`extract_test.go`). The happy
path builds a throwaway `.deb` with `packagingtest.BuildDeb` into a
`t.TempDir()` and asserts every model field against literals hand-written from
the `Spec` the test itself declares — never against a value the extractor,
`Verify` or a contract helper produced. The destination assertion is **set
equality in both directions**, including those synthesized parents and excluding
`/`: a one-directional membership check would pass while the extractor invented
or dropped entries. Around it sits one fixture-backed row per outcome above: a
fixture with no config file, no `Depends` and no postinstall; one whose
postinstall is a zero-byte member; a nonexistent path and a file of arbitrary
non-`ar` bytes, both required to return the zero-value model; a `conffiles`
member holding a relative path, which needs `packagingtest.BuildDebRaw` because
`dpkg-deb` will not `--build` such a tree; and `t.Setenv("PATH", "")` for the
`ErrToolUnavailable` row, which needs no packaging tool and therefore executes
on every host with no skip. `extract_test.go` is an **internal** test (package
`extract`) holding table-driven tests over the unexported `conffiles` and
`Depends` parsers, reaching the shapes a real `dpkg-deb` cannot be made to emit
(an empty comma-separated component, a member of nothing but whitespace); they
supplement the fixture-backed tests rather than replacing them. Every
fixture-backed test resolves `dpkg-deb` through `packagingtest.LookTool`, so on
a host without it they skip with an explicit reason while the parser tables and
the `ErrToolUnavailable` row still run.

**The rpm backend** (`packaging/extract/rpm.go`). `RPM(ctx, path)` runs four
`rpm -qp` metadata queries under `ctx` and extracts the payload once. Nothing is
cached between them:

- the **file table**, in one query:
  `rpm -qp --qf '[%{FILENAMES}\t%{FILEMODES:octal}\t%{FILEFLAGS}\t%{FILEUSERNAME}\t%{FILEGROUPNAME}\n]' <rpm>`.
  Destination, mode, config bit, owner and group all come from this one query,
  so there is no separate `-qpc` pass for the configuration-file designation.
- `rpm -qp --requires <rpm>` for the dependency text, paired index-wise with
  `rpm -qp --qf '[%{REQUIREFLAGS}\n]' <rpm>`, a second query carrying no
  dependency text at all.
- `rpm -qp --qf '%|POSTIN?{HAS\n%{POSTIN}}:{NONE\n}|' <rpm>` for the postinstall
  body behind a tag-presence marker.
- `rpm2archive -n -` reading the artifact on standard input, piped into
  `tar -x --no-same-owner` for the payload bytes. The pipe is wired **in Go**,
  so no shell is invoked and **both** exit statuses are the verdict — a shell
  pipeline would report only the last one. Standard error is folded into an
  error message and is read for nothing else.

  **Why `rpm2archive` and not the older `rpm2cpio`:** nfpm writes
  `RPMTAG_ARCHIVESIZE` as the sum of the file *content* bytes, while `rpmbuild`
  writes the size of the whole uncompressed archive. rpm 4.18's `rpm2cpio`
  validates the bytes it emitted against that tag and exits 1 when they
  disagree — *after* emitting a complete, perfectly valid stream. Every fixture
  here is built by `rpmbuild`, so the disagreement only ever appears against a
  real nfpm artifact, which is exactly what the packaging gate feeds it.
  `rpm2archive` reads both without complaint. Reverting to `rpm2cpio` turns
  that gate red immediately.

**The pipe uses `os.Pipe`, not `StdoutPipe`, and waits on both halves at
once.** This is a correctness requirement, not a style choice. `StdoutPipe`
leaves the *parent* holding the read end, and an earlier implementation waited
on the producer before the consumer: when the consumer exited early, the
producer kept writing
into a full pipe, and because this process still held a reader it never took
`SIGPIPE` — so the wait never returned and extraction hung forever with no
deadline. Both ends are now `*os.File`, handed straight to the children with no
copying goroutine in between, and **this process closes both of its copies as
soon as both children are running**: dropping the read end is what lets
`SIGPIPE` reach a producer whose consumer has gone, and dropping the write end
is what gives the consumer its end of input. The two `Wait` calls then run
concurrently so neither can block the other's reaping.

Error precedence follows from the same asymmetry: the producer's status is
reported first, because a producer that dies mid-stream makes the consumer fail
too and is the more useful root cause — **except** when the producer died of
`SIGPIPE`, which means the consumer went first and the consumer's status is the
real explanation.

**Why the file table is a tab-delimited `--qf` and not rpm's positional dump
alias.** The alias *pads* its columns — an `X` where a symlink target would go,
an all-zero digest for a directory — so its field count is stable at eleven; an
earlier claim that its columns can be empty was wrong and is corrected here. The
reason it is not parsed is different and measured: **a destination containing a
space** (`/opt/phx/with space.txt`) yields **twelve** whitespace fields, so no
whitespace-positional scheme is sound, while the five-field tab query parses the
same package correctly. Two lesser reasons stand: the alias's column set is a
formatting detail rpm documents nowhere and has changed before, and it
pre-digests the config designation into an `isconfig` column where
`%{FILEFLAGS}` gives the raw bit, read with a named `rpmfileConfig = 1` constant
citing rpm's `RPMFILE_CONFIG`. The parse uses `strings.Split(line, "\t")` and
requires exactly five fields, because `Split` preserves empty fields where a
whitespace split would collapse a row whose owner name is empty; the mode is
`strconv.ParseUint(field, 8, 32)` — base 8 **explicitly**, so both `0100644` and
`%{FILEMODES:octal}`'s `100644` read as the same file; the flags field must be
decimal. Empty output and the literal `(none)` mean zero entries; anything else
parses strictly, and a wrong field count, a non-octal mode or a non-decimal
flags field is an error rather than a defaulted row.

**Why the postinstall body comes from the `POSTIN` header tag and not from
rpm's scriptlet report.** That report is human-readable prose with no escaping,
no quoting and no length prefix, so a `%post` body containing the lines
`preuninstall scriptlet (using /bin/sh):` and `postinstall program: /bin/sh`
renders them at column 0 **byte-identically to real headers** (measured on rpm
4.18). Any header-anchored terminator therefore truncates such a body, and the
truncated bytes can still be exactly what the contract expects — a package
shipping *extra* postinstall code would verify clean. No smarter regexp fixes
that: the information needed to tell body from header is not in the output. The
tag-presence marker cannot be fooled, because rpm emits it from a tag-presence
test, before the body, at a fixed position: the parse splits on the **first**
newline and nothing else, `NONE` → `Postinstall = nil` (the measured verdict for
both a missing and an empty `%post`), `HAS` → the remaining bytes verbatim, and
any other prefix is an error. It also disambiguates a body that *is* the literal
text `(none)`, which comes back as `HAS\n(none)`.

**Trailing newlines are rpm's and stay rpm's.** Measured, rpm strips **all**
trailing newlines from a recorded scriptlet (`echo one\n\n\n` → `echo one`). The
extractor returns exactly what rpm recorded and **must not** append one:
re-adding a byte the package does not contain is the same normalization hazard
that forbids rewriting dependency expressions. It is also unnecessary, because
`packaging.Verify`'s scriptlet check already compares
`trimFinalNewline(scriptlet.Content)` against `trimFinalNewline` of the embedded
source, so a package differing only in a final newline produces no finding.

**Dependencies are filtered by provenance flags, never by name.** A requires
entry is dropped **iff** rpm marked it `RPMSENSE_RPMLIB` (`1 << 24`) or
`RPMSENSE_INTERP` (`1 << 8`), both declared as named constants in `rpm.go`.
Both classes are written by the *builder*, not by any packaging author:
`rpmlib` capabilities make an older rpm refuse the package, and the `INTERP`
requirement is what `rpmbuild` derives from a scriptlet's interpreter — measured,
**any** `%post` adds a `/bin/sh` requirement with flag word `1280` even under
`AutoReqProv: no`. Filtering on provenance rather than on a name is what keeps
the check honest in both directions: a package that genuinely declares
`Requires: /bin/sh` records a *second* entry with flag word `0`, and **that one
survives** while the generated one is dropped — a name filter would return
neither, and no filter would return both. Everything surviving passes through
verbatim, version constraints and spacing included. The two queries are
index-parallel and a line-count mismatch is an **error**, never a best-effort
merge. rpm does not preserve declaration order (measured: a dependency declared
third comes back first), so every assertion about rpm dependencies compares
**sorted clones** — which is what `Verify` does too. RPM has no `|` alternative
syntax, so the alternative-preservation rule is a deb-only concern and is tested
there.

**What failure means for `RPM`.** This paragraph is a statement about the rpm
backend alone. Absent optional metadata is not a failure: a package with no
requires table, no owned files or no `%post` yields no dependencies, no entries
and a nil `Postinstall` respectively, because rpm answers each of those with
empty output or the literal `(none)`. Everything else parses **strictly** — a
file-table line whose tab-separated field count is not five, a mode that is not
octal, a non-decimal flags word, a requires/flags line-count mismatch and an
unrecognised postinstall presence marker are all errors, and each returns the
zero-value `packaging.Model` rather than a confidently partial one, which would
otherwise verify as a pile of absent paths indistinguishable from a genuinely
broken package. A tool that is not on `PATH` yields an error wrapping
`ErrToolUnavailable`; a tool that ran and rejected the artifact yields one that
does not, so `errors.Is` still separates "this host has no rpm" from "this file
is not an rpm".

**Where the two backends deliberately differ.** Each row is a statement about
one backend; none of it may be restated as one sentence about "the extractors".

| | `Deb` | `RPM` |
| --- | --- | --- |
| mode source | read off the tree `dpkg-deb -x` extracted, measured to reproduce recorded modes exactly | `%{FILEMODES:octal}` from the header, masked to `0o777`; setuid/setgid/sticky are not mapped to Go's `ModeSetuid`/`ModeSetgid`/`ModeSticky`, since the contract compares `Mode.Perm()` only |
| owner/group | left empty — a `dpkg-deb`-extracted tree cannot recover them | populated per entry from `%{FILEUSERNAME}`/`%{FILEGROUPNAME}` |
| intermediate directories | present — `dpkg-deb` archives `./opt/`, `./opt/phx/`, `./usr/` and `./usr/bin/` for a package rooted at `/opt/phx/…` and `/usr/bin/…`, and each becomes an entry | absent — rpm owns only what `%files` lists, so a package rooted at the same paths yields **no** entry for `/opt`, `/opt/phx`, `/usr` or `/usr/bin` |
| empty scriptlet | constructible: a zero-byte `postinst` member yields a non-nil `Scriptlet` with empty `Content`, distinct from a nil one | not constructible: an empty `%post` builds but records no body, and its `POSTIN` presence marker reads `NONE`, so it is indistinguishable from shipping none |
| config designation | the `conffiles` control member | the `RPMFILE_CONFIG` bit of the same file-table query |
| dependency order | preserved, since it comes from splitting one field | not preserved by rpm, so callers sort |

What both share: directory entries carry `Mode` and nil `Content`; regular files
carry their bytes except under `/usr/bin`, whose nil `Content` means
"deliberately not captured"; symlinks, devices and fifos are not emitted at all,
so a required destination shipped in one of those shapes surfaces as a loud
`missing_path`; and every error from running a tool carries the
`packaging/extract: <tool>: ` prefix, with `ErrToolUnavailable` wrapped in only
when the tool is not on `PATH`.

**The rpm backend's tests** (`packaging/extract/rpm_test.go`). This file is an
**internal** test (package `extract`), because three of its tables drive
unexported parsers. The happy path builds a throwaway `.rpm` with
`packagingtest.BuildRPM` and asserts every model field against literals
hand-written from the `Spec` the test itself declares: destination **set
equality in both directions**, containing exactly the declared paths and **no**
synthesized parents; `Mode.Perm()` per entry including a `0750` directory, a
`0700` directory and a `0640` file; `fs.ModeDir` and nil `Content` on the
directories; `Config` true for exactly the `%config` file; byte-equal `Content`
for every regular file except the `/usr/bin` one, whose `Content` is nil; and
**three distinct owner/group pairs**, so an extractor that read one owner and
reused it cannot pass and no assertion could pass against a hardcoded `root`.
Its dependency assertion compares sorted clones against a hand-written literal
and adds two separate provenance assertions — no returned element carries the
`rpmlib` prefix, and none is `/bin/sh` — even though the fixture ships a
`%post`. A second fixture declares a `%post` body containing the two
header-shaped lines above and asserts `Postinstall.Content` byte-equal to it;
that test is the standing guard against reintroducing a report-anchored parse,
which returns a truncated body for exactly that fixture. A third pair of
fixtures pins the trailing-newline behaviour in both directions. The pipe helper
gets its own three rows, each required to surface as an error carrying the
failing half's `packaging/extract: <tool>: ` prefix: `rpm2archive` against a file
that is not an rpm (the source half exits non-zero), a real artifact extracted
into a directory that does not exist (the destination half never starts), and a
real artifact whose payload is deliberately larger than any pipe buffer piped
into a `tar` given an option it does not implement (**the destination half
exits before draining the pipe**). That last row is the deadlock guard: it is
the case a sequential `Wait` hangs on forever — measured, it does not return,
and the row's own 30-second deadline fails it rather than letting the test
binary time out — while the concurrent implementation returns in under a tenth
of a second. It is also the case a happy-path row cannot reach, because the
hang needs the *consumer* to leave first with more than a buffer still to
write. None of the three simulates a denial by `chmod`-ing a directory
unwritable: root and `CAP_DAC_OVERRIDE` write into one anyway, so such a row
asserts a property of whoever ran the test rather than of the code (see
`docs/agents/skills/dont-use-chmod-to-simulate-permission-denied-in-tests.md`);
a file that is not an rpm, a path that does not exist and an unimplemented
option get the same answer for every user. The happy path is the other side of
that contract, proving a producer's progress chatter on standard error is not
treated as a
failure. The four table-driven tables — the file-table line parser, the whole
file-table output, the dependency-pairing function with the *measured* flag words
`0`, `12`, `1280` and `16777226`, and the postinstall presence-marker parser —
need no tool and execute on **every** host. Every fixture-backed test resolves
*all four* of its tools through `packagingtest.LookTool` before it builds
anything — `rpmbuild`, `rpm`, `rpm2archive` and `tar` — so a host missing any one
of them skips with a message naming that tool and
`make docker-ci`, while inside the dev image — which sets
`PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1` — the same call **fails** instead, which
is what makes a green `make docker-ci` proof that they ran.

**The declared-versus-generated `/bin/sh` row** is the end-to-end proof that
rpm dependencies are reconciled by provenance and never by name, and it is a
statement about the rpm backend alone. One fixture **both** declares
`Requires: /bin/sh` **and** ships a `%post`. Measured, its raw
`rpm -qp --requires` output lists `/bin/sh` **twice**, with index-parallel flag
words `0` (declared) and `1280` (`INTERP|SCRIPT_POST`, which `rpmbuild` derives
from the scriptlet's interpreter).
`TestRPMKeepsADeclaredInterpreterAndDropsTheGeneratedOne` asserts that
`Dependencies` contains `/bin/sh` **exactly once**, which discriminates all
three candidate implementations: a name-based
filter returns **0** occurrences and silently disarms a genuinely declared
dependency, an unfiltered pass-through returns **2** and reports one the package
never declared, and only the flag-based filter returns **1**. The raw count of
two is asserted first, so that if `rpmbuild` ever stopped generating the
interpreter requirement the row fails loudly instead of letting a do-nothing
filter satisfy "exactly once" vacuously. The test also re-checks that `alpha`
and `gamma >= 1` survived and that no `rpmlib(` capability did, so exactly-once
cannot be bought by discarding the requires table wholesale.

**What the rpm backend's degenerate and error rows pin.** Every sentence here is
about `RPM`; none of it is a claim about `Deb` or about "the extractors".
`TestRPMOnFixtureWithoutOptionalMetadata` builds one fixture with no
`Requires:` line, nothing marked `%config` and no `%post` section, and makes
three separate assertions on it: `Postinstall` is **nil**; `Dependencies` is
empty **with a nil error**, asserted alongside a check that the raw
`rpm -qp --requires` output for the *same* artifact was **not** empty — measured,
`rpmbuild` writes three `rpmlib(...)` capabilities into every artifact, so that
second check is what proves the provenance filter removed them rather than the
assertion passing vacuously; and `Config` is false on **every** entry, including
the one whose name ends in `.conf`, over a destination set pinned in both
directions first so the sweep cannot run over an empty entry list.
`TestRPMOnUnreadableArtifact` covers a path that does not exist and a file of
arbitrary non-rpm bytes (a fixed literal, so a failure reproduces byte for
byte): each returns a non-nil error and the **zero-value** `packaging.Model`,
names the offending artifact path, carries the `packaging/extract: rpm: `
prefix, and does **not** satisfy `errors.Is(err, ErrToolUnavailable)`, because
`rpm` ran and rejected the file. `TestRPMWithoutToolWrapsErrToolUnavailable` is
the converse: `t.Setenv("PATH", "")` makes the first `rpm` lookup fail, and the
error both wraps `ErrToolUnavailable` and starts with that same prefix. That row
needs no packaging tool, so it executes on **every** host and never skips.

**The one rpm row that is narrowed rather than covered** is the non-nil-but-empty
`Scriptlet`. An empty `%post` builds but records no body at all — measured,
`rpm -qp --scripts` prints `postinstall program: /bin/sh` with no scriptlet
header, `%{POSTIN}` is `(none)`, and rpm's own presence marker
`%|POSTIN?{HAS}:{NONE}|` reads `NONE` — so the rpm backend correctly reports
such a package as `nil`, and the empty-but-present side is proven for the **deb**
backend only, by `TestDebOnFixtureWithEmptyPostinstall` in
`packaging/extract/deb_test.go`, where a zero-byte `postinst` member is
genuinely constructible. `packagingtest.BuildRPM`'s `t.Fatalf` on a non-nil
empty `Postinstall` is what makes the case impossible to reintroduce for rpm by
accident, and both the narrowing and its measurement are recorded in a comment
on `TestRPMOnFixtureWithoutOptionalMetadata` as well as here.

**The command** (`cmd/verify-packages/main.go`). This is what turns the two
backends and `packaging.Verify` into something a person can run. It sits under
`cmd/` because that is where every binary in this repository lives, and its
non-product name says what it is: a repository tool, deliberately **not** added
to `make build` and **not** given a `.goreleaser.yaml` `builds:` entry, so `bin/`
still holds only `pilothouse` and `pilothoused` and no development tool can leak
into a package. It is developer tooling in the strict sense used everywhere else
in this document — it performs no privileged operation, registers no broker query
or action, adds no capability, imports none of `internal/broker`,
`internal/platform` or `internal/capability`, and is unreachable from either
shipped binary. At this commit `make verify-packages` is the one thing that runs
it, and that target is wired into neither `ci` nor `docker-ci`, so nothing
invokes it automatically (see "The make target" below).

*Shape.* `main` is one line, `os.Exit(run(context.Background(), defaultDeps(),
os.Args[1:], os.Stdout, os.Stderr))`. `run` is the composing entry point and the
function whose return value becomes the exit status. It takes a `deps` — an
**ordered slice** of backends (`ext`, `label`, `extract`) plus the verification
function — and `defaultDeps()` is the only thing production code constructs:
`{".deb", "deb", extract.Deb}`, `{".rpm", "rpm", extract.RPM}`, and
`packaging.Verify`. The slice is ordered rather than keyed because discovery
order is a guarantee, not an accident; a map would leave it unspecified.
`packaging.Verify` is named exactly once in non-test code, inside `defaultDeps`,
so every verdict printed for a real artifact is that function's. The command
invents no finding code and never compares a `Finding.Message` — it only prints
one.

*Discovery.* With no positional arguments, `run` globs the `-dir` directory
(default `dist`) once per backend, **in backend order**, sorting each glob's
matches and concatenating them: every `*.deb` in sorted order, then every
`*.rpm` in sorted order. So `z.deb`, `a.deb` and `b.rpm` in one directory are
reported as `a.deb`, `z.deb`, `b.rpm`, and a file matching neither glob is not an
artifact. With positional arguments, those are the paths, in the order given, and
`-dir` is not consulted at all.

*Dispatch.* The format comes from the file extension, matched
case-insensitively against `deps.backends`: `.deb` to `extract.Deb`, `.rpm` to
`extract.RPM`. An extension no backend claims is an error naming the path and its
extension — never a silently skipped file.

*Output shape.* The report goes to standard output; standard error carries only
the reasons no report could be produced (a bad flag, an unreadable directory, no
artifacts). Per artifact, one header line naming the path, its format label and
its finding count — `dist/pilothouse_1.0_amd64.deb (deb): 3 findings` — followed
by one tab-indented line per finding carrying `Code`, `Path` and `Message`. A
finding whose `Path` is empty, which is what a dependency mismatch or a missing
scriptlet is, renders its path column as `-` rather than blank. An artifact whose
extraction fails prints a failure line in place of a header, carrying the path,
the format label and the extractor's error — including the
`packaging/extract: <tool>: ` prefix — folded onto one line, and processing
**continues** to the remaining artifacts, because one unreadable file must not
hide the others' findings. A summary line closes every run.

*Exit semantics.* Non-zero if any finding was reported or any artifact could not
be extracted; zero only when every artifact was extracted and verified with no
findings.

*The empty-`dist/` message*, which is the output this command produces on a
development host and inside the development image, since neither builds
packages by default. It names the directory searched and both globs, names the
three GoReleaser Pro workflows that are the CI producers
(`.github/workflows/release.yml` on a tag, `.github/workflows/snapshot.yml` on
`main`, and `.github/workflows/packaging.yml` on pushes and pull requests
targeting `main`), and states in one line that `make package` is the local producer that
builds them into `dist/` but requires goreleaser Pro, which is not installed on
a stock development host or in the development image, so it will not succeed
there. Every `make` target the message names is really defined in this
repository's Makefile — `TestRunWithNoArtifactsExplainsWhereTheyComeFrom`
checks each one against `Makefile` itself rather than against a second copy of
the expected list — so a reader is never pointed at something that does not
exist.

*The make target.* `make verify-packages` is a one-line target — `$(GO) run
./cmd/verify-packages`, with no prerequisite and no arguments, so it verifies
whatever `dist/` holds — carrying a `##` help comment like every other
non-obvious target and listed in `.PHONY` because it produces no file of its
own. It is **deliberately absent from `ci` and from `docker-ci`**, and that
absence is the point rather than an oversight: `dist/` is empty on this host and
in the development image, so the target fails there by design, printing the
empty-`dist/` message above — the searched directory and both globs, all three
GoReleaser Pro workflows, and, on one line, that `make package` is the local
producer that fills `dist/` but requires goreleaser Pro, absent here and in the
development image. Wiring it
into `ci` would make every local and containerized run of the full gate fail on
a clean checkout and would break the "`make ci` / `make docker-ci` runs every
CI gate that runs without credentials, and local green means the credential-free
gates will be green" promise that `AGENTS.md` and `README.md` both make.
The pull-request workflow enforces its credential-free boundary with an empty
top-level permission map, exact read-only contents access on every job, and
OIDC only on the unit-test job for Codecov. Its security job installs the
reviewed `govulncheck` v1.6.0 release rather than resolving a floating version;
`internal/workflowcheck/test_workflow_test.go` guards both contracts. The
public observability entry point is `docs/metrics/README.md`: it links live
GitHub Actions, Codecov, issue, and pull-request evidence, defines the rolling
90-day acceptance-rate calculation, and explicitly excludes secrets, private
agent inputs, security-sensitive findings, and managed-host telemetry.
`.github/workflows/nightly-compliance.yml` is a scheduled consumer of that
same contract, not another exception: at 04:23 UTC daily (and on manual
dispatch) it installs the native PAM/systemd headers, pinned golangci-lint and
current govulncheck tool on a fresh runner, then invokes `make ci` unchanged.
That compliance job has read-only contents permission, consumes no secrets,
uses commit-pinned checkout/setup actions, and has a 45-minute timeout. A
separate five-minute drift job fails when `COPILOT_ASSIGNMENT_TOKEN` is absent;
neither job publishes anything, and `cancel-in-progress: false` ensures a
delayed run is not hidden by the next one. CI
*does* run two workflow gates outside the local mirror. The packaging gate
is `.github/workflows/packaging.yml`, which
builds the artifacts, runs `make verify-packages` against them, then
installs them on pinned Debian and Fedora containers with
`make verify-package-install`, and finally boots real Debian and Fedora VMs
under QEMU/KVM and validates the installed package on a host with systemd as
PID 1 — but that
gate needs the `GORELEASER_KEY` secret and the goreleaser Pro distribution, so
it cannot run locally at all (and its booted-VM tier needs KVM besides), and
it is one named exception to the mirror-CI promise rather than something
`ci` could reproduce. The other is `.github/workflows/image-tier.yml`, whose
root-owned uCore lifecycle needs live KVM, network access, Podman 5 and cosign
and is described in [image-tier.md](image-tier.md). An agent or developer who
runs `make verify-packages` on a checkout with nothing built should read the
failure as the expected outcome, not as a defect to fix; the target becomes
useful once a build has put real artifacts in `dist/`. The exclusion is meant to
stay checkable by `make -n ci | grep verify-packages` printing nothing, which is
why the same commit made the Makefile's `GOFILES` expand in the shell at recipe
time rather than in make at parse time: `format-check` otherwise inlined every
Go source path — including `cmd/verify-packages`' own files — into the dry-run
text, so the check reported wiring that does not exist. The set of files
formatted is unchanged, and a command-line `GOFILES=` override still wins, which
is what `scripts/bump_test.sh` relies on.

*The local producer.* `make package` is the target that fills `dist/`: after a
guard it runs exactly `goreleaser release --snapshot --clean`, which builds
snapshot `.deb` and `.rpm` artifacts, publishes nothing, needs no tag and needs
no `GITHUB_TOKEN`. It has no prerequisite — GoReleaser's own `before` hooks run
`go mod tidy` and `go tool templ generate` — carries a `##` help comment and is
listed in `.PHONY`. Like `verify-packages` it is **deliberately absent from
`ci` and from `docker-ci`**, and goreleaser is deliberately **not** installed
in the development image, so both gates stay green without a Pro key. The guard
resolves `goreleaser` through a `PATH` lookup **inside the recipe shell** — no
absolute path and no parse-time `$(shell …)` cache — so a per-invocation `PATH`
override reaches it, and it reads the binary's own `goreleaser --version`
banner, rendered by `github.com/caarlos0/go-version`'s `Info.String()`. The
distribution is decided from the banner's **app-name line** (`goreleaser-pro:
…` for Pro, `goreleaser: …` for OSS), never from the version token: the
published goreleaser-pro v2 binary reports `GitVersion:\t2.17.0` with no `-pro`
suffix, so a version-token rule would reject exactly the binary this target
must accept, while older Pro v1 tags did carry the suffix, which is why the
version parse tolerates one without requiring it. The major version is the
integer before the first `.` on the `GitVersion:` line, after an optional
leading `v` and tolerating any pre-release suffix, and it must be `2` — what
the repository's existing `~> v2` pin resolves to. Each of the four failure
modes — not on `PATH`, not the Pro distribution, wrong major version, version
undeterminable (a source build reports `devel`) — prints its own case-specific
reason alongside the required-version statement and
<https://goreleaser.com/pro/>, then exits non-zero **before** any goreleaser
subcommand runs. Failing that guard is the expected outcome on a development
host and in the development image.

*The CI packaging gate* is `.github/workflows/packaging.yml` (workflow name
`Packaging`). It triggers on `push` to `main` and on `pull_request` targeting
`main` with `types: [opened, synchronize, reopened, labeled]` — no tag
trigger, no `workflow_run` — holds `permissions: contents:
read` because it publishes nothing, and runs **three** jobs. The first,
`packages`, installs the packaging and cgo build dependencies (`rpm`
explicitly, since no ambient runner tool may be relied on, plus the
`libpam0g-dev`/`libsystemd-dev` headers and the `gcc-aarch64-linux-gnu` cross
toolchain the arm64 cgo `pilothoused` build needs — `cpio` is **not** among
them: nothing in the workflow uses it, and `packaging/extract`'s rpm backend
runs `rpm2archive` piped into `tar`), runs
`goreleaser/goreleaser-action@v7` with the Pro distribution and the existing
`~> v2` constraint on `release --snapshot --clean`, then asserts and verifies
what came out and uploads the two artifacts. The second, `install`, downloads
those artifacts and installs them on the two digest-pinned distro containers;
it is described in full under "How the script is invoked" in
[install-validation.md](install-validation.md). The third,
`vm-boot`, downloads the same artifacts and boots real Debian and Fedora VMs
with them; it is described in full under "The CI job (`vm-boot`)" in
[vm-harness.md](vm-harness.md). So the gate
does not stop at the artifact payload: it also proves the packages install,
and that the installed package works on a booted host.

It is a **separate file** from `.github/workflows/test.yml` because GitHub does
not hand repository secrets to a run triggered by a fork's pull request: a job
needing `GORELEASER_KEY` inside `test.yml` would fail for every fork
contributor, and `test.yml` must stay green for them and must gain no
`GORELEASER_KEY` dependency. Instead the `packages` job carries
`if: github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository`,
so a fork pull request **skips** the job — neutral, never failed — and never
receives the key. Neither `install` nor `vm-boot` restates that condition:
`needs: packages` means a skipped `packages` skips them too. (`vm-boot` does
carry an `if:` — the `vm-boot` label gate — but nothing about forks.)
`GORELEASER_KEY` is the only secret the
workflow takes; there is
no `GITHUB_TOKEN`, because `--snapshot` publishes nothing and needs no tag.

It is also a separate file from `.github/workflows/snapshot.yml`, which is a
publisher rather than a gate: snapshot.yml runs after `Tests` succeeds on
`main`, so it never sees a pull request, and it publishes the rolling `dev`
pre-release. The consequence is that a push to `main` **builds the packages
twice** — once here, verified and published nowhere, and once in snapshot.yml,
published. That double build is deliberate and must not be optimized away:
a gate chained to the publisher could not block a pull request, and a gate
sharing a run with the publisher could not fail without breaking publication.
Adding verification to the publishing path is a separate follow-up, not part of
this workflow.

Each format's presence is asserted by its **own** step — one `ls dist/*.deb`,
one `ls dist/*.rpm` — so a build that produced only one of them fails on its
own. A single `upload-artifact` step globbing both with
`if-no-files-found: error` would succeed with just one format present, which is
why the uploads are two steps as well, each with `if-no-files-found: error`.
Between the assertions and the uploads the job runs `make verify-packages`,
not `continue-on-error`, so any contract finding fails the job.

*Its tests* live in two files, split by what they need from the host, and every
test in both drives `run` itself, never a reporting or dispatch helper.
`main_test.go` holds the cells that execute on **every** host with no packaging
tool and no skip: discovery over a mixed directory, dispatch by the
`packaging/extract: <tool>: ` error prefix, the unsupported extension, the
missing artifact path, the empty-`dist/` message and the injected clean-path
table. `integration_test.go` holds the cells that need a real synthetic artifact
— an explicit `.deb`, an explicit `.rpm`, and one discovered directory holding
one of each beside a decoy — and those are **deep-gated**: they build their
fixtures with `internal/packagingtest`, which resolves every tool through
`packagingtest.LookTool`, so on a host without `rpmbuild`/`rpm`/`rpm2archive`/`tar`
the `.rpm` and mixed cells skip naming the missing tool, while `make docker-ci`
sets `PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1` and the same lookup **fails** there
instead. A green `docker-ci` is therefore proof those three cells ran. All of
them go through `run(ctx, defaultDeps(), …)` — real artifact in, `extract.Deb` or
`extract.RPM` out, `packaging.Verify` deciding — and none injects a backend or a
verification result, which is what keeps the hand-built `deps` below bounded to
the outcomes a real artifact cannot produce. A placeholder fixture cannot satisfy
the artifact contract, so each block carries real `Verify` findings; the
assertions are structural — the block names its file, carries its own format
label, lists at least one finding, shows no extraction-failure text, and (for the
`.rpm`) contains no `dpkg-deb` text — plus membership of every printed `Code` in
a hand-written literal of the nine codes, so an invented code fails. No test
there asserts the wording of a finding.

The artifacts `main_test.go` stages are arbitrary bytes, so extraction is
expected to fail there, and its dispatch proof reads the
`packaging/extract: <tool>: ` prefix rather than a bare tool name: the deb
block must contain `packaging/extract: dpkg-deb: ` and not
`packaging/extract: rpm`, and the rpm block the converse. That distinction is
load-bearing — a bare `rpm` check would be satisfied by the filename `b.rpm`
itself — and it holds in both environments, since a missing tool yields
`ErrToolUnavailable` and a present one yields a parse failure, both carrying the
prefix. Every substring assertion over a multi-artifact run is applied to the
slice of output belonging to the artifact under test, from its line through the
indented lines beneath it, never to the whole capture.

One test, `TestRunExitStatusIsDerivedFromResults`, is the only place a `deps` is
built by hand, and the reason is worth recording. Exit 0 is correct only for a
package that satisfies the artifact contract, and no such package can be built
here: a conforming synthetic fixture would have to restate `contract.go`'s tables
inside a test, and the repository cannot build a real project package locally or
in `make docker-ci` (`.goreleaser.yaml` uses a GoReleaser Pro block, and the dev
image deliberately has no goreleaser). Asserting the clean path one level down,
on a reporting helper, would leave `run`'s own return value asserted only on
failures — where a `run` that returned 1 unconditionally would pass everything
(`docs/agents/skills/test-the-composing-function-not-its-merge-helper.md`). So
that table injects an extraction result and a verification result, and only
those: a placeholder `Model` with a `verify` returning nothing gives exit 0 with
the zero-finding summary and no failure text, for a `.deb` row and an `.rpm` row;
two hand-written findings, one with an empty `Path`, give exit 1 with both
rendered and the empty path shown as `-`; and a backend erroring on the first of
two artifacts gives exit 1 with the second still processed. The injected backend
records the path it was handed, so the clean rows cannot pass against a `run` that
reports success without invoking a backend. The doubles carry no contract
knowledge — the findings use invented codes that are none of the nine, and the
models are the same `/opt/phx/…` placeholders the fixtures use. Everything a real
artifact *can* demonstrate — discovery, dispatch, extraction failure — is asserted
through `defaultDeps()`, which keeps the seam a seam and not a shim
(`docs/agents/skills/exercise-the-actual-boundary-not-a-precomputed-shim.md`).
The narrowing that remains, stated plainly: **no test asserts exit 0 for a real
artifact**, because no conforming one can be produced here.

