# Authentication and privilege boundary

Pilothouse separates network-facing code from privileged system management.

```text
browser
  │ HTTPS, opaque cookie, per-session CSRF
  ▼
pilothouse              User=pilothouse, TCP listener
  │ HTTP over /run/pilothouse/broker.sock (0660 root:pilothouse)
  ▼
pilothoused             User=root, no TCP listener
  ├── PAM authentication
  ├── opaque session store
  ├── NSS identity and group resolution
  ├── query and action authorization
  ├── narrow Podman, Docker, and local Incus inventory/lifecycle operations
  ├── storage inventory (lsblk/findmnt plus optional SMART/NVMe, MD RAID,
  │   LVM, device-mapper/LUKS, multipath, ZFS, and Btrfs backends) and
  │   managed NFS/SMB remote-mount lifecycle
  ├── bounded journal search and file browsing/upload/download within
  │   explicitly configured roots
  └── updex/systemd-sysext execution
```

## Login

The frontend forwards a bounded username/password request to the broker over the protected socket. The broker performs `pam_authenticate` and `pam_acct_mgmt` using the `pilothouse` PAM service, rejects direct root login, resolves the account and groups through NSS, and creates two independent 256-bit random values: an opaque bearer token and a CSRF token.

Only the opaque token is placed in the browser cookie. The cookie is HTTP-only, same-site strict, and becomes secure when TLS is used or `--secure-cookie` is configured. Sessions expire after 15 minutes without activity and always expire after eight hours. Broker restarts invalidate every session.

Authentication errors are deliberately generic. Empty passwords are rejected before PAM. Failed attempts receive a fixed delay and exponential per-user/per-address backoff.

NSS identity resolution requires a non-negative numeric UID that fits the
platform's integer range and rejects UID 0 before resolving supplementary
groups. Group names are sorted before they are returned. An individual group
ID that disappears during resolution is omitted, while failure to enumerate
the account's group IDs rejects the identity. The optional login group is
enforced after resolution, and administrator status is granted only when a
non-empty administrator group is configured and present.

If local sign-in fails while `pilothoused` is restarting, diagnose the broker
startup failure first with `systemctl status pilothoused` and
`journalctl -u pilothoused`. The web process cannot authenticate without the
broker; a stale login page from a previous web-process instance can also submit
an obsolete login CSRF token and show `invalid csrf token` as a secondary
symptom.

## Authorization

Every authenticated account may read metrics, extension state, storage
inventory, and the narrow system container-engine inventories returned by
the broker. Privileged actions require membership in the broker's
`--admin-group`, whose flag default is `sudo`. The packaging layer, not the
binary, carries the distro difference: the packaged broker unit passes
`--admin-group sudo` on Debian-family hosts
(`packaging/deb/pilothoused.service`) and `--admin-group wheel` on
Fedora-family hosts (`packaging/rpm/pilothoused.service`). An optional
`--login-group` restricts login entirely.

The web process submits fixed query or action IDs and structured parameters. Before each operation, the broker resolves the account again so group removal takes effect without waiting for the session to expire. The registries are the only paths to privileged code; neither can execute caller-supplied commands. Action definitions reject missing and unexpected parameters, derive a canonical resource key, serialize conflicting operations, and require exact confirmation for destructive actions. Podman and Docker operations accept only full hexadecimal container IDs discovered from their system inventories. Incus operations accept only projects and validated instance names rediscovered from the local daemon before each mutation; the broker uses the fixed `/var/lib/incus/unix.socket` path and never loads configured remotes. Storage remote-mount actions (create/mount/unmount/delete for NFS and SMB) are administrator-only and modify only definitions Pilothouse itself created; unmanaged mounts are never touched. The two SMB ownership-mapped create actions additionally require paired canonical numeric `uid`/`gid` values, which the broker renders only as fixed deterministic CIFS `uid=`/`gid=` mount options — it never resolves names or accepts free-form options. Files reads (list/download) and uploads are administrator-only and are bounded to explicitly configured root IDs with a 256 MiB transfer limit; there is no generic filesystem proxy.

Privileged action attempts are recorded by the broker in a root-owned bbolt database under `/var/lib/pilothouse`. The intent record is committed before the mutation begins, so an unavailable audit store prevents the action. Completion records contain the actor, fixed action ID, canonical resource, timing, outcome, and a stable error category; credentials, raw parameters, and backend error text are not retained. Interrupted records are marked unknown when the broker restarts. Only administrators can query the bounded activity history.

Long-running extension updates and refreshes are accepted into a separate root-owned durable job store. The canonical resource lock and original audit record remain active until the detached job finishes. Browser disconnects do not cancel accepted work; broker restarts mark interrupted jobs unknown rather than retrying an uncertain mutation.

## PAM policy

Two PAM policies ship, one per distro family, because the two families name their shared stacks differently. `packaging/pilothouse.pam` is the Debian-family policy: it `@include`s `common-auth` and `common-account`. `packaging/rpm/pilothouse.pam` is the Fedora-family policy: it includes `password-auth` — Fedora's stack for non-tty, network-facing services, as opposed to `system-auth` for local console logins. Both define only the `auth` and `account` groups, because the code calls only `pam_authenticate` and `pam_acct_mgmt`, and both run `pam_nologin.so` before the account include so `/etc/nologin` is honored. Each policy delegates to whatever its host's stack resolves to — local Unix accounts today, while future SSSD, LDAP, Kerberos, smart-card, or other PAM modules can participate without changes to Pilothouse.

The initial HTML conversation supports username and password prompts. Multi-step PAM conversations such as OTP enrollment or password changes will need a stateful conversation extension.

## Deployment rules

- Keep the broker socket inside `/run/pilothouse` and never proxy it.
- The web listener defaults to loopback HTTP. A non-loopback bind requires
  transport encryption: operator TLS material (`--tls-cert`/`--tls-key` or
  `PILOTHOUSE_TLS_CERT`/`PILOTHOUSE_TLS_KEY`), the auto-generated self-signed
  certificate (the fallback when none is configured; the process refuses to
  start if it cannot be prepared), or the explicit `--allow-insecure-http`
  acknowledgment for deployments whose transport is secured elsewhere (a
  TLS-terminating reverse proxy, an encrypting overlay network).
- A hostname in the listen address — even one resolving to loopback — is
  classified non-loopback; no DNS resolution happens at startup.
- The session cookie's `Secure` attribute engages automatically when the
  process itself terminates TLS. Set `--secure-cookie` behind an HTTPS proxy,
  where the process-local connection is plaintext.
- If the proxy does not preserve the public `Host`, set each browser-visible origin with `--allowed-origin` or `PILOTHOUSE_ALLOWED_ORIGINS`. Pilothouse deliberately does not trust forwarded headers implicitly.
- Do not add generic command execution actions.
- Prefer a dedicated admin group if `sudo` is broader than desired.
- Protect both binaries and the PAM file from non-root modification.
