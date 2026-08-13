package program

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/program"
)

// handleToolCall dispatches tool.requested events to the appropriate handler.
func (w *Worker) handleToolCall(ctx context.Context, evt event.Event) {
	callID, _ := evt.Payload["call_id"].(string)
	toolName, _ := evt.Payload["name"].(string)
	callerID := evt.WorkerId
	args, _ := evt.Payload["arguments"].(map[string]any)

	switch toolName {
	case "program.search":
		w.handleSearch(ctx, callID, callerID, args)
	case "program.load":
		w.handleLoad(ctx, callID, callerID, args)
	case "program.register":
		w.handleRegister(ctx, callID, callerID, args)
	case "program.delete":
		w.handleDelete(ctx, callID, callerID, args)
	default:
		w.publishFail(callID, toolName, callerID, fmt.Sprintf("unknown tool: %s", toolName))
	}
}

// handleSearch handles program.search tool calls.
func (w *Worker) handleSearch(ctx context.Context, callID, callerID string, args map[string]any) {
	query, _ := args["query"].(string)
	ctStr, _ := args["content_type"].(string)

	var ct program.ContentType
	switch ctStr {
	case "instruction":
		ct = program.ContentTypeInstruction
	case "playbook":
		ct = program.ContentTypePlaybook
	}

	progs, err := w.search(query, ct)
	if err != nil {
		w.publishFail(callID, "program.search", callerID, fmt.Sprintf("search error: %v", err))
		return
	}

	// Build a readable result: name, content_type, description, tags, locked.
	type resultItem struct {
		Name        string   `json:"name"`
		ContentType string   `json:"content_type"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Locked      bool     `json:"locked"`
	}
	results := make([]resultItem, len(progs))
	for i, p := range progs {
		results[i] = resultItem{
			Name:        p.Name,
			ContentType: string(p.ContentType),
			Description: p.Description,
			Tags:        p.Tags,
			Locked:      p.Locked,
		}
	}

	b, err := json.Marshal(results)
	if err != nil {
		w.publishFail(callID, "program.search", callerID, fmt.Sprintf("marshal error: %v", err))
		return
	}

	w.publishSuccess(callID, "program.search", callerID, string(b))
	log.Printf("[program] search query=%q ct=%q → %d results", query, ctStr, len(results))
}

// handleLoad handles program.load tool calls.
func (w *Worker) handleLoad(ctx context.Context, callID, callerID string, args map[string]any) {
	progName, _ := args["program"].(string)
	contentPath, _ := args["path"].(string)

	if progName == "" || contentPath == "" {
		w.publishFail(callID, "program.load", callerID, "program and path are required")
		return
	}

	// Read from the backend.
	fullPath := joinPath(progName, contentPath)
	raw, err := w.backend.Read(ctx, fullPath)
	if err != nil {
		w.publishFail(callID, "program.load", callerID, fmt.Sprintf("read %s: %v", fullPath, err))
		return
	}

	// For markdown files, strip frontmatter if present.
	_, body, _ := parseFrontmatter(raw)
	if body != "" {
		raw = body
	}

	w.publishSuccess(callID, "program.load", callerID, raw)
	log.Printf("[program] load %s/%s → backend (%d chars)", progName, contentPath, len(raw))
}

// handleRegister handles program.register tool calls.
// Programs registered via this tool are always created as unlocked.
// The Locked flag can only be set through the backend (writing to disk directly)
// — meta-capabilities cannot create locked programs.
func (w *Worker) handleRegister(ctx context.Context, callID, callerID string, args map[string]any) {
	name, _ := args["name"].(string)
	ctStr, _ := args["content_type"].(string)
	desc, _ := args["description"].(string)
	content, _ := args["content"].(string)

	// Parse tags from args.
	var tags []string
	if tagsRaw, ok := args["tags"].([]any); ok {
		for _, t := range tagsRaw {
			if s, ok := t.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	if name == "" || ctStr == "" || content == "" {
		w.publishFail(callID, "program.register", callerID, "name, content_type, and content are required")
		return
	}

	// Check if the program already exists and is locked.
	if existing, err := w.get(name); err == nil && existing.Locked {
		w.publishFail(callID, "program.register", callerID,
			fmt.Sprintf("cannot modify locked program: %q", name))
		return
	}

	var ct program.ContentType
	switch ctStr {
	case "instruction":
		ct = program.ContentTypeInstruction
	case "playbook":
		ct = program.ContentTypePlaybook
	default:
		w.publishFail(callID, "program.register", callerID, fmt.Sprintf("invalid content_type: %s", ctStr))
		return
	}

	// Programs registered via tool are always unlocked.
	// Locked programs can only be created by writing to the backend directly.
	fullContent := fmt.Sprintf("---\nname: %s\ncontent_type: %s\ndescription: %s\ntags: [%s]\n---\n\n%s",
		name, ctStr, desc, strings.Join(tags, ", "), content)

	entryPath := joinPath(name, "PROGRAM.md")
	if err := w.backend.Write(ctx, entryPath, fullContent); err != nil {
		w.publishFail(callID, "program.register", callerID, fmt.Sprintf("write failed: %v", err))
		return
	}

	prog := &program.Program{
		Meta: program.Meta{
			Name:        name,
			ContentType: ct,
			Description: desc,
			Tags:        tags,
			Locked:      false, // always unlocked when created via tool
		},
	}

	if err := w.register(prog); err != nil {
		w.publishFail(callID, "program.register", callerID, fmt.Sprintf("register failed: %v", err))
		return
	}

	w.publishSuccess(callID, "program.register", callerID, fmt.Sprintf("program %q registered", name))
	log.Printf("[program] register: %s (%s)", name, ct)
}

// handleDelete handles program.delete tool calls.
// Locked programs cannot be deleted via this tool.
func (w *Worker) handleDelete(ctx context.Context, callID, callerID string, args map[string]any) {
	name, _ := args["name"].(string)

	if name == "" {
		w.publishFail(callID, "program.delete", callerID, "name is required")
		return
	}

	// Check if the program exists and is locked.
	existing, err := w.get(name)
	if err != nil {
		w.publishFail(callID, "program.delete", callerID, fmt.Sprintf("program %q not found", name))
		return
	}
	if existing.Locked {
		w.publishFail(callID, "program.delete", callerID,
			fmt.Sprintf("cannot delete locked program: %q", name))
		return
	}

	// Remove the program directory from the backend.
	// We use the program name as the directory path.
	if err := w.backend.Remove(ctx, name); err != nil {
		w.publishFail(callID, "program.delete", callerID, fmt.Sprintf("delete failed: %v", err))
		return
	}

	// Remove from the in-memory cache.
	w.pMu.Lock()
	delete(w.programs, name)
	w.pMu.Unlock()

	w.publishSuccess(callID, "program.delete", callerID, fmt.Sprintf("program %q deleted", name))
	log.Printf("[program] delete: %s", name)
}

// ── Helpers ──

func (w *Worker) publishSuccess(callID, toolName, callerID, result string) {
	evt := event.New(event.TypeToolCompleted, w.ID(), map[string]any{
		"call_id": callID,
		"name":    toolName,
		"result":  result,
	})
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

func (w *Worker) publishFail(callID, toolName, callerID, errMsg string) {
	evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
		"call_id": callID,
		"name":    toolName,
		"error":   errMsg,
	})
	_ = w.Channel.Send(context.Background(), evt, callerID)
}
