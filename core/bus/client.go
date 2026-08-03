package bus

import (
	"context"

	"github.com/54c1/niq/core/event"
)

// EventBusClient is a worker's handle to the bus.
// Subscribe and Receive together deliver events matching registered patterns.
// Publish sends events under this worker's identity
type EventBusClient interface {
	// Subscribe registers interest in one or more event patterns.
	Subscribe(patterns []event.EventPattern) error

	// Unsubscribe removes a previous subscription.
	Unsubscribe(patterns []event.EventPattern) error

	// Publish sends one or more events to the bus.
	Publish(events ...event.Event) error

	// Receive returns a channel that delivers matching events.
	Receive(ctx context.Context) (chan event.Event, error)
}
