// Event-to-message translation and transcript input translation.
//
// The reason worker's side of the transcript boundary: events and internal
// lifecycle state are translated here into data-only transcript inputs. The
// placeholder/pairing mechanics themselves live in pkg/reason/transcript.
//
// Event -> user message:   convertEvent / DefaultConverter
// Tool result event -> input: resultMessageInput (event-driven)
// Park / late result:      translated in watch.go handlers via ToolParked/LateResult
package reason

import (
	"encoding/json"
	"fmt"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/pkg/reason/transcript"
)

// isToolResultEvent reports whether the given event type is a tool
// lifecycle event consumed by the LLM worker.
func isToolResultEvent(typ event.EventType) bool {
	return typ == event.TypeToolCompleted || typ == event.TypeToolFailed || typ == event.TypeToolRejected
}

// convertEvent routes an event through the registered EventConverters.
// Matching uses the subscription's full semantics (type + optional source).
func (w *BaseReasonWorker) convertEvent(evt event.Event) []llm.Message {
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

// updatePlaceholderFromEvent translates a tool result event into a
// ToolResult input and applies it, replacing the [pending] placeholder.
func (w *BaseReasonWorker) updatePlaceholderFromEvent(evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	name, _ := evt.Payload["name"].(string)
	if callID == "" {
		return
	}
	text, isErr := resultOutcome(evt)
	w.transcript.Apply(transcript.ToolResult{CallID: callID, Name: name, Text: text, IsErr: isErr})
}

// appendLateResult translates a late-arriving result on a parked call into a
// LateResult input and applies it (appended as a user message - a second
// tool_result for the same call_id would violate the pairing invariant).
// parked carries the cause so the message explains why the call was parked.
func (w *BaseReasonWorker) appendLateResult(parked *ToolCall, evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	name, _ := evt.Payload["name"].(string)
	if callID == "" || name == "" {
		return
	}

	cause := ""
	if parked != nil {
		cause = string(parked.ParkCause)
	}

	if text, _ := resultOutcome(evt); text != "" {
		w.transcript.Apply(transcript.LateResult{CallID: callID, Name: name, Text: text, Cause: cause})
	}
}
