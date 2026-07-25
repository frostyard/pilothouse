# Piping one subprocess into another needs concurrent Wait, not sequential

**When it applies:** Implementing or reviewing Go code that chains two
external commands via a pipe (e.g. `producerCmd.StdoutPipe()` feeding
`consumerCmd.Stdin`, such as `rpm2cpio | cpio`, `tar | gzip`, or any
`producer | consumer` shell-equivalent built with `os/exec`). Applies
whenever both halves are started with `Start()` and must be reaped.

**What to do:** Calling `producer.Wait()` and then `consumer.Wait()` in
sequence looks correct and passes the happy-path test, but deadlocks when
the consumer exits early (e.g. on bad input) before draining a payload
larger than the OS pipe buffer: the producer blocks forever writing into a
full, unread pipe, so `producer.Wait()` never returns and the consumer's
real failure is never observed. There is no context deadline that saves
this — `exec.CommandContext` only kills on ctx cancellation, not on the
sibling process exiting. Reap both processes concurrently (e.g. wait on
each in its own goroutine, or explicitly close the parent's copy of the
pipe's read end once the consumer has started) so a consumer failure is
detected instead of hanging. When reviewing, don't accept a fixture test
that only exercises the case where both halves finish successfully or
where the *first* (producer) side fails fast — the deadlock only shows up
when the *second* (consumer) side exits early on a full pipe, which needs
its own test case with a payload that exceeds the pipe buffer.

**Learned from:** mill run for issue #73 (RPM extractor's `runPipe`
wrapping `rpm2cpio | cpio`). The reviewer raised the identical objection —
sequential `Wait()` calls deadlock if `cpio` exits early — across three
consecutive `chunk_revise` rounds on the same chunk, and each revision
left the sequential-wait pattern in place. The run was terminated as
failed with the objection still unresolved.
