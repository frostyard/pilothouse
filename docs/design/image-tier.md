# Image-tier validation (#80)

Living document; extracted verbatim from [overview.md](overview.md) (formerly
`yeti/OVERVIEW.md` — its phase narrative is historical record, see
[../branding.md](../branding.md)). It covers the five #80 slices that
validate the released RPM on an image-based (uCore/bootc) host: the
process-test foundation, the released-RPM fixture, uCore composition, the VM
consumer, and the root-only lifecycle owner plus its
`.github/workflows/image-tier.yml` CI wiring. The booted-VM harness it builds
on is [vm-harness.md](vm-harness.md); the artifact contract the packages must
satisfy is
[../specs/artifact-contract.md](../specs/artifact-contract.md).

## Where it sits in the tree

```
test/image/releaserpm/
                      test-only Go command for #80's released-RPM fixture.
                      It resolves the latest stable GitHub release, selects
                      exactly one x86_64 RPM, verifies release size and SHA-256
                      while downloading, and writes an explicit manifest plus
                      RPM below a caller-owned ephemeral workspace. It is not a
                      shipped binary; ordinary repository gates still analyze
                      and test it, while the image-tier workflow invokes it
                      through the lifecycle owner (see
                      "Image-tier released-RPM fixture" below)
test/image/compose-ucore.sh
                      third #80 slice: verifies the uCore index and linux/amd64
                      member, revalidates the released RPM and checked-out
                      executables, and builds distinct baseline/update
                      derivatives in workspace-local Podman storage (see
                      "Image-tier uCore composition" below)
test/image/ucore-vm-test.sh
                      fourth #80 slice: consumes those two local images,
                      boots an official checksum-verified Fedora CoreOS QEMU
                      disk, switches it to the baseline through guest-local
                      containers-storage, validates enforcing SELinux and
                      truthful capabilities, switches to the update, then
                      rolls back with digest-slot continuity checks. It
                      quiesces every live resource it owns but leaves
                      exact-store reset and workspace deletion to the enclosing
                      lifecycle owner (see "Image-tier uCore VM consumer" below)
test/image/ucore-image-test.sh
                      fifth #80 slice and root-only lifecycle owner. It runs
                      acquisition, composition and VM validation as bounded
                      process groups, waits for group readiness, forwards and
                      reaps signals, resets the exact private Podman store, then
                      removes its private workspace. image-tier.yml invokes it
                      on main and on vm-boot-labelled pull requests
```

**Image-tier process-test foundation (#80, first slice).**
`packaging/imagetest/image_child_test.go` adds deterministic test support only;
there is no `test/image/` shell harness yet. One `t.TempDir()` owns `cwd`, `home`,
`runner`, `tmp` and `bin` subdirectories. This is the complete test-owned
workspace advertised through environment and cwd, not a filesystem or network
namespace. Children receive only the explicit environment. The tier's sole
child constructor applies a one-second context deadline, starts a dedicated
process group and kills the whole group on expiry. Stdout and stderr use
anonymous regular files created below `tmp`: inherited descriptors cannot
hold a pipe open, no named filesystem entry remains for the child to reopen,
and a no-op child leaves a whole-sandbox snapshot unchanged. A read retains
at most 4 MiB and reads one additional sentinel byte to reject larger output;
this is a read-side memory bound, not a write-side disk quota. A recursive
snapshot records each entry's stat-backed type,
ordinary and special permission bits, size, streaming SHA-256 digest for
regular-file content, and symlink target. Isolated self-tests prove each
recorded field, the layout and non-inherited environment, separated output
and exit status, same-process-group cleanup after both timeout and normal
parent exit, a bounded direct non-shell tool, no-op composition, the capture
read limit, and tool lookup.
Daemonizing into a different session or process group is outside the helper's
contract and must be rejected by later shell-harness policy.
Whole-workspace lifecycle and cleanup, images, VMs, SELinux, update/rollback
and CI wiring are supplied by the subsequent #80 slices below.

`packaging/imagetest/image_process_test.go` now enforces the image-tier process
contract over every live, non-empty Go file in that isolated test subpackage
with Go's parser and AST,
not source-text matching. It resolves import names structurally; rejects
alternate, dot and blank imports of the restricted packages; closes the
allowed `os/exec`, `context`, `os.StartProcess` and build-constraint surface;
and permits only the four named, shape- and count-pinned `syscall` selectors
needed to create and kill the child process group. The isolated package cannot
reach legacy unexported packaging-test helpers, every new local helper joins
the audited file set, and new dependencies are rejected. Restricted import
names may not be shadowed. The one
`CommandContext` must consume the uniquely bound timeout context in
`imageRunChild`; neither that context nor its uniquely bound cancel function
may be reassigned, aliased by address or used anywhere beyond their exact
binding/call/defer positions. The resulting `command` may not be aliased or
used to mutate its cancellation or process identity outside the pinned
process-group flow. Process-group setup, cancellation registration and
the single `Run` execution must be unconditional top-level runner statements;
the cancellation callback has a pinned two-statement straight-line body, and
the runner has exactly one final direct result return. Critical predeclared
identifiers used by those checks may not be shadowed. The constructor call may
not hide in a function literal, and the context cancel must be deferred
directly in the runner body. A small `go/constant` evaluator
proves the package timeout is in `(0, 10s]`. The guard includes one deliberately
invalid temporary source fixture so a vacuous implementation cannot pass.
`image_guard_anchor_test.go` is compiled independently and fails if either
guarded file gains a build constraint or if the guard and process-group
runtime proofs lose any named, top-level `func(*testing.T)` test.

The guard is also validated differentially through the same directory-audit
entry point. Hand-written expectations are compared as exact finding
multisets including file and line, retaining duplicate rule/detail violations.
Clean and unusually reformatted
sources prove the guard is structural rather than text-shaped; negative
fixtures cover import aliases and shadows, every closed spawning surface,
hidden helpers in the complete package set, timeout bounds and context flow,
direct and indirect mutation, command alias and field mutation, nested calls
and cancellation, conditional process setup/cancellation/execution, pinned
process group syscalls, missing execution, early return, predeclared-name
shadowing, ill-typed constants, forbidden imports and build constraints.
This is a closed enumeration of direct Go surfaces, not a claim that arbitrary
future dependencies cannot spawn; adding a dependency or spawning mechanism
requires an explicit guard and specification expansion.

**Image-tier released-RPM fixture (#80, second slice).**
`test/image/releaserpm` is a repository test command, not a product binary and
not a package input. Its only CLI is
`go run ./test/image/releaserpm --workspace ABSOLUTE_PATH`. The workspace must
already exist, be a canonical absolute real directory rather than a symlink,
must be private to one invocation and not be mutated concurrently, and must
not contain `fixture-release-rpm`; the command creates that directory at
`0700` and refuses reuse instead of replacing caller data. On failure,
including failure to write the manifest path to standard output, it removes
only the known files it created and then removes the directory nonrecursively.
Unknown entries are never recursively deleted. On success it deliberately
leaves the fixture for the later image composer, whose enclosing ephemeral
workspace owns final cleanup.

Discovery is fixed to GitHub's
`/repos/frostyard/pilothouse/releases/latest` API. The response is bounded to
1 MiB and must identify a positive-ID, non-draft, non-prerelease release with
a strictly valid semantic-version tag. Exactly one asset name may end in
`.x86_64.rpm`; that asset must have a positive ID, the exact tag-correlated
`frostyard-pilothouse-<version>-1.x86_64.rpm` basename, a size in
`(0, 256 MiB]`, a lowercase `sha256:` digest, and the exact query-free
`https://github.com/frostyard/pilothouse/releases/download/<tag>/<name>` URL.
Userinfo is forbidden. This makes `latest` a one-time discovery input rather
than a filename used again later.

The download is bounded to the advertised size plus one sentinel byte and is
accepted only when HTTP `Content-Length` (when present), bytes written and
streaming SHA-256 all match the same release metadata. Partial files are
`0600`, synchronized and published without replacing an existing destination
only after verification. The
`0600` JSON manifest records schema/kind, release and asset IDs, tag, name,
size, digest, validated release-asset URL and the fixture-relative artifact
basename; it carries no token or host-absolute path. `GITHUB_TOKEN` is optional
for API rate limits and is attached only to `api.github.com` or `github.com`,
only over HTTPS on the default or explicit 443 port, and never to an allowed
`githubusercontent.com` redirect. Redirects have the same scheme and port
restriction. The command performs no install, image operation, upload,
publication or retention.

`main_test.go` uses an in-memory HTTP transport: no test reaches the network.
It proves the happy-path bytes, manifest, modes, request media types and lack
of partial files; rejects ambiguous architecture selection, unstable or
unidentified releases, unsafe metadata, oversized assets, URL disagreement,
HTTP/size/digest failures (including unknown-length short and overlong
streams) and non-canonical destinations; proves cancellation, response-body
closure and close-error rollback, standard-output rollback, known-entry-only
cleanup and no-replace collision preservation; and pins redirect and token
scheme/host/port/userinfo containment.

**Image-tier uCore composition (#80, third slice).**
`test/image/compose-ucore.sh --workspace ABSOLUTE_PATH --bin-dir ABSOLUTE_PATH
--run-id LOWERCASE_ID`
consumes the second slice's manifest and RPM from the same private,
non-concurrently mutated workspace. It rechecks the artifact's exact basename,
size and SHA-256 before making any image operation, so mutation between
acquisition and composition is detected. The output directory
`fixture-ucore-images` must not already exist. It is retained inside the
caller-owned workspace on both success and failure; the enclosing lifecycle
owner removes the whole workspace only after all bounded children have exited.
This deliberately avoids recursively deleting a container store while a
failed builder may still have a helper process unwinding. The composer does
not claim to contain a tool that deliberately detaches into another session,
does not delete or reset its Podman store, and leaves tool progress on
caller-owned stdout/stderr. The production lifecycle owner described in the
fifth slice bounds its log sink, terminates and waits for composer/tool helpers,
then resets this exact Podman store and waits for reset to finish, and only
then removes the enclosing workspace.

Source discovery is exactly `ghcr.io/ublue-os/ucore:latest`. Skopeo resolves
that name once to the OCI index digest. Cosign verifies the digest with the
reviewed uCore key vendored at `test/image/ucore/cosign.pub`; the raw index is
streamed through a 4 MiB limit plus one sentinel byte before it reaches disk,
then required to contain exactly one linux/amd64 member without a variant.
Cosign verifies that member digest separately. Every pull and `FROM` after
discovery names the immutable member digest. The vendored key's upstream
commit and local SHA-256 are recorded beside it in
`test/image/ucore/README.md`; an automated checksum assertion makes key
rotation an explicit code-review event.

Podman's graph root, explicit image store, runroot, libpod temporary directory
and image-download temporary directory all live below the output directory
instead of touching the caller's normal container store. The composer clears
inherited Podman connection and storage environment selectors, points both
configuration surfaces away from ambient files, clears the late
`CONTAINERS_CONF_OVERRIDE` selector, disables file events and remote mode, and
pins `--imagestore`. General configuration uses the explicit empty `/dev/null`
file. A generated mode-0600 `storage.conf` repeats the
overlay driver and all graph/image/run paths, and its path is recorded in the
fixture manifest. Normal system and per-user Podman configuration therefore
cannot redirect writes. It pulls the verified member and builds two distinct,
fixture-labelled local references: `baseline` and `update`. Both use the same
released RPM; their immutable
`/usr/lib/pilothouse-image-test/slot` markers create two deployments for the
later bootc switch/rollback test. The Containerfile installs only the local
RPM with every package repository disabled and build networking set to none,
then overlays the `pilothouse` and `pilothoused` executables built from the
checked-out head. The composer requires those two regular executable inputs
from the explicit canonical `--bin-dir`, records their SHA-256 values, copies
them into the private build context, passes the digests into the offline build,
and the Containerfile verifies the installed bytes. The release RPM is
therefore the immutable packaging/image-delivery substrate, while the
checked-out executables make a pull-request run exercise the current broker
capability and host-image queries. This split is necessary because v0.6.0
predates both queries; treating its unknown-query 403 as current behavior
would falsely claim the acceptance requirement was tested.
The latest release is currently `v0.6.0`, whose RPM predates the per-format
packaging correction and contains the Debian `@include common-auth` PAM
service rather than Fedora's `password-auth` service. That would make the
authentication prerequisite fail on uCore even though #80 explicitly assumes
#67's PAM contract. The composer therefore selects compatibility value
`v0.6.0-debian-pam` only for the immutable GitHub release ID `358276825` and
asset ID `486354638` with their matching tag and basename; all other release
identities select `none`. The Containerfile first requires the installed
legacy policy to have SHA-256
`0e8ab613d8bb5d197ce6ce92d0e67098e70ae0de60eea5678cac8c20e8227992`,
then replaces it with `packaging/rpm/pilothouse.pam`, whose independently
checked SHA-256 is
`af72dc5708248288d056e3ef7d8188d6c24b6991f1f2b50e4805e2108f505993`.
The compatibility choice and release identity are recorded in the image
fixture manifest, including the RPM basename. The VM consumer requires the
legacy compatibility value if and only if all four selector inputs match; a
legacy identity paired with `none`, or a different identity paired with the
legacy value, is rejected. This does not mask a future packaging regression:
every future release receives no override.

It then enables `pilothoused.service` and `pilothouse.service` in the
ephemeral derivative so systemd starts them after every bootc transition,
before running `bootc container lint`. This image-build prerequisite does not
turn the guest validation into a duplicate of #67's activation contract: the
guest neither starts/restarts the units nor asserts their enablement state.
The private build context is below `fixture-ucore-images` and contains only
digest-rechecked copies of the released RPM, reviewed Fedora PAM policy and
two checked-out executables, never the workspace-local container store. The
output manifest records the release and compatibility identity, executable
source and digests, source-image digests, both local refs and IDs, and all five
storage paths. There is no push, upload or host-store image. It
also records the composer's effective UID. A composition intended for the VM
consumer must be run as UID 0 because composition, the consumer's destructive
post-export store emptying, final exact-store reset and workspace cleanup
deliberately share one rootful ownership domain. Rootful Podman cannot safely
reopen a rootless store.

`packaging/imagetest/ucore_compose_test.go` executes the real composer only
against bounded fake Skopeo, Cosign and Podman tools through the image tier's
sole one-second test process helper. That deadline bounds fake-test failures;
it is not a production composition runner. Strict fake argv and environment
checks prove immutable digest, offline build, the exact legacy-release PAM
compatibility selector, the four-file private build context, checked-out
executable digests, local-storage and remote-mode boundaries. The suite also proves both signature-failure
positions, the raw-index byte cap, ambiguous-member rejection, retained
partial storage, exact manifest contents, distinct slots and absence of a
push. An effective
instruction parser prevents commented-out local-RPM installation or
`bootc container lint` from satisfying the Containerfile contract, and a
SHA-256 assertion pins the vendored key.

The fourth slice consumes these fixtures as described next; the fifth owns
their complete CI lifecycle.

**Image-tier uCore VM consumer (#80, fourth slice).**
`test/image/ucore-vm-test.sh --workspace ABSOLUTE_PATH [--ssh-port PORT]` is a
root-only consumer of a completed `fixture-ucore-images/fixture.json`. It
accepts no source image or artifact argument. Before booting anything it
requires the manifest's graphroot, imagestore, runroot, libpod tmpdir, image
tmpdir and storage configuration to equal their fixed paths below the supplied
canonical workspace, requires the baseline/update refs to share the one
fixture prefix, requires the recorded producer UID to be 0, and re-inspects
both local IDs. It is a narrow consumer rather than a read-only one: after
export it removes every image from that isolated store and proves the image
list empty, releasing the separately tagged base as well as both derivatives,
while the outer lifecycle owner retains responsibility for the final
store reset. Every Podman call carries the
same explicit remote-off, root, imagestore, runroot, tmpdir, no-events,
overlay-driver and configuration isolation as the composer.

The baseline deliberately does **not** go through `bootc install to-disk`.
uCore's own `ucore/vm-test.sh` records that this FCOS/uCore combination does
not create a `LABEL=boot` partition while FCOS sets `skip-boot-uuid=true`;
GRUB therefore stops at `no such device: boot`. The supported bootstrap is the
official Fedora CoreOS QEMU disk. The consumer resolves the x86-64 `qcow2.xz`
from the stable stream JSON, requires the download URL to stay under
`builds.coreos.fedoraproject.org`, validates the metadata's compressed
SHA-256, reads the xz index and rejects a declared uncompressed size above
4 GiB before decompression, then validates the independent uncompressed
SHA-256 and deletes the compressed copy. It
then creates a private 40-GiB qcow2 overlay and an Ignition document carrying
one run-local key for the unprivileged `core` account. Skopeo exports both local
uCore refs into one OCI layout; its ref annotations must be exactly `baseline`
and `update`, its config digests must equal the fixture manifest's image IDs,
and shared compressed layer blobs occupy the layout only once. The isolated
host image store is then emptied and proved empty. The layout is capped at 3
GiB, copied into a sparse 3.5-GiB no-journal ext4 fixture disk with
`mkfs.ext4 -d`, and recursively removed from its one fixed transient path
before the FCOS download is materialized.
QEMU attaches that disk as a second read-only virtio drive. The guest mounts it
by the fixed filesystem label with read-only, noload, nosuid, nodev, noexec and
an explicit SELinux container-file context. FCOS's Skopeo streams `baseline`
and `update` from the mounted OCI layout into root containers-storage. This
avoids the full uncompressed random-access temporary tar that containers/image
creates for a compressed Docker archive. Both loaded
Podman image IDs must equal their manifest IDs exactly; the fixture disk is unmounted
before the baseline is staged with
`bootc switch --transport containers-storage`; the first proven reboot is the
transition from FCOS to the baseline under test. All privileged guest calls
use `sudo -n`. The update later switches from the same already-verified
guest-local store.
QEMU runs as a foreground child of the harness (backgrounded only by the
owning shell), with
KVM, q35, OVMF pflash, one writable qcow2 virtio boot disk, one read-only raw
ext4 virtio fixture carrier, the Ignition fw_cfg and one loopback SSH forward.
Its
serial output and stderr remain on the caller's standard streams instead of
growing unbounded workspace log files. SSH connection attempts, copies and
every synchronous Podman or Skopeo operation have explicit timeouts.
Broker readiness uses `guest_sudo test -S /run/pilothouse/broker.sock`:
`RuntimeDirectoryMode=0750` deliberately prevents the unprivileged `core`
account from traversing `/run/pilothouse`, so an unprivileged `test -S` would
be a permanent false negative even while the broker is healthy.

`test/image/guest/validate-ucore.sh` is copied into the guest and invoked
through an explicit `sh` interpreter. It creates one random-credential,
wheel-group account solely because the broker capability and host-image
queries require authentication; this is a prerequisite, not a repetition of
#67's PAM positive/negative matrix. It neither restarts nor asserts activation
of the services #67 already covers. On the baseline, update and rolled-back
baseline it requires the immutable `/usr/lib/pilothouse-image-test/slot`
marker, enforcing SELinux and a functional `bootc status --json`. It compares
the broker's exact sorted capability list with a list produced independently
from systemd, journal, sysext, bootc, rpm-ostree and automatic-update unit-file
observations. The response must be one JSON document containing only canonical
lowercase capability identifiers; embedded line breaks and other output-shape
injections are rejected before `jq -r` can turn them into comparison lines.
Opt-in capabilities remain absent because the packaged unit configures none.
It also requires the read-only host-image broker query to
report bootc available. Shape alone is insufficient: the broker's booted,
staged and rollback image/digest pairs are normalized and compared exactly
with an independent `bootc status --json` captured in the same guest phase.

The SELinux smoke establishes a journal cursor immediately before the two
broker reads and fails on any AVC denial after that cursor except delayed
uCore boot records whose source context is exactly
`system_u:system_r:coreos_boot_mount_generator_t:s0` and which explicitly
carry `permissive=1`. uCore emits those upstream boot-generator records even
while the system is globally enforcing; the narrow domain-plus-permissive
classification prevents them from obscuring the controlled application
window without excusing an enforcing denial.

The other classified record is bootc's own SELinux capability probe. Before
answering `bootc status --json`, bootc deliberately runs `chcon` with an
invalid label to discover whether it has `mac_admin`; upstream's generated
TMT configuration calls the resulting AVC expected and informational
([bootc commit `24ee5eac`](https://github.com/bootc-dev/bootc/blob/24ee5eac452bd590fb8eff92714994ae66ca611a/crates/xtask/src/tmt.rs#L1229-L1240)).
The scanner accepts at most two records total in the controlled two-query
journal window, matching the two probe attempts observed in the enforcing
uCore guest, and only when the permission, command, capability number, source
and target contexts, class, and `permissive=0` value all match exactly. A
third record or any near-miss is unexpected and fails. This constrained
classification avoids granting the broker the much broader `install_t`
privilege just to silence a read-only upstream probe.

On failure the scanner emits at most the first 20 rejected journal records,
keeping diagnostics bounded while identifying the exact denial. It separately
scans the current boot for AVC denials naming Pilothouse, its daemon, runtime
directory or state directory. The test does not claim a dedicated Pilothouse
SELinux domain; the released RPM ships no policy. It intentionally does not
repeat #67's directory ownership, root-login rejection, wrong-password,
journald read-back, runtime sentinel or plain-reboot posture assertions.

The host exports both already-local fixtures as one job-local OCI layout; there
is no local registry and no push. Its two refs share compressed layer blobs.
Emptying the isolated image store before
the FCOS download, deleting the verified compressed FCOS image after
decompression, building the sparse fixture disk only after the store is empty,
and deleting its standalone OCI layout before FCOS acquisition are
load-bearing peak-disk controls for the standard runner. During carrier
construction the standalone layout and the carrier's allocated data blocks
briefly coexist; neither the private image store nor FCOS overlaps that step.
The layout is capped at 3 GiB and the sparse carrier's logical size at 3.5
GiB under the outer 10 GiB free-space preflight. The baseline is switched first
from the FCOS bootstrap. Its staged
name must match the manifested local
reference and its staged digest must be a canonical SHA-256. Each OCI ref's
config digest is checked against the manifest before carrier construction, and
its loaded guest Podman image ID is checked against that manifest again after
import.
That exact staged digest must be booted afterward. The update was loaded and
identity-checked in the same stream, then follows the same local switch path.
After
its reboot, the exact staged update digest must be booted and the former
baseline digest must occupy rollback. `bootc rollback` plus a third proven
reboot must reverse those two digests and restore the baseline slot marker.

The runner never uses `setsid`, `nohup` or daemonization. Its only recursive
deletion targets the fixed transient OCI layout after carrier construction;
the VM directory remains the outer lifecycle owner's responsibility.
Its exit path stops and waits for QEMU. It retains the VM fixture directory and
empty private-store structure. The
enclosing lifecycle owner invokes
acquisition and composition with a bounded log sink, waits for their helpers
and this consumer, resets the exact private store and waits for that reset,
then removes the workspace.
Before acquisition, that owner requires at least 10 GiB available on the
workspace filesystem; insufficient capacity is a named preflight failure rather
than a late ENOSPC during composition or guest import.
QEMU is the only background statement in the runner's complete AST, including
all function bodies. Bash coprocesses, shell coprocess/disown forms and any
process substitution are forbidden. Every other helper remains synchronous,
so no untracked child can retain CI descriptors past cleanup. The download,
SSH/SCP, Podman and Skopeo operations that can wait on external systems retain
their explicit timeouts. The private-store Podman invocations are closed to
exact fixture-ID inspection, all-image removal inside that isolated store and
the empty image-list proof. Bounded Skopeo calls are closed to the two
containers-storage-to-OCI exports and raw inspection of the resulting OCI
refs. On success, cleanup is immediately followed by trap disarm, which is the
penultimate statement; the exact PASS log is last. No process can be created
after the last verified cleanup.

`packaging/imagetest/ucore_vm_test.go` parses both harnesses with
`mvdan.cc/sh/v3/syntax` and inspects executable calls in either the selected
function body or the top-level main region. Named functions must have exactly
one definition across the complete AST, and each script's complete function
name set is fixed so a new function cannot shadow `set`, `trap`, `timeout`,
`cmp` or another reviewed builtin/program. The reviewed shell error modes must
be the first executable statements; dynamic `eval`/source calls are forbidden.
Alias, `shopt`, `enable` and hash-table mutation are forbidden as well, so
later exact command nodes cannot be rebound after parsing; quoted command names,
`command`/`builtin` options and repeated wrapper prefixes are normalized for
that check. Every command position (including a wrapped effective command) must
resolve statically from literal or quoted AST word parts; command names carrying
shell escapes and parameter expansions are rejected rather than normalized, so
they cannot hide a trap or teardown bypass.
The runner main path's complete ordered effective-command sequence is fixed,
including multiplicity. An otherwise unknown outer wrapper cannot introduce a
dynamic interpreter or alternate guest bridge while evading the argv scans.
Its complete output-writer and descriptor-routing set is fixed too, and both
firmware copies are exact direct foreground statements with no redirection.
Together those guards keep the canonical guest validator unchanged between its
source check and the reviewed guest copy.
The non-returning `fail` implementations are exact, as are the resource teardown
and bounded SSH/SCP wrapper bodies. Critical calls are matched as one exact
argument vector rather than pieced together from subsequences; the unique
baseline switch, update switch and QEMU action must occur exactly once across
the whole AST. Quoted or unquoted path-qualified Podman/QEMU executables are
normalized for the same policy. The QEMU statement itself must be backgrounded
and immediately followed by its `$!` capture, and the EXIT trap must be one direct,
foreground parent-shell statement armed before the first disk/resource mutation
and disarmed only after one direct, fatal explicit cleanup. The fixture storage
paths, Podman argument array, QEMU PID and observed deployment-slot identities
become readonly after their separately fail-closed captures. Their assignment
sets are exact, so cleanup cannot be redirected to a different container store
or disk and update/rollback comparisons cannot be made self-referential. The
capture and matching readonly declaration sequences are contiguous top-level
statements; an indirect `read` or other mutation cannot be inserted into the
gap before protection. Every statement in those sequences is foreground,
non-negated and unredirected, so a background readonly declaration cannot
confine protection to a subshell. The same parent-shell execution rule applies
to every runner readonly declaration, including the FCOS stream URL, compressed
archive, verified backing image, disk overlay, Ignition config, the shared OCI
fixture layout, its size limits, ext4 carrier, filesystem label, guest device
and mountpoint, SSH key, credentials and firmware identities.
The canonical workspace and SSH port likewise become readonly immediately
after the exact port-validation statement and before root checks or fixture
paths use them; later code cannot redirect the bootstrap or archives to another
host path. The canonical equality statement,
port validation and readonly declaration are contiguous, and the complete
workspace/canonical-workspace/port assignment sets are exact.
Comparisons and
critical evidence calls must feed `|| fail`. Capability sorting is pinned
separately to the independently probed expected file and decoded broker file;
the expected and actual host-image normalizers likewise accept only their
respective `bootc status` and broker-response inputs. Every critical evidence
file has a complete, exact redirection-writer set and all of those writers must
use the reviewed command, file descriptor, operator and literal target, then
precede its comparison/scan; generic copy/move/truncate/stream writers and
every alternate sort are forbidden on the guest evidence path. Redirection-only
and compound-command statements are recorded too. The write-capable `<>` form
and every `<&`/`>&` descriptor duplication are covered alongside ordinary
output redirections. A critical writer statement must contain its one reviewed
redirection and no additional descriptor routing, so a later `1>&3` cannot
leave an empty evidence file behind. Every pathname target must be one reviewed
literal path, so descriptor changes, variable targets and `/./` path aliases
cannot evade the writer set. The broker query
helper's complete curl/output/status body is exact-pinned because its legitimate
destination is necessarily a parameter. The guest main path also has a closed
allowlist of effective command names; `stdbuf`, `chroot`, `awk`, shell
interpreters and any other unreviewed execution layer fail the guard before
they can hide a writer. Its only four command-prefix assignment sites are
closed as well: the two `LC_ALL=C` sorts and the two credential-to-`jq`
environment projections. Assignment-only statements have a closed name set,
with the constants, expected slot and work directory values pinned; a
standalone or prefixed `PATH=...` cannot alter how an otherwise reviewed
command resolves or make phase identity self-referential. Mutable tools on the
evidence path are narrowed further to their reviewed read-only argument vectors:
`journalctl`, `systemctl`, `bootc`, `rpm-ostree`, `systemd-sysext` and `sed`
cannot gain vacuum, service mutation, deployment mutation or file-writing
modes. The guest has exactly one direct `trap cleanup EXIT`, and both that
invocation and the cleanup body are exact, so an EXIT trap cannot replace a
validation failure with success. Its `log` helper is exact too, and Bash's
variable-writing `printf -v` extension is forbidden on the main path so a
reviewed assignment cannot be changed without appearing in the assignment
audit.
The two AVC scans use
`jq -Rse` predicates whose false result, read error or malformed execution all
feed the same fatal edge, leaving no mutable shell status variable. Both scans
and every core slot/SELinux/capability/host-image assertion or normalizer must
be direct foreground top-level fatal statements, so an unreachable outer branch
cannot preserve their AST while skipping them. The unique cursor
writer/decode/nonempty chain is ordered before both exact broker calls, and its
assignment set is closed, so moving or recapturing the cursor cannot exclude the
operations under test. Both broker calls must in turn precede the controlled
window's journal writer, which must precede the broad AVC predicate; the scan
cannot be moved ahead of the operations it is meant to observe.
Successful top-level
shortcuts are rejected, and the runner's complete trap set is the one EXIT arm
plus its two reviewed disarms; an ERR/DEBUG/RETURN override cannot make a failed
command green. All three guest validation invocations must be direct, foreground
top-level statements, not calls parked behind a false conditional, and their
line ordering is anchored respectively after the FCOS-to-baseline continuity
proof and before the update switch, after the update continuity proofs, and
after the rollback continuity proofs. The baseline/update staged name/digest
shape assertions and all five post-reboot deployment-slot comparisons are
likewise direct foreground fatal statements. Each staged proof is ordered
after its two status captures and before its corresponding reboot; wrapping
continuity or cleanup in an unreachable branch cannot leave the recursive AST
looking valid. Direct fatal guards inspect both child statements for negation
and backgrounding, while the outer `||` cannot carry a redirection, so
`! cmp ... || fail` cannot invert an evidence oracle while retaining the
expected command node.
The runner's main path has closed sets for `guest_copy`, `guest_run`,
`guest_sudo` and `guest_sudo_long`: it may copy only the validator and
credentials; create and restrictively mount the fixed ext4 carrier; perform
exactly two OCI-to-containers-storage Skopeo copies; unmount and remove the
mountpoint; then issue only the reviewed setup, baseline/update switches and
rollback commands. The SSH-up, fixture-device, boot-ID-change, broker-ready
and reboot function bodies are exact alongside the copy/run/sudo wrappers. The runner first
canonicalizes the script file itself with
`readlink -f`; that path and its derived directory become readonly immediately,
and the validator declaration is pinned to
`$SCRIPT_DIR/guest/validate-ucore.sh`; a readable empty file cannot be
substituted at the source end. The source must be a non-symlink, nonempty,
readable regular file. Invoking the runner through a symlink still resolves the
repository's real validator rather than a sibling of the symlink. An extra
host-to-guest command therefore cannot
replace the copied validator while source guards continue inspecting the
pristine repository file. The runner's `log` body is exact as well, so the
post-reboot broker-ready log calls cannot hide an unreviewed guest mutation.
Manifest extraction, fixed-storage comparison and the bounded private-Podman
wrapper bodies are exact too; their reviewed calls cannot be retained while a
function body skips path containment or cleanup work.
Direct main-path SSH/SCP/SFTP/rsync calls and shell/network execution wrappers
are forbidden. Guest execution and transfer must cross the exact
`guest_run*`/`guest_probe`/`guest_copy` bodies; a raw SSH command cannot replace
the validator outside the closed wrapper-call sets. The low-level
`guest_probe` and `guest_run_timeout` helpers have zero direct main-path
callsites and may occur only inside their exact higher-level wrapper bodies.
Every derived resource declaration—VM directory, FCOS stream/download/backing
paths, qcow2 overlay, Ignition config, OCI fixture layout, ext4 carrier,
filesystem label, guest device and mountpoint, SSH key, credentials and
firmware paths—also has one pinned readonly value, not merely a pinned
variable name.
Outside the exact reviewed guest wrappers, interpreters, privilege/process
wrappers and bridge programs are rejected anywhere in a main-path argv, not
only in command position. A `timeout bash -c ...` layer therefore cannot hide
raw SSH behind an otherwise bounded outer command.
Whole-file
negative policies include nested function bodies and recognize direct or
wrapped host Podman bypasses, wrapped/alternate push, any recursive removal,
extra bootc switches/QEMU processes and registry forms. It deliberately does
not search raw shell text: comments, string literals, no-op copies and commands
moved into an uncalled decoy function cannot satisfy the guards. The guest also
requires exactly one capability-response JSON document and binds `jq`, `sort`,
journal and AVC-filter failures directly to fatal edges so pipeline or
line-filter status semantics cannot turn malformed evidence or an inspection
error into a clean result.

**Image-tier lifecycle and CI wiring (#80, fifth slice).**
`test/image/ucore-image-test.sh --run-id LOWERCASE_ID` is the root-only owner
of one complete image-tier run. It accepts no workspace path. Instead it
canonicalizes the repository, creates one unpredictable mode-0700 workspace
below `/var/tmp`, and derives every RPM,
image-store and VM path from that workspace. Acquisition, composition and VM
validation are three direct synchronous `run_bounded` calls. Their respective
outer deadlines are 5, 75 and 75 minutes; the inner tools retain their narrower
deadlines. `capture-bounded-output.sh` runs the command and a `tail` collector
beneath the same timeout-owned process group, retaining at most 4 MiB in the
phase-local regular log. It does not set a process file-size limit, so
compilers, image builders and other phase commands can create artifacts larger
than the log bound, while the outer timeout still covers collection and a
writer that keeps the stream open. `run_bounded` creates exactly one
separate-session background process group, records its PID, confirms the group
exists, waits for its leader, and rejects and terminates any same-group
descendant that survives the direct command before clearing the PID. The tail
collector ignores soft INT/TERM so producer shutdown yields EOF and flushes
the retained diagnostics; the owner's follow-on TERM/KILL escalation still
bounds a collector that cannot drain after `timeout` exits. INT/TERM received
during launch is latched until that readiness check;
then it is forwarded to the entire group and waited before EXIT cleanup
proceeds. Behavioral tests cover INT and TERM both before and after group
readiness and prove the phase process is gone. The outer shell creates no
coprocess, disowned or process-substitution children.

The success path and the EXIT/INT/TERM path converge on the same cleanup
function. If composition reached the point where its exact regular,
non-symlink `storage.conf` exists, cleanup runs `podman system reset --force`
in the foreground with the
same remote-off graphroot, imagestore, runroot, tmpdir, no-events, overlay and
configuration selectors used by the producer and consumer. That reset has its
own 10-minute/process-kill/log-size bound. Only after the reset command has
finished does cleanup recursively remove the one generated workspace with
`--one-file-system`. A reset failure still makes the job fail, but the already
quiescent workspace is removed so derived images are not retained. Successful
cleanup is followed by trap disarm and the final PASS line.
If bounded TERM/KILL escalation cannot quiesce the reset group,
`current_phase_pid` remains set and cleanup refuses recursive removal, leaving
the runner-owned workspace for runner teardown rather than deleting storage
beneath a live process.
If no configuration exists, all three store roots must also be absent; a
partial or redirected store cannot turn reset into a successful no-op.
Once a signal handler begins, it deliberately ignores reentrant INT/TERM while
the bounded cleanup frame finishes; a first signal received while cleanup is
already active is still forwarded to its live reset group, waited, and followed
by workspace removal. Cleanup traps are armed before the workspace directory is
created, and the exact `/proc/sys/kernel/random/uuid` v4 source and validation
are structurally pinned. Cleanup also requires a successful-create ownership
flag before reset or recursive removal, so a collision or failed `mkdir` cannot
authorize deletion of a pre-existing target.

`.github/workflows/image-tier.yml` carries one `ucore-vm` job. It runs on every
push to `main`; for pull requests it runs only while the `vm-boot` label is
present, and `opened`, `synchronize`, `reopened` and `labeled` triggers ensure
the label certifies the current head. The job uses `ubuntu-26.04` specifically:
that runner provides Podman 5 and the `--imagestore` option the private-store
contract requires. It sets up Go, installs the native PAM/systemd headers plus
QEMU/OVMF and cosign, builds the checked-out executables without GoReleaser,
enables live KVM, checks `/dev/kvm` and the Podman option, then invokes the
lifecycle owner once through explicit root `bash` with explicit PATH and token
values. It has read-only contents permission, no dependency on the branch's
GoReleaser package job, no repository `secrets.*` reference, no artifact
upload or download, and no publication command. The packaging substrate
remains the released-RPM fixture selected by acquisition; the two local
executables are the checked-out behavior under test.

`packaging/imagetest/ucore_orchestrator_test.go` parses the lifecycle owner with
the shared shell AST helpers. It pins every non-function top-level statement
including expansions and redirections, function declaration order and
undecorated shape, the exact function set and bodies, static command resolution,
phase argv, one timeout-owned background process group containing the output
collector, bounded timeout and tail calls, one ownership-gated recursive
deletion target, readonly derived paths and absence of publication. Behavioral
regressions prove a phase can create an artifact larger than 4 MiB while its
retained log remains capped, and that output-inheriting or output-closing
descendants cannot outlive the phase. The negative mutations cover command-cache
assignment, a constant UUID source, a declaration-level side effect and a
cleanup function moved below its trap. `test/image/ucore_orchestrator_signal_test.go`
exercises INT/TERM before and after group readiness, cleanup-active return and
owned/unowned workspace cleanup. `packaging/workflow_image_tier_test.go`
decodes the live YAML and fixes its complete permission map and ordered step
bodies in addition to its trigger, label, runner and timeout, so an added
upload step or a fail-open suffix is visible.

