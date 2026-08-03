package inprocess

import (
	"context"
	"sync"

	"github.com/54c1/niq/core/event"
)

// BusHandle is the subset of [bussvc.Bus] that InProcessClient needs.
// Defined here to avoid an import cycle: [bussvc.MemoryBus] imports
// inprocess, and inprocess would import bussvc for *Bus — this interface
// breaks the cycle while keeping the full *Bus available at construction.
type BusHandle interface {
	BindChannel(workerID string) (chan event.Event, error)
	Subscribe(workerID string, patterns []event.EventPattern) error
	Unsubscribe(workerID string, patterns []event.EventPattern) error
	Publish(workerID string, events ...event.Event) error
	UnbindChannel(workerID string)
}

// InProcessClient implements [EventBusClient] by delegating to a shared
// BusHandle with a fixed worker identity. It is the standard per-worker bus
// handle for in-process (single-address-space) deployments.
//
// Each InProcessClient owns a private event channel created on construction.
// Subscribe and Unsubscribe operate on that channel; Receive returns it.
// Publish passes the client's workerID to Bus.Publish for identity and ACL
// verification.
type InProcessClient struct {
	bus      BusHandle
	workerID string
	ch       chan event.Event

	mu       sync.Mutex
	patterns []event.EventPattern
	closed   bool
}

// NewInProcessClient creates a client for a pre-registered worker.
// The worker must already be registered with the bus via [Bus.RegisterWorker].
// BindChannel is called internally — the caller does not need to call it
// separately.
func NewClient(bus BusHandle, workerID string) (*InProcessClient, error) {
	ch, err := bus.BindChannel(workerID)
	if err != nil {
		return nil, err
	}
	return &InProcessClient{
		bus:      bus,
		workerID: workerID,
		ch:       ch,
	}, nil
}

// Subscribe registers interest in the given patterns. The patterns are
// checked against the worker's SubscribeAllow; the bus returns an error
// if any pattern is not permitted.
func (c *InProcessClient) Subscribe(patterns []event.EventPattern) error {
	if err := c.bus.Subscribe(c.workerID, patterns); err != nil {
		return err
	}
	c.mu.Lock()
	c.patterns = patterns
	c.mu.Unlock()
	return nil
}

// Unsubscribe removes previously registered patterns.
func (c *InProcessClient) Unsubscribe(patterns []event.EventPattern) error {
	return c.bus.Unsubscribe(c.workerID, patterns)
}

// Publish publishes events to the bus. Every event's WorkerId must equal
// this client's workerID (anti-spoofing), and each event type must be in
// the worker's PublishAllow list.
func (c *InProcessClient) Publish(events ...event.Event) error {
	return c.bus.Publish(c.workerID, events...)
}

// Receive returns the worker's private event channel. The channel is
// created on construction and shared across Subscribe calls — pattern
// registration determines which events land on it, not channel identity.
// When ctx is cancelled the channel is unbound from the bus.
func (c *InProcessClient) Receive(ctx context.Context) (chan event.Event, error) {
	go func() {
		<-ctx.Done()
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return
		}
		c.closed = true
		c.bus.UnbindChannel(c.workerID)
	}()
	return c.ch, nil
}

// WorkerID returns the identity bound to this client.
func (c *InProcessClient) WorkerID() string {
	return c.workerID
}

// Channel returns the private event channel for direct use in tests.
func (c *InProcessClient) Channel() chan event.Event {
	return c.ch
}
