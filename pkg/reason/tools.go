// worker presence tracking and tool aggregation.
//
// handleWorkerReady / handleWorkerGone: learn/forget a worker's tools & events
// allTools: return all known tools (discovered + built-in)
// handleToolRequest: process tool.requested for built-in tools
// toolDefs / sanitize: build LLM tool definitions (dot → underscore names)
// publishToolRequests: send tool.requested to target workers
package reason

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/core/worker"
)

func (w *BaseReasonWorker) allTools() []worker.Tool {
	tools := make([]worker.Tool, 0, len(w.Tools))
	for _, t := range w.Tools {
		tools = append(tools, t)
	}
	return tools
}

// initBuiltinTools adds tools natively handled by this worker (e.g.
// send_message, list_workers) to w.Tools with Provider set to w.ID()
// so they route back to self via the bus.
func (w *BaseReasonWorker) initBuiltinTools() {
	for _, t := range []worker.Tool{
		{
			Name:        "send_message",
			Description: "Send a message to a specific worker on the bus.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{"type": "string", "description": "Target worker ID"},
					"text":   map[string]any{"type": "string", "description": "Message text"},
				},
				"required": []any{"target", "text"},
			},
		},
		{
			Name:        "list_workers",
			Description: "List all available workers and their capabilities. Returns tools and events published by each worker. Call this first, then set a 2-second timer, then call again to get the latest worker information after re-discovery.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "context.compress",
			Description: "Compact your own conversation history: older messages are replaced by a summary, the most recent messages are kept. Call this when the system reminds you about context budget, or when earlier history is no longer needed in full.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"directive": map[string]any{"type": "string",
						"description": "Optional focus for the summary, e.g. what must be preserved."},
				},
			},
		},
		{
			Name:        "context.rotate",
			Description: "Rotate your context: summarize the current transcript as a carried digest and start a fresh context containing only that digest. Use for periodic/discrete tasks when previous rounds are no longer relevant, or when starting a new topic.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"carry": map[string]any{"type": "string",
						"description": "Optional instruction for what to carry into the digest (conclusions, open items, references)."},
				},
			},
		},
	} {
		t.Provider = w.ID()
		w.Tools[t.Name] = t
	}
}

// handleWorkerReady learns a worker's tools and published events from its
// worker.ready announcement, updating the known tool set and publishes map.
func (w *BaseReasonWorker) handleWorkerReady(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)

	// Parse tools.
	b, err := json.Marshal(evt.Payload["tools"])
	if err == nil {
		var toolsRaw []map[string]any
		if err := json.Unmarshal(b, &toolsRaw); err == nil {
			for _, m := range toolsRaw {
				name, _ := m["name"].(string)
				desc, _ := m["description"].(string)
				params, _ := m["parameters"].(map[string]any)
				if name == "" {
					continue
				}
				prefixed := workerID + "." + name
				w.Tools[prefixed] = worker.Tool{
					Name:        prefixed,
					Description: desc,
					Parameters:  params,
					Provider:    workerID,
				}
			}
			log.Printf("[reason %s] received %d tool(s) from %s", w.ID(), len(toolsRaw), workerID)
		}
	}

	// Parse publishes.
	b, err = json.Marshal(evt.Payload["publishes"])
	if err == nil {
		var eventsRaw []EventPublish
		if err := json.Unmarshal(b, &eventsRaw); err == nil && len(eventsRaw) > 0 {
			w.PublishMap[workerID] = eventsRaw
			log.Printf("[reason %s] received %d event(s) from %s", w.ID(), len(eventsRaw), workerID)
		}
	}
}

// handleWorkerGone forgets a departed worker's tools and published events.
func (w *BaseReasonWorker) handleWorkerGone(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)

	for name, tool := range w.Tools {
		if tool.Provider == workerID {
			delete(w.Tools, name)
		}
	}
	delete(w.PublishMap, workerID)
	log.Printf("[reason %s] removed tools and events from %s", w.ID(), workerID)
}

// handleToolRequest processes tool.requested events targeting this worker.
func (w *BaseReasonWorker) handleToolRequest(evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	toolName, _ := evt.Payload["name"].(string)
	callerID := evt.WorkerId

	args, _ := evt.Payload["arguments"].(map[string]any)

	switch toolName {
	case "send_message":
		w.handleSendMessage(callID, toolName, callerID, args)
	case "list_workers":
		w.handleListWorkers(callID, toolName, callerID, args)
	case "context.compress":
		w.handleCompactTool(callID, toolName, callerID, args, "compress")
	case "context.rotate":
		w.handleCompactTool(callID, toolName, callerID, args, "rotate")
	default:
		evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
			"call_id": callID, "name": toolName,
			"error": fmt.Sprintf("unknown tool: %s", toolName),
		})
		_ = w.Channel.Send(context.Background(), evt, callerID)
	}
}

func (w *BaseReasonWorker) handleSendMessage(callID, toolName, callerID string, args map[string]any) {
	target, _ := args["target"].(string)
	text, _ := args["text"].(string)
	if target == "" || text == "" {
		evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
			"call_id": callID, "name": toolName,
			"error": "target and text are required",
		})
		_ = w.Channel.Send(context.Background(), evt, callerID)
		return
	}

	msgEvt := event.New(event.TypeWorkerInput, w.ID(), map[string]any{
		"text": text,
	})
	msgEvt.TraceID = w.currentTraceID
	_ = w.Channel.Send(context.Background(), msgEvt, target)

	w.sendSuccess(callID, toolName, callerID,
		fmt.Sprintf("message sent to %s", target))
}

// handleListWorkers returns all known workers with their tools and published
// events, grouped by provider. It also triggers a worker.discover to refresh
// the cache for the next call.
func (w *BaseReasonWorker) handleListWorkers(callID, toolName, callerID string, args map[string]any) {
	// Trigger re-discovery so the next call gets fresh data.
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerDiscover, w.ID(), nil))

	// Aggregate tools and publishes by provider.
	type workerInfo struct {
		WorkerID  string         `json:"worker_id"`
		Tools     []worker.Tool  `json:"tools,omitempty"`
		Publishes []EventPublish `json:"publishes,omitempty"`
	}

	providers := make(map[string]*workerInfo)

	// Collect tools grouped by provider.
	for _, tool := range w.Tools {
		info, ok := providers[tool.Provider]
		if !ok {
			info = &workerInfo{
				WorkerID:  tool.Provider,
				Publishes: w.PublishMap[tool.Provider],
			}
			providers[tool.Provider] = info
		}
		info.Tools = append(info.Tools, tool)
	}

	// Collect providers that only publish events (no tools).
	for provider, events := range w.PublishMap {
		if _, ok := providers[provider]; !ok {
			providers[provider] = &workerInfo{
				WorkerID:  provider,
				Publishes: events,
			}
		}
	}

	result := make([]workerInfo, 0, len(providers))
	for _, info := range providers {
		result = append(result, *info)
	}

	b, err := json.Marshal(result)
	if err != nil {
		w.sendFail(callID, toolName, callerID, fmt.Sprintf("marshal error: %v", err))
		return
	}

	w.sendSuccess(callID, toolName, callerID, string(b))
	log.Printf("[reason %s] list_workers → %d workers", w.ID(), len(result))
}

func (w *BaseReasonWorker) sendSuccess(callID, toolName, callerID, result string) {
	evt := event.New(event.TypeToolCompleted, w.ID(), map[string]any{
		"call_id": callID, "name": toolName,
		"result": result,
	})
	evt.TraceID = w.currentTraceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

func (w *BaseReasonWorker) sendFail(callID, toolName, callerID, errMsg string) {
	evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
		"call_id": callID, "name": toolName,
		"error": errMsg,
	})
	evt.TraceID = w.currentTraceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// handleCompactTool serves compress / context.rotate: both run the
// compaction asynchronously (the summary is an LLM call) on their own goroutine
// and reply via the bus when done. rotate turns the page (keepTail=2: keeps
// this call's assistant tool_call + [pending] placeholder so the result stays
// visible to the model and the pairing invariant holds); compress keeps the
// configured tail.
func (w *BaseReasonWorker) handleCompactTool(callID, toolName, callerID string, args map[string]any, kind string) {
	// Capture the trace under the caller's lock; the goroutine runs unlocked.
	traceID := w.currentTraceID

	go func() {
		directive := w.compactDirective()
		if extra, _ := args["directive"].(string); kind == "compress" && extra != "" {
			directive = directive + "\nCaller focus: " + extra
		}
		if carry, _ := args["carry"].(string); kind == "rotate" && carry != "" {
			directive = directive + "\nCarry into the new episode: " + carry
		}

		keepTail := w.keepTail
		if kind == "rotate" {
			// Keep the closing tool call's assistant message + its [pending]
			// placeholder so the result stays visible to the model after
			// replacement (and the pairing invariant holds).
			keepTail = 2
		}

		err := w.compactTranscript(context.Background(), directive, keepTail)

		evt := event.New(event.TypeToolCompleted, w.ID(), map[string]any{
			"call_id": callID, "name": toolName,
			"result": compactResultText(kind, err),
		})
		evt.TraceID = traceID
		_ = w.Channel.Send(context.Background(), evt, callerID)
	}()
}

func compactResultText(kind string, err error) string {
	if err != nil {
		return fmt.Sprintf("compaction failed: %v", err)
	}
	if kind == "rotate" {
		return "episode rotated: history compacted into a carried digest; fresh context started"
	}
	return "history compacted: older messages replaced by a digest; recent messages kept"
}

// toolDefs builds the LLM tool definitions from the known tools, rebuilding the
// sanitized-name mapping (dot → underscore) so tool calls can be desanitized.
func toolDefs(w *BaseReasonWorker, tools []worker.Tool) []llm.ToolDef {
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

func desanitizeToolName(w *BaseReasonWorker, sane string) string {
	if orig, ok := w.toolNameMap[sane]; ok {
		return orig
	}
	return sane
}

// sendToolRequests sends a directed tool.requested event for each tool call
// to its target worker. The tracker only manages the pending map; the caller
// is responsible for delivering the requests to the bus.
func (w *BaseReasonWorker) sendToolRequests(target, callerID string, calls []llm.ContentBlock, traceID string) {
	for _, tc := range calls {
		var argsMap map[string]any
		if tc.ToolArguments != "" {
			json.Unmarshal([]byte(tc.ToolArguments), &argsMap)
		}
		evt := event.New(event.TypeToolRequested, callerID, map[string]any{
			"worker_id": callerID,
			"call_id":   tc.ToolCallID,
			"name":      tc.ToolName,
			"arguments": argsMap,
		})
		evt.TraceID = traceID
		_ = w.Channel.Send(context.Background(), evt, target)
	}
}
