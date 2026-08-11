// event loop and dispatch.
//
// watch: single goroutine, blocks on busCh, calls process + tryReason
// process: routes events through abort/timer/worker-presence/tool_result/input
// tryReason: decision gate — spawns reason() on its own goroutine when
//
//	needReason && !isReasoning, keeping the event loop responsive.
package reason

import (
	"context"
	"fmt"
	"log"

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
// watch loop, and at the end of every reason(). If needReason is set and no
// reasoning is running, it spawns a reasoning round on its own goroutine so
// the watch event loop stays responsive while the LLM call is in flight.
func (w *Worker) tryReason(ctx context.Context) {
	w.mu.Lock()
	if !w.needReason || w.isReasoning {
		w.mu.Unlock()
		return
	}
	w.isReasoning = true
	w.needReason = false
	w.mu.Unlock()

	go w.reason(ctx)
}

// process routes an event through one of the dispatch paths:
//   - abort: park pending tools + recall + end session
//   - timer.timeout: park still-pending tools (cause=timeout)
//   - timer.reminder: reminder timer fired, wake the LLM
//   - worker.ready/gone: learn/forget a worker's tools & events
//   - tool result: resolve / park-late / update placeholder
//   - input: convert to messages + park + set needReason
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
	case evt.Type == "worker.ready":
		w.handleWorkerReady(evt)
	case evt.Type == "worker.gone":
		w.handleWorkerGone(evt)
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

// handleAbort cancels the current LLM call, parks all pending tools (so late
// results can still be contextualized), best-effort recalls them, and records
// the abort in the conversation transcript. The session ends (needReason=false).
func (w *Worker) handleAbort(_ event.Event) {
	w.interruptReason = InterruptCauseAbort
	if w.cancelReason != nil {
		w.cancelReason()
		w.cancelReason = nil
	}
	tcs := w.callTracker.parkAll(InterruptCauseAbort)
	for _, rc := range tcs {
		w.updatePlaceholderToParked(rc)
	}
	w.recallCalls(tcs)
	w.cancelTimeout()
	w.messages = append(w.messages, llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: fmt.Sprintf("[system] reasoning was aborted. %d tool call(s) parked.", len(tcs))}},
	})
	w.needReason = false
	w.publishReasonEnd(w.currentTraceID, "aborted")
}

// recallCalls best-effort cancels in-flight tool calls by sending a directed
// tool.requested name="cancel" to each call's target worker, mirroring the
// timer worker's cancel shape. Cancellation is not guaranteed to succeed.
func (w *Worker) recallCalls(tcs []*ToolCall) {
	byTarget := make(map[string][]string)
	for _, rc := range tcs {
		if rc.TargetID != "" {
			byTarget[rc.TargetID] = append(byTarget[rc.TargetID], rc.CallID)
		}
	}
	for target, callIDs := range byTarget {
		for _, callID := range callIDs {
			evt := event.New("tool.requested", w.ID(), map[string]any{
				"call_id":   callID + "-cancel",
				"name":      "cancel",
				"arguments": map[string]any{"call_id": callID},
			})
			evt.TraceID = w.currentTraceID
			_ = w.Channel.Send(context.Background(), evt, target)
		}
	}
}

// handleTimeout processes a timer.timeout event (from set_tool_timeout).
// Parks all still-pending tool calls with cause=timeout and wakes the LLM.
// Stale timers (cancelled or prior batch) are silently discarded.
func (w *Worker) handleTimeout(evt event.Event) {
	timerID, _ := evt.Payload["timer_id"].(string)

	// Only the current round's timer is meaningful; an orphaned timer from a
	// prior round (whose call_id no longer matches) is silently discarded.
	if w.activeTimeout != "" && timerID == w.activeTimeout {
		tcs := w.callTracker.parkAll(InterruptCauseTimeout)
		for _, tc := range tcs {
			w.updatePlaceholderToParked(tc)
		}
		w.activeTimeout = ""
		w.needReason = true
	}
}

// handleReminder processes a timer.reminder event (from elapse). It wakes the
// LLM — the event payload carries the purpose so the LLM knows what to do. A
// reminder is a gentle wake-up: it schedules fresh reasoning without cancelling
// any in-flight reasoning call (unlike a user input).
func (w *Worker) handleReminder(evt event.Event) {
	if evt.TraceID != "" {
		w.currentTraceID = evt.TraceID
	}
	msgs := w.convertEvent(evt)
	w.messages = append(w.messages, msgs...)
	w.setImmediateReasoning(InterruptCauseReminder)
}

// handleToolResult processes a tool.completed/failed/rejected event.
// Normal path: resolve a Pending call and update its placeholder.
// Late path: match a Parked call and append a contextualized message.
// Untracked call_ids (e.g. synthetic cancel responses) are ignored.
func (w *Worker) handleToolResult(evt event.Event) {
	// Normal resolution of a Pending call.
	if w.callTracker.handleResponse(evt) {
		w.updatePlaceholderFromEvent(evt)
		if w.callTracker.Resolved() {
			w.cancelTimeout()
			w.needReason = true
		}
		return
	}
	// Late result for a Parked call.
	if parked := w.callTracker.resolveLate(evt); parked != nil {
		w.appendLateResult(parked, evt)
	}
}

// handleInput processes an input event. The input_mode field in the payload
// controls how the event interacts with pending tool calls:
//
//	"default" — respond to the user immediately: interrupt the in-flight
//	             reasoning call, park any pending tools (cause=input) so they
//	             don't mix with the new round, and trigger reasoning.
//	"append"  — leave a message only: do not set needReason; the current task
//	             finishes first and the next round notices it.
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
	case "append":
		// Don't set needReason — let pending tools complete first.
	default:
		// "default" or unset: respond to the user immediately. Interrupt any
		// in-flight reasoning call, then schedule a fresh round.
		w.interruptReason = InterruptCauseInput
		if w.cancelReason != nil {
			w.cancelReason()
			w.cancelReason = nil
		}
		w.setImmediateReasoning(InterruptCauseInput)
	}
}

// handleDecisionMade converts a human decision result into a user message and
// schedules fresh reasoning. Like user input, a decision needs a response.
func (w *Worker) handleDecisionMade(evt event.Event) {
	decision, _ := evt.Payload["decision"].(string)
	reasoning, _ := evt.Payload["reasoning"].(string)
	summary, _ := evt.Payload["request_summary"].(string)
	if decision == "" {
		return
	}
	text := fmt.Sprintf("[Human decision on \"%s\"]\nDecision: %s", summary, decision)
	if reasoning != "" {
		text += "\nReasoning: " + reasoning
	}
	w.messages = append(w.messages, llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: text}},
	})
	w.setImmediateReasoning(InterruptCauseInput)
	log.Printf("[reason %s] received human decision on \"%s\": %s", w.ID(), summary, decision)
}

// setImmediateReasoning sets the expectation that a fresh reasoning round should
// run at the next event loop tick, parking any in-flight (Pending) tool calls
// with the given cause first. It mirrors Node's setImmediate — we set the flag
// (needReason) and wait for tryReason to consume it, we do not execute the
// reasoning ourselves. Used by user input (InterruptCauseInput) and reminders (InterruptCauseReminder).
// The parking keeps pending tools from mixing with the new round's tracking.
func (w *Worker) setImmediateReasoning(cause InterruptCause) {
	tcs := w.callTracker.parkAll(cause)
	for _, rc := range tcs {
		w.updatePlaceholderToParked(rc)
	}
	w.cancelTimeout()
	w.needReason = true
}

// cancelTimeout cancels the current round's active timeout timer by
// sending timer.cancel to the timer worker. Called when all tools have resolved
// — the timeout is no longer needed. The request is targeted (not broadcast) to
// avoid confusing other workers that subscribe to tool.requested.
func (w *Worker) cancelTimeout() {
	if w.activeTimeout == "" {
		return
	}
	// Look up the timer worker ID from the set_tool_timeout tool definition.
	targetID := ""
	if t, ok := w.tools["timer.set_tool_timeout"]; ok {
		targetID = t.Provider
	}
	if targetID == "" {
		// Fallback: the timer worker is always named "timer" in the default
		// configuration, but prefer the dynamic lookup above.
		targetID = "timer"
	}
	timerID := w.activeTimeout
	evt := event.New("tool.requested", w.ID(), map[string]any{
		"call_id":   timerID + "-cancel",
		"name":      "cancel",
		"arguments": map[string]any{"timer_id": timerID},
	})
	_ = w.Channel.Send(context.Background(), evt, targetID)
	w.activeTimeout = ""
}
