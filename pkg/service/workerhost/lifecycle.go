// Package workerhost provides the WorkerService — the control-plane engine
// that manages worker lifecycles (register, create, destroy, run, stop).
//
// WorkerService is the "external" of HostWorker: it has no awareness of
// the event bus and no dependency on core/event or core/bus. Like
// wsbackend for workspace workers, WorkerService is a pure domain service.
package workerhost

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"

	"github.com/54c1/niq/core/worker"
)

// workerEntry pairs a managed worker with its type label.
type workerEntry struct {
	worker worker.ManagedWorker
	typ    string
}

// WorkerService manages worker lifecycles. It is a pure domain service
// — no bus awareness, no event types, no subscriptions. It only knows
// about worker.ManagedWorker and lifecycle operations.
type WorkerService struct {
	entries []workerEntry
	mu      sync.Mutex
}

// New creates an empty WorkerService.
func New() *WorkerService {
	return &WorkerService{}
}

// Register adds a pre-built managed worker to the service with the
// given type label (e.g. "host", "workspace", "reason", "timer", "hiw").
func (s *WorkerService) Register(w worker.ManagedWorker, typ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, workerEntry{worker: w, typ: typ})
}

// Unregister removes a worker by ID from the registry.
// Returns an error if the worker is not found.
func (s *WorkerService) Unregister(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.entries {
		if e.worker.ID() == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("worker %s not found", id)
}

// StartAll starts all registered workers in registration order.
// Returns the first error encountered; remaining workers are still attempted.
func (s *WorkerService) StartAll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, e := range s.entries {
		log.Printf("[workerhost] starting worker %s (type=%s)", e.worker.ID(), e.typ)
		if err := e.worker.Start(ctx); err != nil {
			return fmt.Errorf("start worker %s: %w", e.worker.ID(), err)
		}
	}

	return nil
}

// StopAll stops all registered workers in reverse registration order.
func (s *WorkerService) StopAll() {
	s.mu.Lock()
	for _, e := range slices.Backward(s.entries) {
		log.Printf("[workerhost] stopping worker %s", e.worker.ID())
		_ = e.worker.Stop()
	}
	s.mu.Unlock()
}

// Run starts all registered workers and blocks until ctx is cancelled.
// It then stops all workers and returns ctx.Err().
func (s *WorkerService) Run(ctx context.Context) error {
	if err := s.StartAll(ctx); err != nil {
		return err
	}

	<-ctx.Done()
	log.Println("[workerhost] shutting down")
	s.StopAll()
	return ctx.Err()
}

// CreateWorker creates and starts a new worker from a factory.
// typ is the type label (e.g. "workspace", "reason").
func (s *WorkerService) CreateWorker(id, typ string, factory func() worker.ManagedWorker) error {
	if s.find(id) != nil {
		return fmt.Errorf("worker %s already exists", id)
	}
	w := factory()
	if w.ID() != id {
		return fmt.Errorf("factory produced worker with id %s, expected %s", w.ID(), id)
	}
	if err := w.Start(context.Background()); err != nil {
		return fmt.Errorf("start worker %s: %w", id, err)
	}

	s.mu.Lock()
	s.entries = append(s.entries, workerEntry{worker: w, typ: typ})
	s.mu.Unlock()

	log.Printf("[workerhost] created worker %s (type=%s)", id, typ)
	return nil
}

// DestroyWorker stops and removes a worker from the registry.
func (s *WorkerService) DestroyWorker(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, e := range s.entries {
		if e.worker.ID() == id {
			_ = e.worker.Stop()
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			log.Printf("[workerhost] destroyed worker %s", id)
			return nil
		}
	}
	return fmt.Errorf("worker %s not found", id)
}

// ListWorkers returns the IDs of all registered workers matching the
// given type. If typ is empty, returns all workers.
func (s *WorkerService) ListWorkers(typ string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var ids []string
	for _, e := range s.entries {
		if typ == "" || e.typ == typ {
			ids = append(ids, e.worker.ID())
		}
	}
	return ids
}

// WorkerType returns the type label for the given worker ID, or false
// if the worker is not found.
func (s *WorkerService) WorkerType(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, e := range s.entries {
		if e.worker.ID() == id {
			return e.typ, true
		}
	}
	return "", false
}

func (s *WorkerService) find(id string) worker.ManagedWorker {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.worker.ID() == id {
			return e.worker
		}
	}
	return nil
}
