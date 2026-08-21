// Package reason provides the domain-agnostic reasoning core shared by
// reason-family workers: the reasoning round (an LLM call + lifecycle
// broadcasts), budget handling, tool-call tracking, tool dispatch and system
// prompt construction. A reason-family worker embeds BaseReasonWorker and
// layers on its own subscriptions, tool discovery, built-in tools and event
// conversion.
//
// BaseReasonWorker embeds worker.BaseWorker, so it is a bus worker: it has an
// id, subscriptions and a channel, and can broadcast/send events. What it
// does NOT decide is *which* events to subscribe to or *which* tools to expose
// as built-ins — each embedding worker does.
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
// transforms matching events into LLM messages. Domain-agnostic: reason-family
// workers supply their own converters.
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

// Config holds the domain-agnostic inputs to BaseReasonWorker: the pieces every
// reason-family worker needs. Embedding workers (e.g. pkg/worker/reason)
// assemble a Config and call NewBaseReasonWorker.
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

// NewBaseReasonWorker assembles a BaseReasonWorker from a domain-agnostic
// Config: applies budget defaults, initializes the empty tool table / publish
// map / tool-call tracker, and seeds the transcript with any handover brief.
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

	// Install the domain-agnostic built-in tools (send_message, list_workers,
	// compress, context.close_episode). Reason-family workers can add their own
	// via w.Tools before Start.
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

// BaseReasonWorker is the domain-agnostic reasoning core shared by all
// reason-family workers. It embeds worker.BaseWorker (id/subs/channel).
type BaseReasonWorker struct {
	worker.BaseWorker
	mu sync.Mutex

	LLMProvider     llm.LLMProvider
	Transcript      transcript.Transcript
	Tools           map[string]worker.Tool    // tools from the bus + built-ins; read by dispatch
	PublishMap      map[string][]EventPublish // worker ID -> published events
	toolCallTracker *ToolCallTracker

	toolNameMap map[string]string // maps sanitized name -> original tool name

	eventConverters []EventConverter

	reasoningEffort *string
	programs        []program.Program

	started bool

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
