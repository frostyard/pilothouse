package broker

import (
	"context"
	"errors"
)

// ProblemRecorder records an internal fault of the broker under a fixed
// code. cmd/pilothoused connects a github.com/frostyard/std/problems
// recorder, which writes the record only to the local journal; this package
// does not import that library. See docs/adr/0013-local-problem-records.md.
type ProblemRecorder interface {
	Error(code string, err error)
}

// The fixed codes this package records. They equal the constants of the same
// meaning in internal/problemrec (a test pins that).
const (
	// ProblemAuditWriteFailed: an audit Begin or Complete failed.
	ProblemAuditWriteFailed = "audit.write_failed"
	// ProblemJobsWriteFailed: a job Enqueue, Start or Complete failed.
	ProblemJobsWriteFailed = "jobs.write_failed"
)

// recordProblem records err under code. A nil recorder or a nil err records
// nothing, and neither does context.Canceled: a client that went away, or a
// broker that shuts down, is not a fault.
func recordProblem(rec ProblemRecorder, code string, err error) {
	if rec == nil || err == nil || errors.Is(err, context.Canceled) {
		return
	}
	rec.Error(code, err)
}
