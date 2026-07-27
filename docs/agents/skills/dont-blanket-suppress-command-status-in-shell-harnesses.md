# Don't swallow a privileged/critical command's exit status with `|| true` just to tolerate an expected side effect

**When it applies:** Writing or reviewing a shell test harness (e.g.
`test/**/*.sh`) function that runs a command whose success is expected to
produce a disruptive side effect on the *next* command — most commonly
`ssh ... sudo systemctl reboot` (whose SSH session is expected to drop
before it returns cleanly), or any `guest_sudo`/privileged-escalation call
followed by a wait loop that treats "the connection went away" as proof of
success.

**What to do:** Never blanket-discard the command's status and stderr with
`>/dev/null 2>&1 || true` (or bare `|| true`) around the whole invocation.
That pattern makes a rejected `sudo -n` escalation, a missing binary, or any
other real failure indistinguishable from the expected disconnect, and a
downstream "wait for the effect" loop (e.g. `wait_for_ssh_gone`) will then
misreport the failure as success after it times out. Instead:
- Capture the command's actual exit status and stderr separately from the
  connection-drop case (e.g. distinguish SSH exit code 255/"connection
  closed" from a nonzero exit carrying the remote command's own stderr).
- Fail fast and name the specific failure (e.g. "sudo escalation rejected")
  when the command didn't even dispatch, rather than falling through to a
  generic timeout error from the wait loop.
- Only treat the *expected* disconnection symptom as a pass condition, not
  every nonzero exit.

This same objection was raised three consecutive review rounds on the same
line before being accepted — treat "suppress everything so an expected
disconnect doesn't fail the script" as a repo-wide anti-pattern to avoid on
the first attempt, in any new harness function with this shape.

**Learned from:** issue-67 chunk 1 (`test/vm/lib/ssh.sh` `reboot_guest`),
mill run terminated after repeated `chunk_revise` objections on the same
`>/dev/null 2>&1 || true` line.
