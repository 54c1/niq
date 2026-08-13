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
	// Phase 1: Setup (lock). Park leftover tools, snapshot state, build request.
	traceID, req := w.prepareReasoning()

	// Phase 2: Streaming LLM call.
	w.broadcastReasonStart(traceID)

	reasonCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancelReason = cancel
	w.mu.Unlock()

	stream, err := w.openStream(reasonCtx, req)
	if err != nil {
		w.handleStreamStartError(ctx, reasonCtx, traceID, err)
		return
	}

	outcome := w.consumeStream(reasonCtx, stream, traceID)

	// Phase 3: State update (lock).
	w.mu.Lock()
	w.cancelReason = nil

	if outcome.interrupted {
		w.finishInterrupted(ctx, traceID, outcome)
		return
	}
	if outcome.streamErr != nil {
		w.broadcastErrorAndEnd(ctx, traceID, outcome.streamErr)
		return
	}

	finalMsg, err := w.finalMessage(stream)
	if err != nil {
		w.broadcastErrorAndEnd(ctx, traceID, err)
		return
	}
	w.finishReasoning(ctx, traceID, finalMsg)
}

// prepareReasoning snapshots the reasoning state under lock and builds the
// completion request: parks leftover tools, captures the trace and current
// messages, and assembles the request with the current tool set. Returns
// after releasing the lock.
func (w *Worker) prepareReasoning() (traceID string, req *llm.CompletionRequest) {
	w.mu.Lock()
	w.isReasoning = true
	w.activeTimeout = "" // each reasoning starts with no active timeout timer

	// Park any pending tools from the previous reasoning before taking the
	// snapshot, so the LLM sees the parked context. The cause is taken
	// from immediateReasoningCause if set, otherwise falls back to Input.
	cause := w.immediateReasoningCause
	if cause == "" {
		cause = PreemptCauseInput
	}
	w.immediateReasoningCause = ""
	w.parkPending(cause)

	traceID = w.currentTraceID
	tools := w.allTools()
	c := &llm.Context{
		SystemPrompt:    w.buildInstruction(),
		Messages:        slices.Clone(w.messages),
		Tools:           toolDefs(w, tools),
		ReasoningEffort: w.reasoningEffort,
	}
	req = &llm.CompletionRequest{Context: c}
	if len(tools) > 0 {
		req.ToolChoice = llm.ToolChoiceAuto
	}
	w.mu.Unlock()

	return traceID, req
}

// openStream starts the LLM stream, retrying on transient errors.
func (w *Worker) openStream(reasonCtx context.Context, req *llm.CompletionRequest) (*llm.EventStream, error) {
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
	return stream, err
}

// streamOutcome summarizes the result of consuming an LLM stream.
type streamOutcome struct {
	interrupted     bool
	streamErr       error
	partialThinking string
	partialText     string
}

// consumeStream reads deltas off the stream, batching them into periodic
// delta events, until the stream ends or the reasoning is interrupted. On
// interruption it drains the stream and preserves any partial content
// into the transcript so the next reasoning can continue from it.
func (w *Worker) consumeStream(reasonCtx context.Context, stream *llm.EventStream, traceID string) streamOutcome {
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
			w.broadcastThinkingDelta(thinkingBuf.String(), traceID)
			thinkingBuf.Reset()
		}
		if textBuf.Len() > 0 {
			w.broadcastTextDelta(textBuf.String(), traceID)
			textBuf.Reset()
		}
	}

	streamDone := false
	interrupted := false

	for !streamDone {
		select {
		case <-reasonCtx.Done():
			// Interrupted: abort or new input preempted the reasoning. Save
			// accumulated content before flushing (flush resets buffers).
			interrupted = true
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
				// Stream error: flush what we have, surface in Phase 3.
				streamErr = e.Err
				flushBatches()
				streamDone = true
			}
		}
	}

	return streamOutcome{
		interrupted:     interrupted,
		streamErr:       streamErr,
		partialThinking: partialThinking,
		partialText:     partialText,
	}
}

// finalMessage retrieves the final message from a completed stream.
func (w *Worker) finalMessage(stream *llm.EventStream) (llm.Message, error) {
	resultCtx, resultCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer resultCancel()
	return stream.Result(resultCtx)
}

// handleStreamStartError handles a failure to open the LLM stream. If the
// request was interrupted (abort or new input preempted the reasoning), it
// broadcasts the interrupted lifecycle quietly; otherwise it surfaces the error.
func (w *Worker) handleStreamStartError(ctx context.Context, reasonCtx context.Context, traceID string, err error) {
	w.mu.Lock()
	w.cancelReason = nil

	if reasonCtx.Err() != nil {
		log.Printf("[reason %s] reasoning interrupted", w.ID())
		w.broadcastReasonEnd(traceID, StopReasonInterrupted)
		w.isReasoning = false
		w.mu.Unlock()
		w.tryReason(ctx)
		return
	}

	w.broadcastErrorAndEnd(ctx, traceID, err)
}

// finishInterrupted broadcasts the interrupted lifecycle for reasoning that
// was cancelled mid-stream. The partial content is preserved in the
// transcript. Expects w.mu to be held; unlocks it and calls tryReason before
// returning.
func (w *Worker) finishInterrupted(ctx context.Context, traceID string, out streamOutcome) {
	cause := w.interruptReason
	if cause == "" {
		cause = "unknown"
	}
	preserved := out.partialThinking + out.partialText
	log.Printf("[reason %s] reasoning interrupted (cause=%s, preserved=%d chars)", w.ID(), cause, len(preserved))
	evt := event.New("reason.interrupted", w.ID(), map[string]any{
		"reason":          string(cause),
		"preserved_chars": len(preserved),
		"preserved_text":  preserved,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
	w.broadcastReasonEnd(traceID, StopReasonInterrupted)
	w.isReasoning = false
	w.mu.Unlock()
	w.tryReason(ctx)
}

// finishReasoning completes a successful reasoning: records the final
// message, publishes thinking blocks, and either dispatches tool calls or
// broadcasts the text response. Expects w.mu to be held; unlocks it and calls
// tryReason before returning.
func (w *Worker) finishReasoning(ctx context.Context, traceID string, finalMsg llm.Message) {
	log.Printf("[reason %s] LLM response: stop_reason=%s, content_blocks=%d",
		w.ID(), finalMsg.StopReason, len(finalMsg.Content))

	w.messages = append(w.messages, finalMsg)

	// Collect tool calls and thinking blocks from the response.
	var toolCalls []llm.ContentBlock
	var thinkingBlocks []llm.ContentBlock
	for _, block := range finalMsg.Content {
		switch block.Type {
		case llm.ContentToolCall:
			toolCalls = append(toolCalls, block)
		case llm.ContentThinking:
			thinkingBlocks = append(thinkingBlocks, block)
		}
	}
	if len(thinkingBlocks) > 0 {
		log.Printf("[reason %s] publishing %d thinking block(s)", w.ID(), len(thinkingBlocks))
		w.broadcastThinking(thinkingBlocks, traceID)
	} else {
		log.Printf("[reason %s] no thinking blocks in response (content_blocks=%d)", w.ID(), len(finalMsg.Content))
	}

	if len(toolCalls) > 0 {
		w.handleToolCalls(ctx, toolCalls, traceID)
		return
	}

	// Text-only response: publish content, then signal reasoning end.
	w.broadcastResponse(finalMsg, traceID)                         // reason.response — text content
	w.broadcastReasonEnd(traceID, StopReason(finalMsg.StopReason)) // reason.end — lifecycle signal

	w.isReasoning = false
	w.mu.Unlock()

	// calls tryReason again to catch overlapping events.
	w.tryReason(ctx)
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
	// record if the tool exists in w.workerTools — if the providing worker hasn't
	// announced yet or was removed, skip so cancelTimeout/handleTimeout
	// are naturally disabled.
	for _, tc := range busCalls {
		if tc.ToolName == "timer.set_tool_timeout" {
			if _, ok := w.workerTools[tc.ToolName]; ok {
				w.activeTimeout = tc.ToolCallID
			}
		}
	}

	w.insertPlaceholders(busCalls)

	// Group tool calls by target worker, then publish tool.requested
	// for each group. The bus routes each batch to the correct worker.
	// Unknown tools (not in w.workerTools, e.g. hallucinated by the LLM) are
	// failed immediately instead of broadcast — broadcasting confuses
	// other workers that subscribe to tool.requested.
	toolNames := make([]string, len(busCalls))
	for i, tc := range busCalls {
		toolNames[i] = tc.ToolName
	}
	log.Printf("[reason %s] requesting %d tool call(s) via bus: %v", w.ID(), len(busCalls), toolNames)
	callsByTarget := make(map[string][]llm.ContentBlock)
	for _, tc := range busCalls {
		t, ok := w.workerTools[tc.ToolName]
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
		w.toolCallTracker.Add(target, calls)
		w.sendToolRequests(target, w.ID(), calls, traceID)
	}
	w.isReasoning = false
	w.mu.Unlock()
	w.tryReason(ctx)
}

func (w *Worker) broadcastResponse(msg llm.Message, traceID string) {
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

func (w *Worker) broadcastReasonStart(traceID string) {
	evt := event.New("reason.start", w.ID(), map[string]any{
		"worker_id": w.ID(),
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

// StopReason values for broadcastReasonEnd.
// LLM provider stop reasons ("stop", "length", "tool_calls", etc.) are passed through as-is.
type StopReason string

const (
	StopReasonInterrupted StopReason = "interrupted" // reasoning interrupted mid-stream
	StopReasonError       StopReason = "error"       // LLM call failed
	StopReasonAborted     StopReason = "aborted"     // abort received, no reasoning was in flight
)

func (w *Worker) broadcastReasonEnd(traceID string, stopReason StopReason) {
	evt := event.New("reason.end", w.ID(), map[string]any{
		"worker_id":   w.ID(),
		"stop_reason": string(stopReason),
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

func (w *Worker) broadcastThinking(blocks []llm.ContentBlock, traceID string) {
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

func (w *Worker) broadcastThinkingDelta(text, traceID string) {
	evt := event.New("reason.thinking_delta", w.ID(), map[string]any{
		"delta": text,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

func (w *Worker) broadcastTextDelta(text, traceID string) {
	evt := event.New("reason.text_delta", w.ID(), map[string]any{
		"delta": text,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

// broadcastErrorAndEnd broadcasts an error response and ends the current reasoning
// round. Expects w.mu to be held; unlocks it and calls tryReason before returning.
func (w *Worker) broadcastErrorAndEnd(ctx context.Context, traceID string, err error) {
	log.Printf("[reason %s] LLM error: %v", w.ID(), err)
	errEvt := event.New("reason.response", w.ID(), map[string]any{
		"content":     []any{fmt.Sprintf("Error: %v", err)},
		"stop_reason": "error",
	})
	errEvt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), errEvt)
	w.broadcastReasonEnd(traceID, StopReasonError)
	w.isReasoning = false
	w.mu.Unlock()
	w.tryReason(ctx)
}
