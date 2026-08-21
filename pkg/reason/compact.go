// Context budget: when to trigger compaction.
//
// A token ledger records the latest round-trip usage (InputTokens +
// OutputTokens snapshot, never accumulated) from EventDone.Message.Usage;
// against the model's ContextWindow it yields an occupancy ratio with two
// exits:
//
//	>= budget_soft -> guided: inject a reminder, the LLM calls the
//	   context.compress tool
//	>= budget_hard -> direct: the system compacts without waiting for the LLM
//
// The compaction itself is delegated to the worker's Compactor (compact.go);
// this file only decides when to trigger it.
package reason

import (
	"context"
	"fmt"
	"log"

	llm "github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/pkg/reason/transcript"
)

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
		// Direct exit: compact without waiting for the LLM, on our own
		// goroutine so the summarize call (seconds) does not hold the loop.
		log.Printf("[reason %s] context hard budget %.0f%% (%d/%d tokens) - compacting",
			w.ID(), ratio*100, w.lastUsageTokens, w.contextWindow)
		w.budgetReminded = false
		go func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			_ = w.compactor.Compact(ctx, w.transcript, w.compactDirective())
		}()
	case ratio >= w.budgetSoft && !w.budgetReminded:
		// Guided exit: one reminder per crossing; the LLM decides.
		w.budgetReminded = true
		log.Printf("[reason %s] context soft budget %.0f%% (%d/%d tokens) - reminding",
			w.ID(), ratio*100, w.lastUsageTokens, w.contextWindow)
		w.transcript.Apply(transcript.InputEvent{Messages: []llm.Message{{
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
