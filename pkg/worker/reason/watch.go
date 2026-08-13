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
	defer w.mu.Unlock()

	if !w.needReason || w.isReasoning {
		return
	}

	w.isReasoning = true
	w.needReason = false

	go w.reason(ctx)
}

// process routes an event through one of the dispatch paths:
//   - abort: park pending tools + recall, clear needReason
//   - timer.timeout: record system message + schedule (level 2)
//   - timer.reminder: convert to messages + schedule (level 2)
//   - worker.ready/gone: learn/forget a worker's tools & events
//   - tool result: resolve / park-late / update placeholder
//   - input: convert to messages + schedule (level 1/2/3 via input_mode)
//
// Three levels of input handling:
//
//	level 1 (append):  append message, schedule if idle, no park
//	level 2 (reminder/timeout): append message, schedule, park on next round
//	level 3 (default): interrupt, schedule, park on next round
func (w *Worker) process(_ context.Context, evt event.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()

	log.Printf("[reason %s] event: %s (from=%s)", w.ID(), evt.Type, evt.WorkerId)

	switch {
	case evt.Type == "worker.discover":
		if evt.WorkerId != w.ID() {
			w.broadcastReady()
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
// the abort in the conversation transcript. needReason is cleared so no new
// reasoning round starts until the next worker.input.
func (w *Worker) handleAbort(_ event.Event) {
	w.interruptReason = PreemptCauseAbort

	// Broadcast reason end
	hadReasoning := w.cancelReason != nil
	if !hadReasoning {
		// No reasoning round was in flight to publish the interrupt lifecycle.
		w.broadcastReasonEnd(w.currentTraceID, StopReasonAborted)
	}

	// Cancel current running reason
	if w.cancelReason != nil {
		w.cancelReason()
		w.cancelReason = nil
	}

	// Park and recall tool calls
	tcs := w.parkPending(PreemptCauseAbort)
	w.recallToolCalls(tcs)

	// Record the abort in the conversation transcript so the LLM
	// knows what happened when the next round starts.
	w.messages = append(w.messages, llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: fmt.Sprintf("[system] reasoning was aborted. %d tool call(s) parked.", len(tcs))}},
	})

	w.needReason = false
}

// recallToolCalls best-effort cancels in-flight tool calls by sending a
// tool.cancel event to each call's target worker.
func (w *Worker) recallToolCalls(tcs []*ToolCall) {
	byTarget := make(map[string][]string)
	for _, rc := range tcs {
		if rc.TargetID != "" {
			byTarget[rc.TargetID] = append(byTarget[rc.TargetID], rc.CallID)
		}
	}

	for target, callIDs := range byTarget {
		for _, callID := range callIDs {
			evt := event.New("tool.cancel", w.ID(), map[string]any{
				"call_id": callID,
			})
			evt.TraceID = w.currentTraceID
			_ = w.Channel.Send(context.Background(), evt, target)
		}
	}
}

// handleTimeout processes a timer.timeout event (from set_tool_timeout).
// Records a system message and schedules a fresh reasoning round.
// Stale timers (cancelled or prior batch) are silently discarded.
func (w *Worker) handleTimeout(evt event.Event) {
	timerID, _ := evt.Payload["timer_id"].(string)

	// Only the current round's timer is meaningful; an orphaned timer from a
	// prior round (whose call_id no longer matches) is silently discarded.
	if w.activeTimeout != "" && timerID == w.activeTimeout {
		msg := llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "[system] tool call timeout"}},
		}
		w.scheduleInput([]llm.Message{msg}, PreemptCauseTimeout)
	}
}

// handleReminder processes a timer.reminder event (from elapse). It wakes the
// LLM — the event payload carries the purpose so the LLM knows what to do. A
// reminder is a gentle wake-up: it schedules fresh reasoning without cancelling
// any in-flight reasoning call.
func (w *Worker) handleReminder(evt event.Event) {
	if evt.TraceID != "" {
		w.currentTraceID = evt.TraceID
	}
	msgs := w.convertEvent(evt)
	w.scheduleInput(msgs, PreemptCauseReminder)
}

// handleToolResult processes a tool.completed/failed/rejected event.
// Normal path: resolve a Pending call and update its placeholder.
// Late path: match a Parked call and append a contextualized message.
// Untracked call_ids (e.g. synthetic cancel responses) are ignored.
func (w *Worker) handleToolResult(evt event.Event) {
	// Normal resolution of a Pending call.
	if w.toolCallTracker.handleResponse(evt) {
		w.updatePlaceholderFromEvent(evt)
		if w.toolCallTracker.Resolved() {
			w.cancelTimeout()
			w.needReason = true
		}
		return
	}

	// Late result for a Parked call.
	if parked := w.toolCallTracker.resolveLate(evt); parked != nil {
		w.appendLateResult(parked, evt)
	}
}

// handleInput processes an input event. The input_mode field in the payload
// controls how the event interacts with pending tool calls:
//
//	"default" — level 3: interrupt the in-flight reasoning call and schedule
//	             a fresh round. The pending tools are parked when reason()
//	             starts (using the stored cause).
//	"append"  — level 1: append the message and schedule a new round, but only
//	             when the system is idle (no in-flight reasoning, no pending
//	             tool calls). Does not interrupt or park anything.
func (w *Worker) handleInput(evt event.Event) {
	// Capture the trace_id from the incoming event so that all events
	// published during this reasoning round propagate the same trace_id.
	if evt.TraceID != "" {
		w.currentTraceID = evt.TraceID
	}

	msgs := w.convertEvent(evt)

	mode, _ := evt.Payload["input_mode"].(string)
	switch mode {
	case "append":
		w.appendInput(msgs)
	default:
		w.interruptInput(msgs, PreemptCauseInput)
	}
}

// appendInput appends messages and schedules a new round only when the system
// is idle — no in-flight reasoning and no pending tool calls. Does not
// interrupt or park anything. This is the least intrusive input mode (level 1).
func (w *Worker) appendInput(msgs []llm.Message) {
	w.messages = append(w.messages, msgs...)
	if !w.isReasoning && w.toolCallTracker.Resolved() {
		w.needReason = true
	}
}

// scheduleInput appends messages, records the cause, and schedules a fresh
// reasoning round. The pending tools are parked when reason() starts, not
// here. This is the moderate input mode (level 2) — it does not interrupt
// an in-flight reasoning call, but ensures the next round responds promptly.
func (w *Worker) scheduleInput(msgs []llm.Message, cause PreemptCause) {
	w.messages = append(w.messages, msgs...)
	w.immediateReasoningCause = cause
	w.needReason = true
}

// interruptInput cancels the in-flight reasoning call, records the cause,
// and schedules a fresh round. The pending tools are parked when reason()
// starts. This is the strongest input mode (level 3) — it interrupts the
// current LLM call so the new input is handled immediately.
func (w *Worker) interruptInput(msgs []llm.Message, cause PreemptCause) {
	w.messages = append(w.messages, msgs...)
	w.interruptReason = cause
	if w.cancelReason != nil {
		w.cancelReason()
		w.cancelReason = nil
	}
	w.immediateReasoningCause = cause
	w.needReason = true
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
	msg := llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: text}},
	}
	w.scheduleInput([]llm.Message{msg}, PreemptCauseInput)
	log.Printf("[reason %s] received human decision on \"%s\": %s", w.ID(), summary, decision)
}

// parkPending parks all pending tool calls with the given cause and cancels the
// current round's timeout timer. Returns the parked calls for callers that need
// to recall them (e.g., handleAbort).
func (w *Worker) parkPending(cause PreemptCause) []*ToolCall {
	tcs := w.toolCallTracker.parkAll(cause)
	for _, rc := range tcs {
		w.updatePlaceholderToParked(rc)
	}
	w.cancelTimeout()
	return tcs
}

// cancelTimeout cancels the current round's active timeout timer by
// sending a tool.cancel event to the timer worker. Called when all tools have resolved
// — the timeout is no longer needed.
func (w *Worker) cancelTimeout() {
	if w.activeTimeout == "" {
		return
	}
	// Look up the timer worker ID from the set_tool_timeout tool definition.
	targetID := ""
	if t, ok := w.workerTools["timer.set_tool_timeout"]; ok {
		targetID = t.Provider
	}
	if targetID == "" {
		// Fallback: the timer worker is always named "timer" in the default
		// configuration, but prefer the dynamic lookup above.
		targetID = "timer"
	}
	timerID := w.activeTimeout
	evt := event.New("tool.cancel", w.ID(), map[string]any{
		"call_id":  timerID + "-cancel",
		"timer_id": timerID,
	})
	_ = w.Channel.Send(context.Background(), evt, targetID)
	w.activeTimeout = ""
}
