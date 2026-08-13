package reason

import (
	"testing"
	"time"

	"github.com/54c1/niq/core/event"
)

// TestAsyncAbortInterruptsReasoning verifies reason() runs on its own
// goroutine: while an LLM call is in flight, the watch loop still processes
// events (an abort), and the abort's cancelReason interrupts the blocked call
// WITHOUT the test having to release the provider.
func TestAsyncAbortInterruptsReasoning(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, ch, _ := startWorker(t, prov)

	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
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
	ch.in <- event.New(event.TypeWorkerAbort, "swarm", map[string]any{})
	waitCond(t, 2*time.Second, ch.hasInterrupted, "reason.end(interrupted)")
}

// TestAsyncNoFakeErrorOnInterrupt ensures cancellation does not publish a
// spurious "Error: context canceled" response.
func TestAsyncNoFakeErrorOnInterrupt(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, ch, _ := startWorker(t, prov)

	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		select {
		case <-prov.started:
			return true
		default:
			return false
		}
	}, "reasoning to start")
	ch.in <- event.New(event.TypeWorkerAbort, "swarm", map[string]any{})
	waitCond(t, 2*time.Second, ch.hasInterrupted, "reason.end(interrupted)")

	if ch.hasErrorResponse() {
		t.Fatal("published spurious reason.response after interrupt")
	}
}
