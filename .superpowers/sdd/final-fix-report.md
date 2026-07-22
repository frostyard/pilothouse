# Pilothouse Files Final Fix Report

Base: `a9ffe92ff2551b323ed086fe3dd29e091f6145a4`

## Scope

Resolved the final-review findings without changing the approved design, implementation plan, or `.superpowers/sdd/progress.md`.

- JSON broker queries now preserve safe public HTTP statuses in the client, while generic query errors remain sanitized 403 responses.
- Files listing renders its missing/inaccessible state for JSON-query 404 and its transient state for 503.
- Download filenames use slash-path basenames.
- Upload audit and lock resources use a single normalized `directory/name` destination. The 768-byte limit is checked before locks, audit intent, and manager mutation; the safe 400 propagates through the stream registry.
- Root uploads audit as `files/<root>/<name>`.
- Stream action admin authorization occurs before Content-Length rejection.
- The existing generic/uncertain upload result messaging was retained. No claim of absence or blind-retry advice was added for post-publication directory-fsync failures.
- Added coverage for closing a non-nil download body returned with an error.
- The required race run exposed an existing test-helper race; its close-state observation is now synchronized.

## Files

- `internal/broker/client.go`
- `internal/broker/server.go`
- `internal/broker/streams.go`
- `internal/broker/server_test.go`
- `internal/broker/streams_test.go`
- `internal/modules/files/module_test.go`
- `cmd/pilothoused/main.go`
- `cmd/pilothoused/main_test.go`

## TDD Evidence

Tests were added before production changes. Initial focused RED command:

```text
go test ./internal/broker ./internal/modules/files ./cmd/pilothoused
FAIL internal/broker: JSON 404 was 503; non-admin oversized stream action was 413; safe resolver error was 503.
FAIL internal/modules/files: download body returned with error was not closed by the test transport.
FAIL cmd/pilothoused: nested filename retained its path; over-bound destination was 503; root audit resource had a double slash.
```

After the minimal fixes, the same command was green. A subsequent `go test -race` exposed an existing unsynchronized test helper; synchronizing that helper made the race command green.

## Verification

All commands exited 0 unless stated otherwise.

```text
go test ./internal/broker ./internal/modules/files ./cmd/pilothoused
ok internal/broker
ok internal/modules/files
ok cmd/pilothoused

go test -race ./internal/broker ./internal/modules/files ./cmd/pilothoused
ok internal/broker
ok internal/modules/files
ok cmd/pilothoused

make test
go tool templ generate
go test ./...
all packages passed

make docker-build
go build -buildvcs=false -o bin/pilothouse ./cmd/pilothouse
go build -tags sdjournal -buildvcs=false -o bin/pilothoused ./cmd/pilothoused

make docker-test
go tool templ generate
go test ./...
all packages passed

make docker-fmt
gofmt -w [repository Go sources]

make docker-lint
0 issues.

git diff --check
no output
```

## Commit

- `3f346cf fix: harden files broker boundaries`

## Self-Review

- Public status preservation uses the existing `PublicError` wrapper, so `errors.Is(err, ErrUnauthorized)` remains intact.
- Query responses emit only explicit public messages; generic errors retain a 403 status and use `query denied` rather than raw details.
- The upload bound is evaluated by the resource resolver before resource locking, audit intent, or the upload handler.
- The authorization reorder does not alter authorized oversized behavior: it remains 413.
- No changes were made to the documented post-publication fsync tradeoff or its generic uncertainty messaging.

## Concerns

None. The documented directory-fsync uncertainty tradeoff remains intentionally unchanged.
