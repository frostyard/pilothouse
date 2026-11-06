# Problem records

What Pilothouse writes to the local journal when it fails, and what it never
sends ([ADR 0013](adr/0013-local-problem-records.md)).

A release build of `pilothouse` and `pilothoused` records its own faults with
[`github.com/frostyard/std/problems`](https://github.com/frostyard/std): 1
entry in the local systemd journal (`MESSAGE_ID=8915bdd215c84cbab0c31c05a1a33e7e`,
app ID `org.frostyard.pilothouse`, component `daemon`), as the frostyard/core
[problem-report spec](https://github.com/frostyard/core/blob/main/docs/specs/problem-report.md)
defines it. It records:

- a panic in an HTTP handler or in startup, with the panic value and the
  function names of its stack (no arguments, no source paths);
- a crash (an unrecovered panic in any goroutine, or a fatal runtime error),
  as 1 entry at the next start;
- 5 internal errors, each with a fixed code and its error text:

| Code | Binary | When |
| --- | --- | --- |
| `tls.self_signed_failed` | `pilothouse` | a non-loopback bind cannot prepare its self-signed certificate and refuses to start |
| `audit.open_failed` | `pilothoused` | the audit database does not open |
| `jobs.open_failed` | `pilothoused` | the jobs database does not open |
| `audit.write_failed` | `pilothoused` | an audit record (intent or result) cannot be written |
| `jobs.write_failed` | `pilothoused` | a background job record (enqueue, start or result) cannot be written |

A client that goes away (a cancelled request) is not recorded. A login
failure, an authorization refusal or a failed action is not recorded either.

The crash files live in
`/var/lib/pilothouse/frostyard-problems/org.frostyard.pilothouse` (broker) and
`/var/lib/pilothouse/web/frostyard-problems/org.frostyard.pilothouse` (web),
below each unit's `StateDirectory=`. Only a build stamped with a release
version records: a source build (`version` `dev`) and the snapshot and
nightly builds write nothing. The panic and error text stays in the local
journal. Pilothouse sends nothing over the network; a reader such as
chairlift or snosi-health may read the entries, and sends them only after
the user opts in. There is no flag or environment variable for this.
