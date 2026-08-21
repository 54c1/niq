// Package reason provides the reasoning mechanism shared by reason-family
// workers. It is the machinery a reasoning node needs in one piece: the
// reasoning round (an LLM call + lifecycle broadcasts), the transcript, budget
// and compaction, tool-call tracking and dispatch, and system-prompt
// construction.
//
// This package deliberately contains no notion of *what* the worker is
// attending to. The split is:
//   - mechanism (here): how a reasoning node reasons — invokes the LLM, manages
//     its working notes, shrinks them against the finite window, dispatches
//     tools, renders its system prompt. Any reasoning node needs these, no
//     matter what it is pointed at.
//   - attention (the embedding worker): what the node subscribes to, what its
//     built-in tools are, what its event converters produce, what its programs
//     tell it to preserve when compacting. These reflect a specific goal.
//
// An embedding worker composes one goal-specific attention onto this shared
// mechanism.
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
	"github.com/54c1/niq/pkg/reason/transcript"
)

// EventConverter pairs an event pattern with a conversion function that
// transforms matching events into LLM messages. The embedding worker supplies
// its own converters (which events become which input to the model).
type EventConverter struct {
	Pattern   event.EventPattern
	Converter func(evt event.Event) []llm.Message
}

// EventPublish describes an event type that a worker publishes on the bus.
type EventPublish struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

const (
	DefaultBudgetSoft = 0.85
	DefaultBudgetHard = 0.97
	DefaultKeepTail   = 8
)

// Config holds the inputs to BaseReasonWorker — the pieces a reasoning node
// needs. The embedding worker assembles a Config (its goals, programs,
// converters, transcript) and calls NewBaseReasonWorker.
type Config struct {
	ID              string
	Bus             corebus.WorkerSideChannel
	Subscriptions   []event.EventPattern
	Programs        []program.Program
	EventConverters []EventConverter
	Transcript      transcript.Transcript

	Provider        llm.LLMProvider
	ReasoningEffort *string

	ContextWindow    int
	BudgetSoft       float64
	BudgetHard       float64
	KeepTail         int
	CompactDirective string

	SeedMessages []llm.Message
}

// NewBaseReasonWorker assembles a BaseReasonWorker from a Config: applies
// budget defaults, initializes the empty tool table / publish map / tool-call
// tracker, and seeds the transcript with any handover brief.
func NewBaseReasonWorker(cfg Config) *BaseReasonWorker {
	if cfg.Transcript == nil {
		cfg.Transcript = transcript.NewAccumulateTranscript()
	}
	if cfg.BudgetSoft <= 0 {
		cfg.BudgetSoft = DefaultBudgetSoft
	}
	if cfg.BudgetHard <= 0 {
		cfg.BudgetHard = DefaultBudgetHard
	}
	if cfg.KeepTail <= 0 {
		cfg.KeepTail = DefaultKeepTail
	}
	if cfg.ReasoningEffort == nil {
		d := "medium"
		cfg.ReasoningEffort = &d
	}

	w := &BaseReasonWorker{
		BaseWorker:               worker.NewBaseWorker(cfg.ID, cfg.Subscriptions, cfg.Bus),
		LLMProvider:              cfg.Provider,
		Transcript:               cfg.Transcript,
		Tools:                    make(map[string]worker.Tool),
		PublishMap:               make(map[string][]EventPublish),
		eventConverters:          cfg.EventConverters,
		programs:                 cfg.Programs,
		toolNameMap:              make(map[string]string),
		toolCallTracker:          NewToolCallTracker(),
		contextWindow:            cfg.ContextWindow,
		budgetSoft:               cfg.BudgetSoft,
		budgetHard:               cfg.BudgetHard,
		keepTail:                 cfg.KeepTail,
		compactDirectiveOverride: cfg.CompactDirective,
		reasoningEffort:          cfg.ReasoningEffort,
	}
	if len(cfg.SeedMessages) > 0 {
		w.Transcript.Apply(transcript.InputEvent{Messages: cfg.SeedMessages})
	}

	// Install the built-in tools (send_message, list_workers, context.compress,
	// context.rotate). An embedding worker can add its own to
	// w.Tools before Start.
	w.initBuiltinTools()

	return w
}

// Start begins the event watch. It returns an error if the worker is already
// started. Provided by the base so embedding reason-family workers inherit
// the lifecycle without reimplementing it.
func (w *BaseReasonWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		return fmt.Errorf("reason %s: already started", w.ID())
	}

	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancelRun = cancelFn

	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)

	w.broadcastReady()
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerDiscover, w.ID(), map[string]any{
		"worker_id": w.ID(),
	}))

	w.started = true
	return nil
}

// Stop cancels the worker's event watch.
func (w *BaseReasonWorker) Stop() error {
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

// Snapshot captures the worker's durable execution state (the transcript).
func (w *BaseReasonWorker) Snapshot() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Transcript.State()
}

// Restore rehydrates the worker from a Snapshot blob, restoring the reasoning
// transcript. Called after construction and before Start.
func (w *BaseReasonWorker) Restore(state []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Transcript.Restore(state)
}

// BaseReasonWorker is the reasoning mechanism shared by all reason-family
// workers: it embeds worker.BaseWorker (id/subs/channel) and owns the working
// notes, the LLM call, budget, tool table and the run flags of one reasoning
// round. It knows how to reason, not what to attend to.
type BaseReasonWorker struct {
	worker.BaseWorker
	mu sync.Mutex

	LLMProvider     llm.LLMProvider
	Transcript      transcript.Transcript
	Tools           map[string]worker.Tool    // tools from the bus + built-ins; read by dispatch
	PublishMap      map[string][]EventPublish // worker ID -> published events
	toolNameMap     map[string]string         // maps sanitized name -> original tool name
	toolCallTracker *ToolCallTracker
	eventConverters []EventConverter

	reasoningEffort *string
	programs        []program.Program

	started                 bool
	needReason              bool
	isReasoning             bool
	activeTimeout           string       // current round's set_tool_timeout call_id, "" if none
	interruptReason         PreemptCause // why the current reasoning round was interrupted
	immediateReasoningCause PreemptCause // why the next reasoning round was triggered
	currentTraceID          string

	cancelReason context.CancelFunc
	cancelRun    context.CancelFunc

	// Context budget state; guarded by w.mu
	contextWindow            int
	budgetSoft               float64
	budgetHard               float64
	keepTail                 int
	compactDirectiveOverride string
	lastUsageTokens          int
	budgetReminded           bool
	isCompacting             bool
}
