package event

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync/atomic"
	"time"
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
	Type string
}

// NewPattern is a convenience constructor for the common single-type case.
func NewPattern(typ string) EventPattern {
	return EventPattern{Type: typ}
}

// Event is the core data unit of the niq event bus.
type Event struct {
	ID             string         `json:"id"`
	Type           string         `json:"type"`
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
func New(typ, workerId string, payload map[string]any) Event {
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
