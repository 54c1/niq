package reason

import (
	"context"
	"testing"

	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/pkg/reason/transcript"
)

// TestNewWorkerDefaults verifies a worker built with a minimal Config gets a
// sensible ID and the built-in subscriptions.
func TestNewWorkerDefaults(t *testing.T) {
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	if w.ID() != "r1" {
		t.Fatalf("ID = %q, want r1", w.ID())
	}
	// Built-in subscriptions (tool lifecycle, worker presence, timer) must be present.
	subs := w.Subscriptions()
	got := map[string]bool{}
	for _, s := range subs {
		got[string(s.Type)] = true
	}
	for _, want := range []string{"tool.completed", "tool.failed", "tool.rejected", "tool.requested",
		"worker.ready", "worker.gone", "worker.discover", "worker.input", "worker.abort",
		"timer.timeout", "timer.reminder"} {
		if !got[want] {
			t.Errorf("missing built-in subscription %q", want)
		}
	}
}

// TestStartStop verifies Start/Stop lifecycle: Start is idempotent-guarded and
// Stop returns cleanly.
func TestStartStop(t *testing.T) {
	ch := newMockChannel()
	w := NewWorker(Config{ID: "r1", Provider: &staticProvider{}, Bus: ch})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Start(ctx); err == nil {
		t.Fatal("second Start should fail (already started)")
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("second Stop should be a no-op, got %v", err)
	}
}

// TestWorkerImplementsManagedWorker asserts the reason worker satisfies the
// core ManagedWorker contract.
func TestWorkerImplementsManagedWorker(t *testing.T) {
	var _ worker.ManagedWorker = NewWorker(Config{ID: "r1", Bus: newMockChannel()})
}

// TestSnapshotRestoreRoundTrip verifies Snapshot captures the reasoning
// transcript and Restore rehydrates it into a fresh worker — the durable state
// that must survive a suspend/resume or crash recovery.
func TestSnapshotRestoreRoundTrip(t *testing.T) {
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})

	// Seed a transcript: a user message, an assistant response, and a tool result,
	// by restoring it from an accumulated transcript blob (the transcript's own
	// write path). This also exercises Restore as the seed mechanism.
	seed := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hello"}}},
		{Role: llm.RoleAssistant, StopReason: "stop", Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}}},
		{Role: llm.RoleToolResult, ToolCallID: "call_1", ToolName: "workspace.bash", IsError: false,
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}},
	}
	tp := transcript.NewAccumulateTranscript()
	for _, m := range seed {
		tp.Apply(transcript.AssistantOutput{Message: m})
	}
	blob0, _ := tp.State()
	if err := w.Restore(blob0); err != nil {
		t.Fatalf("seed restore: %v", err)
	}

	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(blob) == 0 {
		t.Fatal("snapshot blob is empty")
	}

	// Restore into a fresh worker, as happens on suspend/resume or restart.
	fresh := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}

	gotMsgs, wantMsgs := fresh.Messages(), w.Messages()
	if len(gotMsgs) != len(wantMsgs) {
		t.Fatalf("restored %d messages, want %d", len(gotMsgs), len(wantMsgs))
	}
	for i := range wantMsgs {
		got, want := gotMsgs[i], wantMsgs[i]
		if got.Role != want.Role || got.StopReason != want.StopReason ||
			got.ToolCallID != want.ToolCallID || got.ToolName != want.ToolName ||
			got.IsError != want.IsError {
			t.Fatalf("message %d mismatch after round-trip:\n got %+v\nwant %+v", i, got, want)
		}
		if len(got.Content) != len(want.Content) {
			t.Fatalf("message %d content len mismatch", i)
		}
		for j := range want.Content {
			if got.Content[j].Type != want.Content[j].Type ||
				got.Content[j].Text != want.Content[j].Text {
				t.Fatalf("message %d content[%d] mismatch after round-trip", i, j)
			}
		}
	}
}

// TestSnapshotRestoreBadBlob verifies Restore rejects an invalid blob.
func TestSnapshotRestoreBadBlob(t *testing.T) {
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	if err := w.Restore([]byte("not json")); err == nil {
		t.Fatal("expected error restoring an invalid blob")
	}
}

// TestSnapshotEmptyMessages verifies a fresh worker snapshots to a valid,
// restorable empty-transcript blob.
func TestSnapshotEmptyMessages(t *testing.T) {
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fresh := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(fresh.Messages()) != 0 {
		t.Fatalf("restored %d messages, want 0", len(fresh.Messages()))
	}
}
