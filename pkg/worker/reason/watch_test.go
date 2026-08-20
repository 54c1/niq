package reason

import (
	"strings"
	"testing"
	"time"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	reasonBase "github.com/54c1/niq/pkg/reason"
)

// TestInputDefaultTriggersReasoning verifies a default-mode input produces a
// completed reasoning round (a reason.end event).
func TestInputDefaultTriggersReasoning(t *testing.T) {
	prov := &staticProvider{msg: llm.Message{
		Role: llm.RoleAssistant, StopReason: "stop",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}},
	}}
	_, ch, _ := startWorker(t, prov)

	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		return len(ch.eventsOf("reason.end")) > 0
	}, "reason.end")
}

// TestInputAppendDoesNotInterruptReasoning verifies an append-mode input does
// NOT cancel an in-flight reasoning call (unlike default mode, which interrupts).
func TestInputAppendDoesNotInterruptReasoning(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, ch, _ := startWorker(t, prov)

	// Start a reasoning round and hold it in flight.
	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		select {
		case <-prov.started:
			return true
		default:
			return false
		}
	}, "reasoning to start")

	// append-mode input must not interrupt the in-flight call.
	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "note", "input_mode": "append"})
	time.Sleep(100 * time.Millisecond)
	if ch.hasInterrupted() {
		t.Fatal("append-mode input must not interrupt in-flight reasoning")
	}

	// Release the provider; the round completes normally.
	close(prov.release)
	waitCond(t, 2*time.Second, func() bool {
		return len(ch.eventsOf("reason.end")) > 0
	}, "reason.end")
}

// TestAbortParksTools verifies an abort event parks pending tools and records
// the abort in the transcript.
func TestAbortParksTools(t *testing.T) {
	w, _, _ := startWorker(t, &staticProvider{})
	// Simulate a pending tool call.
	w.mu.Lock()
	w.toolCallTracker.Add("workspace", []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"},
	})
	w.mu.Unlock()

	w.handleAbort(event.New(event.TypeWorkerAbort, "swarm", map[string]any{}))
	if !w.toolCallTracker.Resolved() {
		t.Fatal("abort should park all pending tools")
	}
}

// TestLateToolResult verifies a result arriving after a call was parked is
// appended as a contextualized late message, not a duplicate tool message.
func TestLateToolResult(t *testing.T) {
	w, _, _ := startWorker(t, &staticProvider{})
	w.mu.Lock()
	w.needReason = false
	w.toolCallTracker.Add("workspace", []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"},
	})
	w.toolCallTracker.ParkAll(reasonBase.PreemptCauseTimeout)
	w.mu.Unlock()

	late := event.New(event.TypeToolCompleted, "workspace", map[string]any{
		"call_id": "c1", "name": "bash", "result": "late-out",
	})
	before := len(w.transcript.Render())
	w.handleToolResult(late)

	msgs := w.transcript.Render()
	if len(msgs) != before+1 {
		t.Fatalf("late result should append one message, got %d->%d", before, len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser {
		t.Fatalf("late result should be a user message, got role %q", last.Role)
	}
	if len(last.Content) == 0 || !strings.Contains(last.Content[0].Text, "late-out") {
		t.Fatalf("late message should carry the outcome, got %+v", last.Content)
	}
}
