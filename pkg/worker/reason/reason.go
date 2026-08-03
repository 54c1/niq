// reason.go — LLM reasoning round.
//
//	reason: one-shot LLM call → text response or tool calls with placeholders
//	insertPlaceholders: add pending tool_result entries as conversation inserts
package reason

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/54c1/niq/core/event"
	llm "github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/pkg/helper"
)

func (w *Worker) reason(ctx context.Context) {
	// Phase 1: Setup (lock)
	w.mu.Lock()
	w.isReasoning = true
	w.needReason = false
	tools := w.allTools()
	c := &llm.Context{
		SystemPrompt:    w.buildInstruction(),
		Messages:        w.messages,
		Tools:           toolDefs(w, tools),
		ReasoningEffort: w.reasoningEffort,
	}
	req := &llm.CompletionRequest{Context: c}
	if len(tools) > 0 {
		req.ToolChoice = llm.ToolChoiceAuto
	}
	w.mu.Unlock()

	// Log tool definitions and last few messages for debugging.
	{
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

	// Phase 2: LLM call with retry for transient errors.
	w.publishReasonStart()
	reasonCtx, cancel := context.WithCancel(ctx)
	w.cancelReason = cancel

	var resp *llm.CompletionResponse
	err := helper.Retry(reasonCtx, 5, func() (bool, error) {
		r, callErr := w.llmProvider.Complete(reasonCtx, req)
		if callErr == nil {
			resp = r
			return false, nil
		}
		var llmErr *llm.LLMError
		if !errors.As(callErr, &llmErr) {
			return false, callErr
		}
		return llmErr.Type == llm.ErrorRateLimit || llmErr.Type == llm.ErrorTimeout, callErr
	})
	w.cancelReason = nil

	// Phase 3: State update (lock)
	w.mu.Lock()

	if err != nil {
		log.Printf("[reason %s] LLM error: %v", w.ID(), err)
		errEvt := event.New("reason.response", w.ID(), map[string]any{
			"content":     []any{fmt.Sprintf("Error: %v", err)},
			"stop_reason": "error",
		})
		errEvt.TraceID = w.currentTraceID
		_ = w.Bus.Publish(errEvt)
		w.publishReasonEnd("error")
		w.isReasoning = false
		w.mu.Unlock()
		w.tryReason(ctx)
		return
	}

	log.Printf("[reason %s] LLM response: stop_reason=%s, content_blocks=%d",
		w.ID(), resp.Message.StopReason, len(resp.Message.Content))

	w.messages = append(w.messages, resp.Message)

	// Collect tool calls from the response.
	var toolCalls []llm.ContentBlock
	for _, block := range resp.Message.Content {
		if block.Type == llm.ContentToolCall {
			toolCalls = append(toolCalls, block)
		}
	}

	// Publish thinking content if present (before tool calls or text response).
	var thinkingBlocks []llm.ContentBlock
	for _, block := range resp.Message.Content {
		if block.Type == llm.ContentThinking {
			thinkingBlocks = append(thinkingBlocks, block)
		}
	}
	if len(thinkingBlocks) > 0 {
		log.Printf("[reason %s] publishing %d thinking block(s)", w.ID(), len(thinkingBlocks))
		w.publishThinking(thinkingBlocks)
	} else {
		log.Printf("[reason %s] no thinking blocks in response (content_blocks=%d)", w.ID(), len(resp.Message.Content))
	}

	if len(toolCalls) > 0 {
		busCalls := toolCalls

		if len(busCalls) > 0 {
			// Desanitize tool names first using index-based access so the
			// changes apply to the actual slice elements (for _, tc := range
			// creates copies whose modifications are lost).
			for i := range busCalls {
				busCalls[i].ToolName = desanitizeToolName(w, busCalls[i].ToolName)
			}

			// Track timer call_ids with the current epoch.
			// Only track if the tool actually exists in w.tools — if a worker
			// providing it hasn't announced yet or was removed, skip tracking
			// so stale logic (cancelActiveTickers, handleTimerElapsed) is
			// naturally disabled.
			w.reasonEpoch++
			w.activeTickafters = make(map[string]int)
			w.elapseTickafters = make(map[string]int)
			for _, tc := range busCalls {
				if tc.ToolName == "timer.set_tool_timeout" {
					if _, ok := w.tools[tc.ToolName]; ok {
						w.activeTickafters[tc.ToolCallID] = w.reasonEpoch
					}
				}
				if tc.ToolName == "timer.elapse" {
					if _, ok := w.tools[tc.ToolName]; ok {
						w.elapseTickafters[tc.ToolCallID] = w.reasonEpoch
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
					rc := w.callTracker.Fail(tc.ToolCallID, tc.ToolName, fmt.Sprintf("unknown tool: %s", tc.ToolName))
					w.updatePlaceholder(rc)
					continue
				}
				// Strip the worker ID prefix so the target worker receives
				// the original tool name (e.g. "workspace.bash" → "bash").
				tc.ToolName = strings.TrimPrefix(tc.ToolName, t.Provider+".")
				callsByTarget[t.Provider] = append(callsByTarget[t.Provider], tc)
			}
			for target, calls := range callsByTarget {
				w.callTracker.Request(target, w.ID(), calls, w.currentTraceID)
			}
			w.isReasoning = false
			w.mu.Unlock()
			w.tryReason(ctx)
			return
		}

		w.isReasoning = false
		w.mu.Unlock()
		w.needReason = true
		w.tryReason(ctx)
		return
	}

	// Text-only response.
	w.publishResponse(resp.Message)
	w.publishReasonEnd(resp.Message.StopReason)

	w.isReasoning = false
	w.mu.Unlock()

	// calls tryReason again to catch overlapping events.
	w.tryReason(ctx)
}

func (w *Worker) publishResponse(msg llm.Message) {
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
	evt.TraceID = w.currentTraceID
	_ = w.Bus.Publish(evt)
	log.Printf("[reason %s] published reason.response, text_count=%d", w.ID(), len(texts))
}

func (w *Worker) publishReasonStart() {
	evt := event.New("reason.start", w.ID(), map[string]any{
		"worker_id": w.ID(),
	})
	evt.TraceID = w.currentTraceID
	_ = w.Bus.Publish(evt)
}

func (w *Worker) publishReasonEnd(stopReason string) {
	evt := event.New("reason.end", w.ID(), map[string]any{
		"worker_id":   w.ID(),
		"stop_reason": stopReason,
	})
	evt.TraceID = w.currentTraceID
	_ = w.Bus.Publish(evt)
}

func (w *Worker) publishThinking(blocks []llm.ContentBlock) {
	log.Printf("[reason %s] publishing thinking: %d blocks, total %d chars", w.ID(), len(blocks), len(blocks[0].Text))
	var texts []any
	for _, b := range blocks {
		texts = append(texts, b.Text)
	}
	evt := event.New("reason.thinking", w.ID(), map[string]any{
		"content": texts,
	})
	evt.TraceID = w.currentTraceID
	_ = w.Bus.Publish(evt)
}

// insertPlaceholders adds pending tool_result entries to messages
// for each bus tool call, preserving transcript ordering.
func (w *Worker) insertPlaceholders(calls []llm.ContentBlock) {
	for _, tc := range calls {
		w.messages = append(w.messages, llm.Message{
			Role:       llm.RoleToolResult,
			ToolCallID: tc.ToolCallID,
			ToolName:   tc.ToolName,
			Content:    []llm.ContentBlock{{Type: llm.ContentText, Text: "[pending]"}},
		})
	}
}

func toolDefs(w *Worker, tools []worker.Tool) []llm.ToolDef {
	// Rebuild the sanitized-name mapping.
	w.toolNameMap = make(map[string]string, len(tools))

	out := make([]llm.ToolDef, len(tools))
	for i, t := range tools {
		sane := sanitizeToolName(t.Name)
		w.toolNameMap[sane] = t.Name
		out[i] = llm.ToolDef{
			Name:        sane,
			Description: t.Description,
			Parameters:  t.Parameters,
		}
	}
	return out
}

func sanitizeToolName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

func desanitizeToolName(w *Worker, sane string) string {
	if orig, ok := w.toolNameMap[sane]; ok {
		return orig
	}
	return sane
}
