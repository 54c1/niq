// result.go — tool result → LLM message conversion and placeholder management.
//
//	Normal path: toolResultToMessage + updatePlaceholder (overwrite in place)
//	Late path:  lateResultToMessage + appendLateResult (append, don't overwrite)
package reason

import (
	"fmt"
	"log"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
)

// isToolResultEvent reports whether the given event type is a tool
// lifecycle event consumed by the LLM worker.
func isToolResultEvent(typ string) bool {
	return typ == "tool.completed" || typ == "tool.failed" || typ == "tool.rejected"
}

// lateResultToMessage converts a late-arriving tool result event into an
// llm.Message. Unlike toolResultToMessage, it reads directly from the event
// payload since the call is no longer tracked in pending.
func lateResultToMessage(evt event.Event) *llm.Message {
	callID, _ := evt.Payload["call_id"].(string)
	name, _ := evt.Payload["name"].(string)
	if callID == "" || name == "" {
		return nil
	}

	msg := llm.Message{
		Role:       llm.RoleToolResult,
		ToolCallID: callID,
		ToolName:   name,
	}

	switch evt.Type {
	case "tool.completed":
		if result, ok := evt.Payload["result"]; ok {
			msg.Content = []llm.ContentBlock{{Type: llm.ContentText, Text: fmt.Sprintf("%v", result)}}
		}
	case "tool.failed":
		msg.IsError = true
		if errMsg, ok := evt.Payload["error"].(string); ok {
			msg.Content = []llm.ContentBlock{{Type: llm.ContentText, Text: fmt.Sprintf("Tool call failed: %s", errMsg)}}
		}
	case "tool.rejected":
		msg.IsError = true
		if reason, ok := evt.Payload["reason"].(string); ok {
			msg.Content = []llm.ContentBlock{{Type: llm.ContentText, Text: fmt.Sprintf("Tool call rejected: %s", reason)}}
		}
	default:
		return nil
	}

	return &msg
}

// toolResultToMessage converts a resolved tool call into a tool_result
// message for the LLM conversation transcript.
func toolResultToMessage(rc *ToolCall) llm.Message {
	msg := llm.Message{
		Role:       llm.RoleToolResult,
		ToolCallID: rc.CallID,
		ToolName:   rc.Name,
		IsError:    rc.IsError(),
	}

	switch {
	case rc.TimedOut():
		msg.Content = []llm.ContentBlock{{
			Type: llm.ContentText,
			Text: fmt.Sprintf("Tool call timed out: %s", rc.ErrorMsg),
		}}
	case rc.Interrupted():
		msg.Content = []llm.ContentBlock{{
			Type: llm.ContentText,
			Text: fmt.Sprintf("Tool call interrupted: %s", rc.ErrorMsg),
		}}
	case rc.Rejected():
		msg.Content = []llm.ContentBlock{{
			Type: llm.ContentText,
			Text: fmt.Sprintf("Tool call rejected: %s", rc.ErrorMsg),
		}}
	case rc.IsError():
		msg.Content = []llm.ContentBlock{{
			Type: llm.ContentText,
			Text: fmt.Sprintf("Tool call failed: %s", rc.ErrorMsg),
		}}
	default:
		msg.Content = []llm.ContentBlock{{
			Type: llm.ContentText,
			Text: rc.Result,
		}}
	}

	return msg
}

// updatePlaceholder finds a tool_result placeholder in messages by call_id
// and replaces it with the resolved result.
func (w *Worker) updatePlaceholder(rc *ToolCall) {
	for i := range w.messages {
		if w.messages[i].Role == llm.RoleToolResult &&
			w.messages[i].ToolCallID == rc.CallID {
			w.messages[i] = toolResultToMessage(rc)
			return
		}
	}
}

// appendLateResult appends a plain text message (role=user) for a
// late-arriving tool result whose placeholder was already resolved
// (timed-out, interrupted, or previously failed). It adds a RoleToolResult
// message would create a duplicate [tool] entry for the same call_id,
// which LLM APIs reject with:
//
//	"Messages with role 'tool' must be a response to a preceding message
//	 with 'tool_calls'"
//
// handleDecisionMade converts a human decision result into a user message
// that the LLM can understand clearly.
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
	w.needReason = true
	log.Printf("[reason %s] received human decision on \"%s\": %s", w.ID(), summary, decision)
}

func (w *Worker) appendLateResult(evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	name, _ := evt.Payload["name"].(string)
	if callID == "" || name == "" {
		return
	}

	var text string
	switch evt.Type {
	case "tool.completed":
		if result, ok := evt.Payload["result"]; ok {
			text = fmt.Sprintf("[Late result for tool call %s (%s)]: %v", callID, name, result)
		}
	case "tool.failed":
		if errMsg, ok := evt.Payload["error"].(string); ok {
			text = fmt.Sprintf("[Late result for tool call %s (%s)]: failed: %s", callID, name, errMsg)
		}
	case "tool.rejected":
		if reason, ok := evt.Payload["reason"].(string); ok {
			text = fmt.Sprintf("[Late result for tool call %s (%s)]: rejected: %s", callID, name, reason)
		}
	}

	if text != "" {
		w.messages = append(w.messages, llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: text}},
		})
	}
}
