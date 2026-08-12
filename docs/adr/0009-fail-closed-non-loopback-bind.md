# 0009 — Fail closed on non-loopback binds: TLS, self-signed, or refuse

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

The web binary serves login forms and session cookies for a root-backed
management surface. Binding beyond loopback in plaintext sends credentials
over the network in the clear, and the packaged default configuration flow
(ADR-0008: uncomment `PILOTHOUSE_LISTEN=0.0.0.0:8888` in an env file) makes
that exact misstep one line away. Requiring operator-provisioned
certificates for every LAN deployment would push homelab users back to
plaintext; silently generating throwaway certs per boot breaks pinning and
trust-on-first-use. There is also a classification trap: deciding
"loopback?" by resolving a hostname makes startup depend on DNS, and a
spoofed or split-horizon answer could class a public bind as loopback.

## Decision

When the listen address is non-loopback, the web binary serves TLS or does
not serve. `decideServeMode` (`cmd/pilothouse/listen.go:79-90`) is the whole
policy: operator-provided cert/key (`--tls-cert`/`--tls-key` or env) is used
if configured; otherwise loopback serves plain HTTP; otherwise the explicit
acknowledgment `--allow-insecure-http` (env
`PILOTHOUSE_ALLOW_INSECURE_HTTP`) serves plaintext with a logged warning
(`cmd/pilothouse/main.go:93-95`); otherwise the binary generates a
self-signed certificate persisted under the state directory —
`$STATE_DIRECTORY` (first entry), then `$XDG_STATE_HOME/pilothouse`, then
`~/.local/state/pilothouse` (`listen.go:100-113`), files
`self-signed.crt`/`self-signed.key` written atomically, key before cert,
directory `0700` (`internal/tlscert/tlscert.go:54-82, 222-254`), reused
across restarts. If generation or persistence fails, the process **refuses
to start** with a message naming all three remedies
(`cmd/pilothouse/main.go:78-91`, `listen.go:120-124`) — it never falls back
to plaintext.

Loopback classification does no DNS: `isLoopbackListen`
(`listen.go:57-73`) uses only `net.SplitHostPort`, `net.ParseIP`
(`IsLoopback` for literals), and a case-insensitive comparison against the
literal `localhost`. Empty and unspecified hosts (`0.0.0.0`, `::`) and every
other hostname fail closed as non-loopback — a hostname that happens to
resolve to 127.0.0.1 still requires TLS or the explicit acknowledgment. No
resolver API appears anywhere in `cmd/` or `internal/tlscert/`.

Enforcement: the eight-row truth table
(`cmd/pilothouse/listen_test.go:107-126`), hostname/unspecified/zone cases
(`listen_test.go:62-101`), state-dir resolution (`:128-157`),
`internal/tlscert/tlscert_test.go` (generation, reuse, regeneration,
SAN coverage, uncreatable directory), and end-to-end against the real
binary: `TestSelfSignedTLSOnNonLoopbackBind` verifies the served cert
against the persisted one, and `TestNonLoopbackPlaintextRefusedWithoutStateDir`
asserts the nonzero exit and refusal message
(`test/e2e/tls_test.go:188-268`).

## Consequences

- The one-line env-file edit that exposes the UI on a LAN yields HTTPS by
  default (self-signed, stable across restarts, so browsers ask once) —
  never silent plaintext. Plaintext beyond loopback exists only behind a
  named, greppable acknowledgment flag.
- Startup cannot be swayed by DNS: classification is pure string/IP logic.
  The cost is that a genuinely-loopback hostname alias is treated as
  non-loopback — fail-closed by design.
- If the state directory is unwritable and no cert or acknowledgment is
  given, the service does not come up; the refusal message must (and does)
  name every way out, because that error is the operator's UX.
- Self-signed certs are a stopgap, not identity: operators wanting trusted
  TLS still provision real certificates via the provided-cert path.

## Alternatives considered

- **Refuse non-loopback without operator certs (no self-signed tier):**
  pushes exactly the homelab audience toward the plaintext override;
  self-signed-with-persistence is strictly safer.
- **Ephemeral in-memory certs per boot:** new fingerprint every restart
  defeats trust-on-first-use and re-prompts every browser.
- **Silent plaintext fallback when cert generation fails:** converts a disk
  permission problem into credential exposure; the entire point is that
  this path does not exist.
- **DNS-resolving loopback classification:** startup behavior becomes a
  function of resolver state and spoofable answers.

## References

- Shapes: [design/overview.md](../design/overview.md) (configuration and
  serving), [authentication.md](../authentication.md) (session cookie
  security), `site/content/reference/cli.md`
- Implemented in: `cmd/pilothouse/listen.go`, `cmd/pilothouse/main.go`,
  `internal/tlscert/`
- Enforced by: `cmd/pilothouse/listen_test.go`,
  `internal/tlscert/tlscert_test.go`, `test/e2e/tls_test.go`
- Related: [0008 — flags and env configuration](0008-no-config-file-flags-env-precedence.md)
