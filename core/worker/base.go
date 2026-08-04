package worker

import (
	"context"

	corebus "github.com/54c1/niq/core/bus"

	"github.com/54c1/niq/core/event"
)

// BaseWorker provides a partial [Worker] implementation that other workers
// embed. It stores an id and subscription list; [Start] is a
// intentional no-ops that callers are expected to override.
type BaseWorker struct {
	id      string
	subs    []event.EventPattern
	Bus     corebus.EventBusClient // Deprecated: use Channel
	Channel corebus.WorkerSideChannel
}

// NewBaseWorker returns a BaseWorker with the given id and subscriptions.
func NewBaseWorker(id string, subs []event.EventPattern, busClient corebus.EventBusClient) BaseWorker {
	return BaseWorker{
		id:  id,
		subs: subs,
		Bus: busClient,
	}
}

// NewBaseWorkerV2 creates a BaseWorker with the new WorkerSideChannel.
func NewBaseWorkerV2(id string, subs []event.EventPattern, ch corebus.WorkerSideChannel) BaseWorker {
	return BaseWorker{
		id:      id,
		subs:    subs,
		Channel: ch,
	}
}

func (w *BaseWorker) ID() string { return w.id }

// Subscriptions returns the event patterns this worker is interested in.
// Implements [Worker].
func (w *BaseWorker) Subscriptions() []event.EventPattern {
	return w.subs
}

// Start is a no-op stub. Embedding workers should override.
func (w *BaseWorker) Start(ctx context.Context) error {
	return nil
}
