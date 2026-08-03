// tracker.go — tool call lifecycle tracking.
//
//	ToolCallTracker: publish tool.requested, track pending calls,
//	match tool.completed/failed/rejected events, resolve/timeout/cancel.
package reason

import (
	"encoding/json"
	"fmt"
	"sync"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
)

// ToolReqStatus tracks the lifecycle stage of a tool call.
type ToolReqStatus string

const (
	ToolPending     ToolReqStatus = "pending"
	ToolCompleted   ToolReqStatus = "completed"
	ToolFailed      ToolReqStatus = "failed"
	ToolRejected    ToolReqStatus = "rejected"
	ToolTimedOut    ToolReqStatus = "timedout"
	ToolInterrupted ToolReqStatus = "interrupted"
)

// PendingCall tracks a single tool call through its event-bus lifecycle.
type ToolCall struct {
	TickType  string // set_timer tick_type ("" for normal tool calls)
	CallID    string
	Name      string
	Arguments string
	Status    ToolReqStatus

	// Populated by Handle on resolution.
	Result   string
	ErrorMsg string
}

// IsError reports whether this call reached a non-success terminal status.
func (tc *ToolCall) IsError() bool {
	return tc.Status == ToolFailed || tc.Status == ToolRejected || tc.Status == ToolTimedOut || tc.Status == ToolInterrupted
}

// Rejected reports whether this call was rejected by an interceptor.
func (tc *ToolCall) Rejected() bool { return tc.Status == ToolRejected }

// TimedOut reports whether this call timed out.
func (tc *ToolCall) TimedOut() bool    { return tc.Status == ToolTimedOut }
func (tc *ToolCall) Interrupted() bool { return tc.Status == ToolInterrupted }

// ToolCallTracker manages the lifecycle of tool calls on the event bus.
// It publishes tool.requested events and tracks pending calls; when
// tool.completed, tool.failed, or tool.rejected events arrive, it
// resolves the corresponding call.
type ToolCallTracker struct {
	bus     corebus.EventBusClient
	mu      sync.Mutex
	pending map[string]*ToolCall // keyed by callID
}

// NewToolCallTracker creates a ToolCallTracker backed by the given bus client.
func NewToolCallTracker(bus corebus.EventBusClient) *ToolCallTracker {
	return &ToolCallTracker{
		bus:     bus,
		pending: make(map[string]*ToolCall),
	}
}

// Fail marks a tool call as failed immediately without publishing to the
// bus. Used when the LLM calls an unknown tool — broadcasting would
// confuse other workers that subscribe to tool.requested.
func (m *ToolCallTracker) Fail(callID, toolName, errMsg string) *ToolCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	rc := &ToolCall{
		CallID:   callID,
		Name:     toolName,
		Status:   ToolFailed,
		ErrorMsg: errMsg,
	}
	m.pending[callID] = rc
	return rc
}

// Request publishes tool.requested events for each tool call and begins
// tracking their lifecycle.
// targetID is the worker the tool call is addressed to (derived from the
// tool name prefix, e.g. "ws-docs" from "ws-docs.read_file").
// callerID is the requesting worker making the request.
func (m *ToolCallTracker) Request(targetID, callerID string, toolCalls []llm.ContentBlock, traceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tc := range toolCalls {
		if tc.Type != llm.ContentToolCall {
			continue
		}
		m.pending[tc.ToolCallID] = &ToolCall{
			CallID:    tc.ToolCallID,
			Name:      tc.ToolName,
			Arguments: tc.ToolArguments,
			Status:    ToolPending,
		}
		var argsMap map[string]any
		if tc.ToolArguments != "" {
			json.Unmarshal([]byte(tc.ToolArguments), &argsMap)
		}
		evt := event.New("tool.requested", callerID, map[string]any{
			"worker_id": callerID,
			"call_id":   tc.ToolCallID,
			"name":      tc.ToolName,
			"arguments": argsMap,
		})
		evt.TargetWorkerID = targetID
		evt.TraceID = traceID
		_ = m.bus.Publish(evt)
	}
}

// Handle processes a tool result event. Returns the resolved call if the
// event matches a pending tool call, or nil otherwise.
func (m *ToolCallTracker) handleResponse(evt event.Event) (*ToolCall, bool) {
	callID, _ := evt.Payload["call_id"].(string)
	if callID == "" {
		return nil, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	run, ok := m.pending[callID]
	if !ok {
		return nil, false
	}

	// Late result for an already-resolved (timed-out or interrupted) call —
	// upgrade to completed. The placeholder gets overwritten with the real result.
	if run.Status == ToolTimedOut || run.Status == ToolInterrupted {
		if evt.Type == "tool.completed" {
			run.Status = ToolCompleted
			run.Result = fmt.Sprintf("%v", evt.Payload["result"])
		}
		return nil, false // upgrade: do not update placeholder
	}
	// Still pending — normal path.
	if run.Status != ToolPending {
		return nil, false
	}

	run.Result = ""
	run.ErrorMsg = ""

	switch evt.Type {
	case "tool.completed":
		run.Status = ToolCompleted
		run.Result = fmt.Sprintf("%v", evt.Payload["result"])
	case "tool.failed":
		run.Status = ToolFailed
		if errMsg, ok := evt.Payload["error"].(string); ok {
			run.ErrorMsg = errMsg
		}
	case "tool.rejected":
		run.Status = ToolRejected
		if reason, ok := evt.Payload["reason"].(string); ok {
			run.ErrorMsg = reason
		}
	default:
		return nil, false
	}

	delete(m.pending, callID)
	return run, true
}

// Resolved reports whether all pending tool calls have reached a terminal status.

// TimeoutPending marks all still-pending tool calls as timed out.
// Already-resolved calls (completed, rejected, already timed out) are skipped.
// Returns the list of calls that were marked as timed out.
func (m *ToolCallTracker) TimeoutPending() []*ToolCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var tcs []*ToolCall
	for _, call := range m.pending {
		if call.Status != ToolPending {
			continue
		}
		call.Status = ToolTimedOut
		call.ErrorMsg = "timed out"
		tcs = append(tcs, call)
		// Kept in pending — late results match and overwrite naturally.
	}
	return tcs
}

// CancelAll marks all pending tool calls as rejected and removes them.
func (m *ToolCallTracker) CancelAll() []*ToolCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var tcs []*ToolCall
	for cid, call := range m.pending {
		call.Status = ToolRejected
		call.ErrorMsg = "cancelled"
		tcs = append(tcs, call)
		delete(m.pending, cid)
	}
	return tcs
}

// InterruptPending marks all still-pending tool calls as interrupted —
// the reasoner chose to proceed without waiting for their results.
// Already-resolved calls are skipped.
func (m *ToolCallTracker) InterruptPending() []*ToolCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var tcs []*ToolCall
	for _, call := range m.pending {
		if call.Status != ToolPending {
			continue
		}
		call.Status = ToolInterrupted
		call.ErrorMsg = "interrupted: reasoner proceeded without waiting"
		tcs = append(tcs, call)
		// Kept in pending — late results will match via call_id
		// and overwrite the placeholder naturally.
	}
	return tcs
}

func (m *ToolCallTracker) Resolved() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, call := range m.pending {
		if call.Status == ToolPending {
			return false
		}
	}
	return true
}

// Timeout resolves a pending call as timed out. Returns nil if the call
// is no longer pending (normal response arrived before the timeout).
func (m *ToolCallTracker) Timeout(callID, name string) *ToolCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	run, ok := m.pending[callID]
	if !ok {
		return nil
	}

	run.Status = ToolTimedOut
	run.ErrorMsg = "tool call timed out"
	delete(m.pending, callID)
	return run
}
