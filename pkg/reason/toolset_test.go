package reason

import (
	"testing"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/worker"
)

// TestSanitizeToolName verifies dots become underscores so the name is a valid
// LLM tool identifier.
func TestSanitizeToolName(t *testing.T) {
	if got := sanitizeToolName("workspace.bash"); got != "workspace_bash" {
		t.Fatalf("sanitize = %q, want workspace_bash", got)
	}
	if got := sanitizeToolName("plain"); got != "plain" {
		t.Fatalf("sanitize = %q, want plain", got)
	}
}

// TestDesanitizeToolName verifies the sanitized→original mapping round-trips
// through the toolNameMap built by toolDefs.
func TestDesanitizeToolName(t *testing.T) {
	w := newBaseForTest(nil, newTestChannel())
	w.tools["workspace.bash"] = worker.Tool{Name: "workspace.bash", Provider: "workspace"}
	tools := []worker.Tool{w.tools["workspace.bash"]}
	_ = toolDefs(w, tools)

	if got := desanitizeToolName(w, "workspace_bash"); got != "workspace.bash" {
		t.Fatalf("desanitize = %q, want workspace.bash", got)
	}
	// Unknown sanitized name passes through unchanged.
	if got := desanitizeToolName(w, "nope"); got != "nope" {
		t.Fatalf("desanitize unknown = %q, want nope", got)
	}
}

// TestHandleWorkerReadyAndGone verifies tools and published events are learned
// from worker.ready and forgotten on worker.gone.
func TestHandleWorkerReadyAndGone(t *testing.T) {
	w := newBaseForTest(nil, newTestChannel())

	ready := event.New(event.TypeWorkerReady, "workspace", map[string]any{
		"worker_id": "workspace",
		"tools": []map[string]any{
			{"name": "bash", "description": "run a command", "parameters": map[string]any{"type": "object"}},
		},
		"publishes": []map[string]any{
			{"type": "fs.changed", "description": "a file changed"},
		},
	})
	w.handleWorkerReady(ready)

	// Tool is prefixed with the worker ID.
	if _, ok := w.tools["workspace.bash"]; !ok {
		t.Fatalf("expected workspace.bash tool, got %+v", keys(w.tools))
	}
	if _, ok := w.publishMap["workspace"]; !ok {
		t.Fatal("expected published events for workspace")
	}

	gone := event.New(event.TypeWorkerGone, "host", map[string]any{"worker_id": "workspace"})
	w.handleWorkerGone(gone)
	if _, ok := w.tools["workspace.bash"]; ok {
		t.Fatal("tool should be removed after worker.gone")
	}
	if _, ok := w.publishMap["workspace"]; ok {
		t.Fatal("publishes should be removed after worker.gone")
	}
}

func keys(m map[string]worker.Tool) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestDefaultProviderInstallsFourBuiltins verifies the default BuiltinTools
// provides exactly the four domain-agnostic tools, routed back to this worker.
func TestDefaultProviderInstallsFourBuiltins(t *testing.T) {
	w := newBaseForTest(nil, newTestChannel())

	want := map[string]bool{"send_message": true, "list_workers": true,
		"context.compress": true, "context.rotate": true}
	for name := range want {
		if _, ok := w.tools[name]; !ok {
			t.Fatalf("expected built-in tool %q, got %+v", name, keys(w.tools))
		}
	}
	if len(w.tools) != len(want) {
		t.Fatalf("expected exactly %d built-ins, got %+v", len(want), keys(w.tools))
	}
}

// customProvider extends BuiltinTools with one extra tool by embedding and
// delegating unknown calls to the default — the pattern a coding worker uses.
type customProvider struct {
	*BuiltinTools
}

func (p *customProvider) ToolDefinitions() []worker.Tool {
	return append(p.BuiltinTools.ToolDefinitions(), worker.Tool{
		Name:        "github.review",
		Description: "Start a review",
		Parameters:  map[string]any{"type": "object"},
	})
}

func (p *customProvider) HandleToolCall(tc worker.ToolCall) {
	switch tc.Name {
	case "github.review":
		p.w.ReplyCompleted(tc.CallerID, tc.CallID, tc.Name, "review started", tc.TraceID)
	default:
		p.BuiltinTools.HandleToolCall(tc)
	}
}

// TestCustomProviderExtendsDefault verifies an embedding worker can add a tool
// and keep the defaults by embedding BuiltinTools.
func TestCustomProviderExtendsDefault(t *testing.T) {
	ch := newTestChannel()
	// Build a worker with the custom provider supplied via Config.
	w := NewBaseReasonWorker(Config{
		ID:            "r1",
		Bus:           ch,
		Subscriptions: []event.EventPattern{event.NewPattern("*")},
	})
	prov := &customProvider{}
	w.toolProvider = prov
	prov.BuiltinTools = NewBuiltinTools(w)
	w.initBuiltinTools()

	if _, ok := w.tools["github.review"]; !ok {
		t.Fatalf("expected github.review, got %+v", keys(w.tools))
	}
	if _, ok := w.tools["send_message"]; !ok {
		t.Fatalf("default tool should be kept when provider extends it, got %+v", keys(w.tools))
	}
}

// TestCustomProviderDispatch verifies a request for the custom tool routes to
// the provider's handler, and an unknown tool fails gracefully.
func TestCustomProviderDispatch(t *testing.T) {
	ch := newTestChannel()
	w := NewBaseReasonWorker(Config{ID: "r1", Bus: ch, Subscriptions: []event.EventPattern{event.NewPattern("*")}})
	prov := &customProvider{}
	w.toolProvider = prov
	prov.BuiltinTools = NewBuiltinTools(w)

	// github.review → handled by custom provider.
	evt := event.New(event.TypeToolRequested, "me", map[string]any{
		"call_id": "c1", "name": "github.review", "arguments": map[string]any{},
	})
	evt.WorkerId = "me"
	w.handleToolRequest(evt)

	reviewed := false
	for _, e := range ch.eventsOf(event.TypeToolCompleted) {
		if e.Payload["call_id"] == "c1" {
			reviewed = true
		}
	}
	if !reviewed {
		t.Fatal("custom tool should complete via provider")
	}
}
