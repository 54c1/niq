package reason

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/pkg/reason/transcript"
)

// summarizeProvider routes Complete (summarizer) and CompleteStream (reasoning)
// to different fixed messages, recording the summarizer's system prompt.
type summarizeProvider struct {
	mu          sync.Mutex
	seenPrompt  string
	summarized  string
	chatMessage llm.Message
}

func (p *summarizeProvider) Complete(_ context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seenPrompt = req.Context.SystemPrompt
	return &llm.CompletionResponse{Message: llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "DIGEST(" + p.summarized + ")"}},
	}}, nil
}

func (p *summarizeProvider) CompleteStream(_ context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextEnd{})
	es.End(p.chatMessage)
	return es, nil
}

func (p *summarizeProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func (p *summarizeProvider) prompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seenPrompt
}

// TestSoftBudgetInjectsReminder verifies crossing the soft threshold appends
// exactly one system reminder per crossing, and no compaction runs.
func TestSoftBudgetInjectsReminder(t *testing.T) {
	prov := &summarizeProvider{summarized: "unused",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
			Usage:   &llm.Usage{InputTokens: 900, OutputTokens: 10},
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}}}
	w := NewBaseReasonWorker(Config{ID: "r1", Provider: prov, Bus: newTestChannel(),
		ContextWindow: 1000, BudgetSoft: 0.85, BudgetHard: 0.97})

	w.handleBudget(context.Background(), llm.Message{Usage: &llm.Usage{InputTokens: 900, OutputTokens: 10}})

	msgs := w.transcript.Render()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content[0].Text, "91%") {
		t.Fatalf("expected one budget reminder, got %+v", msgs)
	}

	// Same side of the threshold: no duplicate reminder.
	w.handleBudget(context.Background(), llm.Message{Usage: &llm.Usage{InputTokens: 950, OutputTokens: 5}})
	if got := len(w.transcript.Render()); got != 1 {
		t.Fatalf("reminder should fire once per crossing, got %d messages", got)
	}
}

// TestHardBudgetCompacts verifies crossing the hard threshold compacts the
// transcript asynchronously: digest head + keepTail tail.
func TestHardBudgetCompacts(t *testing.T) {
	prov := &summarizeProvider{summarized: "hard-budget",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	w := NewBaseReasonWorker(Config{ID: "r1", Provider: prov, Bus: newTestChannel(),
		ContextWindow: 1000, BudgetSoft: 0.85, BudgetHard: 0.97, KeepTail: 2})

	seed := func(n int) {
		for i := 0; i < n; i++ {
			w.transcript.Apply(transcript.InputEvent{Messages: []llm.Message{
				{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: string(rune('a' + i))}}},
			}})
		}
	}
	seed(6)

	w.handleBudget(context.Background(), llm.Message{Usage: &llm.Usage{InputTokens: 990, OutputTokens: 5}})

	// Poll under the worker lock: Render() is only safe with w.mu held
	// (builders are passive; the caller serializes access).
	waitCond(t, testTimeout, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		msgs := w.transcript.Render()
		return len(msgs) == 3 && strings.Contains(msgs[0].Content[0].Text, "DIGEST")
	}, "compaction to apply")

	msgs := w.transcript.Render()
	if msgs[1].Content[0].Text != "e" || msgs[2].Content[0].Text != "f" {
		t.Fatalf("keepTail=2 should keep last two messages, got %q %q",
			msgs[1].Content[0].Text, msgs[2].Content[0].Text)
	}
	if !strings.Contains(prov.prompt(), "Summarize") {
		t.Fatalf("summarizer prompt = %q, want fallback directive", prov.prompt())
	}
}

// TestCompactToolRotates verifies the context.rotate built-in:
// keepTail=1 keeps the call's own placeholder, the digest heads the fresh
// episode, and a completed tool result is delivered via the bus.
func TestCompactToolRotates(t *testing.T) {
	prov := &summarizeProvider{summarized: "episode",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	ch := newTestChannel()
	w := NewBaseReasonWorker(Config{ID: "r1", Provider: prov, Bus: ch,
		ContextWindow: 1000})

	// Seed an episode and the call's own placeholder.
	seed := func(n int) {
		for i := 0; i < n; i++ {
			w.transcript.Apply(transcript.InputEvent{Messages: []llm.Message{
				{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: string(rune('a' + i))}}},
			}})
		}
	}
	seed(4)
	// The closing call's assistant message + placeholder, as
	// handleToolCalls produces them (pairing invariant holds in the tail).
	w.transcript.Apply(transcript.AssistantOutput{Message: llm.Message{
		Role: llm.RoleAssistant, StopReason: "tool_calls",
		Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolCallID: "call_ep", ToolName: "context_rotate"}},
	}})
	w.transcript.Apply(transcript.ToolPlaceholders{Calls: []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "call_ep", ToolName: "context_rotate"},
	}})

	w.mu.Lock()
	w.handleCompactTool("call_ep", "context.rotate", w.ID(), map[string]any{}, "rotate")
	w.mu.Unlock()

	// Tool completion arrives on the bus.
	waitCond(t, testTimeout, func() bool {
		for _, e := range ch.eventsOf(event.TypeToolCompleted) {
			if e.Payload["call_id"] == "call_ep" {
				return true
			}
		}
		return false
	}, "rotate tool completion")

	// Fresh episode: digest + the closing assistant tool_call + placeholder.
	msgs := w.transcript.Render()
	if len(msgs) != 3 {
		t.Fatalf("fresh episode should be digest + tool_call + placeholder, got %d: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].Content[0].Text, "DIGEST") {
		t.Fatalf("digest head missing: %+v", msgs[0])
	}
	if msgs[1].Content[0].Type != llm.ContentToolCall || msgs[2].ToolCallID != "call_ep" {
		t.Fatalf("closing call/placeholder not kept: %+v %+v", msgs[1], msgs[2])
	}
}

// TestProjectTranscriptStrips verifies the projection drops thinking blocks
// and truncates oversized tool results.
func TestProjectTranscriptStrips(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.ContentText, Text: "hello"},
			{Type: llm.ContentImage, Data: strings.Repeat("x", 100000)},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.ContentThinking, Text: "long reasoning that must not leak"},
		}},
		{Role: llm.RoleToolResult, ToolName: "bash", Content: []llm.ContentBlock{
			{Type: llm.ContentText, Text: strings.Repeat("y", 5000)},
		}},
	}
	out := projectTranscript(msgs)
	if strings.Contains(out, "long reasoning") {
		t.Fatal("thinking must be stripped from projection")
	}
	if strings.Contains(out, "xxxx") {
		t.Fatal("image data must not appear in projection")
	}
	if !strings.Contains(out, "[truncated]") {
		t.Fatal("oversized tool result must be truncated")
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "omitted") {
		t.Fatal("text content and image omission marker expected")
	}
}

// TestCompactionUpdateMode verifies a second compaction detects the carried
// digest and sends it to the summarizer for incremental merging.
func TestCompactionUpdateMode(t *testing.T) {
	prov := &summarizeProvider{summarized: "v2",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	w := NewBaseReasonWorker(Config{ID: "r1", Provider: prov, Bus: newTestChannel(),
		ContextWindow: 1000, KeepTail: 1})

	// Seed a transcript already headed by a digest (previous compaction).
	w.transcript.Apply(transcript.InputEvent{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: "[context digest] v1: goal=fix bug; done=analysis"}}},
	}})
	for i := 0; i < 4; i++ {
		w.transcript.Apply(transcript.InputEvent{Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "step"}}},
		}})
	}

	w.mu.Lock()
	err := w.compactor.Compact(context.Background(), w.transcript, w.compactDirective())
	w.mu.Unlock()
	if err != nil {
		t.Fatalf("compact: %v", err)
	}

	prompt := prov.prompt()
	if !strings.Contains(prompt, "v1: goal=fix bug") {
		t.Fatalf("update mode must pass previous digest to summarizer, prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "update it incrementally") {
		t.Fatalf("update-mode directive missing, prompt: %q", prompt)
	}
	// New head carries the v2 digest.
	msgs := w.transcript.Render()
	if !strings.Contains(msgs[0].Content[0].Text, "DIGEST(v2)") {
		t.Fatalf("updated digest head expected: %+v", msgs[0])
	}
}

// TestCurrentDigestNegative verifies currentDigest returns empty for a
// transcript whose head is not a digest message.
func TestCurrentDigestNegative(t *testing.T) {
	if d := currentDigest([]llm.Message{{Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hello"}}}}); d != "" {
		t.Fatalf("expected empty digest, got %q", d)
	}
}

// (messages only, no cursor) restores cleanly - Restore must read older blobs.
func TestSnapshotOldBlobCompatibility(t *testing.T) {
	w := NewBaseReasonWorker(Config{ID: "r1", Bus: newTestChannel()})
	old := `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
	if err := w.Restore([]byte(old)); err != nil {
		t.Fatalf("old blob restore: %v", err)
	}
	msgs := w.transcript.Render()
	if len(msgs) != 1 || msgs[0].Content[0].Text != "hello" {
		t.Fatalf("old blob content lost: %+v", msgs)
	}
}
