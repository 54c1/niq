package bus

import (
	"context"
	"testing"
	"time"

	"github.com/54c1/niq/core/event"
	inprocesspkg "github.com/54c1/niq/pkg/service/bus/transport/inprocess"
)

func newTestBus() *Bus {
	return NewBus()
}

// register helper creates a worker and returns a bound InProcessClient.
func registerHelper(t *testing.T, b *Bus, id string, pubAllow, subAllow []string) *inprocesspkg.InProcessClient {
	t.Helper()
	if err := b.RegisterWorker(id, pubAllow, subAllow); err != nil {
		t.Fatalf("RegisterWorker(%s): %v", id, err)
	}
	c, err := inprocesspkg.NewClient(b, id)
	if err != nil {
		t.Fatalf("NewInProcessClient(%s): %v", id, err)
	}
	return c
}

// ── Identity registration ──

func TestBus_RegisterWorker(t *testing.T) {
	b := newTestBus()

	err := b.RegisterWorker("w1", []string{"a"}, []string{"b"})
	if err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	// Duplicate registration must fail.
	err = b.RegisterWorker("w1", []string{}, []string{})
	if err == nil {
		t.Fatal("expected duplicate registration error, got nil")
	}
}

func TestBus_UnregisterWorker(t *testing.T) {
	b := newTestBus()
	_ = b.RegisterWorker("w1", []string{"e"}, []string{"*"})

	// Subscribe through an InProcessClient so there are subscriber entries.
	c, err := inprocesspkg.NewClient(b, "w1")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Subscribe([]event.EventPattern{{Type: "e"}})

	b.UnregisterWorker("w1")

	// Publish should fail — identity is gone.
	err = b.Publish("w1", event.New("e", "w1", nil))
	if err == nil {
		t.Fatal("expected 'unknown publisher' error after UnregisterWorker")
	}

	// Subscriber entries should be cleaned up.
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subscribers {
		if s.WorkerID == "w1" {
			t.Fatal("subscriber entry for unregistered worker still present")
		}
	}
}

// ── Publish authorization ──

func TestBus_Publish_IdentityVerified(t *testing.T) {
	b := newTestBus()
	_ = registerHelper(t, b, "w1", []string{"e1"}, []string{"*"})

	// Correct identity: w1 publishes an event with WorkerId=w1.
	err := b.Publish("w1", event.New("e1", "w1", nil))
	if err != nil {
		t.Fatalf("expected Publish to succeed: %v", err)
	}

	// Spoofing: w1 tries to publish as w2.
	err = b.Publish("w1", event.New("e1", "w2", nil))
	if err == nil {
		t.Fatal("expected spoofing error, got nil")
	}
}

func TestBus_Publish_ACLEnforced(t *testing.T) {
	b := newTestBus()
	_ = registerHelper(t, b, "w1", []string{"e1"}, []string{"*"})

	// w1 publishes e1 — allowed.
	err := b.Publish("w1", event.New("e1", "w1", nil))
	if err != nil {
		t.Fatalf("expected e1 to be allowed: %v", err)
	}

	// w1 publishes e2 — not in PublishAllow.
	err = b.Publish("w1", event.New("e2", "w1", nil))
	if err == nil {
		t.Fatal("expected ACL error for e2, got nil")
	}
}

// ── Subscribe authorization ──

func TestBus_Subscribe_ACLEnforced(t *testing.T) {
	b := newTestBus()
	_ = b.RegisterWorker("w1", []string{"*"}, []string{"a.*"})

	c, err := inprocesspkg.NewClient(b, "w1")
	if err != nil {
		t.Fatal(err)
	}

	// "a.b" matches "a.*" — allowed.
	err = c.Subscribe([]event.EventPattern{{Type: "a.b"}})
	if err != nil {
		t.Fatalf("expected Subscribe a.b to succeed: %v", err)
	}

	// "b.c" does NOT match "a.*" — denied.
	err = c.Subscribe([]event.EventPattern{{Type: "b.c"}})
	if err == nil {
		t.Fatal("expected Subscribe ACL error for b.c, got nil")
	}
}

// ── Route: TargetWorkerID matching ──

func TestBus_Route_TargetWorkerID(t *testing.T) {
	b := newTestBus()
	c1 := registerHelper(t, b, "w1", []string{"e1"}, []string{"e1"})
	c2 := registerHelper(t, b, "w2", []string{"e2"}, []string{"e1"})

	_ = c1.Subscribe([]event.EventPattern{{Type: "e1"}})
	_ = c2.Subscribe([]event.EventPattern{{Type: "e1"}})

	// Directed event to w2 — all subscribers with matching type should receive it.
	evt := event.New("e1", "w1", nil)
	evt.TargetWorkerID = "w2"

	// Publish as w1 (which only has "e1" in PublishAllow).
	err := b.Publish("w1", evt)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// w1 should ALSO receive — directed delivery no longer excludes the publisher.
	select {
	case received := <-c1.Channel():
		if received.TargetWorkerID != "w2" {
			t.Fatalf("expected TargetWorkerID=w2, got %s", received.TargetWorkerID)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("w1 did not receive directed event")
	}

	// w2 SHOULD receive.
	select {
	case received := <-c2.Channel():
		if received.TargetWorkerID != "w2" {
			t.Fatalf("expected TargetWorkerID=w2, got %s", received.TargetWorkerID)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("w2 did not receive directed event")
	}
}

// ── Route: broadcast (no TargetWorkerID) ──

func TestBus_Route_Broadcast(t *testing.T) {
	b := newTestBus()
	c1 := registerHelper(t, b, "w1", []string{"e1"}, []string{"e1"})
	c2 := registerHelper(t, b, "w2", []string{"e2"}, []string{"e1"})

	_ = c1.Subscribe([]event.EventPattern{{Type: "e1"}})
	_ = c2.Subscribe([]event.EventPattern{{Type: "e1"}})

	// Broadcast (no TargetWorkerID) — both should receive.
	err := b.Publish("w1", event.New("e1", "w1", nil))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	for _, tc := range []struct {
		name string
		ch   chan event.Event
	}{
		{"w1", c1.Channel()},
		{"w2", c2.Channel()},
	} {
		select {
		case <-tc.ch:
		case <-time.After(50 * time.Millisecond):
			t.Fatalf("%s did not receive broadcast event", tc.name)
		}
	}
}

// ── Route: pattern matching (wildcards) ──

func TestBus_Route_Wildcards(t *testing.T) {
	b := newTestBus()
	c := registerHelper(t, b, "w1", []string{"e.foo", "g"}, []string{"e.*"})

	_ = c.Subscribe([]event.EventPattern{{Type: "e.*"}})

	// "e.foo" matches "e.*"
	err := b.Publish("w1", event.New("e.foo", "w1", nil))
	if err != nil {
		t.Fatalf("Publish e.foo: %v", err)
	}
	select {
	case <-c.Channel():
	case <-time.After(50 * time.Millisecond):
		t.Fatal("did not receive e.foo through e.* subscription")
	}

	// "g" does not match "e.*"
	err = b.Publish("w1", event.New("g", "w1", nil))
	if err != nil {
		t.Fatalf("Publish g: %v", err)
	}
	select {
	case <-c.Channel():
		t.Fatal("received g through e.* subscription (should not match)")
	case <-time.After(50 * time.Millisecond):
	}
}

// ── InProcessClient: Receive shutdown ──

func TestInProcessClient_Receive_Cleanup(t *testing.T) {
	b := newTestBus()
	_ = b.RegisterWorker("w1", []string{"e1"}, []string{"e1"})

	c, _ := inprocesspkg.NewClient(b, "w1")
	ctx, cancel := context.WithCancel(context.Background())

	_ = c.Subscribe([]event.EventPattern{{Type: "e1"}})
	ch, _ := c.Receive(ctx)

	// Cancel context — cleanup goroutine should unbind channel.
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Channel should still exist but subscriptions should be cleaned.
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subscribers {
		if s.Ch == ch {
			t.Fatal("subscriber entries still reference cleaned-up channel")
		}
	}
}

// ── UpdateWorkerACL ──

func TestBus_UpdateWorkerACL(t *testing.T) {
	b := newTestBus()
	_ = registerHelper(t, b, "w1", []string{"e1"}, []string{"*"})

	// e2 not allowed initially.
	err := b.Publish("w1", event.New("e2", "w1", nil))
	if err == nil {
		t.Fatal("expected ACL error for e2 before update")
	}

	// Update ACL to allow e2.
	b.UpdateWorkerACL("w1", []string{"e1", "e2"}, []string{"*"})

	err = b.Publish("w1", event.New("e2", "w1", nil))
	if err != nil {
		t.Fatalf("expected e2 to be allowed after ACL update: %v", err)
	}
}
