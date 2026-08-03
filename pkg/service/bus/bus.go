// Package bus defines the shared event bus that is the sole communication medium
// for all workers in a niq swarm.
package bus

import (
	"log"

	"fmt"
	"strings"
	"sync"

	"github.com/54c1/niq/core/event"
)

// identityEntry holds the registered state for a single worker known to the bus.
// It is populated by the control-plane management API and read by the data-plane
// (Publish, Subscribe, Route) at enforcement points.
type identityEntry struct {
	WorkerID       string
	PublishAllow   []string // event types this worker may publish (exact match)
	SubscribeAllow []string // event patterns this worker may subscribe to
	Ch             chan event.Event
}

// subscriberEntry represents a single pattern subscription bound to a
// specific worker's channel. It is created by Subscribe and consumed by Route.
type subscriberEntry struct {
	Ch       chan event.Event
	WorkerID string // bound at Subscribe time; used by Route for TargetWorkerID matching
	Pattern  string // the type pattern being subscribed to
}

// Bus is the core event routing engine. It holds the worker identity
// registry and subscription tables, and enforces publish/subscribe
// authorization at every enforcement point.
//
// Bus is transport-agnostic: InProcessClient, UDS servers, and SSE servers
// all call the same Bus methods. The Bus itself does not define a transport
// abstraction — transports are built on top of Bus.
//
// Management API methods (RegisterWorker, UpdateWorkerACL, UnregisterWorker)
// are privileged operations not exposed through the bus data plane. In
// MemoryBus scenarios they are called directly by the swarm assembly layer;
// in NetworkBus scenarios they are gated behind an OpenAPI key check on the
// transport server.
type Bus struct {
	mu          sync.RWMutex
	identities  map[string]*identityEntry // WorkerID → registered identity
	subscribers []subscriberEntry         // flat list for Route to iterate

	onEvent func(event.Event) // optional hook, called for every published/routed event
}

// NewBus creates an empty Bus ready for worker registration.
func NewBus() *Bus {
	return &Bus{
		identities: make(map[string]*identityEntry),
	}
}

// OnEvent registers a callback invoked for every event published via
// Publish or Route. Used by MemoryBus for persistence.
func (b *Bus) OnEvent(fn func(event.Event)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onEvent = fn
}

// ── Management API (control plane) ──

// RegisterWorker creates a new identity entry on the bus and returns an error
// if the worker ID has already been registered. This is a control-plane
// operation: in-process callers access it directly; networked callers must
// present a valid OpenAPI key to the transport server.
func (b *Bus) RegisterWorker(id string, pubAllow, subAllow []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.identities[id]; exists {
		return fmt.Errorf("bus: worker %s already registered", id)
	}
	b.identities[id] = &identityEntry{
		WorkerID:       id,
		PublishAllow:   pubAllow,
		SubscribeAllow: subAllow,
	}
	return nil
}

// UpdateWorkerACL replaces the allow lists for an existing worker.
func (b *Bus) UpdateWorkerACL(id string, pubAllow, subAllow []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry, ok := b.identities[id]
	if !ok {
		return fmt.Errorf("bus: worker %s not found", id)
	}
	entry.PublishAllow = pubAllow
	entry.SubscribeAllow = subAllow
	return nil
}

// UnregisterWorker removes a worker from the identity table and cleans up
// all of its subscriptions.
func (b *Bus) UnregisterWorker(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.identities[id]; !ok {
		return fmt.Errorf("bus: worker %s not found", id)
	}
	delete(b.identities, id)

	// Remove all subscriber entries belonging to this worker.
	var kept []subscriberEntry
	for _, s := range b.subscribers {
		if s.WorkerID != id {
			kept = append(kept, s)
		}
	}
	b.subscribers = kept
	return nil
}

// BindChannel creates an event channel for a registered worker and attaches
// it to the identity entry. It returns an error if the worker is not
// registered or already has a channel bound.
func (b *Bus) BindChannel(workerID string) (chan event.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry, ok := b.identities[workerID]
	if !ok {
		return nil, fmt.Errorf("bus: worker %s not registered", workerID)
	}
	if entry.Ch != nil {
		return nil, fmt.Errorf("bus: worker %s already has a bound channel", workerID)
	}
	ch := make(chan event.Event, 64)
	entry.Ch = ch
	return ch, nil
}

// UnbindChannel detaches the event channel from a worker and removes all
// subscription entries that route to it.
func (b *Bus) UnbindChannel(workerID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry, ok := b.identities[workerID]
	if !ok {
		return
	}
	ch := entry.Ch
	entry.Ch = nil

	if ch == nil {
		return
	}
	var kept []subscriberEntry
	for _, s := range b.subscribers {
		if s.Ch != ch {
			kept = append(kept, s)
		}
	}
	b.subscribers = kept
}

// Channel returns the bound channel for a worker, or nil if not bound.
func (b *Bus) Channel(workerID string) chan event.Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if entry, ok := b.identities[workerID]; ok {
		return entry.Ch
	}
	return nil
}

// ── Data plane ──

// Subscribe registers patterns for a worker. It checks each pattern against
// the worker's SubscribeAllow and, on success, creates a subscriberEntry
// that carries the worker's ID and channel.
func (b *Bus) Subscribe(workerID string, patterns []event.EventPattern) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry, ok := b.identities[workerID]
	if !ok {
		return fmt.Errorf("bus: worker %s not registered", workerID)
	}
	if entry.Ch == nil {
		return fmt.Errorf("bus: worker %s has no bound channel; call BindChannel first", workerID)
	}

	for _, p := range patterns {
		if !patternAllowed(p.Type, entry.SubscribeAllow) {
			return fmt.Errorf("bus: subscribe %q not allowed for worker %s", p.Type, workerID)
		}
		// Deduplicate: skip if this pattern+channel combination already exists.
		dup := false
		for _, s := range b.subscribers {
			if s.Ch == entry.Ch && s.Pattern == p.Type {
				dup = true
				break
			}
		}
		if !dup {
			b.subscribers = append(b.subscribers, subscriberEntry{
				Ch:       entry.Ch,
				WorkerID: workerID,
				Pattern:  p.Type,
			})
		}
	}
	return nil
}

// Unsubscribe removes previously registered patterns for a worker.
func (b *Bus) Unsubscribe(workerID string, patterns []event.EventPattern) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var kept []subscriberEntry
	removeSet := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		removeSet[p.Type] = true
	}
	for _, s := range b.subscribers {
		if s.WorkerID == workerID && removeSet[s.Pattern] {
			continue
		}
		kept = append(kept, s)
	}
	b.subscribers = kept
	return nil
}

// Publish verifies the publisher's identity and ACL, then routes each event.
// publisherID is the authenticated identity of the caller; every event's
// WorkerId must match it (anti-spoofing). Event types are checked against
// the publisher's PublishAllow list.
//
// After routing, if the event is not an event.delivered itself, a synthetic
// event.delivered is emitted so that monitoring subscribers (e.g. HIW) can
// observe which workers received the event.
func (b *Bus) Publish(publisherID string, events ...event.Event) error {
	b.mu.RLock()
	entry, ok := b.identities[publisherID]
	b.mu.RUnlock()
	if !ok {
		return fmt.Errorf("bus: unknown publisher: %s", publisherID)
	}

	for i := range events {
		evt := events[i]
		if evt.WorkerId != publisherID {
			return fmt.Errorf("bus: publish spoofing: event.WorkerId=%q but authenticated as %s", evt.WorkerId, publisherID)
		}
		if !typeInAllowList(evt.Type, entry.PublishAllow) {
			log.Printf("[bus] Publish REJECTED: type=%q worker=%s allow=%v", evt.Type, publisherID, entry.PublishAllow)
			return fmt.Errorf("bus: publish %q not allowed for worker %s", evt.Type, publisherID)
		}
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for i := range events {
		evt := events[i]
		if b.onEvent != nil {
			b.onEvent(evt)
		}
		recipients := b.routeLocked(evt)
		if evt.Type != "event.delivered" && len(recipients) > 0 {
			b.emitDeliveryEventLocked(evt, recipients)
		}
	}
	return nil
}

// Route delivers an event to all matching subscribers. Unlike Publish,
// Route performs no identity or ACL checks — it is pure metadata matching.
// It is intended for use by transport servers that have already verified
// the publisher's identity through their own handshake.
func (b *Bus) Route(evt event.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.onEvent != nil {
		b.onEvent(evt)
	}
	recipients := b.routeLocked(evt)
	if evt.Type != "event.delivered" && len(recipients) > 0 {
		b.emitDeliveryEventLocked(evt, recipients)
	}
}

// routeLocked assumes b.mu is held (read lock is sufficient).
// Returns the list of worker IDs that matched the subscription.
func (b *Bus) routeLocked(evt event.Event) []string {
	var recipients []string
	seen := make(map[chan event.Event]bool, len(b.subscribers))
	for _, s := range b.subscribers {
		// Dimension 1: type match.
		if !TypeMatches(s.Pattern, evt.Type) {
			continue
		}
		// Dimension 2: if TargetWorkerID is set, only deliver to the target,
		// the publisher, or any subscriber with a wildcard pattern (*).
		if evt.TargetWorkerID != "" && s.Pattern != "*" &&
			s.WorkerID != evt.TargetWorkerID && s.WorkerID != evt.WorkerId {
			continue
		}
		// Deduplicate: skip if this channel already received this event
		// (multiple patterns from the same worker may overlap).
		if seen[s.Ch] {
			continue
		}
		seen[s.Ch] = true
		recipients = append(recipients, s.WorkerID)

		select {
		case s.Ch <- evt:
		default:
		}
	}
	return recipients
}

// emitDeliveryEventLocked creates and routes an event.delivered event.
// Must be called with b.mu held (read lock is sufficient).
func (b *Bus) emitDeliveryEventLocked(original event.Event, recipients []string) {
	deliveryEvt := event.New("event.delivered", original.WorkerId, map[string]any{
		"event_id":   original.ID,
		"event_type": original.Type,
		"source":     original.WorkerId,
		"target":     original.TargetWorkerID,
		"recipients": recipients,
	})
	if b.onEvent != nil {
		b.onEvent(deliveryEvt)
	}
	b.routeLocked(deliveryEvt)
}

// ── Helpers ──

func patternAllowed(typ string, allow []string) bool {
	for _, a := range allow {
		if a == "*" {
			return true
		}
		if a == typ {
			return true
		}
		if prefix, ok := strings.CutSuffix(a, ".*"); ok && (typ == prefix || strings.HasPrefix(typ, prefix+".")) {
			return true
		}
		if TypeMatches(a, typ) {
			return true
		}
	}
	return false
}

// typeInAllowList returns true when the given type appears in the list
// (exact match only for PublishAllow).
func typeInAllowList(typ string, allow []string) bool {
	for _, a := range allow {
		if a == "*" {
			return true
		}
		if a == typ {
			return true
		}
		if prefix, ok := strings.CutSuffix(a, ".*"); ok && (typ == prefix || strings.HasPrefix(typ, prefix+".")) {
			return true
		}
		if a == typ {
			return true
		}
	}
	return false
}
