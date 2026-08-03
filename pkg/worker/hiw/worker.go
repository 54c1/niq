package hiw

import (
	"context"
	"fmt"
	"log"
	"sync"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/store"
	"github.com/54c1/niq/core/worker"
)

// UserInterface represents a UI bound to this worker's lifecycle.
// UIs implementing this interface are started and stopped with the worker.
type UserInterface interface {
	Start(ctx context.Context) error
}

// Worker is the Human Interface Worker.
type Worker struct {
	worker.BaseWorker
	store    store.EventStore
	started  bool
	cancelCh chan struct{}
	mu       sync.Mutex

	// Follow subscribers: channel → filter for real-time event broadcasting.
	followMu   sync.Mutex
	followSubs map[chan event.Event]Filter

	// uis holds externally-attached user interfaces (e.g. WebUI).
	uis []UserInterface

	workers           map[string]string // worker ID → type
	pendingDecisions  map[string]*DecisionRequest
	resolvedDecisions []DecisionRequest // history of resolved decisions, newest first
}

// Config holds the configuration for a HIW.
type Config struct {
	ID    string // worker ID, defaults to "hiw"
	Bus   corebus.EventBusClient
	Store store.EventStore
}

func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "hiw"
	}
	return &Worker{
		BaseWorker: worker.NewBaseWorker(id, []event.EventPattern{
			event.NewPattern("*"),
		}, cfg.Bus),
		store:            cfg.Store,
		cancelCh:         make(chan struct{}),
		workers:          make(map[string]string),
		pendingDecisions: make(map[string]*DecisionRequest),
		followSubs:       make(map[chan event.Event]Filter),
	}
}

func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("hiw: already started")
	}

	w.Bus.Subscribe(w.Subscriptions())
	busCh, _ := w.Bus.Receive(context.Background())

	// Announce tools so the reason worker can discover request_human_decision.
	w.publishReady()
	_ = w.Bus.Publish(event.New("worker.discover", w.ID(), nil))

	ch := w.cancelCh
	go func() {
		for {
			select {
			case evt := <-busCh:
				w.handleEvent(evt)
				w.broadcastToFollowers(evt)
			case <-ch:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start externally-attached UIs (e.g. WebUI).
	for _, ui := range w.uis {
		ui := ui
		go func() {
			if err := ui.Start(ctx); err != nil {
				log.Printf("[hiw] ui error: %v", err)
			}
		}()
	}

	w.started = true
	return nil
}

func (w *Worker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	close(w.cancelCh)
	w.started = false
	return nil
}

// SetUI attaches an external UI (e.g. WebUI) to be started with the worker lifecycle.
// The typ parameter identifies the UI type ("webui", etc.) for logging purposes.
func (w *Worker) SetUI(typ string, ui UserInterface) {
	log.Printf("[hiw] attaching %s interface", typ)
	w.uis = append(w.uis, ui)
}

// broadcastToFollowers sends a copy of the event to every Follow subscriber
// whose filter matches. Called from the main event loop so Follow goroutines
// do not compete with the main loop for Bus channel reads.
func (w *Worker) broadcastToFollowers(evt event.Event) {
	w.followMu.Lock()
	defer w.followMu.Unlock()
	if len(w.followSubs) == 0 {
		return
	}
	for ch, f := range w.followSubs {
		if matchesFilter(evt, f) {
			select {
			case ch <- evt:
			default:
				log.Printf("[hiw] follow subscriber dropped event type=%s", evt.Type)
			}
		}
	}
}

// publishReady announces HIW's available tool for human decision requests.
func (w *Worker) publishReady() {
	_ = w.Bus.Publish(event.New("worker.ready", w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "hiw",
		"tools": []map[string]any{{
			"name":        "request_human_decision",
			"description": "Submit a decision request to the human user. The tool returns immediately with 'submitted'; the human will respond later with their decision. When they do, a new message will arrive with the result. Use this when you need human judgment, approval, or additional information that you cannot determine on your own.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{
						"type":        "string",
						"description": "A one-line summary of what decision is needed.",
					},
					"context": map[string]any{
						"type":        "string",
						"description": "Detailed context and background information for the human.",
					},
					"options": map[string]any{
						"type":        "array",
						"description": "Optional list of predefined choices for the human to pick from.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":    map[string]any{"type": "string"},
								"label": map[string]any{"type": "string"},
							},
						},
					},
				},
				"required": []any{"summary", "context"},
			},
		}},
		"publishes": []map[string]any{
			{"type": "worker.input", "description": "User input event"},
			{"type": "decision.made", "description": "Human decision result"},
		},
	}))
}

func (w *Worker) Snapshot() ([]byte, error)  { return nil, nil }
func (w *Worker) Restore(state []byte) error { return nil }

// registerFollowSub adds a channel to receive events matching the filter.
// Returns a cleanup function the caller must call on ctx.Done().
func (w *Worker) registerFollowSub(ch chan event.Event, f Filter) func() {
	w.followMu.Lock()
	w.followSubs[ch] = f
	w.followMu.Unlock()
	log.Printf("[hiw] follow subscriber added, subs=%d", len(w.followSubs))
	return func() {
		w.followMu.Lock()
		delete(w.followSubs, ch)
		close(ch)
		w.followMu.Unlock()
		log.Printf("[hiw] follow subscriber removed, subs=%d", len(w.followSubs))
	}
}
