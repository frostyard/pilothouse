# Logs Module Design

## Purpose

Add an administrator-only Logs module that shows the entire systemd journal in quasi-realtime. The page will use the same filter layout and interaction model as Services while preserving Pilothouse's fixed-query privilege boundary.

The module is for live operational inspection. It will appear in navigation but will not add a dashboard card.

## Scope

The module will:

- Show journal records from the entire system journal, including records without a systemd unit.
- Refresh the displayed records every five seconds through HTMX.
- Filter by message text, priority threshold, exact systemd unit, and a bounded recent time window.
- Offer `15m`, `1h`, `6h`, and `24h` windows, with `1h` as the default.
- Show newest records first and return at most 200 records.
- Restrict access to administrators in both the web handler and broker query.

The module will not:

- Stream through SSE or WebSockets.
- Accept arbitrary journal fields, match expressions, date ranges, or `journalctl` arguments.
- Expose unbounded journal history or raw journal records.
- Add a dashboard summary or alerting behavior.
- Change the existing `/services/{unit}/logs` page or its broker query.

## Architecture

Add a self-contained vertical slice under `internal/modules/logs`:

- `module.go` defines the manifest, parses HTTP filters, enforces the administrator-only page, dispatches the fixed broker query, and renders the response.
- `manager.go` defines the narrow presentation model, filter validation, systemd unit inventory boundary, journal-reader boundary, and privileged query implementation.
- `views.templ` renders the filter form, polling results article, entry table, empty states, access-denied state, and unavailable state.
- `journal/journal_sdjournal.go` implements bounded journal access behind the existing `sdjournal` build tag.
- `journal/journal_stub.go` reports journal access as unavailable when systemd development support is absent.
- Tests remain in the module and cover handlers, rendering, manager behavior, and broker registration.

Register the module in `cmd/pilothouse`. Its manifest uses ID `logs`, path `/logs`, and an order after Services and Backups. `Dashboard` returns no cards.

Add one fixed query ID, `broker.QueryLogs`, and register its implementation only in `cmd/pilothoused`. The query requires an administrator. The web process never opens journald or the systemd D-Bus connection.

The Logs module owns its broad journal reader and all-unit inventory. Services keeps its existing narrow single-unit journal reader. The modules share no domain code. The existing Services filter CSS selectors will be renamed to generic filter-bar selectors and used by both modules; each module retains its own form markup and filter behavior.

## Components And Interfaces

### Web Module

`GET /logs` performs these steps:

1. Check `host.Identity(r).Admin` before issuing a broker query. A non-administrator receives an in-page access-denied view, matching Activity's existing behavior.
2. Parse and normalize `query`, `priority`, `unit`, and `window` from the URL.
3. Reject an oversized text query with `400 Bad Request`.
4. Send only the four named filter parameters through `host.Query` using `broker.QueryLogs`.
5. Render the full page. HTMX polling selects and replaces only the results article from subsequent full-page responses.

Invalid priority, unit, and window URL values normalize to their safe defaults for the page. The privileged manager validates all values independently, so direct broker clients cannot bypass the allowed filter grammar.

### Privileged Manager

The manager exposes one operation that accepts a filter value object and returns a page state containing:

- Up to 200 journal entries.
- A sorted list of all currently known systemd units for the unit filter.
- The normalized active filters.
- Whether the scan limit truncated coverage before reaching the requested window boundary.

The manager depends on two narrow interfaces:

- A systemd unit lister that returns all known unit names, including services, sockets, timers, scopes, mounts, targets, and other unit types.
- A journal reader that accepts the validated filters and fixed resource limits.

The selected unit must be empty or exactly match a freshly enumerated known unit. This check occurs inside the privileged process immediately before reading the journal.

### Journal Reader

The real reader opens the system journal, seeks to the current end, and walks backward. It stops when any of these conditions is met:

- 200 matching entries have been collected.
- The selected window boundary has been reached.
- 10,000 records have been inspected.
- The five-second reader timeout expires.
- The aggregate result reaches 256 KiB.

The reader returns entries newest-first. An unusually busy journal or rare text search may hit the 10,000-record scan cap before covering the entire selected window. The response marks this condition so the UI does not imply complete coverage.

Only these journal values are read into the presentation model:

- Realtime timestamp.
- `PRIORITY`.
- `MESSAGE`.
- `_SYSTEMD_UNIT`.
- `SYSLOG_IDENTIFIER`.
- `_COMM`.
- `_TRANSPORT`.

Display source is selected in that order after timestamp, priority, and message validation: `_SYSTEMD_UNIT`, then `SYSLOG_IDENTIFIER`, then `_COMM`, then `_TRANSPORT`. A missing systemd unit is valid because the module covers the entire system journal. Missing or malformed timestamp, priority, or message fields, oversized output, unexpected reader data, and invalid priorities fail closed without returning partial records.

## Filter Semantics

The filter bar mirrors Services visually and functionally:

| Control | Parameter | Behavior |
| --- | --- | --- |
| Find entries | `query` | Trimmed, bounded, case-insensitive substring match against `MESSAGE`. Empty means all messages. |
| Priority | `priority` | Allowlisted journal priority. A selected priority includes it and every more-severe level. Empty means all priorities. |
| Unit | `unit` | Dropdown of all known systemd units. Empty means all units and records without units. |
| Time range | `window` | One of `15m`, `1h`, `6h`, or `24h`. Missing or invalid values normalize to `1h`. |

Priority threshold behavior follows journal numeric ordering: priority `0` is most severe and `7` is least severe. For example, `warning` includes priorities `0` through `4`.

The filter form uses a normal GET request. **Apply filters** submits the form, and **Reset filters** links to `/logs`. This remains usable without JavaScript.

## Quasi-Realtime Refresh

The results article uses the established Docker and Podman log-viewer pattern:

- `hx-get` points to `/logs` with the active filters encoded in the query string.
- `hx-trigger` is `every 5s`.
- `hx-select` and `hx-target` identify the results article.
- `hx-swap` is `outerHTML`.

Each refresh reruns the same bounded fixed broker query. The filter form remains outside the replacement target, so user controls and focus remain stable. No custom JavaScript, cursor protocol, or persistent stream is introduced.

## Interface

The page intro explains that the view contains administrator-only system journal data.

The filter card uses the same grid, labels, controls, buttons, responsive breakpoints, and reset behavior as Services. The Services-specific CSS names become generic filter-bar names so both modules use one visual primitive.

The results toolbar shows:

- The number of displayed records.
- The selected time window.
- An `updates every 5s` disclosure.
- A coverage-truncated disclosure when the scan cap was reached before the time boundary.

The table columns are Timestamp, Priority, Source, and Message. Priority uses existing badge styling. Messages are HTML-escaped, preserve line breaks, and wrap long content without widening the page. The existing horizontally scrollable table behavior supports narrow screens.

The page distinguishes these states:

- No records exist in the selected window.
- Records exist, but none match the active filters.
- The bounded scan produced no matches and truncated coverage.
- Journal data is temporarily unavailable.
- The current user lacks administrator access.

An unavailable results article retains its polling attributes and retries after five seconds.

## Security And Limits

The broker query is administrator-only even though the web route performs its own role check. Every parameter has a fixed name and grammar, and the privileged manager rejects unknown broker parameters, malformed values, and values outside the allowlists.

The query never accepts arbitrary journal fields, raw sdjournal matches, paths, commands, sockets, or date expressions. It returns only the narrow presentation model. Text, record-count, scanned-record, time-window, execution-time, per-entry, and aggregate-output bounds prevent unbounded work or response sizes.

Messages and source values are rendered as escaped text. Errors shown to users are stable descriptions and do not include raw privileged-reader errors or journal contents.

## Error Handling

The initial page and each poll use a bounded request context. If the broker or journal query fails, the handler renders the page with an unavailable results article rather than exposing the underlying error. The article continues polling so transient failures recover without a manual reload.

Malformed privileged records, an oversized record, aggregate-size violations, and reader inconsistencies fail the query closed. The manager does not return a partial successful response in those cases.

Non-administrator HTTP requests render an access-denied page without contacting the broker. Direct non-administrator broker requests are rejected by query authorization.

## Testing

Manager tests cover:

- Default and allowlisted windows.
- Exact known-unit validation across all systemd unit types.
- Priority threshold semantics.
- Case-insensitive message matching.
- Newest-first ordering.
- Source fallback order.
- Records without `_SYSTEMD_UNIT`.
- Count, scan, time, and byte limits.
- Truncated-coverage reporting.
- Missing, malformed, mismatched, and oversized records.
- Reader and unit-inventory failures.

Handler and broker-registration tests cover:

- Administrator access and non-administrator denial without a broker call.
- Dispatch through only `broker.QueryLogs`.
- Exact encoded filter parameters.
- Normalization and oversized-query handling.
- Administrator-only query registration.
- Rejection of unknown or malformed broker parameters.
- Unavailable-state rendering without privileged error disclosure.

Rendering tests cover:

- All four controls, selected values, Apply, and Reset.
- Five-second HTMX polling with filter-preserving URLs.
- Timestamp, priority, source, escaped multiline message, and newest-first output.
- Empty, filtered-empty, truncated, unavailable, and access-denied states.
- Generic filter-bar classes and responsive structure.
- Rendered component output with no literal `@web.` syntax.

After templ changes, run `make generate`. Before handoff, run `make build`, `make test`, `make fmt`, and `make lint`, using the matching Docker targets if native systemd dependencies are unavailable.
