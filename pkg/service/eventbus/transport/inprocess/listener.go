// Package inprocess provides an in-process transport implementation for the event bus.
//
// It implements the "球和箭" model using shared Go channels:
//   - InProcListener: the "守塔人" that accepts connections and returns BusSideChannel
//   - bus.go: BusSideChannel implementation (bus side)
//   - worker.go: WorkerSideChannel implementation (worker side)
package inprocess

import (
	"context"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
)

// conn carries a paired transport connection from Connect to Accept.
type conn struct {
	busSide    *busSide
	workerSide *workerSide
}

// InProcListener is an in-process Listener that accepts connections from
// workers running in the same process. It implements the "守塔人" role:
// Accept blocks until a worker connects, then returns a BusSideChannel.
//
// Usage:
//
//	listener := NewInProcListener()
//	go func() {
//	    for {
//	        ch, _ := listener.Accept(ctx)
//	        eventbus.Attach(ctx, engine, ch.WorkerID(), ch)
//	    }
//	}()
//	// A worker connects:
//	workerCh, _ := listener.Connect(ctx, "worker-1")
type InProcListener struct {
	acceptCh chan conn
}

// NewInProcListener creates a new in-process listener.
func NewInProcListener() *InProcListener {
	return &InProcListener{
		acceptCh: make(chan conn),
	}
}

// Accept blocks until a worker connects via Connect, then returns the
// BusSideChannel. This is called by the bus side (the "守塔人").
func (l *InProcListener) Accept(ctx context.Context) (corebus.BusSideChannel, error) {
	select {
	case c := <-l.acceptCh:
		return c.busSide, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Connect creates a paired transport and returns the WorkerSideChannel.
// The BusSideChannel is delivered to Accept() synchronously.
// This is called by the worker side.
func (l *InProcListener) Connect(ctx context.Context, workerID string) (corebus.WorkerSideChannel, error) {
	toBus := make(chan corebus.Request, 64)
	toWorker := make(chan event.Event, 64)

	bs := &busSide{
		workerID: workerID,
		toWorker: toWorker,
		toBus:    toBus,
	}
	ws := &workerSide{
		workerID: workerID,
		toBus:    toBus,
		toWorker: toWorker,
	}

	select {
	case l.acceptCh <- conn{busSide: bs, workerSide: ws}:
		return ws, nil
	case <-ctx.Done():
		close(toBus)
		close(toWorker)
		return nil, ctx.Err()
	}
}