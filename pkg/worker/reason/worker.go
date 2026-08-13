package reason

import (
	"context"
	"fmt"
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
	EventConverters []EventConverter
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
	mu sync.Mutex

	llmProvider     llm.LLMProvider
	toolCallTracker *ToolCallTracker
	eventConverters []EventConverter

	reasoningEffort     *string
	programs            []program.Program
	workerTools         map[string]worker.Tool    // tools from the bus (worker.ready events)
	workerPublishEvents map[string][]EventPublish // events published by each worker (provider → events)
	toolNameMap         map[string]string         // maps sanitized ('.' → '_') name → original tool name

	started                 bool
	needReason              bool
	isReasoning             bool
	activeTimeout           string       // current round's set_tool_timeout call_id, "" if none
	interruptReason         PreemptCause // why the current reasoning round was interrupted
	immediateReasoningCause PreemptCause // why the next reasoning round was triggered; set by setImmediateReasoning, consumed by reason()
	currentTraceID          string
	messages                []llm.Message

	cancelReason context.CancelFunc
	cancelRun    context.CancelFunc
}

// NewWorker creates a Worker from the given configuration.
func NewWorker(cfg Config) *Worker {
	// Built-in subscriptions (tool results, worker presence, etc.) come with
	// their own handlers. Users can add custom patterns and converters via cfg.Handlers.
	subs := make([]event.EventPattern, 0, len(cfg.EventConverters)+5)
	for _, h := range cfg.EventConverters {
		subs = append(subs, h.Pattern)
	}
	subs = append(subs,
		event.NewPattern("tool.completed"),
		event.NewPattern("tool.failed"),
		event.NewPattern("tool.rejected"),
		event.NewPattern("tool.requested"),
		event.NewPattern("worker.ready"),
		event.NewPattern("worker.gone"),
		event.NewPattern("worker.discover"),
		event.NewPattern("worker.input"),
		event.NewPattern("worker.abort"),
		event.NewPattern("timer.timeout"),
		event.NewPattern("timer.reminder"),
		event.NewPattern("decision.made"),
	)

	w := &Worker{
		BaseWorker:          worker.NewBaseWorker(cfg.ID, subs, cfg.Bus),
		llmProvider:         cfg.Provider,
		toolCallTracker:     NewToolCallTracker(),
		eventConverters:     cfg.EventConverters,
		workerTools:         make(map[string]worker.Tool),
		workerPublishEvents: make(map[string][]EventPublish),
		programs:            cfg.Programs,
		toolNameMap:         make(map[string]string),
		reasoningEffort: func() *string {
			if cfg.ReasoningEffort != nil {
				return cfg.ReasoningEffort
			}
			d := "medium"
			return &d
		}(),
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
		return fmt.Errorf("reason %s: already started", w.ID())
	}

	// context
	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancelRun = cancelFn

	// Receive from the bus
	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)

	// Announce presence so other workers can discover this one.
	w.broadcastReady()

	// Broadcast worker.discover to trigger other Workers already on the bus
	// to re-announce their capabilities via worker.ready.
	_ = w.Channel.Broadcast(context.Background(), event.New("worker.discover", w.ID(), map[string]any{
		"worker_id": w.ID(),
	}))

	w.started = true
	return nil
}

// publishReady re-announces this worker's presence on the bus.
func (w *Worker) broadcastReady() {
	_ = w.Channel.Broadcast(context.Background(), event.New("worker.ready", w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "reason",
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
