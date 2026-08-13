package host

import (
	"context"
	"fmt"
	"strings"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	programpkg "github.com/54c1/niq/core/program"
	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/ext/service/wsbackend"
	"github.com/54c1/niq/ext/worker/workspace"
	"github.com/54c1/niq/pkg/providercfg"
	inprocess "github.com/54c1/niq/pkg/service/eventbus/transport/inprocess"
	"github.com/54c1/niq/pkg/worker/reason"
)

func (w *HostWorker) handleToolCall(evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	toolName, _ := evt.Payload["name"].(string)
	callerID := evt.WorkerId
	traceID := evt.TraceID

	args, _ := evt.Payload["arguments"].(map[string]any)

	var result string
	var err error

	switch toolName {
	case "spawn":
		result, err = w.handleSpawn(args)
	case "destroy":
		result, err = w.handleDestroy(args)
	default:
		err = fmt.Errorf("unknown tool: %s", toolName)
	}

	if err != nil {
		w.publishFailed(callerID, callID, toolName, err, traceID)
		return
	}
	w.publishCompleted(callerID, callID, toolName, result, traceID)
}

func (w *HostWorker) handleSpawn(args map[string]any) (string, error) {
	typ, _ := args["type"].(string)
	if typ == "" {
		return "", fmt.Errorf("type is required (workspace or reason)")
	}

	switch typ {
	case "workspace":
		return w.spawnWorkspace(args)
	case "reason":
		return w.spawnReason(args)
	default:
		return "", fmt.Errorf("unknown worker type: %s", typ)
	}
}

func (w *HostWorker) spawnWorkspace(args map[string]any) (string, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	id := "ws-" + sanitizeID(path)

	// Register identity.
	if err := w.registry.Register(corebus.Identity{
		WorkerID:       id,
		Type:           "workspace",
		PublishAllow:   []string{"*"},
		SubscribeAllow: []string{"tool.requested", "worker.discover"},
	}); err != nil {
		if !strings.Contains(err.Error(), "already registered") {
			return "", err
		}
	}

	// Create worker side channel via the listener.
	childCh := inprocess.NewWorkerSide(id, w.listener)
	if err := childCh.Connect(context.Background(), "inproc://niq"); err != nil {
		return "", err
	}

	var err error
	err = w.engine.CreateWorker(id, "workspace", func() worker.ManagedWorker {
		return workspace.New(workspace.Config{
			ID:      id,
			Bus:     childCh,
			Backend: wsbackend.NewEmbeddedBackend(path),
		})
	})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return fmt.Sprintf(`{"worker_id":"%s","status":"already_exists"}`, id), nil
		}
		return "", err
	}
	return fmt.Sprintf(`{"worker_id":"%s","status":"created"}`, id), nil
}

func (w *HostWorker) spawnReason(args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}

	provider, _ := args["provider"].(string)
	apiKey, _ := args["api_key"].(string)
	baseURL, _ := args["base_url"].(string)
	model, _ := args["model"].(string)

	// Parse programs from args.
	programs := parsePrograms(args, id)

	// Parse event subscriptions from args.
	events := parseEvents(args)

	// Register identity.
	subAllow := []string{
		"tool.completed", "tool.failed", "tool.rejected",
		"worker.ready", "worker.gone", "worker.discover", "worker.abort",
		"timer.timeout", "timer.reminder", "worker.input", "tool.requested",
	}
	if err := w.registry.Register(corebus.Identity{
		WorkerID:       id,
		Type:           "reason",
		PublishAllow:   []string{"*"},
		SubscribeAllow: subAllow,
	}); err != nil {
		if !strings.Contains(err.Error(), "already registered") {
			return "", err
		}
	}

	// Create worker side channel via the listener.
	childCh := inprocess.NewWorkerSide(id, w.listener)
	if err := childCh.Connect(context.Background(), "inproc://niq"); err != nil {
		return "", err
	}

	var err error
	err = w.engine.CreateWorker(id, "reason", func() worker.ManagedWorker {
		return reason.NewWorker(reason.Config{
			ID:              id,
			Provider:        providerFromArgs(provider, apiKey, baseURL, model),
			Programs:        programs,
			EventConverters: events,
			Bus:             childCh,
		})
	})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return fmt.Sprintf(`{"worker_id":"%s","status":"already_exists"}`, id), nil
		}
		return "", err
	}
	return fmt.Sprintf(`{"worker_id":"%s","status":"created"}`, id), nil
}

func providerFromArgs(provider, apiKey, baseURL, model string) llm.LLMProvider {
	if provider != "" {
		if p, ok := providercfg.Find(provider); ok {
			return providercfg.BuildWithOverrides(p, apiKey, baseURL, model)
		}
		if p, ok := providercfg.FindByType(provider); ok {
			return providercfg.BuildWithOverrides(p, apiKey, baseURL, model)
		}
		return providercfg.Build(providercfg.Provider{
			Type:    provider,
			APIKey:  apiKey,
			BaseURL: baseURL,
			Model:   model,
		})
	}

	if p, ok := providercfg.Default(); ok {
		return providercfg.BuildWithOverrides(p, apiKey, baseURL, model)
	}

	return providercfg.Build(providercfg.Provider{
		Type:    "deepseek",
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	})
}

// parsePrograms extracts a simplified program list from spawn args.
// Each program is a flat object with name, content_type, description, and content.
// If no programs are provided, a default instruction program is created.
func parsePrograms(args map[string]any, workerID string) []programpkg.Program {
	raw, ok := args["programs"].([]any)
	if !ok || len(raw) == 0 {
		// Fallback: create a default instruction program.
		return []programpkg.Program{
			{
				Meta: programpkg.Meta{
					Name:        workerID + "-instruction",
					ContentType: programpkg.ContentTypeInstruction,
				},
			},
		}
	}

	progs := make([]programpkg.Program, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		ctStr, _ := m["content_type"].(string)
		if name == "" || ctStr == "" {
			continue
		}

		var ct programpkg.ContentType
		switch ctStr {
		case "instruction":
			ct = programpkg.ContentTypeInstruction
		case "playbook":
			ct = programpkg.ContentTypePlaybook
		default:
			continue
		}

		desc, _ := m["description"].(string)
		content, _ := m["content"].(string)

		progs = append(progs, programpkg.Program{
			Meta: programpkg.Meta{
				Name:        name,
				ContentType: ct,
				Description: desc,
			},
			EntryContent: programpkg.ProgramContent{
				Content: content,
			},
		})
	}

	if len(progs) == 0 {
		// Fallback: create a default instruction program.
		progs = append(progs, programpkg.Program{
			Meta: programpkg.Meta{
				Name:        workerID + "-instruction",
				ContentType: programpkg.ContentTypeInstruction,
			},
		})
	}

	return progs
}

// parseEvents extracts event type subscriptions from spawn args.
// Each event type becomes an EventConverter using the default converter.
func parseEvents(args map[string]any) []reason.EventConverter {
	raw, ok := args["events"].([]any)
	if !ok {
		return nil
	}

	handlers := make([]reason.EventConverter, 0, len(raw))
	for _, r := range raw {
		evtType, ok := r.(string)
		if !ok || evtType == "" {
			continue
		}
		handlers = append(handlers, reason.EventConverter{
			Pattern:   event.NewPattern(event.EventType(evtType)),
			Converter: reason.DefaultConverter,
		})
	}
	return handlers
}

func (w *HostWorker) handleDestroy(args map[string]any) (string, error) {
	workerID, _ := args["worker_id"].(string)
	if workerID == "" {
		return "", fmt.Errorf("worker_id is required")
	}
	if workerID == "host" || workerID == "timer" || workerID == "hiw" {
		return "", fmt.Errorf("cannot destroy system worker: %s", workerID)
	}

	if err := w.engine.DestroyWorker(workerID); err != nil {
		return "", err
	}
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerGone, w.ID(), map[string]any{
		"worker_id": workerID,
	}))
	return fmt.Sprintf(`{"worker_id":"%s","status":"destroyed"}`, workerID), nil
}

// ── Bus publishing ──

func (w *HostWorker) publishReady() {
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "host",
		"tools": []map[string]any{
			{
				"name":        "spawn",
				"description": "Create a new worker. Type determines worker kind (workspace or reason).",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"type": map[string]any{
							"type": "string", "enum": []any{"workspace", "reason"},
							"description": "Worker type to spawn.",
						},
						"id": map[string]any{
							"type":        "string",
							"description": "Worker ID (required for reason workers).",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Workspace directory path (required for workspace workers).",
						},
						"programs": map[string]any{
							"type":        "array",
							"description": "List of programs for the worker. Each program has name, content_type (instruction|playbook), and optionally description and content. For instruction programs, content is the full instruction text. For playbook programs, content is optional (can be loaded later via program.load).",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"name": map[string]any{
										"type":        "string",
										"description": "Program name.",
									},
									"content_type": map[string]any{
										"type":        "string",
										"enum":        []any{"instruction", "playbook"},
										"description": "instruction = binding constraints; playbook = procedural steps.",
									},
									"description": map[string]any{
										"type":        "string",
										"description": "Optional description of what this program does.",
									},
									"content": map[string]any{
										"type":        "string",
										"description": "Program content. Required for instruction programs, optional for playbooks.",
									},
								},
								"required": []any{"name", "content_type"},
							},
						},
						"events": map[string]any{
							"type":        "array",
							"description": "Optional list of event types this worker should subscribe to (e.g. [\"pr.ready\", \"review.requested\"]). Received events are converted to LLM messages using the default converter.",
							"items": map[string]any{
								"type": "string",
							},
						},
					},
					"required": []any{"type"},
				},
			},
			{
				"name":        "destroy",
				"description": "Destroy a worker by its ID.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"worker_id": map[string]any{
							"type":        "string",
							"description": "ID of the worker to destroy.",
						},
					},
					"required": []any{"worker_id"},
				},
			},
		},
	}))
}

func (w *HostWorker) publishCompleted(callerID, callID, toolName, result, traceID string) {
	evt := event.New(event.TypeToolCompleted, w.ID(), map[string]any{
		"call_id": callID,
		"name":    toolName,
		"result":  result,
	})
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

func (w *HostWorker) publishFailed(callerID, callID, toolName string, err error, traceID string) {
	evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
		"call_id": callID,
		"name":    toolName,
		"error":   err.Error(),
	})
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

func sanitizeID(path string) string {
	s := strings.TrimPrefix(path, "/")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
