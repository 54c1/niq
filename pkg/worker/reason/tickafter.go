// tickafter.go — tickafter timeout lifecycle.
//
// tickafter is a timer tool provided by TimerWorker. The reason worker calls
// it as a normal bus tool (via tool.requested → tool.completed). It returns
// immediately with status "scheduled". The actual timeout fires later as a
// timer.elapsed trigger event.
//
// Two protection mechanisms ensure correctness:
//
//	reasonEpoch — incremented per reason(). Tracks which "batch" a tickafter
//	belongs to. Stale timer.elapsed events from prior batches are discarded.
//
//	activeTickafters — map of tickafter call_id → epoch. Entries are added
//	in reason() when tickafter calls are produced. They are removed in two
//	places:
//	  - cancelActiveTickers(): all non-tickafter tools resolved, send
//	    timer.cancel events and delete entries
//	  - timer.elapsed route: timeout fired, consume the entry
//	When timer.elapsed arrives and the entry is missing (cancelled) or the
//	epoch doesn't match (prior batch), the event is silently discarded.
//
// Normally activeTickafters holds at most one entry per reason batch.
package reason

import (
	"github.com/54c1/niq/core/event"
)

// cancelActiveTickers sends timer.cancel to the timer worker for all
// active tickafter calls in the current batch. Called when all tools have
// resolved — the timeout is no longer needed.
// The request is targeted to the timer worker (not broadcast) to avoid
// confusing other workers that subscribe to tool.requested.
func (w *Worker) cancelActiveTickers() {
	// Look up the timer worker ID from the set_tool_timeout tool definition.
	targetID := ""
	if t, ok := w.tools["timer.set_tool_timeout"]; ok {
		targetID = t.Provider
	}
	if targetID == "" {
		// Fallback: the timer worker is always named "timer" in the
		// default configuration, but prefer the dynamic lookup above.
		targetID = "timer"
	}
	for timerID := range w.activeTickafters {
		evt := event.New("tool.requested", w.ID(), map[string]any{
			"call_id":   timerID + "-cancel",
			"name":      "cancel",
			"arguments": map[string]any{"timer_id": timerID},
		})
		evt.TargetWorkerID = targetID
		_ = w.Bus.Publish(evt)
		delete(w.activeTickafters, timerID)
	}
}
