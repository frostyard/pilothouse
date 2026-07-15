# Security Review Report: Incus Storage Display Module

**Scope:** Analysis of code changes for Incus container/VM display and management features  
**Date:** 2026-07-15  
**Focus:** NEW incus module (manager.go, views.templ, helpers.go) and module.go integration

---

## PHASE 1: CONTEXT RESEARCH

### Architecture & Data Flow
- **Connection Model:** Privileged LocalClient connects to `/var/lib/incus/unix.socket` (local daemon only)
- **Data Flow:** Incus API → SystemManager.State() → JSON marshaling → templ templates
- **Operations:** Read-only display operations + state-changing actions (start/stop/restart/remove)
- **Auth Model:** CSRF token validation via host.ValidateAction(); admin check via host.Identity
- **Broker Integration:** Fixed queries (QueryIncusState) and fixed actions (ActionIncusStart/Stop/etc.)

### Trust Boundaries
- **Untrusted Input:** Project names from URL query params, instance/action names from API
- **Trusted Components:** Incus daemon (local), broker service, templ rendering engine
- **Threat Model:** Incus daemon compromise (unlikely), malformed API responses, malicious user input

---

## PHASE 2: COMPARATIVE ANALYSIS

### Security Best Practices Checklist

✅ **Input Validation**
- Instance names validated via `validInstanceName()`: lowercase [a-z0-9-], max 63 chars, no leading/trailing hyphen
- Project names validated against API-returned list before use
- Action values limited to {start, stop, restart, remove} via switch statement
- Range-based slicing in `stateLabel()` is safe (Go handles out-of-bounds slicing gracefully)

✅ **Template Security (XSS Prevention)**
- Uses templ framework which auto-escapes `{ }` expressions
- Proper context-aware escaping in HTML content and attributes
- `templ.SafeURL()` used appropriately for URL construction (parameters pre-validated)
- No unescaped template output

✅ **Network Security**
- Connection to Unix socket only (no remote network exposure)
- 30-second HTTP client timeout on Incus connections
- Context timeouts on broker queries (10s) and actions (2min)

✅ **CSRF Protection**
- Form-based actions require CSRF token validation
- `host.ValidateAction()` enforces token check before processing

✅ **Authorization**
- Admin check displayed in templates
- Read-only data shown for non-admin users
- State-changing actions available only to admins (enforced by host.ValidateAction)

✅ **Code Flow Validation**
- No dynamic command construction or SQL
- No filesystem access outside daemon interaction
- No arbitrary code execution paths

---

## PHASE 3: VULNERABILITY ASSESSMENT

### Finding 1: Error Message Information Disclosure
**File:** `internal/modules/incus/module.go`, line 51  
**Severity:** MEDIUM  
**Confidence:** 9/10  
**Description:**  
Raw error messages from broker queries are returned directly to HTTP clients:
```go
http.Error(w, err.Error(), http.StatusServiceUnavailable)
```

If the broker returns error messages containing system paths, configuration details, or Incus internals (e.g., "/var/lib/incus/...", IPC details), these would be exposed to users.

**Exploitability:**  
User triggers invalid project name or connection error → broker returns detailed error → details leaked in HTTP 503 response body

**Recommended Fix:**  
```go
// Log actual error for debugging
log.Printf("broker query failed: %v", err)

// Return generic message to user
if r.URL.Query().Get("project") != "" && strings.Contains(err.Error(), "project is not available") {
    values := url.Values{"kind": {"error"}, "notice": {"Selected project is no longer available"}}
    http.Redirect(w, r, "/incus?"+values.Encode(), http.StatusSeeOther)
    return
}
http.Error(w, "Incus service unavailable", http.StatusServiceUnavailable)
```

---

### Finding 2: Error-Based Control Flow (Code Smell, Low Risk)
**File:** `internal/modules/incus/module.go`, line 46  
**Severity:** LOW  
**Confidence:** 7/10  
**Description:**  
Error handling uses string matching on error messages:
```go
if r.URL.Query().Get("project") != "" && strings.Contains(err.Error(), "project is not available")
```

This is fragile—if the broker changes its error message format, the conditional breaks. Not a security vulnerability but poor defensive coding.

**Recommended Fix:**  
Define custom error types in broker package:
```go
// In broker package
type ProjectNotFoundError struct{}
func (e *ProjectNotFoundError) Error() string { return "project not found" }

// In manager
if errors.As(err, &ProjectNotFoundError{}) { ... }
```

---

## Summary of Findings

| Severity | Count | Confidence | Status |
|----------|-------|------------|--------|
| HIGH     | 0     | —          | ✓ None |
| MEDIUM   | 1     | 9/10       | ⚠️ Fix recommended |
| LOW      | 1     | 7/10       | 💡 Code quality |

---

## Passed Validations

✅ No XSS vulnerabilities (proper template escaping)  
✅ No path traversal (instance names validated)  
✅ No CSRF bypasses (token validation enforced)  
✅ No SQL injection (no dynamic queries)  
✅ No unauthorized data access (read-only unless admin + CSRF)  
✅ No buffer overflows or panics (safe string handling)  
✅ No remote code execution paths  
✅ Proper timeout management (connection, query, action)  

---

## Recommendations

1. **Immediate:** Fix error message disclosure (Finding 1) to prevent information leakage
2. **Follow-up:** Refactor error handling to use typed errors instead of string matching
3. **Enhancement:** Add audit logging for admin actions (start/stop/restart/remove)
4. **Enhancement:** Add rate limiting on action endpoints to prevent DoS

