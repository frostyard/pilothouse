# 0008 — No config file: flags plus PILOTHOUSE_* env, explicit flag wins

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

Both binaries need configuration that works three ways at once: packaged
systemd units (where an admin should configure without editing `ExecStart=`),
ad-hoc invocation on a terminal, and tests. A config file adds a parser
dependency, a file-location search order, and a third precedence layer — for
two binaries whose whole surface is a handful of options. But flags and env
have a classic precedence trap: with stdlib `flag`, a flag passed *at its
default value* is indistinguishable from an unpassed flag by value alone, so
naive `if *flag != default` logic lets an env var silently override an
operator's explicit choice.

## Decision

There is no config file and no config-file parser; both binaries use stdlib
`flag` only (`cmd/pilothouse/main.go:47-56`, `cmd/pilothoused/main.go:55-74`)
plus `PILOTHOUSE_*` environment variables, typically supplied via systemd
`EnvironmentFile`. The runtime variables are `PILOTHOUSE_LISTEN`,
`PILOTHOUSE_TLS_CERT`, `PILOTHOUSE_TLS_KEY`,
`PILOTHOUSE_ALLOW_INSECURE_HTTP`, `PILOTHOUSE_ALLOWED_ORIGINS` (web) and
`PILOTHOUSE_BACKUP_TIMERS` (broker).

**Precedence for scalar options is explicit-flag → env → default**,
implemented with `flag.Visit` (`cmd/pilothouse/main.go:59-67`): `flag.Visit`
records which flags were actually passed, so an explicit
`--listen 127.0.0.1:8888` beats `PILOTHOUSE_LISTEN` even though it equals
the default. `resolveString`/`resolveBool` (`cmd/pilothouse/listen.go:30-55`)
implement the rule; a malformed boolean env value is a startup error, never
a silent false. The exact "flag set to default still beats env" case is
test-pinned (`cmd/pilothouse/listen_test.go:19`, malformed-env at `:46`).

**Repeatable list options merge instead of overriding:** comma-separated env
values are appended to flag-provided values after `flag.Parse()`
(`--allowed-origin` + `PILOTHOUSE_ALLOWED_ORIGINS`,
`cmd/pilothouse/main.go:49-50,57,210-216`; `--backup-timer` +
`PILOTHOUSE_BACKUP_TIMERS`, `cmd/pilothoused/main.go:59-60,75,500-517`) —
for allowlists, union is the safe combination; "flag beats env" would
silently drop entries the admin set in the env file.

The packaging side completes the contract: the packaged web unit deliberately
passes **no `--listen`** in `ExecStart=` (`packaging/pilothouse.service:9`)
so the env file can control it — "A `--listen` flag in ExecStart would
override this variable; the packaged unit deliberately passes none"
(`packaging/pilothouse.env:22-23`) — and the shipped env files
(`packaging/pilothouse.env`, `packaging/pilothoused.env`) are **fully
commented out**: "installing the package changes no runtime behavior"
(both files, lines 3-6). The units reference them with the optional-`-`
prefix, and their exact bytes are pinned into the packages via `go:embed`
(`packaging/contract.go:20,30-31,193-194`).

## Consequences

- One precedence rule an operator can state in a sentence; systemd
  drop-ins editing the env file configure everything without touching
  `ExecStart=`.
- Installing the package changes nothing until an admin uncomments a line —
  secure, inert defaults, at the cost of a mandatory post-install step for
  non-loopback deployments (which ADR-0009 then makes fail closed).
- The `flag.Visit` precedence currently guards the web binary's four scalar
  options; the broker's only env-configured option is a merged list. Any
  future broker scalar env variable must adopt the same `flag.Visit`
  pattern — this ADR is the instruction.
- List options have no "flag beats env": union only. An admin cannot use a
  flag to *remove* an env-file entry; they edit the env file.
- No config file means no place for structured per-module config; if that
  ever becomes necessary, superseding this ADR is the honest move.

## Alternatives considered

- **Config file (TOML/YAML):** parser dependency, search-order ambiguity,
  and a third precedence layer for ~10 options; systemd `EnvironmentFile`
  already provides the packaged-config story.
- **Env beats flags (12-factor style):** inverts operator intent at the
  terminal — an explicit CLI argument should never lose to ambient
  environment.
- **`if *flag != default` to detect "flag passed":** the exact bug
  `flag.Visit` avoids; a flag passed at its default would silently lose to
  env.
- **Override (not merge) for list env vars:** an env file entry would vanish
  the moment any `--allowed-origin` flag appears; for allowlists, silent
  narrowing is the dangerous direction.

## References

- Shapes: [design/overview.md](../design/overview.md) (configuration),
  `site/content/reference/cli.md` (operator-facing statement)
- Implemented in: `cmd/pilothouse/main.go`, `cmd/pilothouse/listen.go`,
  `cmd/pilothoused/main.go`, `packaging/pilothouse.env`,
  `packaging/pilothoused.env`, `packaging/pilothouse.service`
- Enforced by: `cmd/pilothouse/listen_test.go`,
  `packaging/goreleaser_config_test.go`
  (`TestOverridesDeclareOwnedDirectoriesAndEnvFiles`)
- Related: [0009 — fail-closed non-loopback bind](0009-fail-closed-non-loopback-bind.md),
  [0006 — opt-in capabilities](0006-opt-in-capabilities-zero-io-omission.md)
