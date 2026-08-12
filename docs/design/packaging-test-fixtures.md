# Packaging test fixtures (`internal/packagingtest`)

Living document; extracted verbatim from [overview.md](overview.md) (formerly
`yeti/OVERVIEW.md`). It covers the shared test-support package for packaging
tests: the `LookTool` skip-vs-fail tool gate and the declarative `.deb`/`.rpm`
fixture builders. Consumers include the artifact-contract tests
([../specs/artifact-contract.md](../specs/artifact-contract.md)) and the
extractor tests ([artifact-extraction.md](artifact-extraction.md)).

## The dev container image and `PILOTHOUSE_REQUIRE_PACKAGING_TOOLS`

`.docker/` holds the development container image (Go + PAM + systemd headers,
plus the systemd package so `systemd-analyze` exists and `shellcheck` for the
packaging scriptlet) for the `docker-*` make targets. It includes `jq` so the
real uCore composer runs in offline tests. It also installs `rpm` (which on
the Debian bookworm base provides `rpm`, `rpmbuild` and `rpm2cpio`) and
`cpio`, the latter only because `cmd/verify-packages/integration_test.go`
still resolves it in its rpm tool list — the RPM extractor itself runs
`rpm2archive` piped into `tar`; `dpkg-deb` already comes from the Debian base
image, so no package is needed for it. The image declares
`ENV PILOTHOUSE_REQUIRE_PACKAGING_TOOLS=1`, which reaches every `docker-*`
target through the Makefile's `DOCKER_RUN` with no per-target flag: because
the image guarantees those tools, a tool-dependent test that would otherwise
skip when one is missing must fail inside this image instead. The one reader
in Go code is `internal/packagingtest.LookTool`, which exposes the variable's
name as `packagingtest.RequireEnv`. `make docker-tools-check` asserts the
whole set — it resolves `dpkg-deb`, `rpm`, `rpmbuild`, `rpm2archive` and
`tar` and prints the flag's value, alongside the `svu` and `golangci-lint`
checks it has always run — and stays outside `ci`/`docker-ci`.

## The package

`internal/packagingtest` is an ordinary Go package — its implementation files
carry no `_test.go` suffix — whose only consumers are test files. It exists
because the helpers below are needed by the tests of more than one package, and
an unexported helper living in a `_test.go` file is unreachable from another
package. It sits under `internal/` because it ships in no binary, and it imports
nothing else in this repository, so it can hold no knowledge of any packaging
contract: a fixture it builds describes only what its caller declared.

**The tool gate.** `LookTool(t, name)` resolves an external packaging tool and
is the only place in the package that looks a name up on `PATH`; every
tool-dependent test goes through it, so none can reach a tool around the gate.
When the tool cannot be resolved, the behaviour depends on `RequireEnv`
(`PILOTHOUSE_REQUIRE_PACKAGING_TOOLS`, which `.docker/Dockerfile` sets to `1`):

- variable unset — `Skipf` with a reason naming the tool and pointing at
  `.docker/Dockerfile`, following the wording of `packaging/units_test.go`'s
  `systemd-analyze` skip and `packaging/postinstall_test.go`'s `shellcheck`
  skip;
- variable set to `1` — `Fatalf` instead, because the environment declares the
  tool present and skipping there would silently hide the check.

The parameter is a local three-method `TestingT` interface (`Helper`, `Skipf`,
`Fatalf`) that `*testing.T` satisfies, rather than `testing.TB`, which has an
unexported method and cannot be implemented outside `testing`. That is what
lets a recording fake observe both branches from a test that itself passes, and
it keeps `testing` out of the package's non-test imports.

**The shared fixture vocabulary.** One hand-written `Spec` feeds both builders:
`Dirs` and `Files`, each with a declared mode, an `Owner` and a `Group`; a
per-file `Config` flag; `Depends`; and a `*string` `Postinstall`. An empty
`Owner` or `Group` means `DefaultOwner` (`alpha`) or `DefaultGroup` (`beta`).
Those defaults are placeholder names rather than `root` on purpose: an assertion
that an entry's ownership was read out of a package's own metadata would pass
against a reader that hardcoded `root` and would prove nothing, whereas against
a placeholder it cannot. Only `BuildRPM` records ownership; `BuildDeb` ignores
both fields, for the reason given below.

Sharing one `Spec` across two formats has one caveat, and it is in `Depends`:
the strings are written into each format's own metadata **verbatim**, never
translated, so a `Spec` handed to both builders must use syntax both parse
alike — plain dependency names. Each format's own constraint syntax is
format-specific: a deb-only fixture may write `alpha | beta` and
`gamma (>= 1)`, while an rpm-only fixture needs the **spaced** form
`gamma >= 1`, since `gamma>=1` without the spaces is parsed by rpm as a
dependency whose *name* is that whole string.

**The deb fixture builder.** `BuildDeb(t, outDir, spec)` builds a throwaway
`.deb` from a `Spec` and returns the artifact's path. `outDir` is
caller-supplied, so fixtures can be built straight into a directory the caller
later scans; the staging tree is scratch space inside it. `Depends` entries pass
through verbatim, joined with `", "`, so alternatives and version constraints
are never rewritten.
`DEBIAN/conffiles` is written only when at least one `File` is marked `Config`.
`Postinstall` keeps three states apart: nil ships no `DEBIAN/postinst` member at
all, a pointer to `""` ships a zero-byte one, and a pointer to a body ships
those bytes. `dpkg-deb` is resolved through `LookTool`, so a host without it
skips with an explicit reason and the dev image cannot skip.

`BuildDeb` **ignores** `Owner` and `Group` on every entry. It builds with
`dpkg-deb --root-owner-group`, which records every archived path as `root/root`,
and a deb's payload is read back by extracting it to disk, from which the
archived ownership cannot be recovered at all — so honouring the fields would
record something no reader of the artifact could observe.

Two measured `dpkg-deb` constraints shape the builder. `dpkg-deb --build`
refuses a control directory outside 0755-0775 (`control directory has bad
permissions 700`) while `t.TempDir()` and `os.MkdirTemp` produce 0700
directories, so `DEBIAN` is chmodded to 0755 explicitly. And `dpkg-deb`
synthesizes the intermediate directories of every declared path into the
archive, so each intermediate directory the builder creates is chmodded to 0755,
making every synthesized parent's archived mode determinate rather than a
product of the caller's umask. Declared modes are applied with an explicit
`os.Chmod` for the same reason, rather than relying on the permission argument
of `os.Mkdir`/`os.WriteFile`.

**The raw escape hatch.** `BuildDebRaw(t, outDir, tree, modes)` packs an
already-staged tree verbatim instead of rendering a `Spec`: `tree` maps a
relative path to the bytes of a regular file there — `DEBIAN/control` included,
since nothing is synthesized — and `modes` gives a path's mode, naming a
directory when `tree` has no such file. It exists because a control area a
broken package could genuinely carry is not always expressible through `Spec`:
`dpkg-deb --build` **rejects** a `DEBIAN/conffiles` line that is not an absolute
path outright (`conffile name 'etc/phx/relative.conf' is not an absolute
pathname`, exit 2) — the message the package's own test requires it to print.
So the only way to obtain that artifact is `--nocheck`, which `dpkg-deb`
documents as building any archive you want no matter how broken. `BuildDebRaw`
passes `--nocheck` and `BuildDeb` does not, which keeps malformed metadata in
reach of only the tests that ask for it rather than making it expressible by
every `Spec`. It carries `BuildDeb`'s umask defences over: every intermediate
directory it creates is chmodded to 0755, each staged file is chmodded to its
declared mode explicitly, declared directory modes are applied deepest path
first once every file is written, and `DEBIAN` is chmodded to 0755 last.

**The rpm fixture builder.** `BuildRPM(t, outDir, spec)` builds a throwaway
`.rpm` from the same `Spec` and returns the artifact's path, so one fixture
declaration can be built in both formats. Everything is scoped to scratch space
inside `outDir`: a throwaway `_topdir` holding the generated spec file, the
staged payload tree and `rpmbuild`'s own working directories. The spec file
declares `%global debug_package %{nil}`, `AutoReqProv: no` and
`BuildArch: noarch`, so the package holds exactly the declared payload, and
every declared dependency appears verbatim. The requires set is not limited to
the declared ones: outside the builder's control rpm also records the
`rpmlib(...)` capabilities `rpmbuild` writes into every artifact, plus
`/bin/sh` whenever a `%post` section is present — both even under
`AutoReqProv: no`. A caller asserting on requires therefore checks each
declared entry is present, not that the set is exactly the declared one. Each
`Depends` element becomes one `Requires:` line verbatim. `%files` emits
`%dir %attr(<mode>, <owner>, <group>)` per `Dir` and
`%attr(...)` per `File`, prefixed with `%config` where declared, so rpm records
each entry's mode and ownership from the declaration rather than from the build
account — measured to hold when building as an unnamed uid 1000 with
`HOME=/tmp`, which is exactly how the `make docker-*` targets invoke the dev
image. The build runs `rpmbuild -bb` with `_topdir` and `_rpmdir` defined, then
copies the single artifact produced into `outDir`. `rpmbuild` is resolved
through `LookTool` like every other tool here.

Because rpm owns only what `%files` lists, an rpm fixture holds **exactly** its
declared destinations and no synthesized parents. That is the opposite of the
deb builder, where `dpkg-deb` archives the intermediate directories of every
declared path, and the two must never be stated as one claim.

`BuildRPM` calls `Fatalf` when `Postinstall` is non-nil but empty, naming the
limitation rather than quietly building something else. Measured, an empty
`%post` builds but records **no body**: `rpm -qp --scripts` prints only
`postinstall program: /bin/sh` with no scriptlet header, `%{POSTIN}` is
`(none)`, and rpm's own tag-presence marker `%|POSTIN?{HASPOST}:{NOPOST}|` reads
`NOPOST`. A zero-byte rpm scriptlet is therefore not a state an artifact can
represent, let alone one a reader could tell apart from shipping none — so the
empty-but-present postinstall case is exercised for **deb only**, and only the
`nil` side is available in both formats. Two further measured caveats bind a
declared body, and `BuildRPM`'s doc comment records both: rpm strips **all**
trailing newlines from a recorded scriptlet, so a caller wanting byte-exact
equality downstream declares a body without one; and a body line beginning with
`%` at column 0 would be read by `rpmbuild` as the start of a new spec section.

The package's own tests check each builder against that format's own tooling,
never against other Go code: the deb builders against `dpkg-deb` — `-c` for the
path/mode table, `-f` for the dependency field, `-e` for the control directory —
and `BuildRPM` against `rpm` — `-qp -l` for the owned paths, `--requires` for
the dependencies, `-qpc` for the configuration files, `--scripts` for the
postinstall body, and a `%{FILENAMES}`/`%{FILEUSERNAME}`/`%{FILEGROUPNAME}`
query proving both declared ownership pairs reached the header per entry. The
gate's two branches and `BuildRPM`'s empty-`Postinstall` refusal are driven
through a recording `TestingT`, in tests that need no external tool on any host.
`BuildDebRaw`'s tests additionally pin the premise behind its `--nocheck`: an
ordinary `--build` of the very same staged tree is required to fail, so the
escape hatch's justification is a runnable claim rather than a comment.

