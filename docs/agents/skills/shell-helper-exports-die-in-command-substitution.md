# A shell helper that must export variables to its caller cannot be invoked via command substitution

**When it applies:** Writing or reviewing a shell library function (e.g.
`test/**/lib/*.sh`) whose documented/intended contract is to set up state
and `export` variables (SSH keys, generated credentials, temp paths) for
later commands in the *same* shell — such as `generate_credentials`
exporting `VM_SSH_KEY`/`VM_CREDS_ENV` for subsequent `guest_run`/`guest_copy`
calls — where the call site captures the function's stdout with
`x="$(some_func ...)"`.

**What to do:** Command substitution `$(...)` always forks a subshell, so
any `export`/variable assignment the function performs is invisible once
the subshell exits — only its stdout survives. If a helper's job is both to
print a value *and* export state for the caller, it cannot be called via
`$(...)`; call it directly (`some_func ...`) and read its output through a
different channel (a global variable it sets, a temp file, or splitting
"produce a value" and "export state" into two separate functions/calls).
When documenting or specifying a shell library's API in a spec/plan, write
the exact invocation pattern for any function with side-effecting exports,
and check that pattern isn't `x="$(fn ...)"` if `fn` also needs to leak
variables into the caller's shell.

**Learned from:** issue-67 chunk 1 (`test/vm/lib/cloudinit.sh`
`create_seed`/`generate_credentials`), where the documented
`seed="$(create_seed ...)"` call site silently dropped the exported
`VM_SSH_KEY`, breaking every later `guest_run`/`guest_copy` call.
