package builder

import (
	"fmt"
	"strings"
	"testing"

	"github.com/54c1/niq/core/llm"
)

func textMsg(role llm.Role, text string) llm.Message {
	return llm.Message{Role: role, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: text}}}
}

func userMsg(text string) llm.Message      { return textMsg(llm.RoleUser, text) }
func assistantMsg(text string) llm.Message { return textMsg(llm.RoleAssistant, text) }

func toolCall(callID, name string) llm.ContentBlock {
	return llm.ContentBlock{Type: llm.ContentToolCall, ToolCallID: callID, ToolName: name}
}

// TestApplyLifecycle walks the full lifecycle through Apply and asserts the
// transcript order and shapes: input, assistant output, placeholders,
// in-place result replacement, park replacement, late result.
func TestApplyLifecycle(t *testing.T) {
	b := NewAccumulate()

	b.Apply(InputEvent{Messages: []llm.Message{userMsg("hi")}})
	b.Apply(AssistantOutput{Message: assistantMsg("hello")})
	b.Apply(ToolPlaceholders{Calls: []llm.ContentBlock{toolCall("c1", "bash"), toolCall("c2", "read")}})
	b.Apply(ToolResult{CallID: "c1", Name: "bash", Text: "0"})

	got := b.Render()
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4", len(got))
	}
	if got[0].Role != llm.RoleUser || got[1].Role != llm.RoleAssistant {
		t.Fatalf("order: got roles %q, %q", got[0].Role, got[1].Role)
	}
	// c1 resolved in place...
	if got[2].ToolCallID != "c1" || got[2].Content[0].Text != "0" {
		t.Fatalf("c1 not replaced in place: %+v", got[2])
	}
	// ...c2 still pending at its original position.
	if got[3].ToolCallID != "c2" || got[3].Content[0].Text != "[pending]" {
		t.Fatalf("c2 placeholder disturbed: %+v", got[3])
	}

	// Park c2: in-place replacement with the cause explanation.
	b.Apply(ToolParked{CallID: "c2", Name: "read", Cause: "timeout"})
	got = b.Render()
	if got[3].Content[0].Text != parkReason("timeout") {
		t.Fatalf("c2 park text: %q", got[3].Content[0].Text)
	}

	// Late result for parked c2: appended as a user message, not a second
	// tool_result for the same call_id.
	b.Apply(LateResult{CallID: "c2", Name: "read", Text: "late-out", Cause: "timeout"})
	got = b.Render()
	if len(got) != 5 {
		t.Fatalf("got %d messages, want 5", len(got))
	}
	last := got[4]
	if last.Role != llm.RoleUser {
		t.Fatalf("late result role = %q, want user", last.Role)
	}
}

// TestPartialOutputPreserved verifies interrupted-round content lands in the
// transcript like any assistant output.
func TestPartialOutputPreserved(t *testing.T) {
	b := NewAccumulate()
	b.Apply(PartialOutput{Message: llm.Message{
		Role: llm.RoleAssistant, StopReason: "interrupted",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "partial"}},
	}})
	got := b.Render()
	if len(got) != 1 || got[0].StopReason != "interrupted" {
		t.Fatalf("partial output lost: %+v", got)
	}
}

// TestStateRestoreRoundTrip verifies the snapshot cache round-trips.
func TestStateRestoreRoundTrip(t *testing.T) {
	b := NewAccumulate()
	b.Apply(InputEvent{Messages: []llm.Message{userMsg("hello")}})
	b.Apply(AssistantOutput{Message: assistantMsg("hi")})

	state, err := b.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	fresh := NewAccumulate()
	if err := fresh.Restore(state); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	a, b2 := b.Render(), fresh.Render()
	if len(a) != len(b2) {
		t.Fatalf("restored %d messages, want %d", len(b2), len(a))
	}
	for i := range a {
		if a[i].Role != b2[i].Role || a[i].StopReason != b2[i].StopReason ||
			a[i].ToolCallID != b2[i].ToolCallID {
			t.Fatalf("message %d mismatch:\n got %+v\nwant %+v", i, b2[i], a[i])
		}
	}
}

// TestToolResultUnknownCallIsSafe verifies a ToolResult for an unknown
// call_id does not corrupt the transcript (no matching placeholder: no-op).
func TestToolResultUnknownCallIsSafe(t *testing.T) {
	b := NewAccumulate()
	b.Apply(InputEvent{Messages: []llm.Message{userMsg("hi")}})
	b.Apply(ToolResult{CallID: "ghost", Name: "x", Text: "y"})
	if got := b.Render(); len(got) != 1 {
		t.Fatalf("unknown tool result should be a no-op, got %d messages", len(got))
	}
}

// TestCompactAppliesDigestAndKeepsTail verifies Compact replaces everything
// before the last keepTail messages with a digest message, tail preserved in
// order, and that it is a no-op when the transcript is already within the tail.
func TestCompactAppliesDigestAndKeepsTail(t *testing.T) {
	b := NewAccumulate()
	for i := 0; i < 5; i++ {
		b.Apply(InputEvent{Messages: []llm.Message{userMsg(fmt.Sprintf("m%d", i))}})
	}

	b.Compact("summary of m0-m2", 2)

	got := b.Render()
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3 (digest + 2 tail)", len(got))
	}
	if got[0].Role != llm.RoleUser || !strings.Contains(got[0].Content[0].Text, "summary of m0-m2") {
		t.Fatalf("digest head missing: %+v", got[0])
	}
	if got[1].Content[0].Text != "m3" || got[2].Content[0].Text != "m4" {
		t.Fatalf("tail order disturbed: %q, %q", got[1].Content[0].Text, got[2].Content[0].Text)
	}

	// No-op when everything fits in the tail.
	b.Compact("again", 10)
	if len(b.Render()) != 3 {
		t.Fatal("compact within tail must be a no-op")
	}
}

// TestCompactAlignsCutToPairing verifies the cut point never leaves orphan
// tool_results at the tail head: a keepTail that would cut between an
// assistant(tool_calls) and its placeholder absorbs the placeholder into the
// compacted side.
func TestCompactAlignsCutToPairing(t *testing.T) {
	b := NewAccumulate()
	b.Apply(InputEvent{Messages: []llm.Message{userMsg("q")}})
	b.Apply(AssistantOutput{Message: llm.Message{
		Role: llm.RoleAssistant, StopReason: "tool_calls",
		Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"}},
	}})
	b.Apply(ToolPlaceholders{Calls: []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"},
	}})
	b.Apply(InputEvent{Messages: []llm.Message{userMsg("after")}})

	// keepTail=1 would cut before the placeholder (orphan tool_result);
	// alignment must move the cut past it.
	b.Compact("digest", 1)

	got := b.Render()
	if got[0].Role != llm.RoleUser || !strings.Contains(got[0].Content[0].Text, "digest") {
		t.Fatalf("digest head missing: %+v", got[0])
	}
	for _, m := range got[1:] {
		if m.Role == llm.RoleToolResult {
			t.Fatalf("tail must not start with orphan tool_result: %+v", got)
		}
		break
	}
	if got[len(got)-1].Content[0].Text != "after" {
		t.Fatalf("recent messages must be preserved: %+v", got)
	}
}

// TestCompactTurnsThePage verifies keepTail = 0 starts a fresh episode from
// the digest alone.
func TestCompactTurnsThePage(t *testing.T) {
	b := NewAccumulate()
	b.Apply(InputEvent{Messages: []llm.Message{userMsg("a")}})
	b.Apply(AssistantOutput{Message: assistantMsg("b")})

	b.Compact("episode summary", 0)

	got := b.Render()
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1 (digest only)", len(got))
	}
	if !strings.Contains(got[0].Content[0].Text, "episode summary") {
		t.Fatalf("digest missing: %+v", got[0])
	}
}
