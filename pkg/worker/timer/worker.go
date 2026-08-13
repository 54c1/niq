package timer

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/worker"
)

// Worker is the TimerWorker — a bus-connected timer service.
// tickafter returns immediately; the actual timer fires a timer.elapsed trigger event later.
type Worker struct {
	worker.BaseWorker
	timers    map[string]*Entry
	started   bool
	cancelRun context.CancelFunc
	mu        sync.Mutex
}

// Config holds TimerWorker configuration.
type Config struct {
	ID  string // worker ID, defaults to "timer"
	Bus corebus.WorkerSideChannel
}

// New creates a TimerWorker.
func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "timer"
	}
	return &Worker{
		BaseWorker: worker.NewBaseWorker(id, []event.EventPattern{
			event.NewPattern(event.TypeToolRequested),
			event.NewPattern(event.TypeWorkerDiscover),
		}, cfg.Bus),
		timers: make(map[string]*Entry),
	}
}

// Start subscribes to the bus and begins watching for timer requests.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("timer: already started")
	}
	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancelRun = cancelFn

	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.publishReady()
	w.started = true
	return nil
}

func (w *Worker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	for _, e := range w.timers {
		e.Stop()
	}
	w.cancelRun()
	w.cancelRun = nil
	w.started = false
	return nil
}

func (w *Worker) Snapshot() ([]byte, error)  { return nil, nil }
func (w *Worker) Restore(state []byte) error { return nil }

func (w *Worker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) process(evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerDiscover:
		w.publishReady()
	case event.TypeToolRequested:
		if evt.TargetWorkerID != "" && evt.TargetWorkerID != w.ID() {
			return
		}
		w.handleToolCall(evt)
	}
}

func (w *Worker) handleToolCall(evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	toolName, _ := evt.Payload["name"].(string)
	callerID := evt.WorkerId
	args, _ := evt.Payload["arguments"].(map[string]any)
	traceID := evt.TraceID

	switch toolName {
	case "set_tool_timeout":
		// set_tool_timeout always uses tool_call_timeout tick_type.
		args["tick_type"] = "tool_call_timeout"
		w.handleTickAfter(callID, toolName, callerID, args, traceID)
	case "elapse":
		// elapse always uses reminder tick_type.
		args["tick_type"] = "reminder"
		w.handleTickAfter(callID, toolName, callerID, args, traceID)
	case "cancel":
		w.handleCancel(callID, toolName, callerID, args, traceID)
	default:
		w.publishFailed(callerID, callID, toolName,
			fmt.Errorf("unknown tool: %s", toolName), traceID)
	}
}

func getIntArg(m map[string]any, key string, defaultVal int) int {
	v, ok := m[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return defaultVal
}

func getStringArg(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func (w *Worker) handleTickAfter(callID, toolName, callerID string, args map[string]any, traceID string) {
	durationMS := getIntArg(args, "duration_ms", 0)
	purpose := getStringArg(args, "purpose")
	tickType := getStringArg(args, "tick_type")

	w.mu.Lock()
	w.timers[callID] = afterFunc(w.ID(), w.Channel, callID, callerID,
		durationMS, purpose, tickType, traceID)
	w.mu.Unlock()

	result, _ := json.Marshal(map[string]any{
		"tick_type": tickType,
		"purpose":   purpose,
		"status":    "scheduled",
	})
	w.publishCompleted(callerID, callID, toolName, string(result), traceID)
}

func (w *Worker) handleCancel(callID, toolName, callerID string, args map[string]any, traceID string) {
	timerID := getStringArg(args, "timer_id")

	w.mu.Lock()
	e, ok := w.timers[timerID]
	if ok {
		e.Stop()
		delete(w.timers, timerID)
	}
	w.mu.Unlock()

	if ok {
		w.publishCompleted(callerID, callID, toolName, `{"status":"cancelled"}`, traceID)
	} else {
		w.publishFailed(callerID, callID, toolName,
			fmt.Errorf("timer not found: %s", timerID), traceID)
	}
}

func (w *Worker) publishReady() {
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "timer",
		"tools": []map[string]any{{
			"name":        "set_tool_timeout",
			"description": "Set a timeout for your pending tool calls. When the timer fires, unresponsive tool calls will be automatically cancelled so you can proceed. If all tool calls complete before the timeout, the timer is cancelled automatically. Call this after issuing tool calls that may take a while.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"duration_ms": map[string]any{
						"type":        "integer",
						"description": "Timeout in milliseconds.",
					},
					"purpose": map[string]any{
						"type":        "string",
						"description": "Why this timeout is needed.",
					},
				},
				"required": []any{"duration_ms", "purpose"},
			},
		}, {
			"name":        "elapse",
			"description": "Set a reminder timer. After the specified duration, you will receive a timer.elapsed event. Unlike set_tool_timeout, this timer is never automatically cancelled — it always fires. Use this for general-purpose timing and reminders.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"duration_ms": map[string]any{
						"type":        "integer",
						"description": "Duration in milliseconds.",
					},
					"purpose": map[string]any{
						"type":        "string",
						"description": "Natural language description of what to do when this timer fires.",
					},
				},
				"required": []any{"duration_ms", "purpose"},
			},
		}},
		"publishes": []map[string]any{
			{"type": "timer.timeout", "description": "A set_tool_timeout timer has fired"},
			{"type": "timer.reminder", "description": "An elapse reminder timer has fired"},
		},
	}))
}

func (w *Worker) publishCompleted(callerID, callID, toolName, result, traceID string) {
	evt := event.New(event.TypeToolCompleted, w.ID(), map[string]any{
		"call_id": callID,
		"name":    toolName,
		"result":  result,
	})
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

func (w *Worker) publishFailed(callerID, callID, toolName string, err error, traceID string) {
	evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
		"call_id": callID,
		"name":    toolName,
		"error":   err.Error(),
	})
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}
