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
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	w.workerTools["workspace.bash"] = worker.Tool{Name: "workspace.bash", Provider: "workspace"}
	tools := []worker.Tool{w.workerTools["workspace.bash"]}
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
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})

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
	if _, ok := w.workerTools["workspace.bash"]; !ok {
		t.Fatalf("expected workspace.bash tool, got %+v", keys(w.workerTools))
	}
	if _, ok := w.workerPublishEvents["workspace"]; !ok {
		t.Fatal("expected published events for workspace")
	}

	gone := event.New(event.TypeWorkerGone, "host", map[string]any{"worker_id": "workspace"})
	w.handleWorkerGone(gone)
	if _, ok := w.workerTools["workspace.bash"]; ok {
		t.Fatal("tool should be removed after worker.gone")
	}
	if _, ok := w.workerPublishEvents["workspace"]; ok {
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
