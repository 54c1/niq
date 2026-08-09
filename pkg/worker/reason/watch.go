// watch.go — event loop and five-route dispatch.
//
//	watch: single goroutine, blocks on busCh, calls process + tryReason
//	process: routes events through abort/timer.elapsed/capability/tool_result/input
//	tryReason: decision gate — starts reason() when needReason && !isReasoning
package reason

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
)

// watch is the single event loop goroutine. It blocks on busCh waiting
// for events, calls process() to handle them, and then calls tryReason()
// which starts reasoning when needReason is set and no reasoning is running.
func (w *Worker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(ctx, evt)
		case <-ctx.Done():
			return
		}
		w.tryReason(ctx)
	}
}

// tryReason is the decision gate. It is called after every process() in the
// watch loop, and at the end of every reason(). If needReason is set and
// no reasoning is currently running, it triggers a new reasoning round.
func (w *Worker) tryReason(ctx context.Context) {
	w.mu.Lock()
	if !w.needReason || w.isReasoning {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	w.reason(ctx)
}

// process routes an event through one of the dispatch paths:
//   - abort: cancel current reasoning + pending tools
//   - timer.timeout: timeout still-pending tool calls
//   - timer.reminder: reminder timer fired, wake the LLM
//   - capability: update discovered tools without triggering reasoning
//   - tool result: update placeholder + check resolved
//   - input: convert to messages + set needReason
func (w *Worker) process(_ context.Context, evt event.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if evt.TargetWorkerID != "" && evt.TargetWorkerID != w.ID() {
		return
	}

	log.Printf("[reason %s] event: %s (from=%s)", w.ID(), evt.Type, evt.WorkerId)

	switch {
	case evt.Type == "worker.discover":
		if evt.WorkerId != w.ID() {
			w.publishReady()
		}
	case evt.Type == "worker.abort":
		w.handleAbort(evt)
	case evt.Type == "timer.timeout":
		w.handleTimeout(evt)
	case evt.Type == "timer.reminder":
		w.handleReminder(evt)
	case isCapabilityEvent(evt.Type):
		w.handleCapability(evt)
	case isToolResultEvent(evt.Type):
		w.handleToolResult(evt)
	case evt.Type == "tool.requested":
		w.handleToolRequest(evt)
	case evt.Type == "decision.made":
		w.handleDecisionMade(evt)
	default:
		w.handleInput(evt)
	}
}

// handleAbort cancels the current LLM call, marks pending tools as cancelled,
// and records the abort in the conversation transcript.
func (w *Worker) handleAbort(_ event.Event) {
	if w.cancelReason != nil {
		w.cancelReason()
		w.cancelReason = nil
	}
	tcs := w.callTracker.CancelAll()
	for _, rc := range tcs {
		w.updatePlaceholder(rc)
	}
	w.messages = append(w.messages, llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: fmt.Sprintf("[system] reasoning was aborted. %d tool call(s) cancelled.", len(tcs))}},
	})
	w.needReason = false
	w.publishReasonEnd("aborted")
}

// handleTimeout processes a timer.timeout event (from set_tool_timeout).
// Marks remaining pending tools as timed out and wakes the LLM.
// Stale timers (cancelled or prior batch) are silently discarded.
func (w *Worker) handleTimeout(evt event.Event) {
	timerID, _ := evt.Payload["timer_id"].(string)

	if epoch, ok := w.activeTickafters[timerID]; ok && epoch == w.reasonEpoch {
		tcs := w.callTracker.TimeoutPending()
		for _, tc := range tcs {
			w.updatePlaceholder(tc)
		}
		delete(w.activeTickafters, timerID)
		w.needReason = true
	}
}

// handleReminder processes a timer.reminder event (from elapse).
// Wakes the LLM — the event payload carries the purpose so the LLM
// knows what to do. Stale reminders from prior batches are discarded.
func (w *Worker) handleReminder(evt event.Event) {
	timerID, _ := evt.Payload["timer_id"].(string)

	if epoch, ok := w.elapseTickafters[timerID]; ok && epoch == w.reasonEpoch {
		delete(w.elapseTickafters, timerID)
		w.handleInput(evt)
	}
}

// handleToolResult processes a tool.completed/failed/rejected event.
// Normal path: update the existing placeholder with the result.
// Late path (timed-out/interrupted): append a plain text message via
// appendLateResult so the LLM learns about the late result without
// creating a duplicate [tool] entry for the same call_id.
func (w *Worker) handleToolResult(evt event.Event) {
	rc, shouldUpdate := w.callTracker.handleResponse(evt)
	if rc != nil && shouldUpdate {
		w.updatePlaceholder(rc)
		if w.callTracker.Resolved() {
			w.cancelActiveTickers()
			w.needReason = true
		}
	} else if _, ok := evt.Payload["call_id"]; ok {
		w.appendLateResult(evt)
	}
}

// handleInput processes an input event. The input_mode field in the payload
// controls how the event interacts with pending tool calls:
//
//	"append"    — add to messages, do NOT set needReason (wait for tools to complete)
//	"default"   — add to messages, set needReason (trigger LLM now, keep pending tools)
//	"interrupt" — add to messages, interrupt pending tools, set needReason
func (w *Worker) handleInput(evt event.Event) {
	// Capture the trace_id from the incoming event so that all events
	// published during this reasoning round propagate the same trace_id.
	if evt.TraceID != "" {
		w.currentTraceID = evt.TraceID
	}

	msgs := w.convertEvent(evt)
	w.messages = append(w.messages, msgs...)

	mode, _ := evt.Payload["input_mode"].(string)
	switch mode {
	case "interrupt":
		// Cancel the current LLM call if running.
		if w.cancelReason != nil {
			w.cancelReason()
			w.cancelReason = nil
		}
		if !w.callTracker.Resolved() {
			tcs := w.callTracker.InterruptPending()
			for _, rc := range tcs {
				w.updatePlaceholder(rc)
			}
			w.cancelActiveTickers()
		}
		w.needReason = true
	case "append":
		// Don't set needReason — let pending tools complete first.
	default:
		// "default" or unset: trigger LLM now, keep pending tools.
		w.needReason = true
	}
}

// typeMatches reports whether an event type matches a subscription pattern.
// Supports exact match, "*" (any), and "Prefix.*" prefix wildcards.
func typeMatches(pattern, eventType string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if pattern == eventType {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, ".*"); ok {
		return eventType == prefix || strings.HasPrefix(eventType, prefix+".")
	}
	return false
}

// convertEvent routes an event through the registered EventConverters.
func (w *Worker) convertEvent(evt event.Event) []llm.Message {
	for _, h := range w.eventConverter {
		if typeMatches(h.Pattern.Type, evt.Type) && (h.Pattern.SourceID == "" || h.Pattern.SourceID == evt.WorkerId) {
			return h.Converter(evt)
		}
	}
	return DefaultConverter(evt)
}

// DefaultConverter formats an event as a plain-text user message.
// Convention: if the payload contains a "text" field, it is used as the
// primary content (first line), followed by the event metadata and full
// payload JSON. This lets event producers provide a human-readable summary
// while preserving the full structured data for the LLM.
func DefaultConverter(evt event.Event) []llm.Message {
	payloadStr := "{}"
	if evt.Payload != nil {
		b, err := json.Marshal(evt.Payload)
		if err == nil {
			payloadStr = string(b)
		}
	}

	text, _ := evt.Payload["text"].(string)
	if text != "" {
		return []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{{
				Type: llm.ContentText,
				Text: fmt.Sprintf("%s\n\n[Event: %s from %s]\n%s", text, evt.Type, evt.WorkerId, payloadStr),
			}},
		}}
	}

	return []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{
			Type: llm.ContentText,
			Text: fmt.Sprintf("[Event: %s from %s]\n%s", evt.Type, evt.WorkerId, payloadStr),
		}},
	}}
}
