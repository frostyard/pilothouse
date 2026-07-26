# Don't rely on chmod'd directory permissions to force a test failure

**When it applies:** Writing or reviewing a test that needs to force a
real filesystem operation (write, extraction, etc.) to fail with a
permission error — e.g. `os.Chmod(dir, 0o500)` on a directory, then
asserting that writing into it errors.

**What to do:** `chmod` cannot make a directory unwritable to root, and
`make docker-ci` / this repo's containerized gates commonly run as root
(or with `CAP_DAC_OVERRIDE`), so the "unwritable" directory is silently
still writable and the required failure branch never executes — the test
passes for the wrong reason, or a later assertion fails in a confusing
way. Don't gate an acceptance criterion on chmod-simulated permission
denial. Prefer a failure mode that's deterministic regardless of
privilege: e.g. point the destination at a path that doesn't exist and
can't be created, use a read-only bind mount, or inject the failure at a
level the test controls directly (a fake/stub that returns the error)
instead of asking the real OS to enforce a permission bit against a
possibly-root process.

**Learned from:** mill run for issue #73 (RPM extractor test for a
pipe-destination failure). The reviewer objected that
`chmod(dir, 0o500)` followed by an extraction-into-`dir` assertion is
host-dependent because root or `CAP_DAC_OVERRIDE` can still write there,
making the required failure assertion non-deterministic in this repo's
containerized test environment.
