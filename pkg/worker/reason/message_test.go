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

// Placeholder-family behavior (resultMessage/parkReason/unavailable/
// late-result, previously tested here) now lives in the builder package:
// see builder/builder_test.go.

// TestEventPatternMatches verifies the subscription matching semantics,
// including type wildcards and optional source filtering.
func TestEventPatternMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern event.EventPattern
		typ     string
		source  string
		want    bool
	}{
		{"wildcard", event.NewPattern("*"), "anything", "", true},
		{"exact", event.NewPattern("tool.completed"), "tool.completed", "", true},
		{"exact-miss", event.NewPattern("tool.completed"), "tool.failed", "", false},
		{"prefix", event.NewPattern("github.*"), "github.issue.new", "", true},
		{"prefix-bare", event.NewPattern("github.*"), "github", "", true},
		{"prefix-miss", event.NewPattern("github.*"), "gitlab.issue", "", false},
		{"empty-miss", event.EventPattern{}, "anything", "", false}, // empty pattern matches nothing
		{"source-match", event.EventPattern{Type: "pr.ready", SourceID: "gh"}, "pr.ready", "gh", true},
		{"source-miss", event.EventPattern{Type: "pr.ready", SourceID: "gh"}, "pr.ready", "gitlab", false},
		{"source-wildcard", event.EventPattern{Type: "*", SourceID: "gh"}, "pr.ready", "gh", true},
	}
	for _, c := range cases {
		evt := event.Event{Type: event.EventType(c.typ), WorkerId: c.source}
		if got := c.pattern.Matches(evt); got != c.want {
			t.Errorf("%s: Match() = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestEventPatternSourceFilter verifies source-filtered matching end to end
// through convertEvent.
func TestEventPatternSourceFilter(t *testing.T) {
	var converted bool
	marker := func(evt event.Event) []llm.Message {
		converted = true
		return []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "marker"}}}}
	}
	w := &Worker{
		eventConverters: []EventConverter{
			{Pattern: event.EventPattern{Type: "pr.ready", SourceID: "gh"}, Converter: marker},
		},
	}

	// Matching source: marker converter selected.
	w.convertEvent(event.Event{Type: "pr.ready", WorkerId: "gh"})
	if !converted {
		t.Errorf("source match: expected marker converter to run")
	}

	// Non-matching source: marker converter NOT selected (falls back).
	converted = false
	w.convertEvent(event.Event{Type: "pr.ready", WorkerId: "gitlab"})
	if converted {
		t.Errorf("source miss: marker converter should not run")
	}
}
