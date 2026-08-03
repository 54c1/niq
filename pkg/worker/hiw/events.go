package hiw

import (
	"log"
	"strings"

	"github.com/54c1/niq/core/event"
)

func (w *Worker) handleEvent(evt event.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.handleLifecycle(evt)
	w.handleDecision(evt)
	w.handleDiscovery(evt)
	w.handleToolCall(evt)
}

func (w *Worker) handleLifecycle(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)
	if workerID == "" {
		return
	}
	if strings.HasPrefix(evt.Type, "worker.ready") {
		log.Printf("[hiw] discovered worker: %s", workerID)
		workerType, _ := evt.Payload["type"].(string)
		w.workers[workerID] = workerType
	} else if strings.HasPrefix(evt.Type, "worker.gone") {
		delete(w.workers, workerID)
	}
}

// handleDiscovery re-publishes worker.ready in response to worker.discover,
// so workers that started later can discover HIW's tools.
func (w *Worker) handleDiscovery(evt event.Event) {
	if evt.Type == "worker.discover" && evt.WorkerId != w.ID() {
		w.publishReady()
	}
}

// handleToolCall processes tool.requested events targeting HIW's tools.
func (w *Worker) handleToolCall(evt event.Event) {
	if evt.Type != "tool.requested" {
		return
	}
	name, _ := evt.Payload["name"].(string)
	if name != "request_human_decision" {
		return
	}
	callID, _ := evt.Payload["call_id"].(string)
	callerID := evt.WorkerId
	args, _ := evt.Payload["arguments"].(map[string]any)

	summary, _ := args["summary"].(string)
	contextStr, _ := args["context"].(string)

	// Create a pending decision request from this tool call.
	reqID := "hiw-" + callID
	// Parse optional options.
	var opts []Option
	if optsRaw, ok := args["options"].([]any); ok {
		for _, o := range optsRaw {
			if m, ok := o.(map[string]any); ok {
				id, _ := m["id"].(string)
				label, _ := m["label"].(string)
				if id != "" {
					opts = append(opts, Option{ID: id, Label: label})
				}
			}
		}
	}

	req := &DecisionRequest{
		ID:        reqID,
		CallID:    callID,
		WorkerID:  callerID,
		Summary:   summary,
		Context:   contextStr,
		Options:   opts,
		Status:    "pending",
		CreatedAt: evt.Timestamp,
	}
	w.pendingDecisions[reqID] = req
	log.Printf("[hiw] human decision requested: %s from %s — %s", reqID, callerID, summary)

	// Respond with tool.completed so the caller knows the request was submitted.
	evtResp := event.New("tool.completed", w.ID(), map[string]any{
		"call_id":   callID,
		"name":      "request_human_decision",
		"worker_id": callerID,
		"result":    "decision request submitted, waiting for human response",
	})
	evtResp.TargetWorkerID = callerID
	_ = w.Bus.Publish(evtResp)
}

// ── helpers ──

func getStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func matchesFilter(evt event.Event, f Filter) bool {
	if f.WorkerID != "" && evt.WorkerId != f.WorkerID && evt.TargetWorkerID != f.WorkerID {
		return false
	}
	if f.TraceID != "" && evt.TraceID != f.TraceID {
		return false
	}
	if f.Type != "" && evt.Type != f.Type {
		return false
	}
	return true
}
