# 0013 — Record problems to the local journal only, with fixed codes

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

frostyard/core ADR-0069 and its spec `docs/specs/problem-report.md` define how
frostyard apps report their own faults: an app writes a problem record (a
panic, or an error with a fixed code) to the local systemd journal, and only
a separate reader on the host (chairlift, snosi-health) may send it, after the
user opts in. `github.com/frostyard/std/problems` (v0.4.0) is the Go writer;
`org.frostyard.pilothouse` is in its app ID allowlist. Plan T857 adopts it
in each Go app.

Pilothouse has 2 daemons. `pilothoused` runs as root and owns the audit and
job databases; a failed audit or job write there is the fault that most needs
a trace, but `internal/broker` keeps several of those errors only in its
return value, and discards some (`_ = r.audit.Complete(...)`). `pilothouse`
has 1 startup fault of its own: the fail-closed self-signed path of ADR 0009
refusing to start. Neither binary had a version variable; `.goreleaser.yaml`
built both with `-s -w` only. The project rules forbid new network paths and
any change to the broker protocol, routes, flags or output.

## Decision

Both binaries record panics and 5 coded internal errors with
`github.com/frostyard/std/problems`, as component `daemon`, to the local
journal only:

| Code | Recorded at |
| --- | --- |
| `tls.self_signed_failed` | `cmd/pilothouse/selfsigned.go` `prepareSelfSigned`: `certStateDir` or `tlscert.LoadOrCreate` fails |
| `audit.open_failed` | `cmd/pilothoused` `openAuditStore`: `audit.Open` fails |
| `jobs.open_failed` | `cmd/pilothoused` `openJobStore`: `jobs.Open` fails |
| `audit.write_failed` | `internal/broker` `actions.go`/`streams.go`: every failed audit `Begin` or `Complete`, also the ones whose error is otherwise discarded |
| `jobs.write_failed` | `internal/broker` `actions.go`: every failed job `Enqueue`, `Start` or `Complete` |

- `internal/problemrec` holds the app ID, the codes, `New(version, opts...)`
  and `Handler(rec, next)`, a middleware that does `defer rec.RecoverPanic()`.
  It has no network code.
- `internal/broker` does not import the library. It declares
  `ProblemRecorder interface{ Error(code string, err error) }` and the
  2 write codes, with `UseProblems` setters on `ActionRegistry` and
  `StreamActionRegistry` (nil records nothing). It never records
  `context.Canceled` (a client that went away, or shutdown). Return values,
  error texts, audit outcomes and categories are unchanged.
  `cmd/pilothoused`'s `newActionRegistries` connects the real recorder.
- Each `run()` makes the recorder early (`pilothoused` after its root
  check), defers `rec.RecoverPanic()`, calls `rec.CrashOutput()` and ignores
  its error with no log line, and wraps its `http.Server` handler in
  `problemrec.Handler`. net/http still recovers the re-panic, so serving
  behaves as before.
- `var version = "dev"` in both `main` packages; `.goreleaser.yaml` stamps
  `-X main.version={{ .Version }}` in both builds
  (`packaging.TestBuildsStampMainVersion`). A version that is not a release
  semver (`dev`, `0.0.0-<commit>-snapshot`, nightly `<n>.0.0-dev`) makes the
  recorder inert, so only tagged releases record.
- Crash files live below each unit's `StateDirectory=`:
  `/var/lib/pilothouse/frostyard-problems/org.frostyard.pilothouse` (broker)
  and `/var/lib/pilothouse/web/frostyard-problems/org.frostyard.pilothouse`
  (web). The units do not change.

## Consequences

- A release host keeps a local, schema-validated trace of daemon panics,
  crashes and store faults that chairlift or snosi-health can read and, after
  opt-in, send. Pilothouse itself sends nothing.
- The record text is the error text, which can hold a local path (for
  example a database path); it stays in the local journal, and the senders
  apply the spec's own redaction.
- Adding a code needs a change in `internal/problemrec` (and, for broker
  codes, in `internal/broker/problems.go`, pinned by
  `TestBrokerProblemCodesMatchProblemrec`) and this ADR's table, plus a test
  that reads the record from a test socket.
- `go.mod` now has 1 internal dependency, `github.com/frostyard/std`.
- The library's per-process rate cap (10 records a minute) can drop records
  during a burst of store failures; that is accepted.
- A deliberate `panic(http.ErrAbortHandler)` would be recorded as a panic;
  no pilothouse code uses it today.

## Alternatives considered

- **Import the library in `internal/broker`:** rejected; the broker package
  stays free of the recorder's transport and testable with a fake.
- **A flag or environment variable to turn recording off:** rejected; the
  rules forbid new configuration, and an unstamped build is already inert.
- **Record client errors and authorization refusals too:** rejected; they
  are not faults of pilothouse and would fill the rate cap.

## References

- Shapes: [design/overview.md](../design/overview.md) ("Problem records stay
  in the local journal"), README "Problem records"
- Builds on: [ADR-0009](0009-fail-closed-non-loopback-bind.md) (the
  self-signed refusal), frostyard/core
  [ADR-0069](https://github.com/frostyard/core/blob/main/docs/adr/0069-report-problems-local-first-opt-in-with-one-sender-per-host.md)
  and spec
  [problem-report.md](https://github.com/frostyard/core/blob/main/docs/specs/problem-report.md);
  plan T857
