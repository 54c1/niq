package reason

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
)

// ── mock bus channel ────────────────────────────────────────────────────────

// mockChannel is a minimal WorkerSideChannel for tests: it delivers events the
// test feeds in, and records everything the worker publishes/sends.
type mockChannel struct {
	in  chan event.Event
	mu  sync.Mutex
	out []event.Event
}

func newMockChannel() *mockChannel { return &mockChannel{in: make(chan event.Event, 16)} }

func (m *mockChannel) ID() string { return "mock" }
func (m *mockChannel) Send(_ context.Context, evt event.Event, _ ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.out = append(m.out, evt)
	return nil
}
func (m *mockChannel) Broadcast(_ context.Context, evt event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.out = append(m.out, evt)
	return nil
}
func (m *mockChannel) Receive(_ context.Context) (<-chan event.Event, error) { return m.in, nil }
func (m *mockChannel) Close() error                                          { return nil }

// eventsOf returns all recorded events of the given type.
func (m *mockChannel) eventsOf(typ event.EventType) []event.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []event.Event
	for _, e := range m.out {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (m *mockChannel) hasInterrupted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.out {
		if e.Type == "reason.end" {
			if sr, _ := e.Payload["stop_reason"].(string); sr == "interrupted" {
				return true
			}
		}
	}
	return false
}

func (m *mockChannel) hasErrorResponse() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.out {
		if e.Type == "reason.response" {
			return true
		}
	}
	return false
}

// ── LLM providers ───────────────────────────────────────────────────────────

// staticProvider returns a fixed completion immediately. Used for tests that
// just need a deterministic reasoning round to complete.
type staticProvider struct {
	msg llm.Message
}

func (p *staticProvider) Complete(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{Message: p.msg}, nil
}
func (p *staticProvider) CompleteStream(_ context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextEnd{})
	es.End(p.msg)
	return es, nil
}
func (p *staticProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

// blockingProvider blocks CompleteStream until its ctx is cancelled or release
// is closed, letting a test hold a reasoning round in flight.
type blockingProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingProvider) Complete(ctx context.Context, _ *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
	}
	return &llm.CompletionResponse{Message: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}, nil
}
func (p *blockingProvider) CompleteStream(ctx context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	p.once.Do(func() { close(p.started) })
	es := llm.NewEventStream()
	go func() {
		select {
		case <-ctx.Done():
			es.Abort(ctx.Err())
		case <-p.release:
			es.Push(llm.EventTextStart{})
			es.Push(llm.EventTextDelta{Delta: "hi"})
			es.Push(llm.EventTextEnd{})
			es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
				Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}}})
		}
	}()
	return es, nil
}
func (p *blockingProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

// ── helpers ─────────────────────────────────────────────────────────────────

func waitCond(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

func startWorker(t *testing.T, prov llm.LLMProvider) (*Worker, *mockChannel, context.CancelFunc) {
	t.Helper()
	ch := newMockChannel()
	w := NewWorker(Config{ID: "r1", Provider: prov, Bus: ch})
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		w.Stop()
		cancel()
	})
	return w, ch, cancel
}
