// Context budget: token ledger, threshold detection, and the compaction loop.
//
// The ledger records the latest round-trip usage (InputTokens + OutputTokens
// snapshot, never accumulated) from EventDone.Message.Usage. Against the
// model's ContextWindow it yields an occupancy ratio with two exits:
//
//	>= budget_soft -> guided: inject a reminder, the LLM calls the
//	   context.compress tool
//	>= budget_hard -> direct: the system compacts without waiting for the LLM
//
// Compaction is orchestrated here, not in the transcript: the summary is an LLM
// call (seconds), so it runs outside w.mu - snapshot under lock, summarize
// unlocked, apply under lock. The transcript's Compact evaluates keepTail at
// apply time, so messages appended during summarization are preserved.
//
// Projection (fixed first step of any summarize): image blocks (base64, token
// blind), thinking blocks and oversized tool results are stripped/truncated -
// the digest never needs them and the payload shrinks 5-10x.
package reason

import (
	"context"
	"fmt"
	"log"
	"strings"

	llm "github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/pkg/reason/transcript"
)

// fallbackCompactDirective is the summarizer system prompt when no
// program-provided directive is configured. The digest format is
// program-driven by design (see context-transcript.md §3.5); this is only the
// built-in fallback template.
const fallbackCompactDirective = `Summarize the following reasoning transcript for continuation.
Preserve: the task/goal, decisions made and their reasons, established facts (paths, ids, versions, errors),
open TODOs, and the latest state. Drop: process detail, failed exploration steps (keep one-line lessons),
verbose tool output. Output only the summary, in a compact structured form.`

// recordUsage updates the token ledger from a completed round's final message
// and checks both budget thresholds. Expects w.mu held.
func (w *BaseReasonWorker) recordUsage(ctx context.Context, msg llm.Message) {
	if msg.Usage == nil || w.contextWindow <= 0 {
		return
	}
	w.lastUsageTokens = msg.Usage.InputTokens + msg.Usage.OutputTokens

	ratio := float64(w.lastUsageTokens) / float64(w.contextWindow)
	switch {
	case ratio >= w.budgetHard:
		// Direct exit: compact without waiting for the LLM. Async - the
		// summary call takes seconds and must not hold the event loop.
		log.Printf("[reason %s] context hard budget %.0f%% (%d/%d tokens) - compacting",
			w.ID(), ratio*100, w.lastUsageTokens, w.contextWindow)
		w.budgetReminded = false
		go w.compactTranscript(ctx, w.compactDirective(), w.keepTail)
	case ratio >= w.budgetSoft && !w.budgetReminded:
		// Guided exit: one reminder per crossing; the LLM decides.
		w.budgetReminded = true
		log.Printf("[reason %s] context soft budget %.0f%% (%d/%d tokens) - reminding",
			w.ID(), ratio*100, w.lastUsageTokens, w.contextWindow)
		w.Transcript.Apply(transcript.InputEvent{Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText,
				Text: fmt.Sprintf("[system] Context usage is at %d%% of the model window (%d/%d tokens). "+
					"Consider calling the context.compress tool to summarize older history before continuing.",
					int(ratio*100), w.lastUsageTokens, w.contextWindow)}},
		}}})
	case ratio < w.budgetSoft:
		w.budgetReminded = false
	}
}

// compactDirective returns the summarizer system prompt: program/config
// provided, else the built-in fallback.
func (w *BaseReasonWorker) compactDirective() string {
	if w.compactDirectiveOverride != "" {
		return w.compactDirectiveOverride
	}
	return fallbackCompactDirective
}

// compactTranscript runs the full compaction loop: snapshot (under lock),
// summarize (unlocked), apply (under lock). Single-flight: a compaction
// already in flight makes this a no-op error. When the transcript already
// carries a digest (head message from a previous compaction), the summarizer
// runs in update mode: merge new progress into the old summary instead of
// rebuilding from scratch, so early goals and constraints survive repeated
// compactions.
func (w *BaseReasonWorker) compactTranscript(ctx context.Context, directive string, keepTail int) error {
	w.mu.Lock()
	if w.isCompacting {
		w.mu.Unlock()
		return fmt.Errorf("compaction already in flight")
	}
	w.isCompacting = true
	msgSnapshot := w.Transcript.Render()
	projection := projectTranscript(msgSnapshot)
	previousDigest := currentDigest(msgSnapshot)
	w.mu.Unlock()

	digest, err := w.summarize(ctx, projection, directive, previousDigest)

	w.mu.Lock()
	w.isCompacting = false
	if err != nil {
		w.mu.Unlock()
		return fmt.Errorf("summarize: %w", err)
	}
	w.Transcript.Compact(digest, keepTail)
	w.mu.Unlock()

	log.Printf("[reason %s] transcript compacted (keepTail=%d, digest=%d chars, update=%v)",
		w.ID(), keepTail, len(digest), previousDigest != "")
	return nil
}

// summarize calls the worker's own provider once, non-streaming. With a
// previous digest present, the request runs in update mode: merge the new
// progress into the old summary instead of rebuilding from scratch (early
// goals and constraints survive repeated compactions). The digest is
// whatever the directive-shaped summary produces; the builder treats it as
// opaque text.
func (w *BaseReasonWorker) summarize(ctx context.Context, projection, directive, previousDigest string) (string, error) {
	if previousDigest != "" {
		directive = directive + "\n\nA previous summary exists; update it incrementally: merge new progress " +
			"into it, move finished items, keep earlier goals and constraints. Previous summary:\n" + previousDigest
	}
	resp, err := w.LLMProvider.Complete(ctx, &llm.CompletionRequest{
		Context: &llm.Context{
			SystemPrompt: directive,
			Messages:     []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: projection}}}},
		},
	})
	if err != nil {
		return "", err
	}
	for _, block := range resp.Message.Content {
		if block.Type == llm.ContentText && block.Text != "" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("summarizer returned no text")
}

// digestPrefix marks the head message produced by a compaction; used to
// detect a carried digest for update-mode summarization.
const digestPrefix = "[context digest]"

// currentDigest extracts the carried digest text from a transcript whose
// head is a digest message (the result of a previous compaction), if any.
func currentDigest(msgs []llm.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	m := msgs[0]
	if m.Role != llm.RoleUser || len(m.Content) == 0 || m.Content[0].Type != llm.ContentText {
		return ""
	}
	if !strings.HasPrefix(m.Content[0].Text, digestPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(m.Content[0].Text, digestPrefix))
}

// projectTranscript renders a lossy-but-faithful projection of the transcript
// for summarization: images and thinking stripped, tool results truncated,
// one line per message. Keep in sync with the projection rules in
// context-transcript.md §5.3.
func projectTranscript(msgs []llm.Message) string {
	const maxToolResult = 2000

	var b strings.Builder
	for i, m := range msgs {
		switch m.Role {
		case llm.RoleToolResult:
			text := ""
			if len(m.Content) > 0 && m.Content[0].Text != "" {
				text = m.Content[0].Text
			}
			if len(text) > maxToolResult {
				text = text[:maxToolResult] + "...[truncated]"
			}
			fmt.Fprintf(&b, "[%d] tool_result %s: %s\n", i, m.ToolName, text)
		default:
			for _, block := range m.Content {
				switch block.Type {
				case llm.ContentText:
					fmt.Fprintf(&b, "[%d] %s: %s\n", i, m.Role, block.Text)
				case llm.ContentToolCall:
					fmt.Fprintf(&b, "[%d] %s tool_call %s(%s)\n", i, m.Role, block.ToolName, block.ToolArguments)
				case llm.ContentImage:
					fmt.Fprintf(&b, "[%d] %s image(%d bytes, omitted)\n", i, m.Role, len(block.Data))
				case llm.ContentThinking:
					// stripped: reasoning process, not carried into digests
				}
			}
		}
	}
	return b.String()
}
