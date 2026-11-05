// Package problemrec connects pilothouse and pilothoused to
// github.com/frostyard/std/problems: it holds the app ID, the fixed set of
// error codes the two binaries record, the recorder constructor, and an HTTP
// middleware that records a handler panic.
//
// Problem records go only to the local systemd journal (1 datagram to the
// journal socket). This package has no network code and sends nothing
// anywhere else. A recorder made with a version that is not a release semver
// (for example the default "dev", or a "0.0.0-<commit>-snapshot" build) is
// inert and writes nothing. See docs/adr/0013-local-problem-records.md.
package problemrec

import (
	"net/http"

	"github.com/frostyard/std/problems"
)

// AppID is the frostyard app ID of both binaries (S8 allowlist).
const AppID = "org.frostyard.pilothouse"

// The fixed error codes. Only internal faults get a code; a client error,
// an authorization refusal or a cancelled request is never recorded.
const (
	// CodeTLSSelfSignedFailed: pilothouse cannot prepare the self-signed
	// certificate of a non-loopback bind and refuses to start.
	CodeTLSSelfSignedFailed = "tls.self_signed_failed"
	// CodeAuditOpenFailed: pilothoused cannot open the audit database.
	CodeAuditOpenFailed = "audit.open_failed"
	// CodeJobsOpenFailed: pilothoused cannot open the jobs database.
	CodeJobsOpenFailed = "jobs.open_failed"
	// CodeAuditWriteFailed: the broker cannot write an audit record
	// (Begin or Complete).
	CodeAuditWriteFailed = "audit.write_failed"
	// CodeJobsWriteFailed: the broker cannot write a job record (Enqueue,
	// Start or Complete).
	CodeJobsWriteFailed = "jobs.write_failed"
)

// Codes lists every code above, in declaration order.
var Codes = []string{
	CodeTLSSelfSignedFailed,
	CodeAuditOpenFailed,
	CodeJobsOpenFailed,
	CodeAuditWriteFailed,
	CodeJobsWriteFailed,
}

// New returns the recorder of a pilothouse binary at version, as component
// daemon. opts are passed to problems.New (tests use problems.WithSocket).
func New(version string, opts ...problems.Option) *problems.Recorder {
	return problems.New(AppID, version, problems.ComponentDaemon, opts...)
}

// Handler returns next wrapped so that a panic in it is recorded by rec and
// then panics again with the same value. net/http's own per-connection
// recovery still sees the panic, so the server behaves exactly as without
// the wrapper. A nil or inert rec records nothing.
func Handler(rec *problems.Recorder, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer rec.RecoverPanic()
		next.ServeHTTP(w, r)
	})
}
