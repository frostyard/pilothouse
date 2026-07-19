package broker

import (
	"context"
	"sync"
)

type resourceLock struct {
	ready chan struct{}
	refs  int
}

type resourceLocks struct {
	locks map[string]*resourceLock
	mu    sync.Mutex
}

func newResourceLocks() *resourceLocks {
	return &resourceLocks{locks: make(map[string]*resourceLock)}
}

func (l *resourceLocks) tryLock(resource string) (func(), bool) {
	l.mu.Lock()
	if _, exists := l.locks[resource]; exists {
		l.mu.Unlock()
		return nil, false
	}
	entry := &resourceLock{ready: make(chan struct{}, 1), refs: 1}
	l.locks[resource] = entry
	l.mu.Unlock()
	return func() {
		entry.ready <- struct{}{}
		l.releaseReference(resource, entry)
	}, true
}

func (l *resourceLocks) lock(ctx context.Context, resource string) (func(), error) {
	l.mu.Lock()
	entry := l.locks[resource]
	if entry == nil {
		entry = &resourceLock{ready: make(chan struct{}, 1)}
		entry.ready <- struct{}{}
		l.locks[resource] = entry
	}
	entry.refs++
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		l.releaseReference(resource, entry)
		return nil, ctx.Err()
	case <-entry.ready:
	}
	return func() {
		entry.ready <- struct{}{}
		l.releaseReference(resource, entry)
	}, nil
}

func (l *resourceLocks) releaseReference(resource string, entry *resourceLock) {
	l.mu.Lock()
	entry.refs--
	if entry.refs == 0 {
		delete(l.locks, resource)
	}
	l.mu.Unlock()
}
