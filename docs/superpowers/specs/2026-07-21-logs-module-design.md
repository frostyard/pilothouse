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

Register the module in `cmd/pilothouse`. Its manifest uses ID `logs`, path `/logs`, and order `37`, after Services and Backups. `Dashboard` returns no cards.

Add one fixed query ID, `broker.QueryLogs`, and register its implementation only in `cmd/pilothoused`. The query requires an administrator. The web process never opens journald or the systemd D-Bus connection.

The Logs module owns its broad journal reader and all-unit inventory. Services keeps its existing narrow single-unit journal reader. The modules share no domain code. The existing Services filter CSS selectors will be renamed to generic filter-bar selectors and used by both modules; each module retains its own form markup and filter behavior.

## Components And Interfaces

### Web Module

`GET /logs` performs these steps:

1. Check `host.Identity(r).Admin` before issuing a broker query. A non-administrator receives an in-page access-denied view, matching Activity's existing behavior.
2. Parse and normalize `query`, `priority`, `unit`, and `window` from the URL.
3. Normalize the text query to at most 200 Unicode code points and 1 KiB of UTF-8 data.
4. Send only the four named filter parameters through `host.Query` using `broker.QueryLogs`.
5. Render the full page. HTMX polling selects and replaces only the results article from subsequent full-page responses.

Invalid priority and window URL values normalize to their safe defaults for the page. A unit is accepted only when it follows the bounded systemd unit-name grammar. The privileged manager validates all values independently and rejects oversized or malformed direct broker parameters, so direct clients cannot bypass the allowed filter grammar.

### Privileged Manager

The manager exposes one operation that accepts a filter value object and returns a page state containing:

- Up to 200 journal entries.
- A sorted list of all currently known systemd units for the unit filter.
- The normalized active filters.
- Whether the entry, scan, or aggregate-size limit truncated coverage before reaching the requested window boundary.

The manager depends on two narrow interfaces:

- A systemd unit lister that returns all known unit names, including services, sockets, timers, scopes, mounts, targets, and other unit types.
- A journal reader that accepts the validated filters and fixed resource limits.

The unit inventory supplies dropdown options, but it is not an authorization boundary. A selected unit must be empty or pass the bounded systemd unit-name grammar, and the journal reader applies it only as an exact `_SYSTEMD_UNIT` match. This permits a bookmarked or transient unit that disappeared from the current inventory to retain access to its recent records without broadening the query. Records identified only by `_SYSTEMD_USER_UNIT` remain visible in unfiltered results but are treated as records without a system unit.

The single fixed query re-enumerates units on every full request and five-second poll even though HTMX discards the refreshed filter form. This bounded cost is accepted for the initial implementation to avoid a cache, a second privileged query, or request-mode parameters; access is restricted to active administrator sessions. The implementation can optimize this later only with measured evidence.

### Journal Reader

The real reader opens the system journal, seeks to the current end, and walks backward. It stops when any of these conditions is met:

- 200 matching entries have been collected.
- The selected window boundary has been reached.
- 10,000 records have been inspected.
- The aggregate result reaches 256 KiB.

The reader returns entries newest-first. Reaching the 200-entry, 10,000-record, or 256 KiB aggregate limit before the window boundary returns the entries collected so far and marks coverage as truncated. The reader checks the aggregate size before appending an entry, so the returned model never exceeds 256 KiB. Expiration of the separate four-second reader timeout is an error and returns no partial records.

Only these journal values are read into the presentation model:

- Realtime timestamp.
- `PRIORITY`.
- `MESSAGE`.
- `_SYSTEMD_UNIT`.
- `SYSLOG_IDENTIFIER`.
- `_COMM`.
- `_TRANSPORT`.

Display source is selected in that order after timestamp, priority, and message validation: `_SYSTEMD_UNIT`, then `SYSLOG_IDENTIFIER`, then `_COMM`, then `_TRANSPORT`. A missing systemd unit is valid because the module covers the entire system journal. `MESSAGE` is limited to 64 KiB, and each source field is limited to 4 KiB. The reader validates every non-empty source field before selecting the display source. Missing `PRIORITY` or `MESSAGE`, invalid priority, zero timestamp, selected-unit mismatch, and oversized individual presentation fields cause that record to be skipped so surrounding valid records remain available. Absent raw journal fields are represented as absent data; malformed raw `FIELD=value` framing and non-absence journal API errors fail the whole read.

## Filter Semantics

The filter bar mirrors Services visually and functionally:

| Control | Parameter | Behavior |
| --- | --- | --- |
| Find entries | `query` | Trimmed, limited to 200 Unicode code points and 1 KiB of UTF-8 data, and matched case-insensitively against `MESSAGE`. Empty means all messages. |
| Priority | `priority` | One of `emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info`, or `debug`. A selected priority includes it and every more-severe level. Empty means all priorities. |
| Unit | `unit` | Dropdown of all known systemd units. Empty means all units and records without units. |
| Time range | `window` | One of `15m`, `1h`, `6h`, or `24h`. Missing or invalid values normalize to `1h`. |

Priority threshold behavior follows journal numeric ordering: `emerg=0`, `alert=1`, `crit=2`, `err=3`, `warning=4`, `notice=5`, `info=6`, and `debug=7`. For example, `warning` includes priorities `0` through `4`.

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
- A coverage-truncated disclosure when the entry, scan, or aggregate-size cap was reached before the time boundary.

The table columns are Timestamp, Priority, Source, and Message. Priority uses existing badge styling. Messages are HTML-escaped, preserve line breaks, and wrap long content without widening the page. The existing horizontally scrollable table behavior supports narrow screens.

The page distinguishes these states:

- No records exist in the selected window when no filters are active.
- No records match the active filters.
- The bounded scan produced no matches and truncated coverage.
- Journal data is temporarily unavailable.
- The current user lacks administrator access.

An unavailable results article retains its polling attributes and retries after five seconds.

## Security And Limits

The broker query is administrator-only even though the web route performs its own role check. Every parameter has a fixed name and grammar, and the privileged manager rejects unknown broker parameters, malformed values, and values outside the allowlists.

The query never accepts arbitrary journal fields, raw sdjournal matches, paths, commands, sockets, or date expressions. It returns only the narrow presentation model. Text, record-count, scanned-record, time-window, execution-time, per-entry, and aggregate-output bounds prevent unbounded work or response sizes.

Messages and source values are rendered as escaped text. Errors shown to users are stable descriptions and do not include raw privileged-reader errors or journal contents.

## Error Handling

The initial page and each poll use an eight-second broker request context, while the privileged journal read has its own four-second timeout. This leaves time to serialize and return the bounded response before the next five-second poll. If the broker or journal query fails, the handler renders the page with an unavailable results article rather than exposing the underlying error. The article continues polling so transient failures recover without a manual reload.

Source/API iteration errors, malformed raw `FIELD=value` framing, reader timeouts, and context cancellation fail the query closed with no partial successful response. Individual presentation-record defects (missing `PRIORITY` or `MESSAGE`, invalid priority, zero timestamp, selected-unit mismatch, or oversized `MESSAGE` or source field) are skipped while scanning continues. Reaching the aggregate result cap is instead a successful truncated response that remains within the cap.

Non-administrator HTTP requests render an access-denied page without contacting the broker. Direct non-administrator broker requests are rejected by query authorization.

## Testing

Manager tests cover:

- Default and allowlisted windows.
- All-unit inventory across systemd unit types and bounded unit-name validation.
- Priority threshold semantics.
- Case-insensitive message matching.
- Newest-first ordering.
- Source fallback order.
- Records without `_SYSTEMD_UNIT`.
- Count, scan, timeout, per-field, and aggregate-byte limits.
- Truncated-coverage reporting.
- Invalid presentation records skipped between valid records, including missing `PRIORITY` or `MESSAGE`, invalid priorities, zero timestamps, selected-unit mismatches, and oversized messages or any source field.
- Whole-read failures for iteration/API errors, malformed raw framing, timeout, and cancellation.
- Reader and unit-inventory failures.

Handler and broker-registration tests cover:

- Administrator access and non-administrator denial without a broker call.
- Dispatch through only `broker.QueryLogs`.
- Exact encoded filter parameters.
- Normalization and text-query length handling.
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

Services rendering tests will also be updated to assert the renamed generic filter-bar classes, selected controls, and unchanged component output. The existing responsive breakpoints will remain behaviorally unchanged after the selector rename.

After templ changes, run `make generate`. Before handoff, run `make build`, `make test`, `make fmt`, and `make lint`, using the matching Docker targets if native systemd dependencies are unavailable.
