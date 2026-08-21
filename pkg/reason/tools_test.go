package reason

import (
	"testing"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/worker"
)

// TestEncodeToolNameBuiltin verifies a tool whose provider is this worker (or
// empty) keeps a bare name, with inner dots turned to underscores, so it is a
// valid LLM tool identifier.
func TestEncodeToolNameBuiltin(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())
	if got := encodeToolName(w, worker.Tool{Name: "context.compress", Provider: w.ID()}); got != "context_compress" {
		t.Fatalf("builtin encode = %q, want context_compress", got)
	}
	if got := encodeToolName(w, worker.Tool{Name: "send_message", Provider: ""}); got != "send_message" {
		t.Fatalf("bare encode = %q, want send_message", got)
	}
}

// TestEncodeToolNameExternal verifies a tool backed by another worker becomes
// provider__name, so the worker/tool boundary is unambiguous.
func TestEncodeToolNameExternal(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())
	if got := encodeToolName(w, worker.Tool{Name: "bash", Provider: "workspace"}); got != "workspace__bash" {
		t.Fatalf("external encode = %q, want workspace__bash", got)
	}
	if got := encodeToolName(w, worker.Tool{Name: "set_tool_timeout", Provider: "timer"}); got != "timer__set_tool_timeout" {
		t.Fatalf("timer encode = %q, want timer__set_tool_timeout", got)
	}
}

// TestHandleWorkerReadyAndGone verifies tools and published events are learned
// from worker.ready and forgotten on worker.gone.
func TestHandleWorkerReadyAndGone(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())

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

	// Tool is prefixed with the worker ID (encoded as provider__name).
	if _, ok := w.tools["workspace__bash"]; !ok {
		t.Fatalf("expected workspace__bash tool, got %+v", keys(w.tools))
	}
	if _, ok := w.publishMap["workspace"]; !ok {
		t.Fatal("expected published events for workspace")
	}

	gone := event.New(event.TypeWorkerGone, "host", map[string]any{"worker_id": "workspace"})
	w.handleWorkerGone(gone)
	if _, ok := w.tools["workspace__bash"]; ok {
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
	w := newTestWorker(nil, newTestChannel())

	want := map[string]bool{"send_message": true, "list_workers": true,
		"context_compress": true, "context_rotate": true}
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
	case "github_review":
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

	if _, ok := w.tools["github_review"]; !ok {
		t.Fatalf("expected github_review, got %+v", keys(w.tools))
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
		"call_id": "c1", "name": "github_review", "arguments": map[string]any{},
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
