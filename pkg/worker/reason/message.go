// LLM message construction and conversation transcript management.
//
// Converts incoming events and tool results into llm.Messages, and manages the
// [pending] placeholders in the transcript.
//
// Event → user message:   convertEvent / DefaultConverter
// Tool result → tool msg: resultMessageFromEvent / resultOutcome (event-driven)
// Park / unknown tool:    parkResultMessage / failMessage (no-event driven)
// Late result:            appendLateResult (event + parked call)
// Placeholder:            insertPlaceholders / replacePlaceholder / updatePlaceholderToParked*
package reason

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
)

// isToolResultEvent reports whether the given event type is a tool
// lifecycle event consumed by the LLM worker.
func isToolResultEvent(typ string) bool {
	return typ == "tool.completed" || typ == "tool.failed" || typ == "tool.rejected"
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
		if typeMatches(h.Pattern.Type, evt.Type) {
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
func parkReason(cause InterruptCause) string {
	switch cause {
	case InterruptCauseTimeout:
		return "Tool call timed out; reasoner proceeded without waiting"
	case InterruptCauseInput:
		return "Tool call interrupted by new input; reasoner proceeded without waiting"
	case InterruptCauseAbort:
		return "Tool call aborted"
	case InterruptCauseReminder:
		return "Tool call interrupted by reminder; reasoner proceeded without waiting"
	default:
		return "Tool call parked; reasoner proceeded"
	}
}

// parkResultMessage builds a tool_result message replacing the [pending]
// placeholder of a parked call. Parking has no result event, so it is driven
// by the call's ParkCause.
func parkResultMessage(rc *ToolCall) llm.Message {
	return llm.Message{
		Role:       llm.RoleToolResult,
		ToolCallID: rc.CallID,
		ToolName:   rc.Name,
		Content:    []llm.ContentBlock{{Type: llm.ContentText, Text: parkReason(rc.ParkCause)}},
	}
}

// failMessage builds a tool_result message for a call that failed without a
// result event (e.g. an unknown / hallucinated tool).
func failMessage(callID, name, reason string) llm.Message {
	return llm.Message{
		Role:       llm.RoleToolResult,
		ToolCallID: callID,
		ToolName:   name,
		IsError:    true,
		Content:    []llm.ContentBlock{{Type: llm.ContentText, Text: "Tool call failed: " + reason}},
	}
}

// resultOutcome extracts the human-readable outcome text and error flag from a
// tool result event. Used by both the normal-resolution and late-result paths.
func resultOutcome(evt event.Event) (string, bool) {
	switch evt.Type {
	case "tool.completed":
		if r, ok := evt.Payload["result"]; ok {
			return fmt.Sprintf("%v", r), false
		}
	case "tool.failed":
		if e, ok := evt.Payload["error"]; ok {
			return "Tool call failed: " + fmt.Sprintf("%v", e), true
		}
	case "tool.rejected":
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
	return llm.Message{
		Role:       llm.RoleToolResult,
		ToolCallID: callID,
		ToolName:   name,
		IsError:    isError,
		Content:    []llm.ContentBlock{{Type: llm.ContentText, Text: text}},
	}
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
		case InterruptCauseTimeout:
			label = "Timed-out tool call"
		case InterruptCauseInput:
			label = "Interrupted tool call"
		case InterruptCauseAbort:
			label = "Aborted tool call"
		case InterruptCauseReminder:
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
