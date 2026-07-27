# A shell entrypoint meant to be both sourced (for unit tests) and executed needs a `BASH_SOURCE` guard around its `main` call — don't lock the acceptance criterion to a bare final line

**When it applies:** Writing or reviewing a spec/plan for a Bash entrypoint
script that must satisfy two things at once: (1) a test harness sources the
script (e.g. `source rpm-fixture-test.sh`) to unit-test its functions
(`parse_args`, helpers, etc.) without triggering the full program, and (2)
an acceptance criterion requires the script's last executable line to
unconditionally be exactly `main "$@"`.

**What to do:** These two requirements are in direct conflict. A script
whose final line is a bare `main "$@"` runs its entire program — network
calls, downloads, side effects — the instant any test `source`s it, which
defeats the "test the functions without running the entrypoint" goal and
will make the harness itself trigger the real workflow during `make test`.
The standard fix is a source-guard:

```bash
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
```

When writing the acceptance criterion, phrase it to require the *guarded*
call form, e.g. "the script's final executable statement invokes `main` only
when executed directly, verified by a guard comparing `BASH_SOURCE[0]` to
`$0`" — not "the last line is exactly `main \"$@\"\"". Otherwise the
criterion is unsatisfiable together with the sourcing requirement and will
be rejected in plan/chunk review.

**Learned from:** mill run for issue #80 (RPM fixture script), plan revision
round 6 — the plan required tests to `source` the entrypoint to drive
`parse_args` directly while also requiring the unconditional final line
`main "$@"`, which cannot be true of a safely-sourceable script. The run
exhausted its plan-revision round limit and terminated as failed.
