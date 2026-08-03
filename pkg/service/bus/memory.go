// Package bus provides the in-process bus service implementation.
// It wraps the core bus with event persistence and transport management.
package bus

import (
	"context"
	"sync"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/store"
	inprocesspkg "github.com/54c1/niq/pkg/service/bus/transport/inprocess"
)

// MemoryBus is the in-process implementation of the event bus. It wraps a
// core [corebus.Bus] with event persistence (store.EventStore) and transport
// management (EventBusServer). Per-worker data-plane handles are obtained
// via [Client]; the returned [corebus.InProcessClient] implements
// [corebus.EventBusClient] with identity and ACL enforcement.
type MemoryBus struct {
	bus     *Bus
	clients map[string]*inprocesspkg.InProcessClient
	mu      sync.Mutex

	storeMu sync.Mutex
	store   store.AppendStore
	events  []event.Event
}

// NewMemoryBus creates an in-process MemoryBus with an internal core Bus.
func NewMemoryBus() *MemoryBus {
	b := &MemoryBus{
		bus:     NewBus(),
		clients: make(map[string]*inprocesspkg.InProcessClient),
		events:  make([]event.Event, 0, 256),
	}
	b.bus.OnEvent(b.appendEvent)
	return b
}

// RegisterWorker delegates to the core Bus's management API.
func (b *MemoryBus) RegisterWorker(id string, pubAllow, subAllow []string) error {
	return b.bus.RegisterWorker(id, pubAllow, subAllow)
}

// UpdateWorkerACL delegates to the core Bus.
func (b *MemoryBus) UpdateWorkerACL(id string, pubAllow, subAllow []string) error {
	return b.bus.UpdateWorkerACL(id, pubAllow, subAllow)
}

// Client returns a per-worker [corebus.EventBusClient]. If the worker has not
// been registered via [RegisterWorker], an error is returned.
func (b *MemoryBus) Client(workerID string) (corebus.EventBusClient, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if c, ok := b.clients[workerID]; ok {
		return c, nil
	}
	c, err := inprocesspkg.NewClient(b.bus, workerID)
	if err != nil {
		return nil, err
	}
	b.clients[workerID] = c
	return c, nil
}

// SharedBus returns the underlying core Bus for control-plane operations.
func (b *MemoryBus) SharedBus() *Bus {
	return b.bus
}

// ── Lifecycle ──

// Listen marks the bus as ready to accept connections.
func (b *MemoryBus) Listen(addr string) error {
	return nil
}

// Shutdown cleans up channels and state.
func (b *MemoryBus) Shutdown(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, c := range b.clients {
		delete(b.clients, id)
		_ = c
	}
	return nil
}

// Route delivers and persists an event.
func (b *MemoryBus) Route(evt event.Event) error {
	b.bus.Route(evt)
	return nil
}

// ── store.EventStore ──

// SetStore replaces the internal event sink.
func (b *MemoryBus) SetStore(s store.AppendStore) {
	b.storeMu.Lock()
	defer b.storeMu.Unlock()
	b.store = s
}

func (b *MemoryBus) appendEvent(evt event.Event) {
	b.storeMu.Lock()
	defer b.storeMu.Unlock()
	if b.store != nil {
		_ = b.store.Append(context.TODO(), evt)
		return
	}
	b.events = append(b.events, evt)
}

// List returns persisted events matching the query.
func (b *MemoryBus) List(ctx context.Context, workerID string, opts store.QueryOpts) ([]event.Event, error) {
	b.storeMu.Lock()
	defer b.storeMu.Unlock()

	if b.store != nil {
		return b.store.List(ctx, workerID, opts)
	}

	var afterTs, beforeTs int64
	if opts.AfterID != "" {
		for _, e := range b.events {
			if e.ID == opts.AfterID {
				afterTs = e.Timestamp
				break
			}
		}
	}
	if opts.BeforeID != "" {
		for _, e := range b.events {
			if e.ID == opts.BeforeID {
				beforeTs = e.Timestamp
				break
			}
		}
	}

	var result []event.Event
	allWorkers := workerID == "*"
	for _, e := range b.events {
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
