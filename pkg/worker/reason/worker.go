package reason

import (
	"context"
	"fmt"
	"log"
	"sync"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/core/program"
	"github.com/54c1/niq/core/worker"
)

// EventConverter pairs an event pattern with a conversion function
// that transforms matching events into LLM messages.
type EventConverter struct {
	Pattern   event.EventPattern
	Converter func(evt event.Event) []llm.Message
}

// Config holds the configuration for a Worker.
type Config struct {
	ID              string
	Handlers        []EventConverter
	Provider        llm.LLMProvider
	Programs        []program.Program
	Bus             corebus.WorkerSideChannel
	ReasoningEffort *string
}

// Reason Worker is an event-driven reasoning node. It receives input
// events from the bus, maintains conversation state (w.messages), and calls
// the LLM provider when needReason is set. Each reasoning round produces
// either a text response or tool calls (all handled uniformly via the bus).
type Worker struct {
	worker.BaseWorker

	llmProvider    llm.LLMProvider
	callTracker    *ToolCallTracker
	eventConverter []EventConverter

	programs  []program.Program         // loaded programs
	tools     map[string]worker.Tool    // tools from the bus (worker.ready events)
	publishes map[string][]EventPublish // events published by each worker (provider → events)
	// Tool name sanity: maps sanitized name → original name.
	// LLM API rejects names containing dots; we replace '.' with '_' before
	// sending and map back when tool calls return.
	toolNameMap map[string]string

	reasoningEffort *string

	// currentTraceID holds the trace_id from the most recent worker.input.
	// All events published during this reasoning round propagate this trace_id
	// so that the frontend can correlate them into a single conversation turn.
	currentTraceID string

	mu            sync.Mutex
	started       bool
	needReason    bool
	isReasoning   bool
	activeTimeout string // current round's set_tool_timeout call_id, "" if none
	messages      []llm.Message

	cancelReason context.CancelFunc
	cancelRun    context.CancelFunc

	// interruptReason records why the current reasoning round was interrupted
	// ("abort" or "input"). Set by the watch loop before calling cancelReason()
	// so the reason goroutine can read it after the context is cancelled.
	interruptReason string
}

// NewWorker creates a Worker from the given configuration.
func NewWorker(cfg Config) *Worker {
	// Built-in subscriptions (tool results, worker presence, etc.) come with
	// their own handlers.
	// Users can add custom patterns and converters via cfg.Handlers.
	subs := make([]event.EventPattern, 0, len(cfg.Handlers)+5)
	for _, h := range cfg.Handlers {
		subs = append(subs, h.Pattern)
	}
	subs = append(subs,
		event.NewPattern("tool.completed"),
		event.NewPattern("tool.failed"),
		event.NewPattern("tool.rejected"),
		event.NewPattern("worker.ready"),
		event.NewPattern("worker.gone"),
		event.NewPattern("worker.discover"),
		event.NewPattern("worker.abort"),
		event.NewPattern("timer.timeout"),
		event.NewPattern("timer.reminder"),
		event.NewPattern("worker.input"),
		event.NewPattern("tool.requested"),
		event.NewPattern("decision.made"),
	)

	// Programs are used as-is (inline content). Lazy loading via Program
	// Worker is handled at runtime through program.load tool calls.

	w := &Worker{
		BaseWorker:      worker.NewBaseWorker(cfg.ID, subs, cfg.Bus),
		llmProvider:     cfg.Provider,
		callTracker:     NewToolCallTracker(),
		eventConverter:  cfg.Handlers,
		tools:           make(map[string]worker.Tool),
		publishes:       make(map[string][]EventPublish),
		programs:        cfg.Programs,
		toolNameMap:     make(map[string]string),
		reasoningEffort: cfg.ReasoningEffort,
	}
	w.initBuiltinTools()
	return w
}

// Start begins the event watch. It returns an error if the worker
// has already been started.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		return fmt.Errorf("llmworker %s: already started", w.ID())
	}

	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancelRun = cancelFn

	// Subscribe and receive from the bus
	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)

	// Announce presence so other workers can discover this one.
	log.Printf("[llmworker %s] publishing worker.ready", w.ID())
	_ = w.Channel.Broadcast(context.Background(), event.New("worker.ready", w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "reason",
		"publishes": []map[string]any{
			{"type": "reason.response", "description": "Reasoning result text response"},
			{"type": "reason.thinking", "description": "Reasoning thinking process"},
			{"type": "reason.interrupted", "description": "Reasoning interrupted (abort/input) with preserved content"},
			{"type": "reason.start", "description": "Reasoning round started"},
			{"type": "reason.end", "description": "Reasoning round ended"},
		},
	}))

	// Publish worker.discover to trigger other Workers already on the bus
	// to re-announce their capabilities via worker.ready.
	_ = w.Channel.Broadcast(context.Background(), event.New("worker.discover", w.ID(), map[string]any{
		"worker_id": w.ID(),
	}))

	w.started = true
	return nil
}

// publishReady re-announces this worker's presence on the bus.
func (w *Worker) publishReady() {
	_ = w.Channel.Broadcast(context.Background(), event.New("worker.ready", w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "reason",
		"publishes": []map[string]any{
			{"type": "reason.response", "description": "Reasoning result text response"},
			{"type": "reason.thinking", "description": "Reasoning thinking process"},
			{"type": "reason.interrupted", "description": "Reasoning interrupted (abort/input) with preserved content"},
			{"type": "reason.start", "description": "Reasoning round started"},
			{"type": "reason.end", "description": "Reasoning round ended"},
		},
	}))
}

// Stop cancels the worker's event watch.
func (w *Worker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.started {
		return nil
	}

	w.cancelRun()
	w.cancelRun = nil
	w.started = false

	return nil
}

func (w *Worker) Snapshot() ([]byte, error)  { return nil, nil }
func (w *Worker) Restore(state []byte) error { return nil }
