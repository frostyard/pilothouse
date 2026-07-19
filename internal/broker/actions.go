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
)

type ActionHandler func(context.Context, auth.Identity, map[string]string) error
type QueryHandler func(context.Context, auth.Identity, map[string]string) (any, error)

type ActionRegistry struct {
	actions map[string]registeredAction
	audit   auditStore
	locks   *resourceLocks
	mu      sync.RWMutex
}

type registeredAction struct {
	definition ActionDefinition
}

type ActionDefinition struct {
	Admin                bool
	ConfirmationRequired bool
	Handler              ActionHandler
	ID                   string
	LockResource         func(map[string]string) (string, error)
	Parameters           []string
	Resource             func(map[string]string) (string, error)
}

type auditStore interface {
	Begin(context.Context, audit.Attempt) (audit.Record, error)
	Complete(context.Context, uint64, string, string) error
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
	return &ActionRegistry{actions: map[string]registeredAction{}, audit: store, locks: newResourceLocks()}
}

func NewQueryRegistry() *QueryRegistry {
	return &QueryRegistry{queries: map[string]registeredQuery{}}
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
	unlock, err := r.locks.lock(ctx, lockResource)
	if err != nil {
		return fmt.Errorf("wait for resource action: %w", err)
	}
	defer unlock()

	var record audit.Record
	if r.audit != nil {
		record, err = r.audit.Begin(ctx, audit.Attempt{Action: id, Resource: resource, Username: identity.Username, UID: identity.UID})
		if err != nil {
			return fmt.Errorf("record action intent: %w", err)
		}
	}
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
