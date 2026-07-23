package broker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/jobs"
)

type ActionHandler func(context.Context, auth.Identity, map[string]string) error
type ActionPrepare func(context.Context, auth.Identity, map[string]string) (map[string]string, error)
type QueryHandler func(context.Context, auth.Identity, map[string]string) (any, error)

type ActionRegistry struct {
	actions map[string]registeredAction
	audit   auditStore
	jobCtx  context.Context
	jobStop context.CancelFunc
	jobs    jobStore
	locks   *resourceLocks
	mu      sync.RWMutex
	wg      sync.WaitGroup
}

type registeredAction struct {
	definition ActionDefinition
}

type ActionDefinition struct {
	Admin                bool
	Background           bool
	ConfirmationRequired bool
	Handler              ActionHandler
	ID                   string
	LockResource         func(map[string]string) (string, error)
	NonBlocking          bool
	Parameters           []string
	Prepare              ActionPrepare
	RebootRequired       bool
	Resource             func(map[string]string) (string, error)
	Timeout              time.Duration
}

type auditStore interface {
	Begin(context.Context, audit.Attempt) (audit.Record, error)
	Complete(context.Context, uint64, string, string) error
}

type jobStore interface {
	Complete(context.Context, uint64, string, string, bool) error
	Enqueue(context.Context, jobs.Attempt) (jobs.Job, error)
	Start(context.Context, uint64) error
}

var ErrConfirmationRequired = errors.New("action confirmation required")

type QueryRegistry struct {
	mu      sync.RWMutex
	queries map[string]registeredQuery
}

type registeredQuery struct {
	admin   bool
	handler QueryHandler
}

func NewActionRegistry(stores ...auditStore) *ActionRegistry {
	var store auditStore
	if len(stores) > 0 {
		store = stores[0]
	}
	jobCtx, jobStop := context.WithCancel(context.Background())
	return &ActionRegistry{actions: map[string]registeredAction{}, audit: store, jobCtx: jobCtx, jobStop: jobStop, locks: newResourceLocks()}
}

func NewQueryRegistry() *QueryRegistry {
	return &QueryRegistry{queries: map[string]registeredQuery{}}
}

func (r *ActionRegistry) UseJobs(store jobStore) {
	r.jobs = store
}

func (r *ActionRegistry) Execute(ctx context.Context, identity auth.Identity, id string, parameters map[string]string, confirmation string) error {
	r.mu.RLock()
	action, ok := r.actions[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown action %q", id)
	}
	definition := action.definition
	if definition.Admin && !identity.Admin {
		return fmt.Errorf("%s is not authorized for %s", identity.Username, id)
	}
	if err := validateParameters(definition.Parameters, parameters); err != nil {
		return fmt.Errorf("action parameters: %w", err)
	}
	if definition.Prepare != nil {
		prepared, err := definition.Prepare(ctx, identity, cloneParameters(parameters))
		if err != nil {
			return fmt.Errorf("prepare action: %w", err)
		}
		if err := validatePreparedParameters(parameters, prepared); err != nil {
			return fmt.Errorf("prepared action parameters: %w", err)
		}
		parameters = cloneParameters(prepared)
	}
	resource, err := definition.Resource(parameters)
	if err != nil {
		return fmt.Errorf("action resource: %w", err)
	}
	if resource == "" || len(resource) > 1024 || strings.ContainsAny(resource, "\r\n\x00") {
		return fmt.Errorf("action resource is invalid")
	}
	if definition.ConfirmationRequired && confirmation != resource {
		return ErrConfirmationRequired
	}
	lockResource := resource
	if definition.LockResource != nil {
		lockResource, err = definition.LockResource(parameters)
		if err != nil || lockResource == "" || len(lockResource) > 1024 || strings.ContainsAny(lockResource, "\r\n\x00") {
			return fmt.Errorf("action lock resource is invalid")
		}
	}
	var unlock func()
	if definition.Background || definition.NonBlocking {
		var acquired bool
		unlock, acquired = r.locks.tryLock(lockResource)
		if !acquired {
			return fmt.Errorf("resource already has a maintenance job")
		}
	} else {
		unlock, err = r.locks.lock(ctx, lockResource)
		if err != nil {
			return fmt.Errorf("wait for resource action: %w", err)
		}
	}
	var record audit.Record
	if r.audit != nil {
		record, err = r.audit.Begin(ctx, audit.Attempt{Action: id, Resource: resource, Username: identity.Username, UID: identity.UID})
		if err != nil {
			unlock()
			return fmt.Errorf("record action intent: %w", err)
		}
	}
	if definition.Background {
		if r.jobs == nil || r.audit == nil {
			unlock()
			return fmt.Errorf("background action storage is unavailable")
		}
		job, enqueueErr := r.jobs.Enqueue(ctx, jobs.Attempt{Action: id, AuditID: record.ID, Resource: resource, Username: identity.Username, UID: identity.UID})
		if enqueueErr != nil {
			unlock()
			completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			_ = r.audit.Complete(completeCtx, record.ID, audit.OutcomeFailed, "enqueue_failed")
			return fmt.Errorf("queue background action: %w", enqueueErr)
		}
		parameters = cloneParameters(parameters)
		r.wg.Add(1)
		go r.runBackground(definition, identity, parameters, record, job, unlock)
		return nil
	}
	defer unlock()
	actionErr := definition.Handler(ctx, identity, parameters)
	if r.audit != nil {
		outcome := audit.OutcomeSucceeded
		category := ""
		if actionErr != nil {
			outcome = audit.OutcomeFailed
			category = errorCategory(actionErr)
		}
		completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := r.audit.Complete(completeCtx, record.ID, outcome, category); err != nil {
			return fmt.Errorf("action result is uncertain: record completion: %w", err)
		}
	}
	return actionErr
}

func (r *ActionRegistry) runBackground(definition ActionDefinition, identity auth.Identity, parameters map[string]string, record audit.Record, job jobs.Job, unlock func()) {
	defer r.wg.Done()
	defer unlock()
	timeout := definition.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.jobCtx, timeout)
	defer cancel()
	if err := r.jobs.Start(ctx, job.ID); err != nil {
		completeCtx, completeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer completeCancel()
		_ = r.audit.Complete(completeCtx, record.ID, audit.OutcomeUnknown, "job_start_failed")
		return
	}
	actionErr := definition.Handler(ctx, identity, parameters)
	status := jobs.StatusSucceeded
	outcome := audit.OutcomeSucceeded
	category := ""
	if actionErr != nil {
		status = jobs.StatusFailed
		outcome = audit.OutcomeFailed
		category = errorCategory(actionErr)
	}
	completeCtx, completeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer completeCancel()
	rebootRequired := actionErr == nil && definition.RebootRequired
	if err := r.jobs.Complete(completeCtx, job.ID, status, category, rebootRequired); err != nil {
		outcome = audit.OutcomeUnknown
		category = "job_completion_failed"
	}
	_ = r.audit.Complete(completeCtx, record.ID, outcome, category)
}

func (r *ActionRegistry) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (r *ActionRegistry) Shutdown(ctx context.Context) error {
	r.jobStop()
	return r.Wait(ctx)
}

func cloneParameters(parameters map[string]string) map[string]string {
	cloned := make(map[string]string, len(parameters))
	for name, value := range parameters {
		cloned[name] = value
	}
	return cloned
}

func (r *ActionRegistry) Register(id string, admin bool, handler ActionHandler) error {
	return r.RegisterDefinition(ActionDefinition{ID: id, Admin: admin, Handler: handler, Resource: func(map[string]string) (string, error) { return id, nil }})
}

func (r *ActionRegistry) RegisterDefinition(definition ActionDefinition) error {
	if definition.ID == "" || definition.Handler == nil || definition.Resource == nil {
		return fmt.Errorf("action requires id, handler, and resource resolver")
	}
	if slices.Contains(definition.Parameters, "") {
		return fmt.Errorf("action %q has an empty parameter name", definition.ID)
	}
	parameters := slices.Clone(definition.Parameters)
	slices.Sort(parameters)
	compacted := slices.Compact(parameters)
	if len(compacted) != len(definition.Parameters) {
		return fmt.Errorf("action %q has duplicate parameters", definition.ID)
	}
	definition.Parameters = compacted
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.actions[definition.ID]; exists {
		return fmt.Errorf("action %q already registered", definition.ID)
	}
	r.actions[definition.ID] = registeredAction{definition: definition}
	return nil
}

// Registered reports whether id is currently registered. It manages its own
// locking internally; callers must not hold any registry lock around it.
func (r *ActionRegistry) Registered(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.actions[id]
	return ok
}

func validateParameters(expected []string, parameters map[string]string) error {
	if len(parameters) != len(expected) {
		return fmt.Errorf("expected parameters %v", expected)
	}
	for _, name := range expected {
		value, ok := parameters[name]
		if !ok || value == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("parameter %q is missing or invalid", name)
		}
	}
	return nil
}

func validatePreparedParameters(external, prepared map[string]string) error {
	if len(prepared) != len(external) && len(prepared) != len(external)+1 {
		return fmt.Errorf("prepared parameters differ from external parameters")
	}
	for name, value := range external {
		if prepared[name] != value {
			return fmt.Errorf("parameter %q was changed", name)
		}
	}
	for name, value := range prepared {
		if name == "_id" {
			if value == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
				return fmt.Errorf("parameter %q is missing or invalid", name)
			}
			continue
		}
		if external[name] != value {
			return fmt.Errorf("parameter %q is not external", name)
		}
	}
	return nil
}

func errorCategory(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "operation_failed"
}

func (r *QueryRegistry) Execute(ctx context.Context, identity auth.Identity, id string, parameters map[string]string) (any, error) {
	r.mu.RLock()
	query, ok := r.queries[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown query %q", id)
	}
	if query.admin && !identity.Admin {
		return nil, fmt.Errorf("%s is not authorized for %s", identity.Username, id)
	}
	return query.handler(ctx, identity, parameters)
}

func (r *QueryRegistry) Register(id string, admin bool, handler QueryHandler) error {
	if id == "" || handler == nil {
		return fmt.Errorf("query requires id and handler")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.queries[id]; exists {
		return fmt.Errorf("query %q already registered", id)
	}
	r.queries[id] = registeredQuery{admin: admin, handler: handler}
	return nil
}

// Registered reports whether id is currently registered. It manages its own
// locking internally; callers must not hold any registry lock around it.
func (r *QueryRegistry) Registered(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.queries[id]
	return ok
}
