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
