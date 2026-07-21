package storage

import (
	"context"
	"fmt"
	"slices"
	"time"
)

type SystemManager struct {
	adapters []Adapter
	now      func() time.Time
}

func NewSystemManager(adapters ...Adapter) *SystemManager {
	return &SystemManager{adapters: slices.Clone(adapters), now: time.Now}
}

func (m *SystemManager) State(ctx context.Context) (Snapshot, error) {
	overall, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	results := make(chan collectedResult, len(m.adapters))
	for _, adapter := range m.adapters {
		go func(adapter Adapter) {
			adapterCtx, cancel := context.WithTimeout(overall, 5*time.Second)
			defer cancel()
			result, err := adapter.Collect(adapterCtx)
			if adapterCtx.Err() == context.DeadlineExceeded {
				err = context.DeadlineExceeded
			}
			results <- collectedResult{name: adapter.Name(), core: adapter.Core(), result: result, err: err}
		}(adapter)
	}
	collected := make([]collectedResult, 0, len(m.adapters))
	received := make(map[string]bool, len(m.adapters))
	for len(collected) < len(m.adapters) {
		select {
		case result := <-results:
			collected = append(collected, result)
			received[result.name] = true
		case <-overall.Done():
			for _, adapter := range m.adapters {
				if !received[adapter.Name()] {
					collected = append(collected, collectedResult{name: adapter.Name(), core: adapter.Core(), err: context.DeadlineExceeded})
				}
			}
		}
	}
	for _, result := range collected {
		if result.core && result.err != nil {
			return Snapshot{}, fmt.Errorf("core adapter %s: %w", result.name, result.err)
		}
	}
	return normalize(m.now(), collected)
}
