// LLM message construction and conversation transcript management.
//
// Converts incoming events and tool results into llm.Messages, and manages the
// [pending] placeholders in the transcript.
//
// Event → user message:   convertEvent / DefaultConverter
// Tool result → tool msg: resultMessageFromEvent / resultOutcome (event-driven)
// Park / unavailable tool: parkResultMessage / unavailableToolMessage (no-event driven)
// Late result:            appendLateResult (event + parked call)
// Placeholder:            insertPlaceholders / replacePlaceholder / updatePlaceholderToParked*
package reason

import (
	"encoding/json"
	"fmt"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
)

// isToolResultEvent reports whether the given event type is a tool
// lifecycle event consumed by the LLM worker.
func isToolResultEvent(typ event.EventType) bool {
	return typ == event.TypeToolCompleted || typ == event.TypeToolFailed || typ == event.TypeToolRejected
}

// convertEvent routes an event through the registered EventConverters.
// Matching uses the subscription's full semantics (type + optional source).
func (w *Worker) convertEvent(evt event.Event) []llm.Message {
	for _, h := range w.eventConverters {
		if h.Pattern.Matches(evt) {
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

// parkReason returns the explanatory text shown in the [pending] placeholder
// when a call is parked, describing why the reasoner stopped waiting on it.
func parkReason(cause PreemptCause) string {
	switch cause {
	case PreemptCauseTimeout:
		return "Tool call timed out; reasoner proceeded without waiting"
	case PreemptCauseInput:
		return "Tool call interrupted by new input; reasoner proceeded without waiting"
	case PreemptCauseAbort:
		return "Tool call aborted"
	case PreemptCauseReminder:
		return "Tool call interrupted by reminder; reasoner proceeded without waiting"
	default:
		return "Tool call parked; reasoner proceeded"
	}
}

// toolResultMessage builds a tool_result message for a tool call with the
// given outcome text and error flag. The outcome paths — event-driven
// (resultMessageFromEvent), parked (parkResultMessage), and undispatchable
// (unavailableToolMessage) — share this shape, so it is factored into one
// constructor to keep the message structure consistent.
func toolResultMessage(callID, name, text string, isError bool) llm.Message {
	return llm.Message{
		Role:       llm.RoleToolResult,
		ToolCallID: callID,
		ToolName:   name,
		IsError:    isError,
		Content:    []llm.ContentBlock{{Type: llm.ContentText, Text: text}},
	}
}

// parkResultMessage builds a tool_result message replacing the [pending]
// placeholder of a parked call. Parking has no result event, so it is driven
// by the call's ParkCause.
func parkResultMessage(rc *ToolCall) llm.Message {
	return toolResultMessage(rc.CallID, rc.Name, parkReason(rc.ParkCause), false)
}

// unavailableToolMessage builds a tool_result message for a tool call that
// could not be dispatched because its name resolved to no known tool at
// dispatch time. With a strict tool schema the model rarely invents a name, so
// the usual triggers are transient: a worker whose worker.ready hasn't been
// processed yet, a worker that just went away (worker.gone), or a name that
// didn't round-trip through sanitization. Unlike a dispatched tool that failed
// during execution, nothing was executed here — the message tells the LLM the
// tool is unavailable and it should pick a currently-resolvable one.
func unavailableToolMessage(callID, name string) llm.Message {
	return toolResultMessage(callID, name, "Unknown tool '"+name+"': not dispatched — tool not available.", true)
}

// resultOutcome extracts the human-readable outcome text and error flag from a
// tool result event. Used by both the normal-resolution and late-result paths.
func resultOutcome(evt event.Event) (string, bool) {
	switch evt.Type {
	case event.TypeToolCompleted:
		if r, ok := evt.Payload["result"]; ok {
			return fmt.Sprintf("%v", r), false
		}
	case event.TypeToolFailed:
		if e, ok := evt.Payload["error"]; ok {
			return "Tool call failed: " + fmt.Sprintf("%v", e), true
		}
	case event.TypeToolRejected:
		if r, ok := evt.Payload["reason"]; ok {
			return "Tool call rejected: " + fmt.Sprintf("%v", r), true
		}
	}
	return "", false
}

// resultMessageFromEvent builds the tool_result message for a Pending call
// resolved by a tool result event.
func resultMessageFromEvent(evt event.Event) llm.Message {
	callID, _ := evt.Payload["call_id"].(string)
	name, _ := evt.Payload["name"].(string)

	text, isError := resultOutcome(evt)
	return toolResultMessage(callID, name, text, isError)
}

// insertPlaceholders adds pending tool_result entries to messages for each bus
// tool call, preserving transcript ordering. These are replaced in place when
// results arrive (updatePlaceholderToParked*) or parked.
func (w *Worker) insertPlaceholders(calls []llm.ContentBlock) {
	for _, tc := range calls {
		w.messages = append(w.messages, llm.Message{
			Role:       llm.RoleToolResult,
			ToolCallID: tc.ToolCallID,
			ToolName:   tc.ToolName,
			Content:    []llm.ContentBlock{{Type: llm.ContentText, Text: "[pending]"}},
		})
	}
}

// replacePlaceholder replaces the tool_result placeholder for callID, if any.
func (w *Worker) replacePlaceholder(callID string, msg llm.Message) {
	for i := range w.messages {
		if w.messages[i].Role == llm.RoleToolResult &&
			w.messages[i].ToolCallID == callID {
			w.messages[i] = msg
			return
		}
	}
}

// updatePlaceholderToParked replaces the placeholder for a parked call.
func (w *Worker) updatePlaceholderToParked(rc *ToolCall) {
	w.replacePlaceholder(rc.CallID, parkResultMessage(rc))
}

// updatePlaceholderFromEvent replaces the placeholder for a call resolved by a
// tool result event.
func (w *Worker) updatePlaceholderFromEvent(evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	if callID == "" {
		return
	}
	w.replacePlaceholder(callID, resultMessageFromEvent(evt))
}

// appendLateResult appends a plain text user message for a late-arriving tool
// result on a parked call. Adding a RoleToolResult message would create a
// duplicate [tool] entry for the same call_id, which LLM APIs reject with:
//
//	"Messages with role 'tool' must be a response to a preceding message
//	 with 'tool_calls'"
//
// parked carries the cause so the message explains why the call was parked.
func (w *Worker) appendLateResult(parked *ToolCall, evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	name, _ := evt.Payload["name"].(string)
	if callID == "" || name == "" {
		return
	}

	label := "Late result for tool call"
	if parked != nil {
		switch parked.ParkCause {
		case PreemptCauseTimeout:
			label = "Timed-out tool call"
		case PreemptCauseInput:
			label = "Interrupted tool call"
		case PreemptCauseAbort:
			label = "Aborted tool call"
		case PreemptCauseReminder:
			label = "Interrupted tool call"
		}
	}

	if outcome, _ := resultOutcome(evt); outcome != "" {
		text := fmt.Sprintf("[%s %s (%s) returned late]: %s", label, callID, name, outcome)
		w.messages = append(w.messages, llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: text}},
		})
	}
}
