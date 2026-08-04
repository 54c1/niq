package inprocess

import (
	"context"
	"fmt"
	"sync"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
)

// workerSide implements WorkerSideChannel for in-process transport.
type workerSide struct {
	workerID  string
	toBus     chan corebus.Request
	toWorker  chan event.Event
	connected bool
	closeOnce sync.Once
}

func (ch *workerSide) ID() string { return ch.workerID }

// Connect marks the connection as established. For in-process transport,
// the connection is already established when Connect is called on the
// listener. The endpoint parameter is ignored.
func (ch *workerSide) Connect(ctx context.Context, endpoint string) error {
	ch.connected = true
	return nil
}

func (ch *workerSide) Send(ctx context.Context, evt event.Event, targets ...string) error {
	if !ch.connected {
		return fmt.Errorf("inprocess: worker %s not connected", ch.workerID)
	}
	if len(targets) == 0 {
		return fmt.Errorf("inprocess: Send requires at least one target")
	}
	req := corebus.Request{
		Type:    corebus.RequestSend,
		Events:  []event.Event{evt},
		Targets: targets,
	}
	select {
	case ch.toBus <- req:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (ch *workerSide) Broadcast(ctx context.Context, evt event.Event) error {
	if !ch.connected {
		return fmt.Errorf("inprocess: worker %s not connected", ch.workerID)
	}
	req := corebus.Request{
		Type:   corebus.RequestBroadcast,
		Events: []event.Event{evt},
	}
	select {
	case ch.toBus <- req:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (ch *workerSide) Receive(ctx context.Context) (<-chan event.Event, error) {
	return ch.toWorker, nil
}

func (ch *workerSide) Close() error {
	ch.closeOnce.Do(func() {
		ch.connected = false
		close(ch.toBus)
		close(ch.toWorker)
	})
	return nil
}

// Compile-time check.
var _ corebus.WorkerSideChannel = (*workerSide)(nil)