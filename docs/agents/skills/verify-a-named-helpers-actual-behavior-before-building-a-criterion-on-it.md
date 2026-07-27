# Before requiring a shell-command-fence test to be "quote-aware," check whether the extractor it's specified to consume already strips quoting

**When it applies:** Writing or reviewing an acceptance criterion for a
permanent "command fence" or "command-word allowlist" test — one that scans
a shell script's executable lines for disallowed commands (`curl` with
upload flags, arbitrary `docker run`, etc.) and must distinguish a literal
quoted string (e.g. a comment or an error message containing the text
`$(curl ...)`) from an actual executable command substitution — where the
criterion also names a specific existing helper/extractor (e.g. a
`vmCodeLines`-style function) as the thing the new test should reuse or
build on.

**What to do:** Read the existing extractor's implementation before writing
the criterion. If it already normalizes lines by stripping shell quoting
(common in helpers built for a different purpose, like counting statements
or diffing code), it structurally cannot tell a quoted literal from a real
command substitution — every downstream test built on top of it inherits
that blindness, and no amount of "quote-aware" language in the acceptance
criterion changes what the shared extractor discards. Either:

1. Specify a different, quote-preserving raw-line parse path for this
   specific guard (don't route it through the lossy extractor), or
2. Verify and cite that the extractor is not lossy for the properties the
   new test needs, before writing the criterion around it.

This is a specific case of a general rule: when a plan/spec names an
existing internal helper as the mechanism a new acceptance criterion must
rely on, check that helper's actual behavior (not its name or its use in
other tests) before asserting the new criterion is satisfiable through it.

**Learned from:** mill run for issue #80 (RPM fixture script), plan revision
round 6 — the plan required a command-word allowlist test to consume
`vmCodeLines`, which strips shell quoting, while also requiring it to
distinguish quoted literal `$(curl ...)` text from executable command
substitutions — a distinction the extractor cannot make. The run exhausted
its plan-revision round limit and terminated as failed.
