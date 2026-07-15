package broker

import (
	"context"
	"fmt"
	"sync"

	"github.com/frostyard/pilothouse/internal/auth"
)

type ActionHandler func(context.Context, auth.Identity, map[string]string) error
type QueryHandler func(context.Context, auth.Identity, map[string]string) (any, error)

type ActionRegistry struct {
	actions map[string]registeredAction
	mu      sync.RWMutex
}

type registeredAction struct {
	admin   bool
	handler ActionHandler
}

type QueryRegistry struct {
	mu      sync.RWMutex
	queries map[string]registeredQuery
}

type registeredQuery struct {
	admin   bool
	handler QueryHandler
}

func NewActionRegistry() *ActionRegistry {
	return &ActionRegistry{actions: map[string]registeredAction{}}
}

func NewQueryRegistry() *QueryRegistry {
	return &QueryRegistry{queries: map[string]registeredQuery{}}
}

func (r *ActionRegistry) Execute(ctx context.Context, identity auth.Identity, id string, parameters map[string]string) error {
	r.mu.RLock()
	action, ok := r.actions[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown action %q", id)
	}
	if action.admin && !identity.Admin {
		return fmt.Errorf("%s is not authorized for %s", identity.Username, id)
	}
	return action.handler(ctx, identity, parameters)
}

func (r *ActionRegistry) Register(id string, admin bool, handler ActionHandler) error {
	if id == "" || handler == nil {
		return fmt.Errorf("action requires id and handler")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.actions[id]; exists {
		return fmt.Errorf("action %q already registered", id)
	}
	r.actions[id] = registeredAction{admin: admin, handler: handler}
	return nil
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
