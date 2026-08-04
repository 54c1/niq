package eventbus

import (
	"context"
	"sync"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/store"
)

// MemoryEventStore implements both store.AppendStore and store.EventStore
// using an in-memory slice. It is the simplest possible event store,
// suitable for development and single-process deployments.
type MemoryEventStore struct {
	mu     sync.RWMutex
	events []event.Event
}

// NewMemoryEventStore creates an empty in-memory event store.
func NewMemoryEventStore() *MemoryEventStore {
	return &MemoryEventStore{
		events: make([]event.Event, 0, 256),
	}
}

// Append implements store.AppendStore.
func (s *MemoryEventStore) Append(ctx context.Context, events ...event.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
	return nil
}

// List implements store.EventStore.
func (s *MemoryEventStore) List(ctx context.Context, workerID string, opts store.QueryOpts) ([]event.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var afterTs, beforeTs int64
	if opts.AfterID != "" {
		for _, e := range s.events {
			if e.ID == opts.AfterID {
				afterTs = e.Timestamp
				break
			}
		}
	}
	if opts.BeforeID != "" {
		for _, e := range s.events {
			if e.ID == opts.BeforeID {
				beforeTs = e.Timestamp
				break
			}
		}
	}

	var result []event.Event
	allWorkers := workerID == "*"
	for _, e := range s.events {
		idOK := allWorkers || e.WorkerId == workerID || e.TargetWorkerID == workerID
		if opts.WorkerID != "" {
			idOK = e.WorkerId == opts.WorkerID || e.TargetWorkerID == opts.WorkerID
		}
		if !idOK {
			continue
		}
		if opts.TraceID != "" && e.TraceID != opts.TraceID {
			continue
		}
		if opts.Since > 0 && e.Timestamp < opts.Since {
			continue
		}
		if afterTs > 0 && e.Timestamp <= afterTs {
			continue
		}
		if beforeTs > 0 && e.Timestamp >= beforeTs {
			continue
		}
		result = append(result, e)
	}

	if opts.Desc {
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}
	}

	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

// Compile-time checks.
var _ store.AppendStore = (*MemoryEventStore)(nil)
var _ store.EventStore = (*MemoryEventStore)(nil)