# Booted-VM harness (`test/vm`, #67)

Living document; extracted verbatim from [overview.md](overview.md) (formerly
`yeti/OVERVIEW.md` — its phase narrative is historical record, see
[../branding.md](../branding.md)). It covers Layer B of package validation:
the QEMU/KVM harness that boots a stock Debian or Fedora cloud image,
installs the built package, activates both units, authenticates a real
non-root administrator through PAM, reads the daemon's own journal record
back through the broker, and proves the posture that survives a real reboot —
plus the `vm-boot` CI job that runs it. Layer A (container installs) is
[install-validation.md](install-validation.md); the image-based (uCore)
tier is [image-tier.md](image-tier.md); the static artifact contract is
[../specs/artifact-contract.md](../specs/artifact-contract.md).

## Where it sits in the tree

```
test/vm/              the booted-VM harness (Layer B, #67). vm-boot-test.sh is the
                      one entry point (--family debian|fedora --artifact-dir <dir>),
                      committed executable and meant to be run through an explicit
                      interpreter (`bash test/vm/vm-boot-test.sh`); packaging.yml's
                      vm-boot job is what calls it. images.env is the single pinning site recording
                      each distro family's cloud image URL, checksum algorithm and
                      digest. lib/ holds the sourced, non-executable bash libraries:
                      images.sh (fetch and verify against that pin), cloudinit.sh
                      (generate the run-time credentials into a 0700 workspace and
                      emit the NoCloud seed), vm.sh (QEMU/KVM boot of a qcow2 overlay
                      plus the serial console channel), ssh.sh (the guest's SSH
                      lifecycle) and diagnostics.sh (the failure-time discriminator).
                      guest/ is the single directory holding every guest-side script:
                      the sourced, non-executable lib.sh plus install-package.sh,
                      check-activation.sh, check-pam.sh, check-journal.sh,
                      capture-pre-reboot.sh and check-reboot-posture.sh;
                      every executed one is committed 100755 and invoked as
                      `sudo -n sh ~/vm-boot/guest/<name>.sh`. At this commit a run
                      ends once the guest has come back from a real reboot with both
                      units active unaided, the same capability set, a destroyed and
                      recreated /run/pilothouse and a persisted /var/lib/pilothouse,
                      run by .github/workflows/packaging.yml's vm-boot job on main and
                      on the vm-boot pull-request label; packaging/vm_harness_test.go and
                      packaging/workflow_vm_job_test.go guard it structurally
                      without executing it
```

## Pinned image acquisition (`test/vm/images.env`, `test/vm/lib/images.sh`)

Layer B (#67) — installing the artifacts on a **booted** host with real
systemd — is complete as of this commit. **The harness boots a guest,
installs the package, activates both units, authenticates a real non-root
administrator through PAM, reads the daemon's own journal record back
through the broker and proves the posture that survives a real guest
reboot**, and `.github/workflows/packaging.yml`'s `vm-boot` job runs it in CI.
What exists is image acquisition (this section), the boot mechanism
("Credentials, seed, boot and SSH"), the orchestrator, the guest scripts
and the diagnostics that drive them ("The orchestrator, guest staging and
diagnostics") and the CI job that invokes them ("The CI job (`vm-boot`)").

This section covers the pinning table and the host-side fetcher:

- **`test/vm/images.env`** is the *single* pinning site. Per distro family it
  records three values — the image URL, the checksum algorithm and the digest:
  Debian 12 `genericcloud` amd64 from the dated
  `cloud.debian.org/images/cloud/bookworm/20260722-2547/` directory under
  **SHA-512**, and Fedora 42 `Cloud-Base-Generic` x86_64 from the
  `dl.fedoraproject.org/pub/archive/...` path under **SHA-256**. The algorithm
  differs per family because each digest is the distributor's own published
  value (Debian's `SHA512SUMS`, Fedora's `Fedora-Cloud-42-1.1-x86_64-CHECKSUM`);
  a digest computed here would pin nothing. Fedora 42 is archived — the
  non-archive `releases/42/...` path 404s and must not be "fixed" back. Both
  entries are amd64/x86_64 only, matching the two container images the install
  matrix already uses, and both paths are immutable: a `latest`-style URL would
  move out from under the digest. The table is inert data, not a sourced shell
  program: every non-comment line must be one unique
  `VM_IMAGE_<FAMILY>_<FIELD>="value"` assignment.
- **`test/vm/lib/images.sh`** is a **sourced** bash library (no shebang, `set
  -euo pipefail`, committed non-executable) exposing
  `fetch_image <family> <cache-dir>`. It parses the family's row from
  `images.env` without evaluation, rejecting malformed or duplicate fields even
  when `VM_IMAGES_ENV` redirects the table path, then downloads over HTTPS into
  the cache directory and dispatches to `sha256sum`/`sha512sum` per the declared
  algorithm. A mismatch prints **both**
  the expected and the actual digest and exits non-zero; a file already in the
  cache is re-verified before it is reused, so a truncated or tampered copy
  cannot be handed to a caller. On success the verified path is the function's
  only standard output — progress and failures go to standard error.

**`packaging/vm_harness_test.go`** guards this from the existing `packaging` Go
package, alongside `verify_install_test.go`, because this is Layer B of the same
packaging verification chain. The guards are structural and **never execute the
harness**: the only process any of them spawns is `shellcheck` (`--shell=bash`,
skip-if-absent, the same runner shape `verify_install_test.go` uses). They parse
`images.env` as text rather than sourcing it, assert both families' rows and the
two literal image references, and assert the properties that make a pin worth
anything — HTTPS, no `latest` path segment, and a digest whose hex length matches
its declared algorithm (128 for SHA-512, 64 for SHA-256). They also open the
executed-versus-sourced mode discipline that later harness scripts extend:
`lib/images.sh` is sourced, so `Mode().Perm()&0o111` must be zero — the same
instrument `verify_install_test.go` uses in the opposite direction for the
executed install-validation script.

## Credentials, seed, boot and SSH (`test/vm/lib/cloudinit.sh`, `vm.sh`, `ssh.sh`)

The boot half of Layer B's host side: three sourced bash libraries that can
create a guest and talk to it. **They are mechanism only** — they assert nothing
about a running system. The orchestrator that calls these functions and the
guest-side script that installs the package are described in the next section;
the booted-host assertions and the CI job land later.

**`cloudinit.sh` — every credential is generated here, on the host, at run
time.** `create_run_workspace` makes the per-run directory with mode `0700`;
`generate_credentials` writes into it, and only into it, an ed25519 keypair
(`ssh-keygen -t ed25519 -N ''`) and two passwords (`openssl rand`, falling back
to `/dev/urandom`; never a literal), recording them in `creds.env` as
`PH_ADMIN_USER`, `PH_ADMIN_PASSWORD` and `PH_ROOT_PASSWORD`. No guest-side
command generates anything. The ambient administrator name must match the
single-token Linux account grammar `^[a-z_][a-z0-9_-]{0,31}$` before it can be
written unquoted into either generated format; `creds.env` is structurally
closed to exactly those three safe assignments. Every function that touches a
credential disables shell tracing first, and nothing echoes a password or a
private key, so no generated value can reach a job log.

`write_cloud_init_seed` takes the **family** as an argument and emits the
NoCloud `meta-data`/`user-data`; `build_seed_iso` packs them into a `cidata`
volume. `create_seed` runs the three in order and is called **directly**, never
through a command substitution: it publishes `VM_SSH_KEY`, `VM_CREDS_ENV` and
`VM_SEED_ISO` as exported variables, and a subshell would discard all three.
The seed declares exactly **one** login account, the administrator,
with the generated public key, `sudo: ['ALL=(ALL) NOPASSWD:ALL']`, and the
administrator group its own package expects — `sudo` on Debian, `wheel` on
Fedora, the single token by which the two unit files differ. Both the
administrator's and root's generated passwords are set through cloud-init's
`chpasswd:` module with `expire: false`, so root carries a *valid* password
later checks can be run against.

Root gets **no** authorized key and SSH root login is not enabled: stock cloud
images restrict it, enabling it would make the guest less stock, and the
harness needs root's password to be set and later removed under its own
control. Everything that needs privilege escalates instead — which is why the
`NOPASSWD` grant is load-bearing rather than a convenience. Guest commands are
non-interactive with no TTY, so an escalation that prompted would hang; `sudo
-n` turns the same situation into an immediate, named failure.

The seed also builds the half of the serial-console channel that does not
depend on the guest's kernel command line: a
`/etc/systemd/journald.conf.d/99-console.conf` drop-in
(`ForwardToConsole=yes`, `MaxLevelConsole=info`, `TTYPath=/dev/ttyS0`) and
cloud-init's `output: {all: '| tee -a /dev/console'}`.

**`vm.sh` — boot, without touching the image.** `create_overlay` builds a qcow2
overlay in the run workspace with `qemu-img create -F qcow2 -b <base>`, so the
pinned base downloaded by `images.sh` is read-only input and is never rewritten;
there is deliberately no offline image-editing step, because rewriting the
guest's disk or kernel command line would make it no longer stock (Fedora
likewise stays SELinux-enforcing). `start_vm` runs `qemu-system-x86_64` with
`-accel kvm` (never software emulation), `-display none`, a file chardev bound
to `-serial`, and user-mode networking forwarding a loopback port to the guest's
sshd; it exports `QEMU_CONSOLE_LOG` and `QEMU_STDERR_LOG`, the only two things a
guest that never answers leaves behind.

`wait_for_console_boot` is what makes that channel a **gate** rather than a
hope: it polls the console log for a boot marker and, on `CONSOLE_BOOT_TIMEOUT`
expiry, exits non-zero naming the assertion and dumping both logs. `boot_guest`
calls it **before** `wait_for_ssh`, so a run whose serial log receives no output
cannot pass — the diagnostics channel is proven working on every green run
instead of being discovered broken on a failing one.

**`ssh.sh` — one identity, explicit escalation, bounded waits.** `guest_run`
and `guest_copy` address the guest as the administrator account read back from
`creds.env` (never `root@`), `guest_sudo` escalates with `sudo -n`, and
`wait_for_ssh` polls within the explicit `SSH_READY_TIMEOUT`. `guest_copy`
carries **both** directions through one function — staging a host file into the
guest, and retrieving a staged guest file back to the job workspace — and
requires **exactly one** of its two paths to be a guest path, recognised by its
leading `~`: that keeps a single audited site where a guest end of a copy is
constructed, and makes a host-to-host or guest-to-guest call fail by name
instead of copying locally. `reboot_guest`
issues the reboot through `guest_sudo` and then uses a bounded
`SSH_REBOOT_TIMEOUT` poll until SSH can read a boot ID different from the one
captured before the reboot. A changed kernel boot ID directly proves a new
boot; successful retrieval also proves that SSH on that new boot is reachable.

Three things keep that sequence from passing without proving a reboot, and each
closes a hole the SSH probes cannot close by themselves:

- **The reboot command's status is not discarded.** A successful reboot kills
  the connection, which ssh reports as 255; that one status is the expected
  symptom. Any other non-zero status means the command never dispatched — a
  rejected escalation being the likely cause — and `reboot_guest` fails
  immediately carrying the remote stderr. The blanket `>/dev/null 2>&1 || true`
  this replaced made a rejected escalation indistinguishable from the expected
  disconnect, and the wait loop then reported the real failure as a shutdown
  timeout.
- **The guest's `boot_id` is compared across the reboot.** `guest_boot_id`
  reads `/proc/sys/kernel/random/boot_id`, which the kernel regenerates on
  every boot; `reboot_guest` captures it before and polls for a different
  non-empty value afterward. The poll intentionally does not require an
  observed SSH outage: a fast reboot can complete between two probes, while a
  changed boot ID cannot be supplied by the old boot. This is the only
  assertion here that cannot be satisfied by an
  sshd that merely restarted, and it cannot fail on a genuine reboot, so it is
  immune to how the probes happen to fall.

The endpoint it dials, `VM_SSH_HOST`/`VM_SSH_PORT`, is declared with the same
`:-` default in both `ssh.sh` and `vm.sh` — the library that forwards it — so
the two can be sourced in either order; a guard test holds the two
declarations identical so the dialled and forwarded endpoints cannot drift.

`packaging/vm_harness_test.go` guards all three as text and by file mode, and
still executes nothing but `shellcheck`: every file under `test/vm/lib` must be
non-executable, both family branches must exist, the `NOPASSWD` grant must be
present *and* no password-prompting escalation form may appear anywhere in the
library directory, no file under `test/vm` may contain a key blob, a PEM block,
a literal password value or the string `root@`, and no file may disable KVM or
introduce an offline image-editing step. It also pins the three reboot
invariants above by text, so a regression to the suppressed form fails the unit
suite rather than waiting for a VM run to misreport it.

Two of those guards are worth calling out because their earlier forms passed
without proving anything. The private-key print check scans **line by line**: a
single regex anchored with `$` and no `(?m)` matches only at end of file, so it
silently permitted every occurrence that was not the last thing in the file.
It also matches both spellings of the key — the literal `id_ed25519` and the
`$VM_SSH_KEY` variable the scripts actually use — while exempting a reference
followed by `.pub`, since the public key is not a credential.

## The orchestrator, guest staging and diagnostics (`test/vm/vm-boot-test.sh`, `test/vm/lib/diagnostics.sh`, `test/vm/guest/`)

The harness's first end-to-end runnable path. **A run ends once
the guest has come back from a real reboot with its posture proven** (see
"Reboot posture" below); `.github/workflows/packaging.yml`'s `vm-boot` job is
what invokes it (see "The CI job (`vm-boot`)" below).

**`test/vm/vm-boot-test.sh` is the one entry point**, taking
`--family debian|fedora` and `--artifact-dir <dir>`; any other family is
rejected by name rather than defaulted. It fetches and verifies the pinned
image, builds the seed, boots, gates on serial-console output, waits for sshd,
probes escalation, creates the staging directory, selects the artifact, stages
the artifact/guest scripts/`creds.env`, installs the credentials privileged,
runs the guest bootstrap and the activation, PAM and journal checks, and
finally captures the pre-reboot posture, reboots the guest and runs the
post-reboot check.

**One guest identity, staged files, explicit escalation.** The only SSH identity
in the guest is the administrator account cloud-init created. That account
cannot write `/root`, cannot install packages and cannot read a `0600`
root-owned file, so nothing is copied to a privileged destination directly:

- The orchestrator creates `~/vm-boot` (mode `0700`) and `~/vm-boot/guest/` **as
  that account, before anything is copied**, and every guest-bound destination
  is inside it. Nothing ever addresses the guest as root over SSH.
- `creds.env` is staged there and then placed with
  `sudo -n install -o root -g root -m 0600 ~/vm-boot/creds.env
  /root/.pilothouse-vm-creds`, after which the staged copy is removed with
  `rm -f` — the generated root credential must not linger in a file the
  unprivileged account can read. No credential is ever a command-line argument;
  only the path of the file holding it is.
- Every guest script is invoked as **`sudo -n sh ~/vm-boot/guest/<name>.sh`** —
  explicit interpreter *and* explicit escalation. Package installation,
  `systemctl enable --now`, reading that credentials file, opening the `0660`
  `root:pilothouse` broker socket and `journalctl -u` all require root.
- Immediately after the guest is reachable, the orchestrator probes
  `sudo -n true` and fails with a named assertion. A broken `NOPASSWD` grant is
  then reported once, at the top of the run, instead of surfacing obscurely
  three scripts deep.

**Executed versus sourced is enforced by two mechanisms, not one.** Every script
invoked as a program — the orchestrator and every file under `test/vm/guest`
except `lib.sh` — is committed `100755` **and** is run through an explicit
interpreter (`bash test/vm/vm-boot-test.sh` for the orchestrator, whose caller
lands with the CI job; `sh <staged path>` for each guest script, which the
orchestrator already does today). `scp` does not preserve the executable bit
without `-p`, so a harness that trusted the copied mode would carry the same
defect one layer down.
Sourced libraries (`test/vm/lib/*.sh` and `test/vm/guest/lib.sh`) stay
non-executable and are guarded as such, so the two categories cannot blur in
either direction.

**Artifact selection is arch-qualified.** The `packages` job uploads both an
amd64 and an arm64 file per format, and the runner is x86_64 with KVM requiring
guest and host architectures to match, so the orchestrator globs
`"${artifact_dir}"/*_amd64.deb` for Debian and `"${artifact_dir}"/*.x86_64.rpm`
for Fedora, then requires **exactly one** match and otherwise fails naming the
count and the matched basenames. This is the same rule
`packaging/verify-install.sh` already applies for Layer A. The selected file is
staged under a fixed name, so the guest script has nothing to choose.

**`test/vm/guest/` is the single directory every guest-side assertion script
lives in.** They are POSIX `sh` (Debian's `/bin/sh` is dash, Fedora's is bash)
with `set -eu`, and they share `guest/lib.sh`: exactly one `fail()`, which
prints the failing assertion by name and exits non-zero so the script aborts on
its **first** failure, plus `require_root()`, `expect_owner_mode`, the
credentials loader and the `broker_curl`/`web_curl` wrappers. `require_root` is
each script's first effective statement and is the converse of the call form: a
call site that lost its `sudo -n` fails immediately and legibly instead of
producing a confusing permission error later. Because the script is already
root, the curl wrappers carry **no inner escalation** — one escalation boundary
is auditable, one per request is not, and `require_root` is what makes that
safe.

`guest/install-package.sh` installs `curl` and `jq` (test fixtures: `curl` makes
the `--unix-socket` requests, `jq` lets later checks match a JSON field rather
than a substring) and then the staged artifact, through the guest's own package
manager. It deliberately restates **nothing** from Layer A (#77) — dependency
resolution, postinstall repair, PAM policy resolution, unit validity, linkage,
reinstall and removal are asserted there, against these same artifacts. The
Fedora guest is SELinux-**enforcing** and stays that way: `setenforce` and
`permissive` appear nowhere in the harness, so an install that policy would
break fails the gate rather than being worked around.

`guest/check-activation.sh` runs next, and is the first check that asserts
anything about the running system. It **enables and starts** both units itself
(`systemctl enable --now`) rather than asserting they are already active:
`packaging/postinstall.sh` contains no `systemctl` call, so installing the
package deliberately starts nothing, and asserting otherwise would assert a
behaviour the packaging does not have. Each unit is then waited for under one
named constant, `UNIT_ACTIVATION_TIMEOUT_SECONDS`, and on expiry the script
prints **that unit's own** `systemctl status` and `journalctl -u` before failing
by name — both processes log to their own unit's journal, so naming the other
one would pass vacuously. With both units active it asserts `/run/pilothouse`
and `/var/lib/pilothouse` are `root:pilothouse` mode `0750` — the state
systemd's `RuntimeDirectory=`/`StateDirectory=` create, which is exactly the
check no container can make and therefore the one #77 does not attempt — and
`/run/pilothouse/broker.sock` is `root:pilothouse` mode `0660`. Liveness is
proved by an **unauthenticated** `POST` to
`/v1/queries/org.frostyard.pilothouse.capabilities.list` over that socket,
bounded by `BROKER_PROBE_TIMEOUT_SECONDS`, which must answer **exactly `401`**
with a JSON `error` body: every broker query calls `authorize()` first, so a
`401` can only come from a server that accepted the connection and parsed the
request, while a refused connection, a stale socket file, a hang, a `200` or
any other status fails. The authenticated capability list needs a session token
and is not attempted here.

`guest/check-pam.sh` runs next and is the reason this tier exists: no container
can run `pam_authenticate` against a live daemon. It **consumes** the
credentials cloud-init delivered — it sources `/root/.pilothouse-vm-creds` and
generates nothing; `openssl`, `/dev/urandom` and `pwgen` appear nowhere in it —
and starts by reading the `--admin-group` token out of the **installed**
`pilothoused.service` (through `systemctl cat`, so it is the file the package
put on *this* guest), asserting it is `sudo` on Debian and `wheel` on Fedora and
that the cloud-init-created administrator is a member of it. That single token
is the only difference between the two packages' broker units.

Then three logins against the web console on `127.0.0.1:8888`, in this order,
each asserting **one exact status**:

1. `GET /login` for the hidden `csrf` input value — `POST /login` rejects a
   missing `csrf` field with `403`, so a bare POST would fail for the wrong
   reason — then the form POST with `csrf`, username and password, expecting
   exactly **`303`**. There is no pre-login cookie to carry: the session cookie
   is set only after authentication succeeds.
2. The same administrator with a wrong password (derived from the real one so
   it is certainly wrong), expecting exactly **`401`**.
3. `root`, with its **valid** cloud-init-set password, expecting exactly
   **`401`**. A locked or password-less root would be refused by PAM before
   Pilothouse ever saw it, which would pass while proving nothing.

**A `429` is a failure of this check, never a pass.** One failed attempt arms a
per-`username`+`remote` lockout that answers `429` *before* `Authenticate` is
called, so the success comes first (it also clears the entry) and every
assertion names one expected status rather than accepting "any non-success".

Two of the claims cannot be carried by a status code at all, so each is proved
from the journal, bounded by a cursor captured **immediately before** the
request it is about, and matched on the record's **parsed JSON `msg` field**
rather than on a substring of the line:

- After the successful login, **no** record past that cursor in
  `journalctl -u pilothouse.service` may have `msg == "refresh capabilities"`.
  The web process runs the broker's capability query on the administrator's
  behalf right after login, but `refreshCapabilities` swallows its error and
  logs that warning, so the `303` alone proves nothing about the query. The
  record comes from `internal/web`, so this is the **web** unit's journal;
  searching the broker's would find nothing whatever happened.
- After the root attempt, a record past its cursor in
  `journalctl -u pilothoused.service` must have
  `msg == "authenticated account rejected"`, `user == "root"` and an `error`
  containing `direct root login is disabled`. Both PAM stacks run an *account*
  phase whose rejection produces the identical `401`, so the status cannot tell
  the UID-zero refusal apart from it; that message is emitted only on the
  `Resolve` path — reached only after `Authenticate` returned nil — and that
  error text is unique to the UID-zero branch. This is the **broker's** journal,
  the opposite unit from the check above.

The chunk also adds the reusable **authenticated direct-socket helper** in
`guest/lib.sh`: `broker_login` posts `/v1/login` with a JSON `username`,
`password` and `remote`, then `broker_query` posts `/v1/queries/{id}` with the
returned bearer token. Its `remote` is `vm-boot-harness`, deliberately **not**
the web process's `127.0.0.1`: the broker keys its lockout on
`lower(username) + NUL + remote`, so a shared value would let the wrong-password
attempt above throttle a direct login of the same account and turn a status
assertion into a statement about the limiter. The `0660 root:pilothouse` socket
needs privilege, and that privilege comes from the whole script running as root
under `sudo -n sh` (which `require_root` proves) — not from an inner `sudo` per
request. `check-pam.sh` uses the helper once, to assert the administrator's
`session.identity.admin` is `true`, which proves the family's administrator
group **functionally** rather than by string comparison, and then runs the
capability query as an authenticated caller. Credentials reach `curl` through
files (`--data-urlencode name@file`, `--header @file`, `--data-binary @file`)
built by `jq` from the environment, so no credential and no session token lands
in the guest's process table. Finally the generated root password is removed
(`passwd -d root`, `usermod -L root`), which succeeds because the script runs as
root; nothing in it assumes an SSH login as root, which the guest does not have.

`guest/check-journal.sh` runs last and proves the daemon built with the
`sdjournal` tag reads the journal **back** on a booted host. It reuses the
authenticated direct route — `broker_login`, then `broker_query` — to run the
broker's own journal-backed read surface, `QueryServicesJournal`
(`org.frostyard.pilothouse.services.journal` in `internal/broker/api.go`,
registered behind `Systemd` **and** `Journald`) with the `unit` parameter its
handler in `cmd/pilothoused/main.go` reads, for `pilothoused.service`.
`broker_query` fails by name on anything other than **exactly `200`**, and the
assertion is then made on the **response body**: the answer must be about that
unit, `entries` must be the array `services.Journal` declares and must be
non-empty (a `200` carrying nothing read back fails on its own name), and some
returned entry's `message` must contain `privileged broker listening` — the
line the *daemon itself* logs on a successful listen, emitted when
`check-activation.sh` started it and so comfortably inside the reader's
one-hour window. The matching entries' own `unit` field is checked too, so the
record's provenance comes from `_SYSTEMD_UNIT` rather than from the query's
parameter.

**Finding that line in `journalctl` output is explicitly not accepted as
evidence** — that would prove systemd logged it, not that the daemon can read
it back — so `check-journal.sh` reads no log for itself: it invokes neither
`journalctl` nor `systemctl`, and a guard in `packaging/vm_harness_test.go`
enforces that alongside grounding the query id, the parameter name and the
searched-for line against `internal/broker/api.go` and
`cmd/pilothoused/main.go`. The guards read the script as text and, as
everywhere else here, never execute it.

**Reboot posture: what a real reboot proves, and the one inode assertion the
harness makes.** `guest/capture-pre-reboot.sh` and
`guest/check-reboot-posture.sh` are the two halves of the run's last stage. The
capture reads the capability set through the **direct authenticated broker
route** (`broker_login` over the Unix socket, then `broker_query` for
`QueryCapabilities`) — never from the web console, which renders a *view* of the
set rather than the set — plants a **sentinel** file inside `/run/pilothouse`,
and records `/var/lib/pilothouse/audit.db`'s inode, both directories'
owner/group/mode and the boot id into `~/vm-boot/pre-reboot-state.env`. That
file is written as root into the administrator-writable staging directory and
`chown`ed to the one login account, so the orchestrator retrieves it with an
ordinary `guest_copy` in the retrieval direction and no escalation; it carries
**no credential** and is printed into the job log.

The orchestrator retrieves and prints that state and dumps `systemctl status`
and `journalctl` for both units (`dump_pre_reboot_diagnostics`) **before** it
issues the reboot: a guest that never comes back cannot be asked anything
afterwards, and this is the evidence that case is diagnosed from alongside the
continuous serial console log. `reboot_guest` then polls through the reboot
until SSH returns a changed kernel boot ID, so no post-reboot check can be
answered by the sshd from the prior boot, even when the reboot is too fast for
the polling cadence to observe an outage;
the post-reboot script re-reads `/proc/sys/kernel/random/boot_id` and fails if
it matches the recorded one.

`check-reboot-posture.sh` then asserts four things and **enables or starts
nothing** — that both units returned *unaided* is the whole claim, so unit state
is read with `is-active` inside a bounded wait, the only other `systemctl` verb
in the file is the `status` of its own failure dump, and a guard scans the file
(comments included) for any enable-or-start form:

| Claim | How it is proven |
| --- | --- |
| Both units active again | `systemctl is-active --quiet`, broker first, bounded by `UNIT_ACTIVATION_TIMEOUT_SECONDS`, dumping that unit's status and journal on expiry |
| Capability set unchanged | the same authenticated broker route, sorted ids compared for equality; a query that fails **or answers an empty set** is a failure on **either** side, so an empty answer can never compare equal against another empty answer |
| `/run/pilothouse` **destroyed and recreated** | the pre-reboot **sentinel is absent** *and* the directory exists again as `root:pilothouse` `0750` — one assertion group, neither half accepted alone |
| `/var/lib/pilothouse` **persisted** | same owner/group/mode *and* `audit.db`'s inode **unchanged** |

The sentinel is the discriminator because a directory that survived a reboot
still carries its contents and one systemd recreated from `RuntimeDirectory=`
cannot. Absence alone would also hold for a directory that never came back, and
correct-looking metadata alone would also hold for a directory that was never
touched — hence one group.

**`/run/pilothouse`'s own inode is recorded and printed, and asserted nowhere.**
`/run` is a fresh tmpfs on every boot whose inode counter restarts, so correct
behaviour can legitimately reuse the same number: requiring the inode to
**differ** is an assertion that could fail on correct behaviour, and requiring it
to match would be wrong outright. It exists only as context for whichever half
of the group fails. The asymmetry with `audit.db` is principled rather than an
oversight: inode **equality** on the guest's persistent root filesystem, where an
inode number is a durable identity, is sound, and that is the harness's **only**
inode assertion. No pre-reboot audit *record* is required to be readable — the
database file's identity is the persistence proof.

`packaging/vm_harness_test.go` guards that shape in **both** directions: it fails
if the sentinel-absence proof or the recreation-metadata check is removed, fails
if `audit.db`'s inode-equality assertion is removed, and fails if an
inode-**inequality** assertion on `/run/pilothouse` ever appears — the last one by
scanning every file under `test/vm` for any line that names the runtime
directory's inode and takes part in a comparison, a `fail` or a `[` test. The
identifiers it looks for are **derived from the harness**, not listed in the
test: any variable assigned from a `stat`-`%i` read of the runtime directory, and
anything aliased from one, joins the set, so an inequality reintroduced under a
fresh name — or written inline with no variable at all — is still caught.

**At this commit the harness run ends there.** The guest's SELinux audit
posture and image-based hosts are #80's. Its image path installs the last
released x86_64 RPM while building an ephemeral uCore-derived image; `.deb`
layering and Snosi sysext delivery are explicitly outside that issue.
`.github/workflows/packaging.yml`'s `vm-boot` job is what invokes all of this
(see "The CI job (`vm-boot`)" below).

**`test/vm/lib/diagnostics.sh` discriminates on whether the guest answers SSH at
the moment of failure**, not on whether it ever did. `install_failure_diagnostics`
arms an `ERR` and an `EXIT` trap; on a non-zero exit it probes reachability
*then* and branches: a reachable guest yields `systemctl status` and
`journalctl` for **both** `pilothoused.service` and `pilothouse.service` (two
processes, two journals, so dumping one would hide the other), through `sudo -n`
because both need privilege; an unreachable one yields the host-side
`QEMU_STDERR_LOG` and `QEMU_CONSOLE_LOG`. Neither branch is silent, a collection
command that does not complete is reported by name rather than swallowed, and
the guest is stopped only *after* the dump — a dump from a killed guest is not a
dump. Everything goes to the job log; nothing is uploaded.

The unreachable branch prints **both** host-side sections unconditionally, even
when the variables naming them are still unset. Diagnostics are armed *before*
the guest is created, so a failure during image verification, seed creation or
overlay creation reaches `dump_boot_diagnostics` before `start_vm` has exported
either path. Skipping an empty path — the obvious reading of "dump the log if
we have one" — left exactly those failures with no output at all, which is the
silent-dump failure mode this file exists to prevent. An absent log is itself
evidence, so it is reported by name (`not created: the run failed before
start_vm launched qemu`) instead of passed over. `stop_vm` is already a no-op
when `QEMU_PID` is unset, so the same early-failure path unwinds cleanly.

`packaging/vm_harness_test.go` grew the matching guards — including, for the PAM
check, the login order and the one exact status per attempt, the cursor captured
as the statement *immediately* before each POST it bounds, the parsed-field
journal matches on the unit that actually emitted the record, the absence of any
credential generator, and the direct route's `remote` differing from the web
process's `127.0.0.1` — still executing nothing but `shellcheck` (`--shell=bash`
for the orchestrator and the host libraries,
`--shell=sh` for the guest files). They discover the guest scripts **on disk**
rather than from a hand-kept list, so a script added later cannot escape the
mode, dialect, `require_root` and invocation-form checks; they enumerate every
guest-script invocation in the orchestrator and require the full
`guest_run sudo -n sh ~/vm-boot/guest/<name>.sh` form, in both directions (no
other form is permitted, and a guest script that is never invoked fails too);
they match the two selection globs against a synthetic listing holding *both*
architectures, which is the case an unqualified glob would silently get wrong;
and they pin the credential path's three steps in order. The `sudo -n` scan that
previously covered `test/vm/lib` now covers **every** file under `test/vm`,
since the orchestrator is where the escalations actually happen.

**Scope is the recurring defect in these guards, and it is invisible from the
test result** — a guard checking the wrong file set still passes, so nothing
fails until the gap is exploited. Two instances are worth remembering. Guest
scripts are enumerated with `filepath.WalkDir`, not `os.ReadDir`: the
single-level listing skipped directories, so `test/vm/guest/subdir/new.sh`
escaped the mode, shebang, `require_root`, shellcheck and invocation guards
simultaneously. And the staging-directory fence is applied to **every** file
under `test/vm`, not only `vm-boot-test.sh`: the invariant is phrased
harness-wide, and any library could call `guest_copy` or reach for `scp`
directly. `scp` is permitted at exactly one site — inside `guest_copy`, the one
place a guest destination is constructed — and that exemption is expressed as
the function's own body rather than as a whole-file pass.

Widening those scans required distinguishing a call from a mention: the fence
now matches `guest_copy`/`scp` only outside string literals, because
`ssh_fail "usage: guest_copy <local> <remote>"` names the function without
calling it. Without that, widening the scan produces a phantom violation, and
the tempting fix — re-narrowing the guard until it goes quiet — is what left
the fence covering one file in the first place.

## The CI job (`vm-boot`)

The third job in `.github/workflows/packaging.yml`, added as of this commit,
is what makes the harness above a gate rather than a script nobody runs. It is
Layer B in CI, and like `install` it is a *consumer* of `packages`, never a
second build.

- `runs-on: ubuntu-latest`, `needs: packages`. The standard GitHub-hosted
  runner is deliberate: no self-hosted runner, no larger runner. Hardware
  acceleration is available there once the `99-kvm4all.rules` udev rule is
  installed (`KERNEL=="kvm", GROUP="kvm", MODE="0666",
  OPTIONS+="static_node=kvm"`, then `udevadm control --reload-rules` and
  `udevadm trigger --name-match=kvm`), which is the job's second step. There
  is **no** software-emulation fallback: `start_vm` passes `-accel kvm`, so a
  runner that cannot accelerate fails the job instead of quietly running a
  slower, different test.
- `strategy.fail-fast: false` with a two-row matrix — `debian` paired with the
  `packages-deb` artifact and `fedora` with `packages-rpm`. Unlike `install`'s
  matrix, no image reference appears here: the guest image is pinned by
  checksum in `test/vm/images.env`, which stays the single pinning site.
- Its `if:` is precisely
  `github.event_name != 'pull_request' || contains(github.event.pull_request.labels.*.name, 'vm-boot')`
  and nothing else. That is label **presence**, not the labeling event, so a
  later push to an already-labelled pull request runs the job again — a gate
  keyed to the `labeled` event alone would certify only the commit that
  happened to be at HEAD when the label was applied. The workflow's
  `pull_request.types` is widened to `[opened, synchronize, reopened,
  labeled]` for the same reason: without `labeled` the trigger cannot fire when
  the label goes on. Widening `types:` also lets `packages` start on a labeling
  event, which is expected and is not a second build. The fork skip is **not**
  restated: `needs: packages` inherits it.
- **No tag trigger was added.** `packaging.yml` is deliberately not
  tag-triggered; adding one so `needs: packages` could be satisfied on tags
  would create a third GoReleaser build alongside `release.yml` for no gain,
  since every tag points at a commit the `main` push trigger already covered.
- Its steps are `actions/checkout@v7` (the harness comes from the tree), the
  KVM rule, an `apt-get install` of `qemu-system-x86`, `qemu-utils` and
  `xorriso` (boot, overlay creation and the NoCloud seed ISO respectively),
  `actions/download-artifact@v5` for the matrix row's artifact **only**, and a
  single run step: `bash test/vm/vm-boot-test.sh --family ${{ matrix.family }}
  --artifact-dir dist`. The **explicit interpreter** is not decoration —
  `test/vm/vm-boot-test.sh` is also committed `100755`, so neither the file
  mode nor the `bash` prefix is a single point of failure for the step
  starting.
- It adds **no** repository secret (the workflow's one `secrets.` use remains
  `GORELEASER_KEY`, in `packages`), carries **no** `actions/upload-artifact`
  step and pushes to no registry: the overlay disk, the NoCloud seed and the
  run-time credentials never leave the job, and diagnostics go to the job log
  (see the orchestrator section). No OS image is built, derived or published.
- It is **not** a required check and is on no branch-protection list, and
  there is no local `make` target for it: it needs KVM, network access and the
  artifact `packages` builds, so it cannot run under `make ci` or
  `make docker-ci` any more than the rest of `packaging.yml` can.

`packaging/workflow_vm_job_test.go` guards all of that, reusing
`loadPackagingWorkflow` from `workflow_install_job_test.go` so both tiers are
asserted against the same live decode of the same file. Its expectations —
the matrix rows, the exact label expression, the `pull_request.types` list —
are hand-written from the specification, never derived from the workflow under
test, and it asserts the harness step's command *starts with* `bash
test/vm/vm-boot-test.sh` and passes `--family` and `--artifact-dir`. It also
asserts the absences that matter: no tag trigger, no fork-skip restatement, no
GoReleaser step in the job (the workflow-wide count stays exactly one), no
upload-artifact and no registry push, no `tcg` fallback, and no `secrets.`
reference outside `packages`. It executes nothing — the harness needs KVM and
a network.

**What this tier proves, and what it does not.** It proves what a container
cannot: units activating on a booted host, systemd itself creating
`/run/pilothouse` and `/var/lib/pilothouse` `root:pilothouse 0750`, a live
broker socket at `0660 root:pilothouse`, PAM authenticating a real non-root
administrator through the running stack, journald reachable through the
broker's own journal query, and the whole posture surviving a real reboot. It
does **not** cover image-based hosts (uCore), SELinux AVC assertion or
policy qualification — the Fedora guest stays enforcing, but nothing scans or
classifies the audit log — or bootc update/rollback of an ephemeral
uCore-derived image containing the last released x86_64 RPM. Those are
**#80**'s; `.deb` layering and Snosi sysext delivery are not.

