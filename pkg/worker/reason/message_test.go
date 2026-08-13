package reason

import (
	"testing"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
)

// TestDefaultConverterText verifies a payload with a "text" field produces a
// user message leading with that text.
func TestDefaultConverterText(t *testing.T) {
	evt := event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello"})
	msgs := DefaultConverter(evt)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Role != llm.RoleUser {
		t.Fatalf("role = %q, want user", m.Role)
	}
	if len(m.Content) != 1 || m.Content[0].Type != llm.ContentText {
		t.Fatalf("expected one text block, got %+v", m.Content)
	}
	if m.Content[0].Text != "hello\n\n[Event: worker.input from hiw]\n{\"text\":\"hello\"}" {
		t.Fatalf("unexpected text: %q", m.Content[0].Text)
	}
}

// TestDefaultConverterNoText verifies an event without a text field still
// becomes a user message with the event metadata.
func TestDefaultConverterNoText(t *testing.T) {
	evt := event.New(event.TypeWorkerReady, "workspace", map[string]any{"worker_id": "workspace"})
	msgs := DefaultConverter(evt)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Role != llm.RoleUser {
		t.Fatalf("role = %q, want user", msgs[0].Role)
	}
}

// TestResultOutcome verifies resultOutcome extracts text and the error flag
// from each tool-result event type.
func TestResultOutcome(t *testing.T) {
	cases := []struct {
		name    string
		evtType event.EventType
		payload map[string]any
		want    string
		isError bool
	}{
		{"completed", event.TypeToolCompleted, map[string]any{"result": "ok"}, "ok", false},
		{"failed", event.TypeToolFailed, map[string]any{"error": "boom"}, "Tool call failed: boom", true},
		{"rejected", event.TypeToolRejected, map[string]any{"reason": "no"}, "Tool call rejected: no", true},
		{"missing", event.TypeToolCompleted, map[string]any{}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evt := event.New(c.evtType, "workspace", c.payload)
			text, isErr := resultOutcome(evt)
			if text != c.want || isErr != c.isError {
				t.Fatalf("resultOutcome = (%q, %v), want (%q, %v)", text, isErr, c.want, c.isError)
			}
		})
	}
}

// TestResultMessageFromEvent verifies a tool-result event becomes a
// RoleToolResult message binding call_id/name and the extracted outcome.
func TestResultMessageFromEvent(t *testing.T) {
	evt := event.New(event.TypeToolCompleted, "workspace", map[string]any{
		"call_id": "c1", "name": "bash", "result": "0",
	})
	m := resultMessageFromEvent(evt)
	if m.Role != llm.RoleToolResult || m.ToolCallID != "c1" || m.ToolName != "bash" {
		t.Fatalf("bad message binding: %+v", m)
	}
	if m.IsError {
		t.Fatal("completed result should not be error")
	}
	if len(m.Content) != 1 || m.Content[0].Text != "0" {
		t.Fatalf("bad content: %+v", m.Content)
	}
}

// TestUnavailableToolMessage verifies an undispatchable tool produces a clear,
// non-executed error message.
func TestUnavailableToolMessage(t *testing.T) {
	m := unavailableToolMessage("c1", "ghost.tool")
	if m.Role != llm.RoleToolResult || m.ToolCallID != "c1" || m.ToolName != "ghost.tool" {
		t.Fatalf("bad message: %+v", m)
	}
	if !m.IsError {
		t.Fatal("unavailable tool must be an error")
	}
	if len(m.Content) == 0 {
		t.Fatal("missing content")
	}
}

// TestParkReason verifies each park cause maps to a distinct explanation.
func TestParkReason(t *testing.T) {
	cases := map[PreemptCause]string{
		PreemptCauseTimeout:  "Tool call timed out",
		PreemptCauseInput:    "Tool call interrupted by new input",
		PreemptCauseAbort:    "Tool call aborted",
		PreemptCauseReminder: "Tool call interrupted by reminder",
	}
	for cause, prefix := range cases {
		if got := parkReason(cause); len(got) == 0 || got[:len(prefix)] != prefix {
			t.Errorf("parkReason(%q) = %q, want prefix %q", cause, got, prefix)
		}
	}
}

// TestParkResultMessage verifies a parked call's placeholder is replaced with a
// non-error explanation.
func TestParkResultMessage(t *testing.T) {
	rc := &ToolCall{CallID: "c1", Name: "bash", Status: ToolParked, ParkCause: PreemptCauseTimeout}
	m := parkResultMessage(rc)
	if m.IsError {
		t.Fatal("parked message should not be an error")
	}
	if m.ToolCallID != "c1" || m.ToolName != "bash" {
		t.Fatalf("bad binding: %+v", m)
	}
}

// TestTypeMatches verifies the subscription matching semantics.
func TestTypeMatches(t *testing.T) {
	cases := []struct {
		pattern string
		typ     string
		want    bool
	}{
		{"*", "anything", true},
		{"tool.completed", "tool.completed", true},
		{"tool.completed", "tool.failed", false},
		{"github.*", "github.issue.new", true},
		{"github.*", "github", true},
		{"github.*", "gitlab.issue", false},
		{"", "anything", false}, // empty pattern matches nothing
	}
	for _, c := range cases {
		if got := typeMatches(event.EventType(c.pattern), event.EventType(c.typ)); got != c.want {
			t.Errorf("typeMatches(%q, %q) = %v, want %v", c.pattern, c.typ, got, c.want)
		}
	}
}
