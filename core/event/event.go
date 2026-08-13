package event

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync/atomic"
	"time"
)

// EventType identifies an event's type on the bus.
type EventType string

// Universal bus protocol event types. Every worker must use these exact
// names to interoperate — a worker implementing a capability must publish/
// subscribe with the matching event type. Worker-specific domain events
// (e.g. reason.*, timer.*) are not listed here; they are defined by their
// owning worker.
const (
	// Worker presence and lifecycle.
	TypeWorkerReady    EventType = "worker.ready"
	TypeWorkerGone     EventType = "worker.gone"
	TypeWorkerDiscover EventType = "worker.discover"
	TypeWorkerInput    EventType = "worker.input"
	TypeWorkerAbort    EventType = "worker.abort"

	// Tool invocation lifecycle.
	TypeToolRequested EventType = "tool.requested"
	TypeToolCancel    EventType = "tool.cancel"
	TypeToolCompleted EventType = "tool.completed"
	TypeToolFailed    EventType = "tool.failed"
	TypeToolRejected  EventType = "tool.rejected"
	TypeToolPartial   EventType = "tool.partial"
)

// EventStatus represents an event's lifecycle stage.
type EventStatus string

const (
	StatusCreated   EventStatus = "created"
	StatusRouted    EventStatus = "routed"
	StatusDelivered EventStatus = "delivered"
)

// EventPattern describes which events a Worker subscribes to.
type EventPattern struct {
	// Type is the event type to match.
	// Supports exact match, "*" (any), and "Prefix.*" (prefix) wildcards.
	Type EventType
}

// NewPattern is a convenience constructor for the common single-type case.
func NewPattern(typ EventType) EventPattern {
	return EventPattern{Type: typ}
}

// Event is the core data unit of the niq event bus.
type Event struct {
	ID             string         `json:"id"`
	Type           EventType      `json:"type"`
	Status         EventStatus    `json:"status"`
	Payload        map[string]any `json:"payload"`
	WorkerId       string         `json:"worker_id"`
	TargetWorkerID string         `json:"target_worker_id,omitempty"`
	TraceID        string         `json:"trace_id,omitempty"`
	SpecVersion    string         `json:"specversion,omitempty"`
	DataSchema     string         `json:"dataschema,omitempty"`
	Timestamp      int64          `json:"timestamp"`
	Recipients     []string       `json:"recipients,omitempty"` // populated by engine during routing
}

// New creates a new Event with defaults.
func New(typ EventType, workerId string, payload map[string]any) Event {
	return Event{
		ID:          newID(),
		Type:        typ,
		WorkerId:    workerId,
		Timestamp:   time.Now().Unix(),
		Payload:     payload,
		SpecVersion: "niq/1.0",
		Status:      StatusCreated,
	}
}

var seq atomic.Uint64

func newID() string {
	b := make([]byte, 4)
	rand.Read(b)
	n := seq.Add(1)
	return hex.EncodeToString(b) + "_" + strconv.FormatUint(n, 10)
}
