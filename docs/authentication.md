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
  └── updex/systemd-sysext execution
```

## Login

The frontend forwards a bounded username/password request to the broker over the protected socket. The broker performs `pam_authenticate` and `pam_acct_mgmt` using the `pilothouse` PAM service, rejects direct root login, resolves the account and groups through NSS, and creates two independent 256-bit random values: an opaque bearer token and a CSRF token.

Only the opaque token is placed in the browser cookie. The cookie is HTTP-only, same-site strict, and becomes secure when TLS is used or `--secure-cookie` is configured. Sessions expire after 15 minutes without activity and always expire after eight hours. Broker restarts invalidate every session.

Authentication errors are deliberately generic. Empty passwords are rejected before PAM. Failed attempts receive a fixed delay and exponential per-user/per-address backoff.

## Authorization

Every authenticated account may read metrics, extension state, and the narrow system container-engine inventories returned by the broker. Privileged actions require membership in the broker's `--admin-group`, which defaults to `sudo`. An optional `--login-group` restricts login entirely.

The web process submits fixed query or action IDs and structured parameters. Before each operation, the broker resolves the account again so group removal takes effect without waiting for the session to expire. The registries are the only paths to privileged code; neither can execute caller-supplied commands. Podman and Docker operations accept only full hexadecimal container IDs discovered from their system inventories. Incus operations accept only validated instance names discovered from the local daemon's default project; the broker uses the fixed `/var/lib/incus/unix.socket` path and never loads configured remotes.

## PAM policy

`packaging/pilothouse.pam` follows the system's common authentication and account policy and honors `/etc/nologin`. Snow currently delegates these common stacks to local Unix accounts, while future SSSD, LDAP, Kerberos, smart-card, or other PAM modules can participate without changes to Pilothouse.

The initial HTML conversation supports username and password prompts. Multi-step PAM conversations such as OTP enrollment or password changes will need a stateful conversation extension.

## Deployment rules

- Keep the broker socket inside `/run/pilothouse` and never proxy it.
- Keep the web listener on loopback unless a TLS reverse proxy is configured.
- Set `--secure-cookie` behind an HTTPS proxy.
- If the proxy does not preserve the public `Host`, set each browser-visible origin with `--allowed-origin` or `PILOTHOUSE_ALLOWED_ORIGINS`. Pilothouse deliberately does not trust forwarded headers implicitly.
- Do not add generic command execution actions.
- Prefer a dedicated admin group if `sudo` is broader than desired.
- Protect both binaries and the PAM file from non-root modification.
