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
	"github.com/54c1/niq/pkg/worker/reason/builder"
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
	// ContextBuilder is the worker's context construction core. nil uses
	// the default accumulate builder (flat transcript). All calls happen
	// under the worker's mutex; builders are passive.
	ContextBuilder builder.ContextBuilder

	// Context budget (see budget.go). ContextWindow is the model's window in
	// tokens; 0 disables all budget handling. BudgetSoft/BudgetHard are
	// occupancy ratios; KeepTail is how many recent messages compaction
	// preserves. CompactDirective overrides the fallback summarizer prompt
	// (program-driven digest formats plug in here).
	ContextWindow    int
	BudgetSoft       float64
	BudgetHard       float64
	KeepTail         int
	CompactDirective string

	// SeedMessages are applied to the builder at construction: the spawner's
	// handover brief (goal goes to Programs instead - see context-builder.md
	// §6). nil for a fresh worker.
	SeedMessages []llm.Message
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
	contextBuilder          builder.ContextBuilder

	// context budget state (budget.go); guarded by w.mu
	contextWindow            int
	budgetSoft               float64
	budgetHard               float64
	keepTail                 int
	compactDirectiveOverride string
	lastUsageTokens          int
	budgetReminded           bool
	isCompacting             bool

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
		contextBuilder: func() builder.ContextBuilder {
			if cfg.ContextBuilder != nil {
				return cfg.ContextBuilder
			}
			return builder.NewAccumulate()
		}(),
		reasoningEffort: func() *string {
			if cfg.ReasoningEffort != nil {
				return cfg.ReasoningEffort
			}
			d := "medium"
			return &d
		}(),
		contextWindow:            cfg.ContextWindow,
		budgetSoft:               cfg.BudgetSoft,
		budgetHard:               cfg.BudgetHard,
		keepTail:                 cfg.KeepTail,
		compactDirectiveOverride: cfg.CompactDirective,
	}
	if w.budgetSoft <= 0 {
		w.budgetSoft = defaultBudgetSoft
	}
	if w.budgetHard <= 0 {
		w.budgetHard = defaultBudgetHard
	}
	if w.keepTail <= 0 {
		w.keepTail = defaultKeepTail
	}

	// Seed the transcript with the spawner's handover brief, if any.
	if len(cfg.SeedMessages) > 0 {
		w.contextBuilder.Apply(builder.InputEvent{Messages: cfg.SeedMessages})
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

// snapshotState is retained for backward compatibility: the durable state
// is now owned by the context builder (its State/Restore round-trip the
// transcript). Snapshot/Restore delegate to the builder directly; this
// comment documents the migration for readers of older blobs.
//
// As niq's meta-capabilities grow, more of the worker's state becomes
// dynamically changeable at runtime rather than fixed at construction. Such
// state - once it can mutate outside Config - must be added to the builder's
// snapshot so it survives a restart too. Restore must stay able to read
// older blobs, so shapes grow rather than reshape.

// Snapshot captures the worker's durable execution state so it can be resumed
// later. The only state that cannot be re-derived is the reasoning transcript
// (owned by the context builder): tools and published events are re-learned
// from worker.ready on restart, programs come from Config, and runtime flags
// (needReason, isReasoning, activeTimeout, ...) are transient and reset on
// resume. Snapshot is meaningful at an idle point (no in-flight reasoning
// round).
func (w *Worker) Snapshot() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.contextBuilder.State()
}

// Restore rehydrates the worker from a Snapshot blob, restoring the reasoning
// transcript. Called after construction and before Start. The worker returns
// to a clean idle state: tools are re-discovered on Start, and the next input
// event triggers reasoning.
func (w *Worker) Restore(state []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.contextBuilder.Restore(state)
}
