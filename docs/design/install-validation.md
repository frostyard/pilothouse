# Install validation (`packaging/verify-install.sh`)

Living document; extracted verbatim from [overview.md](overview.md) (formerly
`yeti/OVERVIEW.md`). It covers Layer A of package validation: the
container-level install/reinstall/removal checks run by
`make verify-package-install` and by `.github/workflows/packaging.yml`'s
`install` job. The static artifact contract the same artifacts must satisfy
is [../specs/artifact-contract.md](../specs/artifact-contract.md); Layer B
(booted hosts) is [vm-harness.md](vm-harness.md).

## The script

`packaging/verify-install.sh` is **Layer A** of package validation: a single
POSIX `sh` script that runs **inside a target distro container image, as root**,
against a directory of built artifacts. It is not a packaged file — it is not
in `contract.go`'s `//go:embed` set, not named by any `packaging.Verify` table,
and not an nfpm content entry in `.goreleaser.yaml`. It exists because the
static artifact contract ([../specs/artifact-contract.md](../specs/artifact-contract.md))
reads bytes out of a `.deb`/`.rpm`; only a real
install can show what the package manager and the postinstall scriptlet
actually produce on disk.

**Container-only, by construction.** The script never requires systemd as PID 1,
never drives a service manager, never restarts a machine, and never depends on
an enforcing SELinux policy; anything needing a booted host is Layer B (#67).
It also never asserts `/run/pilothouse` or `/var/lib/pilothouse`: those are
deliberately unpackaged because systemd's `RuntimeDirectory=`/`StateDirectory=`
own them, and a container has no running systemd to create them.

**Shape.** `set -eu` is the first effective line, and `fail()` prints
`verify-install: <message>` to standard error and exits 1, so the **first**
failed assertion aborts the run. Usage is
`packaging/verify-install.sh <artifact-dir>` with no default: a missing or
non-directory operand is an actionable failure that prints the usage. The
package format is detected from the container's own package manager (`apt-get`
→ deb, `dnf` → rpm, neither → failure), never from an artifact's file name, and
exactly one amd64 artifact of that format (`*_amd64.deb` / `*.x86_64.rpm`) must
be present — zero or several is a failure naming what was found. arm64 install
validation is out of scope.

**What it checks at this commit** — all eight checks:

1. **Install through the distro package manager.** `apt-get update` then
   `apt-get install -y <artifact>` for deb, `dnf install -y <artifact>` for rpm.
   Never a bare `dpkg -i`/`rpm -i`: the point of this check is that the
   hand-written per-format `dependencies` lists in `.goreleaser.yaml` resolve
   against the distro's real repositories.
2. **`check_account()`** — the `pilothouse` user and group exist afterward and
   match the **installed** sysusers declaration. The account *name* is pinned:
   the script asserts the installed declaration declares exactly `pilothouse`,
   because the packaging contract is about that specific account and a file
   declaring some other valid system account must not pass. The account's
   *properties* are not pinned — home directory, shell and GECOS are parsed out
   of `/usr/lib/sysusers.d/pilothouse.conf` on the
   installed filesystem (the live source of truth, not a hardcoded copy of
   `packaging/pilothouse.sysusers`) and compared against
   `getent passwd pilothouse` / `getent group pilothouse`; the account's primary
   group must be `pilothouse` and its uid must be in the system range (< 1000).
3. **`check_owner_mode()`** — on-disk owner, group and mode read with
   `stat -c '%U %G %04a'` from the installed filesystem, never from package
   metadata, for `/etc/pilothouse` (`root:pilothouse 0750`),
   `/etc/pilothouse/storage/credentials` (`root:root 0700`), and both env files
   (`root:pilothouse 0640`).
4. **`check_pam()`** — every stack and every module the **installed**
   `/etc/pam.d/pilothouse` names exists on that distro. Both lists are parsed
   out of the installed policy at run time: the stacks are the operands of
   `@include` lines plus the files named by an `include`/`substack` control
   value, and the modules are every `pam_*.so` token (directory prefix
   stripped). Each stack must exist as `/etc/pam.d/<name>`, and each module
   must exist in one of the candidate module directories — `/lib/security`,
   `/lib64/security`, `/usr/lib/security`, `/usr/lib64/security` and
   `/usr/lib/*-linux-gnu/security` — which are **searched**, not selected by
   distro family. A policy that yields zero stacks or zero modules is an
   explicit failure, so a mis-parse cannot pass vacuously.
5. **`expect_unit()`** — `systemd-analyze verify` is run against both
   **installed** unit paths, `/usr/lib/systemd/system/pilothouse.service` and
   `/usr/lib/systemd/system/pilothoused.service`; a non-zero exit is a failure.
   `packaging/units_test.go` is the precedent for the invocation.
6. **`expect_linked()`** — `ldd /usr/bin/pilothoused` must succeed and no line
   of its output may contain `not found`. This is the check that proves the
   declared libpam and libsystemd dependencies actually satisfy the cgo-linked
   binary. `/usr/bin/pilothouse` is deliberately **not** checked:
   `.goreleaser.yaml` builds it with `CGO_ENABLED=0`, so it is static and `ldd`
   exits non-zero for it for a reason that has nothing to do with the
   dependency lists.
7. **Reinstall.** The **same** artifact is installed over the existing install
   — `apt-get install -y --reinstall <artifact>` for deb, `dnf reinstall -y
   <artifact>` for rpm — and then `check_account` and `check_owner_mode` are
   **re-invoked**. They are called a second time, not copied: the reinstalled
   state is held to exactly the assertions the first install was held to, so a
   postinstall that only repairs ownership on a fresh install fails here.
8. **Removal, asserted per format.** Each format runs its removal verbs in one
   uninterrupted sequence, so no reinstall is needed between dpkg's two verbs
   (`dpkg -P` operates on a removed-but-unpurged package). The account name is
   captured before the first verb, because the sysusers file it is read from
   goes away with the package. `check_removed_paths_gone`,
   `check_conffiles_present`, `check_conffiles_gone`, `check_no_rpmsave` and
   `check_account_survives_removal` are the named assertions, each taking the
   verb it follows so the failure message names it.

**The removal matrix, and why each cell reads the way it does.** dpkg and rpm
do not treat a `type: config` file alike on removal. The behaviour below was
measured in the two pinned images with a minimal package carrying one marked
config file, and the script asserts exactly it:

| | dpkg `remove` (`dpkg -r`) | dpkg `purge` (`dpkg -P`) | rpm `erase` (`rpm -e`) |
|---|---|---|---|
| unmodified config file | **survives** | removed | **removed** |
| modified config file | **survives** | removed | **saved as `.rpmsave`** |

- **Non-config payload, both formats.** `/usr/bin/pilothouse`,
  `/usr/bin/pilothoused`, both units under `/usr/lib/systemd/system` and
  `/usr/lib/sysusers.d/pilothouse.conf` must be **gone**. Neither manager keeps
  non-config payload, so a survivor means the package claimed ownership of a
  file it did not actually own.
- **Debian `dpkg -r`: the three conffiles survive.** `/etc/pam.d/pilothouse`
  and both env files are `type: config`, and a remove is not a purge — dpkg
  deliberately keeps local edits available for a reinstall. Asserting their
  **presence** is what pins that they really were registered as conffiles: a
  package that shipped them as ordinary files would delete them here and fail.
- **Debian `dpkg -P`: the same three are gone.** Covering both verbs rather
  than only the gentler one is the point; a purge that left a conffile behind
  would leave stale credentials-adjacent configuration on an uninstalled host.
- **Fedora `rpm -e`: the same three are gone, and no `.rpmsave` survives.**
  rpm erases an *unmodified* config file outright, so their absence is the
  correct expectation. The `.rpmsave` sweep over `/etc/pilothouse` and
  `/etc/pam.d` is the interesting half: rpm only writes a `.rpmsave` when the
  file's on-disk content diverged from what the package shipped, so a hit means
  the postinstall scriptlet modified a config file its own package shipped —
  a real defect, and invisible to every static check.
- **The account survives both managers.** `systemd-sysusers` creates the
  `pilothouse` user and group from the shipped sysusers file during the
  postinstall; neither dpkg nor rpm owns them, so neither removes them. The
  script asserts they still resolve through `getent` after every verb, so a
  future change that starts deleting the account — orphaning any file still
  owned by its uid — is noticed here rather than on a user's machine.
- **`/etc/pilothouse`'s own pruning is deliberately not pinned.** Whether an
  emptied directory that held surviving conffiles is removed varies between
  managers and versions and carries no user-visible consequence, so asserting
  it either way would be a flaky claim about implementation detail rather than
  about this package. The script asserts only about files *inside* it, and a
  guard test enforces that: no `[ ! -e /etc/pilothouse ]`-shaped assertion, and
  `/etc/pilothouse` may appear in neither expectation set.

**Why `systemd-analyze verify` is Layer A work.** It is a parser: it validates
unit *files* offline, resolving them under the filesystem it is pointed at, and
never contacts PID 1. Starting, enabling or querying a unit does need a running
service manager, so it stays Layer B (#67) — which is why `systemctl` appears
nowhere in the script and `systemd-analyze` does.

**Why the PAM expectations are derived, not tabulated.** The two formats ship
different policies (`packaging/pilothouse.pam` for deb,
`packaging/rpm/pilothouse.pam` for rpm), naming different stacks, and the
distro families keep their modules in different directories. A per-distro table
in the script would be a second copy of the packaging contract that could drift
from the shipped policy silently, and it would still say nothing about the
policy the package actually installed. Reading the installed file makes the
check follow whichever policy the format's override shipped, which is why the
script contains no stack name, no module name and no multiarch module path.

Checks 2 and 3 are shell **functions** rather than inline code, so each is a
single named unit that can be invoked more than once — which check 7 does.

**Expectation-line convention.** Every expected ownership value, every
verified unit path, the cgo-linked binary path and both removal sets are
written as one `expect_owner_mode <path> <owner> <group> <mode>`,
`expect_unit <path>`, `expect_linked <path>`, `expect_conffile <path>` or
`expect_removed <path>` call per line with literal arguments, so the Go guards
can parse the script's expectations deterministically instead of matching
free-form shell. `expect_conffile` and `expect_removed` *print* their path
rather than asserting on it, because the same set is asserted with opposite
polarity at different points of the removal sequence; the assertion functions
iterate over `conffile_paths` and `removed_paths` and decide the polarity.

**Go guards** (`packaging/verify_install_test.go`, tier (c) of the four
tiers in [../specs/artifact-contract.md](../specs/artifact-contract.md)). Per the
spec, no Go test executes the script — it needs a package manager and a
network. The tests read the script as text: the script is executable and starts
with `#!/bin/sh`; `set -eu` is its first effective line;
`shellcheck --shell=sh` is clean (skipped with an explanatory message naming
`.docker/Dockerfile` when `shellcheck` is absent, so it really runs under
`make docker-ci`); the install goes through the package manager and not a bare
installer; the account check reads the installed sysusers file and hardcodes
none of its values; ownership is read with `stat`; no booted-host command
(`systemctl`, `reboot`, `setenforce`, `getenforce`, `systemd-run`) appears; and
neither systemd-managed path is mentioned.
`TestVerifyInstallOwnerModeExpectationsMatchGoreleaserConfig` is a
**bidirectional** drift guard: it reads the live `../.goreleaser.yaml` and
requires set equality, in both directions and for **both** the deb and rpm
overrides, between the script's `expect_owner_mode` destinations and the
content entries carrying a `file_info` block — an unauthorized expectation line
and a dropped packaged entry both fail it.

Checks 4 through 6 add four more guards, all of them text-only:

- `TestVerifyInstallPAMCheckReadsTheInstalledPolicy` requires the script to
  name `/etc/pam.d/pilothouse` and to contain neither repository PAM source nor
  any per-distro literal (`common-auth`, `common-account`, `password-auth`,
  `pam_nologin.so`, a multiarch `…-linux-gnu/security` path), so the
  expectations can only be derived at run time.
- `TestVerifyInstallPAMCheckRejectsAnEmptyParse` pins the emptiness guards on
  the parsed stack list, module list and discovered module directories.
- `TestVerifyInstallVerifiesUnitsWithSystemdAnalyze` pins that check 5 calls
  `systemd-analyze verify` and that `systemctl` appears nowhere.
- `TestVerifyInstallUnitExpectationsMatchGoreleaserConfig` and
  `TestVerifyInstallLinkedBinariesMatchBuilds` are the two further
  **bidirectional** drift guards. The first requires set equality between the
  script's `expect_unit` paths and the live config's destinations under
  `/usr/lib/systemd/system`, for both overrides. The second requires set
  equality between the script's `expect_linked` paths and the `/usr/bin`
  destinations of the live `builds` entries whose `env` contains
  `CGO_ENABLED=1` — today exactly `/usr/bin/pilothoused` — and asserts the
  converse too: no `CGO_ENABLED=0` build may appear there. It declares its own
  local types decoding `builds[].binary` and `builds[].env`, because
  `drift_test.go`'s `buildTarget` decodes `binary` alone.

Checks 7 and 8 add five more guards, all still text-only:

- `TestVerifyInstallReinstallsAndRepeatsTheAccountAndOwnershipChecks` requires
  both reinstall verbs on the same `${artifact}` and counts the bare
  `check_account` / `check_owner_mode` invocation lines: each must appear more
  than once, so a chunk that re-asserted the ownership rules by copying them
  instead of re-invoking the function fails.
- `TestVerifyInstallConffilesMatchGoreleaserConfig` and
  `TestVerifyInstallRemovedPathsMatchGoreleaserConfig` are the two further
  **bidirectional** drift guards, run against both overrides. The first
  requires set equality between the script's `expect_conffile` paths and the
  live config's `type: config` destinations. The second requires set equality
  between the script's `expect_removed` paths and the live destinations that
  are neither `type: config` nor `type: dir`, plus the two `/usr/bin` build
  outputs. Directories are excluded because pruning is deliberately unpinned,
  and config files because their fate depends on the removal verb.
- `TestVerifyInstallRemovalMatrix` is the structural guard over check 8, and it
  enumerates the matrix as data — format × verb × expectation — rather than
  spot-checking one cell. It scopes itself to the lines after the
  `removal_account=` anchor so the earlier `deb)`/`rpm)` case labels cannot be
  mistaken for the removal block's, splits those lines into one segment per
  verb, and requires each verb to appear exactly once, inside its own format's
  branch, followed by exactly the assertions its row lists and by **none** of
  the assertions that would contradict them (so `check_conffiles_gone` after
  `dpkg -r`, or `check_conffiles_present` after `dpkg -P`/`rpm -e`, fails).
- `TestVerifyInstallRemovalChecksAssertTheRightPolarity` reads each of those
  check functions' bodies and pins what they actually assert (`[ -e … ]` versus
  `[ ! -e … ]`, the `.rpmsave` sweep, both `getent` calls), so a matrix cell
  cannot be satisfied by a correctly named function that asserts the opposite.
- `TestVerifyInstallNeverAssertsTheConfigDirectoryIsPruned` is the negative
  guard for the deliberate non-goal, and
  `TestVerifyInstallPackageNameMatchesGoreleaserConfig` keeps the name the
  removal verbs operate on equal to the live `package_name`.

**How the script is invoked.** `make verify-package-install` is the one thing
in the repository that runs it, as of this commit. The target takes two
variables: `INSTALL_IMAGE`, the container image reference, with **no default**,
and `ARTIFACT_DIR`, the directory of built artifacts, defaulting to `dist`. An
unset `INSTALL_IMAGE` is an actionable failure naming both digest-pinned
references (`debian:12@sha256:9344f8b8992482f80cba753f323adeaf17690076c095ccff6cc9536be98185dc`
and `fedora:42@sha256:99e203b80b1c3d8f7e161ec10a68fd02b081ef83a3963553e513c82846b97814`),
following `make package`'s precedent of failing with what was found and what is
required rather than silently picking a distro family; a missing
`ARTIFACT_DIR` fails the same way, naming `make package` as the local producer.

The recipe writes out an explicit `$(DOCKER) run --rm` and deliberately does
**not** reuse the `DOCKER_RUN` macro. That macro is wrong here in three
independent ways: it pins the host UID/GID (installs need root inside the
container), it bind-mounts the whole workspace (only `packaging/` and the
artifact directory should be visible, both read-only), and it hardcodes
`$(DOCKER_IMAGE)`, the development image, when the image under test is the
parameter. Network access is left enabled on purpose — check 1 exists to
resolve dependencies against the distro's real repositories.

Like `verify-packages` and `package`, the target is **deliberately absent from
`ci` and from `docker-ci`** (`make -n ci | grep verify-package-install` prints
nothing): it depends on artifacts a stock checkout does not have, and
additionally on Docker and the network.

**The CI job that runs it.** `.github/workflows/packaging.yml` gained a second
job, `install` ("Install packages on target distros"), as of this commit. It is
Layer A in CI, and it is a *consumer* of the `packages` job, not a second build:

- `needs: packages`, and **no `if:` of its own**. The fork skip lives once, on
  `packages` (a fork pull request never receives `GORELEASER_KEY`); a skipped
  job skips its dependents, so this job inherits the skip instead of carrying a
  second copy of the condition that could drift from the first.
- A `strategy.matrix` of exactly two entries — `debian:12@sha256:9344f8…` paired
  with the `packages-deb` artifact and `fedora:42@sha256:99e203…` paired with
  `packages-rpm` — with `fail-fast: false`, so one family's failure does not
  mask the other's result. The same digest pins the Makefile's unset-variable
  message names, for the same reason: a floating tag makes the gate's result
  depend on when it ran.
- Its steps are `actions/checkout@v7` (the script and the Makefile come from the
  tree, never from the artifact), `actions/download-artifact@v5` fetching the
  matrix row's artifact into `dist/`, then
  `make verify-package-install INSTALL_IMAGE=${{ matrix.image }}
  ARTIFACT_DIR=dist` — the *same* target a developer runs, so CI and a
  workstation cannot disagree about what installing the package proves.
- Reusing the uploaded artifacts is what keeps the bytes under test equal to
  the bytes `make verify-packages` already passed, and keeps the workflow to
  **exactly one** `goreleaser/goreleaser-action` step. Only the matching
  format's artifact is downloaded, so a format mis-detection inside the script
  cannot silently install the wrong file. The job needs no Go, no GoReleaser and
  no packaging build dependencies — only Docker, which `ubuntu-latest` provides.

`packaging/workflow_install_job_test.go` guards all of that by decoding the live
workflow with `gopkg.in/yaml.v3` and asserting the job's `needs`, the absence of
its own `if:`, both matrix rows verbatim, the `make verify-package-install`
invocation, the one-GoReleaser-step count, and that the token `cpio` appears
nowhere in the file. Its expectations are hand-written from the specification,
never derived from the workflow under test.

`make help` was added in the same commit. The Makefile has annotated targets
with `## description` since long before, but nothing printed them; `help` is a
minimal `awk` over `$(MAKEFILE_LIST)` that prints every such annotation, and
both `verify-package-install` and `help` are listed in `.PHONY`.

