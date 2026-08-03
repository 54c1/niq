package hiw

import (
	"context"
	"fmt"
	"log"
	"sort"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/store"
)

// WebUI is the interface the WebUI HTTP server needs from HIW.
// It exists to break the import cycle: hiw → webui → hiw.
type WebUI interface {
	Follow(ctx context.Context, f Filter) (<-chan event.Event, error)
	SendInput(ctx context.Context, text string, target string, mode string) error
	Workers() []WorkerInfo
	PendingDecisions() []DecisionRequest
	MakeDecision(ctx context.Context, reqID, decision, reasoning string) error
	LoadBefore(ctx context.Context, f Filter, anchor string, limit int) ([]event.Event, error)
	Abort(ctx context.Context, target string) error
}

// WorkerInfo describes a known worker.
type WorkerInfo struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Filter describes the focus of an event stream.
type Filter struct {
	WorkerID string // focus on a specific worker
	TraceID  string // focus on a specific trace chain
	Type     string // focus on a specific event type
	Limit    int    // Follow replay limit; 0 = default 100
}

// SendInput publishes a user message to the bus as worker.input.
// If target is non-empty, the message is directed to that worker.
// mode controls input handling ("default", "append", "interrupt").
// A new trace_id is generated for this input.
func (w *Worker) SendInput(ctx context.Context, text string, target string, mode string) error {
	payload := map[string]any{"text": text}
	if mode != "" && mode != "default" {
		payload["input_mode"] = mode
	}
	evt := event.New("worker.input", w.ID(), payload)
	if target != "" {
		evt.TargetWorkerID = target
	}
	evt.TraceID = evt.ID
	return w.Bus.Publish(evt)
}

// Workers returns the current set of known worker IDs and their types.
func (w *Worker) Workers() []WorkerInfo {
	w.mu.Lock()
	defer w.mu.Unlock()
	list := make([]WorkerInfo, 0, len(w.workers))
	for id, typ := range w.workers {
		list = append(list, WorkerInfo{ID: id, Type: typ})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return list
}

// PendingDecisions returns all decision requests (pending + resolved),
// sorted newest first.
func (w *Worker) PendingDecisions() []DecisionRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := len(w.pendingDecisions) + len(w.resolvedDecisions)
	result := make([]DecisionRequest, 0, total)
	for _, req := range w.pendingDecisions {
		result = append(result, *req)
	}
	result = append(result, w.resolvedDecisions...)
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt > result[j].CreatedAt // newest first
	})
	return result
}

// MakeDecision submits a decision result via decision.made event.
func (w *Worker) MakeDecision(ctx context.Context, reqID, decision, reasoning string) error {
	w.mu.Lock()
	req, ok := w.pendingDecisions[reqID]
	if ok {
		delete(w.pendingDecisions, reqID)
		// Move to resolved history with the decision result.
		req.Status = "decided"
		req.Decision = decision
		req.Reasoning = reasoning
		w.resolvedDecisions = append(w.resolvedDecisions, *req)
	}
	w.mu.Unlock()

	// Publish decision.made so the reason worker can convert it into
	// a well-formatted input message for the LLM.
	// Use "append" mode so it doesn't interrupt pending tool calls.
	evt := event.New("decision.made", w.ID(), map[string]any{
		"request_id":      reqID,
		"call_id":         req.CallID,
		"decision":        decision,
		"reasoning":       reasoning,
		"decided_by":      w.ID(),
		"request_summary": req.Summary,
		"request_context": req.Context,
		"input_mode":      "append",
	})
	if ok && req != nil {
		evt.TargetWorkerID = req.WorkerID
	}
	return w.Bus.Publish(evt)
}

// Abort sends a worker.abort event to interrupt a worker's current reasoning.
func (w *Worker) Abort(ctx context.Context, target string) error {
	evt := event.New("worker.abort", w.ID(), map[string]any{
		"worker_id": w.ID(),
	})
	if target != "" {
		evt.TargetWorkerID = target
	}
	return w.Bus.Publish(evt)
}

// Follow returns a channel that delivers events matching the filter.
// It first replays recent history from the EventStore, then seamlessly
// switches to real-time delivery via the main event loop's broadcast.
//
// Unlike previous implementations, this does NOT subscribe to the Bus
// directly — doing so would compete with HIW's own event loop for the
// shared Bus channel. Instead, the main loop in Start() broadcasts
// every event to all Follow subscribers via broadcastToFollowers.
func (w *Worker) Follow(ctx context.Context, f Filter) (<-chan event.Event, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	log.Printf("[hiw] follow start: worker=%q trace=%q type=%q limit=%d",
		f.WorkerID, f.TraceID, f.Type, limit)

	history, err := w.store.List(ctx, "*", store.QueryOpts{
		Limit:    limit,
		Desc:     true,
		WorkerID: f.WorkerID,
		TraceID:  f.TraceID,
	})
	if err != nil {
		return nil, fmt.Errorf("hiw: follow history: %w", err)
	}
	log.Printf("[hiw] follow history loaded: %d events", len(history))

	ch := make(chan event.Event, 256)
	go func() {
		// Send history in chronological order (oldest first),
		// filtered by the same criteria as real-time events.
		for i := len(history) - 1; i >= 0; i-- {
			if !matchesFilter(history[i], f) {
				continue
			}
			select {
			case ch <- history[i]:
			case <-ctx.Done():
				return
			}
		}
		log.Printf("[hiw] follow history replayed, registering for real-time")

		// Register with main loop for real-time broadcast.
		cleanup := w.registerFollowSub(ch, f)
		defer cleanup()

		// Block until ctx is cancelled — events arrive on ch via broadcastToFollowers.
		<-ctx.Done()
	}()

	return ch, nil
}

// LoadBefore returns events older than the given anchor event ID.
// Used by UI for infinite-scroll pagination.
func (w *Worker) LoadBefore(ctx context.Context, f Filter, anchor string, limit int) ([]event.Event, error) {
	if limit <= 0 {
		limit = 50
	}
	return w.store.List(ctx, "*", store.QueryOpts{
		BeforeID: anchor,
		Limit:    limit,
		Desc:     true,
		WorkerID: f.WorkerID,
		TraceID:  f.TraceID,
	})
}
