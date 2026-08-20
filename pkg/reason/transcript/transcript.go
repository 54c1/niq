// Transcript invariants: tool_call/tool_result pairing and the placeholder
// family. The reason worker translates its lifecycle into BuilderInput
// variants; the pairing mechanics live here so alternative transcript
// implementations can embed the same rules.
package transcript

import (
	"fmt"

	"github.com/54c1/niq/core/llm"
)

// parkReason returns the explanatory text shown in the [pending] placeholder
// when a call is parked, describing why the reasoner stopped waiting on it.
func parkReason(cause string) string {
	switch cause {
	case "timeout":
		return "Tool call timed out; reasoner proceeded without waiting"
	case "input":
		return "Tool call interrupted by new input; reasoner proceeded without waiting"
	case "abort":
		return "Tool call aborted"
	case "reminder":
		return "Tool call interrupted by reminder; reasoner proceeded without waiting"
	default:
		return "Tool call parked; reasoner proceeded"
	}
}

// toolResultMessage builds a tool_result message for a tool call with the
// given outcome text and error flag.
func toolResultMessage(callID, name, text string, isError bool) llm.Message {
	return llm.Message{
		Role:       llm.RoleToolResult,
		ToolCallID: callID,
		ToolName:   name,
		IsError:    isError,
		Content:    []llm.ContentBlock{{Type: llm.ContentText, Text: text}},
	}
}

// placeholderMessage builds the initial [pending] tool_result entry.
func placeholderMessage(call llm.ContentBlock) llm.Message {
	return llm.Message{
		Role:       llm.RoleToolResult,
		ToolCallID: call.ToolCallID,
		ToolName:   call.ToolName,
		Content:    []llm.ContentBlock{{Type: llm.ContentText, Text: "[pending]"}},
	}
}

// lateResultMessage appends a plain user message for a late-arriving tool
// result on a parked call. Adding a RoleToolResult message would create a
// duplicate [tool] entry for the same call_id, which LLM APIs reject.
func lateResultMessage(callID, name, text, cause string) llm.Message {
	label := "Late result for tool call"
	switch cause {
	case "timeout":
		label = "Timed-out tool call"
	case "input":
		label = "Interrupted tool call"
	case "abort":
		label = "Aborted tool call"
	case "reminder":
		label = "Interrupted tool call"
	}
	return llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: fmt.Sprintf("[%s %s (%s) returned late]: %s", label, callID, name, text)}},
	}
}

// replacePlaceholder replaces the tool_result placeholder for callID, if any.
// No-op when no placeholder matches.
func replacePlaceholder(msgs []llm.Message, callID string, msg llm.Message) []llm.Message {
	for i := range msgs {
		if msgs[i].Role == llm.RoleToolResult && msgs[i].ToolCallID == callID {
			msgs[i] = msg
			return msgs
		}
	}
	return msgs
}
