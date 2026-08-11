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

func (w *Worker) allTools() []worker.Tool {
	tools := make([]worker.Tool, 0, len(w.tools))
	for _, t := range w.tools {
		tools = append(tools, t)
	}
	return tools
}

// EventPublish describes a single event type that a worker publishes.
type EventPublish struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// initBuiltinTools adds tools natively handled by this worker (e.g.
// publish_message, list_workers) to w.tools with Provider set to w.ID()
// so they route back to self via the bus.
func (w *Worker) initBuiltinTools() {
	for _, t := range []worker.Tool{
		{
			Name:        "publish_message",
			Description: "Publish a message to a specific worker on the bus.",
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
	} {
		t.Provider = w.ID()
		w.tools[t.Name] = t
	}
}

// handleWorkerReady learns a worker's tools and published events from its
// worker.ready announcement, updating the known tool set and publishes map.
func (w *Worker) handleWorkerReady(evt event.Event) {
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
				w.tools[prefixed] = worker.Tool{
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
			w.publishes[workerID] = eventsRaw
			log.Printf("[reason %s] received %d event(s) from %s", w.ID(), len(eventsRaw), workerID)
		}
	}
}

// handleWorkerGone forgets a departed worker's tools and published events.
func (w *Worker) handleWorkerGone(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)

	for name, tool := range w.tools {
		if tool.Provider == workerID {
			delete(w.tools, name)
		}
	}
	delete(w.publishes, workerID)
	log.Printf("[reason %s] removed tools and events from %s", w.ID(), workerID)
}

// handleToolRequest processes tool.requested events targeting this worker.
func (w *Worker) handleToolRequest(evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	toolName, _ := evt.Payload["name"].(string)
	callerID := evt.WorkerId

	args, _ := evt.Payload["arguments"].(map[string]any)

	switch toolName {
	case "publish_message":
		w.handlePublishMessage(callID, toolName, callerID, args)
	case "list_workers":
		w.handleListWorkers(callID, toolName, callerID, args)
	default:
		evt := event.New("tool.failed", w.ID(), map[string]any{
			"call_id": callID, "name": toolName,
			"error": fmt.Sprintf("unknown tool: %s", toolName),
		})
		_ = w.Channel.Send(context.Background(), evt, callerID)
	}
}

func (w *Worker) handlePublishMessage(callID, toolName, callerID string, args map[string]any) {
	target, _ := args["target"].(string)
	text, _ := args["text"].(string)
	if target == "" || text == "" {
		evt := event.New("tool.failed", w.ID(), map[string]any{
			"call_id": callID, "name": toolName,
			"error": "target and text are required",
		})
		_ = w.Channel.Send(context.Background(), evt, callerID)
		return
	}

	msgEvt := event.New("worker.input", w.ID(), map[string]any{
		"text": text,
	})
	msgEvt.TraceID = w.currentTraceID
	_ = w.Channel.Send(context.Background(), msgEvt, target)

	w.publishSuccess(callID, toolName, callerID,
		fmt.Sprintf("message sent to %s", target))
}

// handleListWorkers returns all known workers with their tools and published
// events, grouped by provider. It also triggers a worker.discover to refresh
// the cache for the next call.
func (w *Worker) handleListWorkers(callID, toolName, callerID string, args map[string]any) {
	// Trigger re-discovery so the next call gets fresh data.
	_ = w.Channel.Broadcast(context.Background(), event.New("worker.discover", w.ID(), nil))

	// Aggregate tools and publishes by provider.
	type workerInfo struct {
		WorkerID  string         `json:"worker_id"`
		Tools     []worker.Tool  `json:"tools,omitempty"`
		Publishes []EventPublish `json:"publishes,omitempty"`
	}

	providers := make(map[string]*workerInfo)

	// Collect tools grouped by provider.
	for _, tool := range w.tools {
		info, ok := providers[tool.Provider]
		if !ok {
			info = &workerInfo{
				WorkerID:  tool.Provider,
				Publishes: w.publishes[tool.Provider],
			}
			providers[tool.Provider] = info
		}
		info.Tools = append(info.Tools, tool)
	}

	// Collect providers that only publish events (no tools).
	for provider, events := range w.publishes {
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
		w.publishFail(callID, toolName, callerID, fmt.Sprintf("marshal error: %v", err))
		return
	}

	w.publishSuccess(callID, toolName, callerID, string(b))
	log.Printf("[reason %s] list_workers → %d workers", w.ID(), len(result))
}

func (w *Worker) publishSuccess(callID, toolName, callerID, result string) {
	evt := event.New("tool.completed", w.ID(), map[string]any{
		"call_id": callID, "name": toolName,
		"result": result,
	})
	evt.TraceID = w.currentTraceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

func (w *Worker) publishFail(callID, toolName, callerID, errMsg string) {
	evt := event.New("tool.failed", w.ID(), map[string]any{
		"call_id": callID, "name": toolName,
		"error": errMsg,
	})
	evt.TraceID = w.currentTraceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// toolDefs builds the LLM tool definitions from the known tools, rebuilding the
// sanitized-name mapping (dot → underscore) so tool calls can be desanitized.
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

// publishToolRequests sends a directed tool.requested event for each tool call
// to its target worker. The tracker only manages the pending map; the caller
// is responsible for delivering the requests to the bus.
func (w *Worker) publishToolRequests(target, callerID string, calls []llm.ContentBlock, traceID string) {
	for _, tc := range calls {
		var argsMap map[string]any
		if tc.ToolArguments != "" {
			json.Unmarshal([]byte(tc.ToolArguments), &argsMap)
		}
		evt := event.New("tool.requested", callerID, map[string]any{
			"worker_id": callerID,
			"call_id":   tc.ToolCallID,
			"name":      tc.ToolName,
			"arguments": argsMap,
		})
		evt.TraceID = traceID
		_ = w.Channel.Send(context.Background(), evt, target)
	}
}
