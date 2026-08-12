# 0001 — Version the broker wire surface as four ID-dispatched registry routes

- **Status:** Accepted
- **Date:** 2026-08-12

## Context

Pilothouse splits into an unprivileged web process and a privileged broker
daemon connected by a local IPC boundary. Everything privileged crosses that
boundary, so its shape decides the security model: if the web process could
send the broker an arbitrary command, path, or upstream URL, compromising the
web process would be root. The transport also needs streaming (file
upload/download) and room to evolve without breaking the packaged pairing of
the two binaries.

## Decision

The broker serves HTTP over a Unix socket (default
`/run/pilothouse/broker.sock`, mode `0660`, created in
`cmd/pilothoused/main.go`; the client dials it with a custom
`http.Transport.DialContext` and the literal base URL `http://unix`,
`internal/broker/client.go:26-35`). The wire surface is versioned under `/v1/`
and consists of exactly four ID-dispatched registry routes plus
health/login/logout/session (`internal/broker/server.go:61-78` is the only
route-registration site):

- `POST /v1/actions/{id}` → `ActionRegistry`
- `POST /v1/queries/{id}` → `QueryRegistry`
- `POST /v1/stream-actions/{id}` → `StreamActionRegistry`
- `POST /v1/stream-queries/{id}` → `StreamQueryRegistry`

The operation ID travels in the URL path (`r.PathValue("id")`), never in the
body, and must match a registration in the corresponding registry — four
distinct registries, held as four distinct `Server` fields
(`internal/broker/server.go:19-30`), because actions, queries, and their
streaming variants have different authorization, audit, and serialization
semantics. IDs are the fixed `org.frostyard.pilothouse.*` constants in
`internal/broker/api.go` (per core ADR-0016); module web code passes
constants, never caller-supplied strings.

Streaming reuses plain HTTP bodies plus exactly two custom headers
(`internal/broker/api.go:86-89`): `Pilothouse-Stream-Metadata` (request
header on stream-actions; base64url JSON parameters, capped at 8 KiB before
and after decoding, `internal/broker/server.go:126-142`) and
`Pilothouse-Stream-Name` (response header on stream-queries; base64url
filename, `server.go:110`).

There is no generic proxy, exec, or passthrough route. A new kind of
operation requires a new registration (and usually a new registry route in a
reviewed change), because any transport shape that dispatches on something
other than a registered ID would bypass registry authorization.

## Consequences

- The broker's attack surface from a compromised web process is enumerable:
  the registered IDs, each with its own validation, group recheck, audit, and
  locking. Nothing on the wire can name a command, path, or URL directly.
- The `/v1/` prefix lets a future incompatible surface ship as `/v2/` beside
  it while packaged binaries upgrade.
- Adding a genuinely new interaction shape (e.g. bidirectional streaming) is
  deliberately expensive: a new route and registry, not a flag on an existing
  one.
- Custom `Pilothouse-Stream-*` headers keep streaming on plain HTTP —
  debuggable with curl — at the cost of base64url envelopes and an 8 KiB
  metadata cap.

## Alternatives considered

- **Single generic endpoint with the operation in the body:** loses
  method/path-level dispatch and makes it easy to grow a passthrough
  parameter; ID-in-path keeps authorization tied to routing.
- **A generic exec/proxy escape hatch for "flexibility":** rejected outright;
  it collapses the privilege boundary the split-process design exists for.
- **gRPC or a custom framed protocol:** heavier dependency and tooling for an
  on-host, same-machine boundary; HTTP-over-unix-socket is inspectable and
  uses stdlib only.
- **One combined registry with kind flags:** blurs the distinct
  authorization/serialization semantics that the four registries keep apart.

## References

- Shapes: [design/overview.md](../design/overview.md) (broker contract),
  [modules.md](../modules.md) (rules for actions and privileged reads),
  [capabilities.md](../capabilities.md) (the per-ID registration table)
- Implemented in: `internal/broker/server.go`, `internal/broker/client.go`,
  `internal/broker/api.go`, `cmd/pilothoused/main.go`
- Related: [0002 — resource keys](0002-audited-resource-key-doubles-as-lock-key.md),
  [0006 — opt-in capabilities](0006-opt-in-capabilities-zero-io-omission.md)
- Builds on: [core ADR-0016 — Reverse-DNS org.frostyard.* identifiers](https://github.com/frostyard/core/blob/main/docs/adr/0016-reverse-dns-org-frostyard-identifiers.md)
  (see [org-adrs.md](../org-adrs.md))
