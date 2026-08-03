// Package host provides the HostWorker — the bus-facing worker that exposes
// spawn/list/destroy tools for managing other workers' lifecycles.
//
// HostWorker's "external" is lifecycle.WorkerService, analogous to how
// workspace workers use wsbackend as their external. HostWorker handles
// the bus protocol; WorkerService handles the lifecycle mechanics.
package host

import (
	"context"
	"fmt"
	"log"
	"sync"

	corebus "github.com/54c1/niq/core/bus"
	svcbus "github.com/54c1/niq/pkg/service/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/pkg/service/workerhost"
)

// Config holds the configuration for a HostWorker.
type Config struct {
	ID     string
	Bus    corebus.EventBusClient // data-plane client
	Shared *svcbus.Bus           // control-plane bus for RegisterWorker
	Engine *workerhost.WorkerService
}

// HostWorker is a bus-facing worker that manages other worker lifecycles.
// It subscribes to tool.requested and worker.discover, and exposes
// spawn/list/destroy tools on the bus. Actual lifecycle operations are
// delegated to workerhost.WorkerService.
type HostWorker struct {
	worker.BaseWorker
	sharedBus *svcbus.Bus
	engine    *workerhost.WorkerService
	started   bool
	cancel    context.CancelFunc
	mu        sync.Mutex
}

// New creates a HostWorker that delegates lifecycle operations to engine.
func New(cfg Config) *HostWorker {
	id := cfg.ID
	if id == "" {
		id = "host"
	}
	return &HostWorker{
		BaseWorker: worker.NewBaseWorker(id, []event.EventPattern{
			event.NewPattern("tool.requested"),
			event.NewPattern("worker.discover"),
		}, cfg.Bus),
		sharedBus: cfg.Shared,
		engine:    cfg.Engine,
	}
}

// Start subscribes to the bus and begins watching for tool calls.
func (w *HostWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		return fmt.Errorf("host: already started")
	}

	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancel = cancelFn

	w.Bus.Subscribe(w.Subscriptions())
	busCh, _ := w.Bus.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.publishReady()

	w.started = true
	log.Println("[host] started")

	return nil
}

// Stop shuts down the host worker.
func (w *HostWorker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.started {
		return nil
	}

	w.cancel()
	w.cancel = nil
	w.started = false
	log.Println("[host] stopped")

	return nil
}

func (w *HostWorker) Snapshot() ([]byte, error)  { return nil, nil }
func (w *HostWorker) Restore(state []byte) error { return nil }

// ── Event loop ──

func (w *HostWorker) watch(ctx context.Context, busCh chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *HostWorker) process(evt event.Event) {
	switch evt.Type {
	case "worker.discover":
		if evt.WorkerId != w.ID() {
			w.publishReady()
		}
	case "tool.requested":
		if evt.TargetWorkerID != "" && evt.TargetWorkerID != w.ID() {
			return
		}
		w.handleToolCall(evt)
	}
}
