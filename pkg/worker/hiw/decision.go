package hiw

import (
	"log"
	"strings"

	"github.com/54c1/niq/core/event"
)

// DecisionRequest represents a pending decision request from a worker.
type DecisionRequest struct {
	ID        string   `json:"request_id"`
	CallID    string   `json:"call_id"` // original tool call ID for updating placeholder
	WorkerID  string   `json:"worker_id"`
	Summary   string   `json:"summary"`
	Context   string   `json:"context"`
	Options   []Option `json:"options"`
	TraceID   string   `json:"trace_id"`
	Status    string   `json:"status"` // "pending" | "decided" | "expired"
	CreatedAt int64    `json:"created_at"`
	Decision  string   `json:"decision,omitempty"`
	Reasoning string   `json:"reasoning,omitempty"`
}

// Option represents a choice in a decision request.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func (w *Worker) handleDecision(evt event.Event) {
	if strings.HasPrefix(evt.Type, "decision.requested") {
		req := &DecisionRequest{
			ID:        getStr(evt.Payload, "request_id"),
			WorkerID:  evt.WorkerId,
			Summary:   getStr(evt.Payload, "summary"),
			Context:   getStr(evt.Payload, "context"),
			TraceID:   getStr(evt.Payload, "trace_id"),
			Status:    "pending",
			CreatedAt: evt.Timestamp,
		}
		if optsRaw, ok := evt.Payload["options"].([]any); ok {
			for _, o := range optsRaw {
				if m, ok := o.(map[string]any); ok {
					req.Options = append(req.Options, Option{
						ID:    getStr(m, "id"),
						Label: getStr(m, "label"),
					})
				}
			}
		}
		if req.ID != "" {
			w.pendingDecisions[req.ID] = req
			log.Printf("[hiw] decision requested: %s from %s", req.ID, evt.WorkerId)
		}
	}

	if strings.HasPrefix(evt.Type, "decision.made") {
		reqID := getStr(evt.Payload, "request_id")
		if reqID != "" {
			delete(w.pendingDecisions, reqID)
			log.Printf("[hiw] decision made: %s", reqID)
		}
	}
}
