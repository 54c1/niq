package timer

import (
	"encoding/json"
	"time"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
)

// Entry is the opaque handle returned by afterFunc.
type Entry struct {
	t *time.Timer
}

func (e *Entry) Stop() bool {
	if e.t == nil {
		return false
	}
	return e.t.Stop()
}

// afterFunc sets a timer that publishes a timer.elapsed trigger event when it fires.
func afterFunc(
	workerID string,
	bus corebus.EventBusClient,
	timerID, callerID string,
	durationMS int,
	purpose, tickType, traceID string,
) *Entry {
	result, _ := json.Marshal(map[string]any{
		"tick_type":   tickType,
		"purpose":     purpose,
		"duration_ms": durationMS,
	})

	t := time.AfterFunc(time.Duration(durationMS)*time.Millisecond, func() {
		eventType := "timer.timeout"
		if tickType == "reminder" {
			eventType = "timer.reminder"
		}
		evt := event.New(eventType, workerID, map[string]any{
			"timer_id":  timerID,
			"caller_id": callerID,
			"result":    string(result),
		})
		evt.TargetWorkerID = callerID
		evt.TraceID = traceID
		_ = bus.Publish(evt)
	})

	return &Entry{t: t}
}
