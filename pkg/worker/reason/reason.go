// LLM reasoning round.
//
// reason: one-shot LLM call → text response or tool calls with placeholders
package reason

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/54c1/niq/core/event"
	llm "github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/pkg/helper"
)

func (w *Worker) reason(ctx context.Context) {
	// Phase 1: Snapshot state under lock before the LLM call.
	w.mu.Lock()
	w.isReasoning = true
	w.activeTimeout = "" // each round starts with no active timeout timer
	traceID := w.currentTraceID
	tools := w.allTools()
	c := &llm.Context{
		SystemPrompt:    w.buildInstruction(),
		Messages:        slices.Clone(w.messages),
		Tools:           toolDefs(w, tools),
		ReasoningEffort: w.reasoningEffort,
	}
	req := &llm.CompletionRequest{Context: c}
	if len(tools) > 0 {
		req.ToolChoice = llm.ToolChoiceAuto
	}
	w.mu.Unlock()

	// Log tool definitions and last few messages for debugging.
	w.logLLMCall(c)

	// Phase 2: Streaming LLM call.
	w.publishReasonStart(traceID)
	reasonCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancelReason = cancel
	w.mu.Unlock()

	var stream *llm.EventStream
	err := helper.Retry(reasonCtx, 5, func() (bool, error) {
		s, callErr := w.llmProvider.CompleteStream(reasonCtx, req)
		if callErr == nil {
			stream = s
			return false, nil
		}
		var llmErr *llm.LLMError
		if !errors.As(callErr, &llmErr) {
			return false, callErr
		}
		return llmErr.Type == llm.ErrorRateLimit || llmErr.Type == llm.ErrorTimeout, callErr
	})

	if err != nil {
		w.mu.Lock()
		w.cancelReason = nil

		// case interrupted
		if reasonCtx.Err() != nil {
			log.Printf("[reason %s] reasoning interrupted", w.ID())
			w.publishReasonEnd(traceID, "interrupted")
			w.isReasoning = false
			w.mu.Unlock()
			w.tryReason(ctx)
			return
		}

		log.Printf("[reason %s] LLM error: %v", w.ID(), err)
		errEvt := event.New("reason.response", w.ID(), map[string]any{
			"content":     []any{fmt.Sprintf("Error: %v", err)},
			"stop_reason": "error",
		})
		errEvt.TraceID = traceID
		_ = w.Channel.Broadcast(context.Background(), errEvt) // reason.response — error content
		w.publishReasonEnd(traceID, "error")                  // reason.end — lifecycle signal
		w.isReasoning = false
		w.mu.Unlock()
		w.tryReason(ctx)
		return
	}

	// Event loop: consume streaming deltas, flush every 5s.
	var (
		thinkingBuf     strings.Builder
		textBuf         strings.Builder
		streamErr       error
		partialThinking string
		partialText     string
	)

	const batchInterval = 5 * time.Second
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	flushBatches := func() {
		if thinkingBuf.Len() > 0 {
			w.publishThinkingDelta(thinkingBuf.String(), traceID)
			thinkingBuf.Reset()
		}
		if textBuf.Len() > 0 {
			w.publishTextDelta(textBuf.String(), traceID)
			textBuf.Reset()
		}
	}

	streamDone := false
	interrupted := false

	for !streamDone {
		select {
		case <-reasonCtx.Done():
			// Interrupted: abort or new input preempted the round.
			interrupted = true
			// Save accumulated content before flushing (flush resets buffers).
			partialThinking = thinkingBuf.String()
			partialText = textBuf.String()
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
			stream.Drain(drainCtx)
			drainCancel()
			flushBatches()
			// Preserve partial content to conversation context.
			if partialThinking != "" || partialText != "" {
				var blocks []llm.ContentBlock
				if partialThinking != "" {
					blocks = append(blocks, llm.ContentBlock{Type: llm.ContentThinking, Text: partialThinking})
				}
				if partialText != "" {
					blocks = append(blocks, llm.ContentBlock{Type: llm.ContentText, Text: partialText})
				}
				w.mu.Lock()
				w.messages = append(w.messages, llm.Message{
					Role:       llm.RoleAssistant,
					Content:    blocks,
					StopReason: "interrupted",
				})
				w.mu.Unlock()
			}
			streamDone = true

		case <-ticker.C:
			// 5s elapsed: flush accumulated deltas as batch events.
			flushBatches()

		case evt, ok := <-stream.C():
			if !ok {
				// Stream exhausted: flush remaining content, then get final message.
				flushBatches()
				streamDone = true
				break
			}
			switch e := evt.(type) {
			case llm.EventThinkingDelta:
				// Thinking delta: accumulate for batch flush.
				thinkingBuf.WriteString(e.Delta)
			case llm.EventTextDelta:
				// Text delta: accumulate for batch flush.
				textBuf.WriteString(e.Delta)
			case llm.EventError:
				// Stream error: flush what we have, surface error in Phase 3.
				streamErr = e.Err
				flushBatches()
				streamDone = true
			}
		}
	}

	// Phase 3: State update (lock)
	w.mu.Lock()
	w.cancelReason = nil

	if interrupted {
		cause := w.interruptReason
		if cause == "" {
			cause = "unknown"
		}
		preserved := partialThinking + partialText
		log.Printf("[reason %s] reasoning interrupted (cause=%s, preserved=%d chars)", w.ID(), cause, len(preserved))
		evt := event.New("reason.interrupted", w.ID(), map[string]any{
			"reason":          cause,
			"preserved_chars": len(preserved),
			"preserved_text":  preserved,
		})
		evt.TraceID = traceID
		_ = w.Channel.Broadcast(context.Background(), evt)
		w.publishReasonEnd(traceID, "interrupted")
		w.isReasoning = false
		w.mu.Unlock()
		w.tryReason(ctx)
		return
	}

	if streamErr != nil {
		log.Printf("[reason %s] LLM error: %v", w.ID(), streamErr)
		errEvt := event.New("reason.response", w.ID(), map[string]any{
			"content":     []any{fmt.Sprintf("Error: %v", streamErr)},
			"stop_reason": "error",
		})
		errEvt.TraceID = traceID
		_ = w.Channel.Broadcast(context.Background(), errEvt) // reason.response — error content
		w.publishReasonEnd(traceID, "error")                  // reason.end — lifecycle signal
		w.isReasoning = false
		w.mu.Unlock()
		w.tryReason(ctx)
		return
	}

	// Get final message from the stream.
	resultCtx, resultCancel := context.WithTimeout(context.Background(), 5*time.Second)
	finalMsg, err := stream.Result(resultCtx)
	resultCancel()
	if err != nil {
		log.Printf("[reason %s] stream result error: %v", w.ID(), err)
		errEvt := event.New("reason.response", w.ID(), map[string]any{
			"content":     []any{fmt.Sprintf("Error: %v", err)},
			"stop_reason": "error",
		})
		errEvt.TraceID = traceID
		_ = w.Channel.Broadcast(context.Background(), errEvt) // reason.response — error content
		w.publishReasonEnd(traceID, "error")                  // reason.end — lifecycle signal
		w.isReasoning = false
		w.mu.Unlock()
		w.tryReason(ctx)
		return
	}

	log.Printf("[reason %s] LLM response: stop_reason=%s, content_blocks=%d",
		w.ID(), finalMsg.StopReason, len(finalMsg.Content))

	w.messages = append(w.messages, finalMsg)

	// Collect tool calls from the response.
	var toolCalls []llm.ContentBlock
	for _, block := range finalMsg.Content {
		if block.Type == llm.ContentToolCall {
			toolCalls = append(toolCalls, block)
		}
	}

	// Publish complete thinking content (if any was streamed via deltas).
	var thinkingBlocks []llm.ContentBlock
	for _, block := range finalMsg.Content {
		if block.Type == llm.ContentThinking {
			thinkingBlocks = append(thinkingBlocks, block)
		}
	}
	if len(thinkingBlocks) > 0 {
		log.Printf("[reason %s] publishing %d thinking block(s)", w.ID(), len(thinkingBlocks))
		w.publishThinking(thinkingBlocks, traceID)
	} else {
		log.Printf("[reason %s] no thinking blocks in response (content_blocks=%d)", w.ID(), len(finalMsg.Content))
	}

	if len(toolCalls) > 0 {
		w.handleToolCalls(ctx, toolCalls, traceID)
		return
	}

	// Text-only response: publish content, then signal round end.
	w.publishResponse(finalMsg, traceID)             // reason.response — text content
	w.publishReasonEnd(traceID, finalMsg.StopReason) // reason.end — lifecycle signal

	w.isReasoning = false
	w.mu.Unlock()

	// calls tryReason again to catch overlapping events.
	w.tryReason(ctx)
}

func (w *Worker) logLLMCall(c *llm.Context) {
	toolNames := make([]string, len(c.Tools))
	for i, td := range c.Tools {
		toolNames[i] = td.Name
	}
	lastMsgs := c.Messages
	if len(lastMsgs) > 4 {
		lastMsgs = lastMsgs[len(lastMsgs)-4:]
	}
	msgSummaries := make([]string, len(lastMsgs))
	for i, m := range lastMsgs {
		s := string(m.Role)
		if m.ToolCallID != "" {
			s += "[" + m.ToolCallID + "]"
		}
		msgSummaries[i] = s
	}
	log.Printf("[reason %s] LLM call: tools=%v, last_msgs=%v", w.ID(), toolNames, msgSummaries)

	// Warn if essential timer tools are missing (any worker can provide
	// them — not necessarily the "timer" worker).
	hasTimeout := false
	hasElapse := false
	for name := range w.tools {
		if strings.HasSuffix(name, ".set_tool_timeout") {
			hasTimeout = true
		}
		if strings.HasSuffix(name, ".elapse") {
			hasElapse = true
		}
	}
	if !hasTimeout {
		log.Printf("[reason %s] WARNING: no worker provides 'set_tool_timeout' — tool call timeout unavailable", w.ID())
	}
	if !hasElapse {
		log.Printf("[reason %s] WARNING: no worker provides 'elapse' — reminder support unavailable", w.ID())
	}
}

func (w *Worker) handleToolCalls(ctx context.Context, toolCalls []llm.ContentBlock, traceID string) {
	busCalls := toolCalls

	// Desanitize tool names first using index-based access so the
	// changes apply to the actual slice elements (for _, tc := range
	// creates copies whose modifications are lost).
	for i := range busCalls {
		busCalls[i].ToolName = desanitizeToolName(w, busCalls[i].ToolName)
	}

	// Record the current round's single timeout timer, if any. At most one
	// is meaningful — the first timer to fire parks all pending tools. Only
	// record if the tool exists in w.tools — if the providing worker hasn't
	// announced yet or was removed, skip so cancelTimeout/handleTimeout
	// are naturally disabled.
	for _, tc := range busCalls {
		if tc.ToolName == "timer.set_tool_timeout" {
			if _, ok := w.tools[tc.ToolName]; ok {
				w.activeTimeout = tc.ToolCallID
			}
		}
	}

	w.insertPlaceholders(busCalls)

	// Group tool calls by target worker, then publish tool.requested
	// for each group. The bus routes each batch to the correct worker.
	// Unknown tools (not in w.tools, e.g. hallucinated by the LLM) are
	// failed immediately instead of broadcast — broadcasting confuses
	// other workers that subscribe to tool.requested.
	log.Printf("[reason %s] requesting %d tool call(s) via bus: %v", w.ID(), len(busCalls), func() []string {
		var names []string
		for _, tc := range busCalls {
			names = append(names, tc.ToolName)
		}
		return names
	}())
	callsByTarget := make(map[string][]llm.ContentBlock)
	for _, tc := range busCalls {
		t, ok := w.tools[tc.ToolName]
		if !ok {
			log.Printf("[reason %s] unknown tool: %s — failing immediately", w.ID(), tc.ToolName)
			w.replacePlaceholder(tc.ToolCallID, failMessage(tc.ToolCallID, tc.ToolName, "unknown tool: "+tc.ToolName))
			continue
		}
		// Strip the worker ID prefix so the target worker receives
		// the original tool name (e.g. "workspace.bash" → "bash").
		tc.ToolName = strings.TrimPrefix(tc.ToolName, t.Provider+".")
		callsByTarget[t.Provider] = append(callsByTarget[t.Provider], tc)
	}
	for target, calls := range callsByTarget {
		w.callTracker.Add(target, calls)
		w.publishToolRequests(target, w.ID(), calls, traceID)
	}
	w.isReasoning = false
	w.mu.Unlock()
	w.tryReason(ctx)
}

func (w *Worker) publishResponse(msg llm.Message, traceID string) {
	var texts []any
	for _, block := range msg.Content {
		if block.Type == llm.ContentText {
			texts = append(texts, block.Text)
		}
	}
	evt := event.New("reason.response", w.ID(), map[string]any{
		"content":     texts,
		"stop_reason": msg.StopReason,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
	log.Printf("[reason %s] published reason.response, text_count=%d", w.ID(), len(texts))
}

func (w *Worker) publishReasonStart(traceID string) {
	evt := event.New("reason.start", w.ID(), map[string]any{
		"worker_id": w.ID(),
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

func (w *Worker) publishReasonEnd(traceID, stopReason string) {
	evt := event.New("reason.end", w.ID(), map[string]any{
		"worker_id":   w.ID(),
		"stop_reason": stopReason,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

func (w *Worker) publishThinking(blocks []llm.ContentBlock, traceID string) {
	log.Printf("[reason %s] publishing thinking: %d blocks, total %d chars", w.ID(), len(blocks), len(blocks[0].Text))
	var texts []any
	for _, b := range blocks {
		texts = append(texts, b.Text)
	}
	evt := event.New("reason.thinking", w.ID(), map[string]any{
		"content": texts,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

func (w *Worker) publishThinkingDelta(text, traceID string) {
	evt := event.New("reason.thinking_delta", w.ID(), map[string]any{
		"delta": text,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

func (w *Worker) publishTextDelta(text, traceID string) {
	evt := event.New("reason.text_delta", w.ID(), map[string]any{
		"delta": text,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}
