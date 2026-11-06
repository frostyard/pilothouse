# Spec: Logs module

The contract of the administrator-only Logs module: the 1 fixed broker query
that reads the system journal, its filter parameters and limits, the journal
reader's record rules, and the web page that polls it. Consumers:
`cmd/pilothoused` (`registerLogs`), `internal/modules/logs` (manager, web
module) and `internal/modules/logs/journal` (reader). The web process never
opens journald or the systemd D-Bus connection. This is separate from the
Services module's single-unit journal query
(`org.frostyard.pilothouse.services.journal`); the two share no code.

## Interface

### Broker query

| Item | Value |
| --- | --- |
| ID | `broker.QueryLogs` = `org.frostyard.pilothouse.logs.list` |
| Admin | yes |
| Capability gate | `Systemd` and `Journald` (see [capabilities.md](../capabilities.md)) |
| Result | `logs.State`: `entries`, `filters`, `truncated`, `units` |

### Filter parameters (`ParseBrokerFilters`)

Only these 4 keys are accepted; any other key is an error.

| Parameter | Values | Default |
| --- | --- | --- |
| `query` | trimmed text, at most 200 code points and 1 KiB, no NUL; case-insensitive substring of `MESSAGE` | empty (all) |
| `priority` | `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info`, `debug` | empty (all) |
| `unit` | a systemd unit name (`unitNamePattern`, no `..`, `/`, `\x2f` or `\x00`) | empty (all, also records without a unit) |
| `window` | `15m`, `1h`, `6h`, `24h` | `1h` |

A priority is a threshold on the journal number (`emerg` = 0 … `debug` = 7):
`warning` keeps priorities 0 to 4.

### Entry

| Field | Source |
| --- | --- |
| `timestamp` | journal realtime timestamp |
| `priority`, `severity` | `PRIORITY` (0 to 7) and its name |
| `source` | first non-empty of `_SYSTEMD_UNIT`, `SYSLOG_IDENTIFIER`, `_COMM`, `_TRANSPORT`; else `unknown` |
| `message` | `MESSAGE` |

The reader reads only `PRIORITY`, `MESSAGE`, `_SYSTEMD_UNIT`,
`SYSLOG_IDENTIFIER`, `_COMM` and `_TRANSPORT` (`recordFields`).

### Web route

`GET /logs?query=&priority=&unit=&window=` (manifest ID `logs`, order 37, no
dashboard card), gated with `platform.Gate` on `Systemd` and `Journald`.

## Rules

- `SystemManager.Logs` validates the filters again, lists every unit name
  from `ListUnitsContext` and `ListUnitFilesContext` (sorted, only names that
  pass the unit grammar) for the unit dropdown, then calls the reader with a
  4-second timeout. The unit list is not an authorization list: a valid unit
  name that is not in it is still a valid filter.
- The reader adds the exact match `_SYSTEMD_UNIT=<unit>` when a unit is set,
  seeks to the tail, and walks backward (newest first). It stops at the first
  record older than the window boundary (not truncated), or when 200 entries
  are collected, 10,000 records are inspected, or the next entry would pass
  256 KiB of accounted size; those 3 stops set `truncated`. An entry counts
  `len(message) + len(source) + 64` bytes.
- A record is skipped (scan continues) for: zero timestamp, missing or
  out-of-range `PRIORITY`, missing `MESSAGE`, `MESSAGE` over 64 KiB, any
  source field over 4 KiB, a unit mismatch, a query mismatch, or a priority
  above the threshold. The sdjournal reader sets the data threshold to 64 KiB
  plus `MESSAGE=` plus 1 byte, so an oversized message is detected, not cut.
- The whole read fails (no partial result) on a journal open, seek or
  iteration error, a malformed `FIELD=value` frame, a non-`ENOENT` field
  error, a timeout or a cancellation. The manager returns the fixed error
  "system journal is unavailable"; no reader error text reaches the page.
- Without the `sdjournal` build tag, `journal.New` (`journal_stub.go`) always
  reports the journal as unavailable.
- Web: a non-admin gets the access-denied page with no broker call. The
  handler normalizes the filters (invalid priority or window take the
  default), queries with an 8-second context, and on failure renders an
  unavailable results article. The results article `#journal-logs` polls
  itself with `hx-get` of the same filters, `hx-trigger="every 5s"`,
  `hx-select`/`hx-target` `#journal-logs` and `hx-swap="outerHTML"`; the
  filter form is outside it.

### Tests that pin this contract

- `internal/modules/logs/manager_test.go`:
  `TestParseBrokerFiltersAcceptsOnlyFixedGrammar`,
  `TestSystemManagerListsAllUnitsAndPassesFixedLimits`,
  `TestSystemManagerFailsClosedForInvalidFiltersAndDependencies`.
- `internal/modules/logs/journal/reader_test.go`:
  `TestReaderMarksCountScanAndByteLimitsTruncated`,
  `TestReaderStopsAtWindowWithoutTruncation`,
  `TestReaderUsesExactUnitMatchAndSourceFallback`,
  `TestReaderSkipsInvalidPresentationRecords`,
  `TestReaderSkipsRecordsWithOversizedSourceFields`,
  `TestReaderFailsClosedForIterationAndContextErrors`.
- `internal/modules/logs/journal/journal_sdjournal_test.go`:
  `TestRecordFieldsRejectsMalformedFramingAndAPIErrors`.
- `internal/modules/logs/module_test.go` and `views_test.go`: admin check,
  exact query parameters, gating, 5-second polling, escaped output.

## References

- Rationale: [ADR-0006](../adr/0006-opt-in-capabilities-zero-io-omission.md),
  [ADR-0007](../adr/0007-capability-table-contract-tests.md)
- Context: [design/overview.md](../design/overview.md),
  [design/capability-gating.md](../design/capability-gating.md),
  [modules.md](../modules.md)
