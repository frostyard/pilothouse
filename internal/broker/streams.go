package broker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/frostyard/pilothouse/internal/audit"
	"github.com/frostyard/pilothouse/internal/auth"
)

var ErrStreamTooLarge = errors.New("stream exceeds registered limit")

type StreamQueryHandler func(context.Context, auth.Identity, map[string]string) (StreamResult, error)
type StreamActionHandler func(context.Context, auth.Identity, map[string]string, io.Reader) error

type StreamQueryDefinition struct {
	ID         string
	Admin      bool
	Parameters []string
	Limit      int64
	Timeout    time.Duration
	Handler    StreamQueryHandler
}

type StreamActionDefinition struct {
	ID           string
	Admin        bool
	Parameters   []string
	Limit        int64
	Timeout      time.Duration
	Resource     func(map[string]string) (string, error)
	LockResource func(map[string]string) (string, error)
	Handler      StreamActionHandler
}

type PublicError struct {
	Status   int
	Message  string
	Category string
	Err      error
}

func (e *PublicError) Error() string { return e.Message }

func (e *PublicError) Unwrap() error { return e.Err }

func NewPublicError(status int, message, category string, err error) error {
	return &PublicError{Status: status, Message: message, Category: category, Err: err}
}

func PublicErrorDetails(err error) (status int, message, category string) {
	var public *PublicError
	if errors.As(err, &public) {
		return public.Status, public.Message, public.Category
	}
	return 503, "service unavailable", "unavailable"
}

func StatusCode(err error) int {
	status, _, _ := PublicErrorDetails(err)
	return status
}

type StreamQueryRegistry struct {
	mu      sync.RWMutex
	queries map[string]StreamQueryDefinition
}

type StreamActionRegistry struct {
	mu      sync.RWMutex
	actions map[string]StreamActionDefinition
	audit   auditStore
	locks   *resourceLocks
}

func NewStreamQueryRegistry() *StreamQueryRegistry {
	return &StreamQueryRegistry{queries: make(map[string]StreamQueryDefinition)}
}

func NewStreamActionRegistry(stores ...auditStore) *StreamActionRegistry {
	var store auditStore
	if len(stores) > 0 {
		store = stores[0]
	}
	return &StreamActionRegistry{actions: make(map[string]StreamActionDefinition), audit: store, locks: newResourceLocks()}
}

func (r *StreamQueryRegistry) Register(definition StreamQueryDefinition) error {
	if definition.ID == "" || definition.Handler == nil || definition.Limit < 0 {
		return fmt.Errorf("stream query requires id, handler, and non-negative limit")
	}
	parameters, err := registeredStreamParameters(definition.ID, definition.Parameters)
	if err != nil {
		return err
	}
	definition.Parameters = parameters
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.queries[definition.ID]; exists {
		return fmt.Errorf("stream query %q already registered", definition.ID)
	}
	r.queries[definition.ID] = definition
	return nil
}

func (r *StreamQueryRegistry) Execute(ctx context.Context, identity auth.Identity, id string, parameters map[string]string) (StreamResult, error) {
	r.mu.RLock()
	definition, ok := r.queries[id]
	r.mu.RUnlock()
	if !ok {
		return StreamResult{}, fmt.Errorf("unknown stream query %q", id)
	}
	if definition.Admin && !identity.Admin {
		return StreamResult{}, fmt.Errorf("%s is not authorized for %s", identity.Username, id)
	}
	if err := validateStreamParameters(definition.Parameters, parameters); err != nil {
		return StreamResult{}, fmt.Errorf("stream query parameters: %w", err)
	}
	timeout := definition.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := definition.Handler(ctx, identity, parameters)
	if err != nil {
		return StreamResult{}, err
	}
	if result.Body == nil || result.Size < 0 || result.Size > definition.Limit {
		if result.Body != nil {
			_ = result.Body.Close()
		}
		return StreamResult{}, ErrStreamTooLarge
	}
	return result, nil
}

func (r *StreamActionRegistry) Register(definition StreamActionDefinition) error {
	if definition.ID == "" || definition.Handler == nil || definition.Resource == nil || definition.Limit < 0 {
		return fmt.Errorf("stream action requires id, handler, resource resolver, and non-negative limit")
	}
	parameters, err := registeredStreamParameters(definition.ID, definition.Parameters)
	if err != nil {
		return err
	}
	definition.Parameters = parameters
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.actions[definition.ID]; exists {
		return fmt.Errorf("stream action %q already registered", definition.ID)
	}
	r.actions[definition.ID] = definition
	return nil
}

func (r *StreamActionRegistry) Execute(ctx context.Context, identity auth.Identity, id string, parameters map[string]string, body io.Reader) error {
	r.mu.RLock()
	definition, ok := r.actions[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown stream action %q", id)
	}
	if definition.Admin && !identity.Admin {
		return fmt.Errorf("%s is not authorized for %s", identity.Username, id)
	}
	if err := validateStreamParameters(definition.Parameters, parameters); err != nil {
		return fmt.Errorf("stream action parameters: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(body, definition.Limit+1))
	if err != nil {
		return fmt.Errorf("read stream action body: %w", err)
	}
	if int64(len(data)) > definition.Limit {
		return ErrStreamTooLarge
	}
	resource, err := definition.Resource(parameters)
	if err != nil || !validStreamResource(resource) {
		return fmt.Errorf("stream action resource is invalid")
	}
	lockResource := resource
	if definition.LockResource != nil {
		lockResource, err = definition.LockResource(parameters)
		if err != nil || !validStreamResource(lockResource) {
			return fmt.Errorf("stream action lock resource is invalid")
		}
	}
	unlock, err := r.locks.lock(ctx, lockResource)
	if err != nil {
		return fmt.Errorf("wait for stream action resource: %w", err)
	}
	defer unlock()

	var record audit.Record
	if r.audit != nil {
		record, err = r.audit.Begin(ctx, audit.Attempt{Action: id, Resource: resource, Username: identity.Username, UID: identity.UID})
		if err != nil {
			return fmt.Errorf("record stream action intent: %w", err)
		}
	}
	timeout := definition.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	actionErr := definition.Handler(ctx, identity, parameters, bytes.NewReader(data))
	if r.audit != nil {
		outcome, category := audit.OutcomeSucceeded, ""
		if actionErr != nil {
			outcome, category = audit.OutcomeFailed, errorCategory(actionErr)
		}
		completeCtx, completeCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer completeCancel()
		if err := r.audit.Complete(completeCtx, record.ID, outcome, category); err != nil {
			return fmt.Errorf("stream action result is uncertain: record completion: %w", err)
		}
	}
	return actionErr
}

func registeredStreamParameters(id string, parameters []string) ([]string, error) {
	if slices.Contains(parameters, "") {
		return nil, fmt.Errorf("stream %q has an empty parameter name", id)
	}
	parameters = slices.Clone(parameters)
	slices.Sort(parameters)
	compacted := slices.Compact(parameters)
	if len(compacted) != len(parameters) {
		return nil, fmt.Errorf("stream %q has duplicate parameters", id)
	}
	return compacted, nil
}

func validateStreamParameters(expected []string, parameters map[string]string) error {
	if len(parameters) != len(expected) {
		return fmt.Errorf("expected parameters %v", expected)
	}
	total := 0
	for _, name := range expected {
		value, ok := parameters[name]
		if !ok || len(value) > 4<<10 || containsStreamControl(value) {
			return fmt.Errorf("parameter %q is missing or invalid", name)
		}
		total += len(name) + len(value)
	}
	if total > 8<<10 {
		return fmt.Errorf("encoded metadata is too large")
	}
	return nil
}

func containsStreamControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func validStreamResource(resource string) bool {
	return resource != "" && len(resource) <= 1024 && !strings.ContainsAny(resource, "\r\n\x00")
}
