# Only clear session/auth state on genuine rejection, not on a transient dependency error

**When it applies:** Any handler that validates a session/token by calling
out to another process (the broker, an auth service, a remote API) and then
branches on the call's error — especially session middleware that clears a
cookie or other client-visible state before checking *which* error came
back.

**What to do:** A dependency call like `broker.Session(ctx, token)` can fail
for two structurally different reasons that must not be handled the same
way: a definitive rejection (`broker.ErrUnauthorized` — the session really
is invalid) versus a transient failure (`broker.ErrUnavailable`, a timeout,
a connection refused — the dependency is temporarily unreachable but the
session itself was never judged invalid). Clearing session state
unconditionally on "any non-nil error" logs the user out on a transient
outage, which is wrong on its own and can also silently break a *separate*
recovery mechanism that depends on the session surviving the outage (e.g. a
"refetch capabilities once the broker comes back" path keyed on the same
cookie/session). Write the branch so only the definitive-rejection sentinel
clears state; every other error should surface as a retryable failure
(503/error response) while leaving the session/cookie untouched. When
adding or reviewing this kind of error branch, write a test that drives a
transient error (not just the rejection sentinel) through the handler and
asserts the client-visible state (cookie, cache entry, etc.) is unchanged.

**Learned from:** mill run for issue #54 — `internal/web/server.go`'s
`authenticate()` called `s.clearSessionCookie(w, r)` before checking
whether the error was `broker.ErrUnauthorized`, so a transient
`broker.ErrUnavailable` also logged the user out and made the
`staleAfterOutage` capability-refetch-on-recovery path unreachable (the
session it depended on was already gone). Every per-chunk gate and review
round in the run passed this code because none of the added tests drove a
transient error through `authenticate()` specifically to check the cookie
— it was only caught by a final compliance review after all chunks were
done, and fixed in a follow-up commit (`ed87d29`).
