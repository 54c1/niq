package reason

import (
	"context"
	"encoding/json"
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
		event.NewPattern(event.TypeToolCompleted),
		event.NewPattern(event.TypeToolFailed),
		event.NewPattern(event.TypeToolRejected),
		event.NewPattern(event.TypeToolRequested),
		event.NewPattern(event.TypeWorkerReady),
		event.NewPattern(event.TypeWorkerGone),
		event.NewPattern(event.TypeWorkerDiscover),
		event.NewPattern(event.TypeWorkerInput),
		event.NewPattern(event.TypeWorkerAbort),
		event.NewPattern("timer.timeout"),
		event.NewPattern("timer.reminder"),
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
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerDiscover, w.ID(), map[string]any{
		"worker_id": w.ID(),
	}))

	w.started = true
	return nil
}

// publishReady re-announces this worker's presence on the bus.
func (w *Worker) broadcastReady() {
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
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

// snapshotState is the serializable execution state of a reason worker.
//
// Today it captures only the reasoning transcript (messages) — the durable
// state that survives a suspend/resume or crash recovery. As niq's meta-
// capabilities grow, more of the worker's state becomes dynamically changeable
// at runtime rather than fixed at construction (e.g. dynamically-registered
// programs, tools negotiated at runtime, per-goal context strategies). Such
// state — once it can mutate outside Config — must be added to snapshotState
// so it survives a restart too. Restore must stay able to read older blobs
// (missing fields are simply zero), so extend the struct rather than reshape it.
type snapshotState struct {
	Messages []llm.Message `json:"messages"`
}

// Snapshot captures the worker's durable execution state so it can be resumed
// later. The only state that cannot be re-derived is the reasoning transcript
// (messages): tools and published events are re-learned from worker.ready on
// restart, programs come from Config, and runtime flags (needReason,
// isReasoning, activeTimeout, ...) are transient and reset on resume. Snapshot
// is meaningful at an idle point (no in-flight reasoning round).
func (w *Worker) Snapshot() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return json.Marshal(snapshotState{Messages: w.messages})
}

// Restore rehydrates the worker from a Snapshot blob, restoring the reasoning
// transcript. Called after construction and before Start. The worker returns
// to a clean idle state: tools are re-discovered on Start, and the next input
// event triggers reasoning.
func (w *Worker) Restore(state []byte) error {
	var s snapshotState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("reason %s: restore: %w", w.ID(), err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.messages = s.Messages
	return nil
}
