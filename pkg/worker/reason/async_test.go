package reason

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
)

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

// blockingProvider blocks Complete until its ctx is cancelled or release is
// closed, letting the test hold a reasoning round in flight.
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
	return &llm.CompletionResponse{Message: llm.Message{
		Role: llm.RoleAssistant, StopReason: "stop",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}},
	}}, nil
}
func (p *blockingProvider) CompleteStream(context.Context, *llm.CompletionRequest) (*llm.EventStream, error) {
	return nil, nil
}
func (p *blockingProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

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

// TestAsyncAbortInterruptsReasoning verifies reason() runs on its own
// goroutine: while an LLM call is in flight, the watch loop still processes
// events (an abort), and the abort's cancelReason interrupts the blocked call
// WITHOUT the test having to release the provider.
func TestAsyncAbortInterruptsReasoning(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, ch, _ := startWorker(t, prov)

	ch.in <- event.New("worker.input", "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		select {
		case <-prov.started:
			return true
		default:
			return false
		}
	}, "reasoning to start (LLM call in flight)")

	// Feed an abort while reasoning is blocked. Because reason() is async, the
	// watch loop processes it immediately and cancelReason interrupts the call.
	ch.in <- event.New("worker.abort", "swarm", map[string]any{})
	waitCond(t, 2*time.Second, ch.hasInterrupted, "reason.end(interrupted)")
}

// TestAsyncNoFakeErrorOnInterrupt ensures cancellation does not publish a
// spurious "Error: context canceled" response.
func TestAsyncNoFakeErrorOnInterrupt(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, ch, _ := startWorker(t, prov)

	ch.in <- event.New("worker.input", "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		select {
		case <-prov.started:
			return true
		default:
			return false
		}
	}, "reasoning to start")
	ch.in <- event.New("worker.abort", "swarm", map[string]any{})
	waitCond(t, 2*time.Second, ch.hasInterrupted, "reason.end(interrupted)")

	if ch.hasErrorResponse() {
		t.Fatal("published spurious reason.response after interrupt")
	}
}
