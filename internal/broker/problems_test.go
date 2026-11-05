package broker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/jobs"
	"github.com/frostyard/pilothouse/internal/problemrec"
	"github.com/frostyard/std/problems"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// problemCall is 1 call of fakeProblems.Error.
type problemCall struct {
	code string
	err  error
}

// fakeProblems is a ProblemRecorder that keeps every call.
type fakeProblems struct {
	mu    sync.Mutex
	calls []problemCall
}

func (p *fakeProblems) Error(code string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, problemCall{code: code, err: err})
}

func (p *fakeProblems) codes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	codes := make([]string, 0, len(p.calls))
	for _, call := range p.calls {
		codes = append(codes, call.code)
	}
	return codes
}

// failingAudit is an audit store whose Begin and Complete fail on demand.
type failingAudit struct {
	beginErr    error
	completeErr error
	mu          sync.Mutex
	completed   []audit.Record
}

func (s *failingAudit) Begin(_ context.Context, attempt audit.Attempt) (audit.Record, error) {
	if s.beginErr != nil {
		return audit.Record{}, s.beginErr
	}
	return audit.Record{ID: 7, Action: attempt.Action, Resource: attempt.Resource, Outcome: audit.OutcomeStarted}, nil
}

func (s *failingAudit) Complete(_ context.Context, id uint64, outcome, category string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, audit.Record{ID: id, Outcome: outcome, ErrorCategory: category})
	return s.completeErr
}

func (s *failingAudit) completions() []audit.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]audit.Record(nil), s.completed...)
}

// failingJobs is a job store whose Enqueue, Start and Complete fail on demand.
type failingJobs struct {
	enqueueErr  error
	startErr    error
	completeErr error
}

func (s *failingJobs) Enqueue(context.Context, jobs.Attempt) (jobs.Job, error) {
	if s.enqueueErr != nil {
		return jobs.Job{}, s.enqueueErr
	}
	return jobs.Job{ID: 3}, nil
}

func (s *failingJobs) Start(context.Context, uint64) error { return s.startErr }

func (s *failingJobs) Complete(context.Context, uint64, string, string, bool) error {
	return s.completeErr
}

var errStore = errors.New("store is broken")

var problemAdmin = auth.Identity{Admin: true, UID: 1000, Username: "admin"}

func foregroundRegistry(t *testing.T, store auditStore, handler ActionHandler) *ActionRegistry {
	t.Helper()
	registry := NewActionRegistry(store)
	require.NoError(t, registry.RegisterDefinition(ActionDefinition{
		ID: "test.restart", Admin: true,
		Resource: func(map[string]string) (string, error) { return "service/web", nil },
		Handler:  handler,
	}))
	return registry
}

func backgroundRegistry(t *testing.T, store auditStore, jobStore jobStore) *ActionRegistry {
	t.Helper()
	registry := NewActionRegistry(store)
	registry.UseJobs(jobStore)
	require.NoError(t, registry.RegisterDefinition(ActionDefinition{
		ID: "test.update", Admin: true, Background: true, Timeout: time.Second,
		Resource: func(map[string]string) (string, error) { return "updates/global", nil },
		Handler:  func(context.Context, auth.Identity, map[string]string) error { return nil },
	}))
	return registry
}

func streamRegistry(t *testing.T, store auditStore) *StreamActionRegistry {
	t.Helper()
	registry := NewStreamActionRegistry(store)
	require.NoError(t, registry.Register(StreamActionDefinition{
		ID: "test.upload", Admin: true, Limit: 16,
		Resource: func(map[string]string) (string, error) { return "files/x", nil },
		Handler:  func(context.Context, auth.Identity, map[string]string, io.Reader) error { return nil },
	}))
	return registry
}

func TestActionAuditBeginFailureRecordsProblem(t *testing.T) {
	recorder := &fakeProblems{}
	called := false
	registry := foregroundRegistry(t, &failingAudit{beginErr: errStore}, func(context.Context, auth.Identity, map[string]string) error {
		called = true
		return nil
	})
	registry.UseProblems(recorder)

	err := registry.Execute(context.Background(), problemAdmin, "test.restart", nil, "")
	require.EqualError(t, err, "record action intent: store is broken")
	assert.False(t, called)
	require.Equal(t, []string{ProblemAuditWriteFailed}, recorder.codes())
	assert.ErrorIs(t, recorder.calls[0].err, errStore)
}

func TestActionAuditCompleteFailureRecordsProblem(t *testing.T) {
	recorder := &fakeProblems{}
	store := &failingAudit{completeErr: errStore}
	registry := foregroundRegistry(t, store, func(context.Context, auth.Identity, map[string]string) error { return nil })
	registry.UseProblems(recorder)

	err := registry.Execute(context.Background(), problemAdmin, "test.restart", nil, "")
	require.EqualError(t, err, "action result is uncertain: record completion: store is broken")
	require.Equal(t, []string{ProblemAuditWriteFailed}, recorder.codes())
	require.Len(t, store.completions(), 1)
	assert.Equal(t, audit.OutcomeSucceeded, store.completions()[0].Outcome)
}

func TestActionCanceledAuditFailureRecordsNothing(t *testing.T) {
	recorder := &fakeProblems{}
	cancelled := context.Canceled
	registry := foregroundRegistry(t, &failingAudit{beginErr: cancelled}, func(context.Context, auth.Identity, map[string]string) error { return nil })
	registry.UseProblems(recorder)

	err := registry.Execute(context.Background(), problemAdmin, "test.restart", nil, "")
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, recorder.codes())

	store := &failingAudit{completeErr: errors.Join(errors.New("write"), context.Canceled)}
	registry = foregroundRegistry(t, store, func(context.Context, auth.Identity, map[string]string) error { return nil })
	registry.UseProblems(recorder)
	err = registry.Execute(context.Background(), problemAdmin, "test.restart", nil, "")
	require.ErrorContains(t, err, "action result is uncertain")
	assert.Empty(t, recorder.codes())
}

func TestActionWithoutProblemRecorderStaysSilent(t *testing.T) {
	// The default registry has no recorder: a failing store must not panic
	// and must keep its error text.
	registry := foregroundRegistry(t, &failingAudit{beginErr: errStore}, func(context.Context, auth.Identity, map[string]string) error { return nil })
	err := registry.Execute(context.Background(), problemAdmin, "test.restart", nil, "")
	require.EqualError(t, err, "record action intent: store is broken")

	streams := streamRegistry(t, &failingAudit{beginErr: errStore})
	err = streams.Execute(context.Background(), problemAdmin, "test.upload", map[string]string{}, strings.NewReader("x"))
	require.EqualError(t, err, "record stream action intent: store is broken")
}

func TestBackgroundEnqueueFailureRecordsProblems(t *testing.T) {
	tests := []struct {
		name        string
		completeErr error
		want        []string
	}{
		{"audit completion succeeds", nil, []string{ProblemJobsWriteFailed}},
		{"audit completion fails too", errStore, []string{ProblemJobsWriteFailed, ProblemAuditWriteFailed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &fakeProblems{}
			store := &failingAudit{completeErr: tt.completeErr}
			registry := backgroundRegistry(t, store, &failingJobs{enqueueErr: errStore})
			registry.UseProblems(recorder)

			err := registry.Execute(context.Background(), problemAdmin, "test.update", nil, "")
			require.EqualError(t, err, "queue background action: store is broken")
			require.NoError(t, registry.Wait(context.Background()))
			assert.Equal(t, tt.want, recorder.codes())
			require.Len(t, store.completions(), 1)
			assert.Equal(t, audit.OutcomeFailed, store.completions()[0].Outcome)
			assert.Equal(t, "enqueue_failed", store.completions()[0].ErrorCategory)
		})
	}
}

func TestBackgroundStartFailureRecordsProblems(t *testing.T) {
	tests := []struct {
		name        string
		startErr    error
		completeErr error
		want        []string
	}{
		{"audit completion succeeds", errStore, nil, []string{ProblemJobsWriteFailed}},
		{"audit completion fails too", errStore, errStore, []string{ProblemJobsWriteFailed, ProblemAuditWriteFailed}},
		{"cancelled start is no fault", context.Canceled, nil, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &fakeProblems{}
			store := &failingAudit{completeErr: tt.completeErr}
			registry := backgroundRegistry(t, store, &failingJobs{startErr: tt.startErr})
			registry.UseProblems(recorder)

			require.NoError(t, registry.Execute(context.Background(), problemAdmin, "test.update", nil, ""))
			require.NoError(t, registry.Wait(context.Background()))
			assert.Equal(t, tt.want, recorder.codes())
			require.Len(t, store.completions(), 1)
			assert.Equal(t, audit.OutcomeUnknown, store.completions()[0].Outcome)
			assert.Equal(t, "job_start_failed", store.completions()[0].ErrorCategory)
		})
	}
}

func TestBackgroundJobCompleteFailureRecordsProblems(t *testing.T) {
	tests := []struct {
		name        string
		completeErr error
		want        []string
	}{
		{"audit completion succeeds", nil, []string{ProblemJobsWriteFailed}},
		{"audit completion fails too", errStore, []string{ProblemJobsWriteFailed, ProblemAuditWriteFailed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &fakeProblems{}
			store := &failingAudit{completeErr: tt.completeErr}
			registry := backgroundRegistry(t, store, &failingJobs{completeErr: errStore})
			registry.UseProblems(recorder)

			require.NoError(t, registry.Execute(context.Background(), problemAdmin, "test.update", nil, ""))
			require.NoError(t, registry.Wait(context.Background()))
			assert.Equal(t, tt.want, recorder.codes())
			require.Len(t, store.completions(), 1)
			assert.Equal(t, audit.OutcomeUnknown, store.completions()[0].Outcome)
			assert.Equal(t, "job_completion_failed", store.completions()[0].ErrorCategory)
		})
	}
}

func TestBackgroundSuccessRecordsNothing(t *testing.T) {
	recorder := &fakeProblems{}
	store := &failingAudit{}
	registry := backgroundRegistry(t, store, &failingJobs{})
	registry.UseProblems(recorder)

	require.NoError(t, registry.Execute(context.Background(), problemAdmin, "test.update", nil, ""))
	require.NoError(t, registry.Wait(context.Background()))
	assert.Empty(t, recorder.codes())
	require.Len(t, store.completions(), 1)
	assert.Equal(t, audit.OutcomeSucceeded, store.completions()[0].Outcome)
}

func TestStreamActionAuditFailuresRecordProblem(t *testing.T) {
	tests := []struct {
		name    string
		store   *failingAudit
		wantErr string
		want    []string
	}{
		{"begin fails", &failingAudit{beginErr: errStore}, "record stream action intent: store is broken", []string{ProblemAuditWriteFailed}},
		{"complete fails", &failingAudit{completeErr: errStore}, "stream action result is uncertain: record completion: store is broken", []string{ProblemAuditWriteFailed}},
		{"cancelled begin is no fault", &failingAudit{beginErr: context.Canceled}, "record stream action intent: context canceled", []string{}},
		{"cancelled complete is no fault", &failingAudit{completeErr: context.Canceled}, "stream action result is uncertain: record completion: context canceled", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &fakeProblems{}
			registry := streamRegistry(t, tt.store)
			registry.UseProblems(recorder)

			err := registry.Execute(context.Background(), problemAdmin, "test.upload", map[string]string{}, strings.NewReader("x"))
			require.EqualError(t, err, tt.wantErr)
			assert.Equal(t, tt.want, recorder.codes())
		})
	}
}

func TestBrokerProblemCodesMatchProblemrec(t *testing.T) {
	assert.Equal(t, problemrec.CodeAuditWriteFailed, ProblemAuditWriteFailed)
	assert.Equal(t, problemrec.CodeJobsWriteFailed, ProblemJobsWriteFailed)
}

// TestBrokerRecordsToJournalSocket connects the real recorder, as
// cmd/pilothoused does, and reads the records from a unixgram test socket.
func TestBrokerRecordsToJournalSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "journal")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	recorder := problemrec.New("1.2.3", problems.WithSocket(sock))
	require.NoError(t, recorder.Err())

	actions := backgroundRegistry(t, &failingAudit{}, &failingJobs{enqueueErr: errStore})
	actions.UseProblems(recorder)
	require.Error(t, actions.Execute(context.Background(), problemAdmin, "test.update", nil, ""))
	require.NoError(t, actions.Wait(context.Background()))
	streams := streamRegistry(t, &failingAudit{beginErr: errStore})
	streams.UseProblems(recorder)
	require.Error(t, streams.Execute(context.Background(), problemAdmin, "test.upload", map[string]string{}, strings.NewReader("x")))

	for _, code := range []string{problemrec.CodeJobsWriteFailed, problemrec.CodeAuditWriteFailed} {
		record := readProblemRecord(t, conn)
		assert.Equal(t, problems.MessageID, record["MESSAGE_ID"])
		assert.Equal(t, "org.frostyard.pilothouse", record["FROSTYARD_APP_ID"])
		assert.Equal(t, "1.2.3", record["FROSTYARD_APP_VERSION"])
		assert.Equal(t, "daemon", record["FROSTYARD_COMPONENT"])
		assert.Equal(t, "error", record["FROSTYARD_CLASS"])
		assert.Equal(t, code, record["FROSTYARD_CODE"])
		assert.Equal(t, "store is broken", record["MESSAGE"])
		detail, err := problems.Validate(record)
		require.NoError(t, err)
		assert.Equal(t, code, detail.Code)
	}
}

func readProblemRecord(t *testing.T, conn *net.UnixConn) map[string]string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, 1<<16)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	record, err := problems.DecodeEntry(bytes.Clone(buf[:n]))
	require.NoError(t, err)
	return record
}
