package worker

import (
	"context"

	"github.com/54c1/niq/core/event"
)

// Worker is the contract for built-in Go workers running inside
// WorkerService. External workers connect to the bus via UDS/WS
// and are not constrained by this interface.
type Worker interface {
	// ID returns the unique identifier of this Worker.
	ID() string

	// Start enters the Worker's idle loop, waiting for events.
	Start(ctx context.Context) error

	// Subscriptions returns the event patterns this Worker subscribes to.
	Subscriptions() []event.EventPattern

}

// ManagedWorker extends Worker with Snapshot / Restore to support
// suspend-resume and crash recovery. Implementations serialize their
// full execution state and rehydrate it from a blob.
type ManagedWorker interface {
	Worker

	// Stop terminates the Worker and releases resources.
	Stop() error

	// Snapshot captures the worker's current execution state as an opaque blob.
	// The blob may be persisted and later passed to Restore to resume the
	// worker from the same state — enabling suspend / resume and recovery.
	Snapshot() ([]byte, error)

	// Restore rehydrates the worker from a blob previously returned by
	// Snapshot. Called after construction and before Start.
	Restore(state []byte) error
}
