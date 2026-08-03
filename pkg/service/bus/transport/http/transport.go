package http

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"strings"

	"github.com/54c1/niq/core/event"
	bussvc "github.com/54c1/niq/pkg/service/bus"
)

// HttpTransport exposes a *bussvc.Bus via HTTP endpoints. It handles trust
// determination (loopback vs remote) and delegates all bus operations
// to the underlying Bus. It does not define any new abstraction — it
// is purely an HTTP translation layer for Bus methods.
type HttpTransport struct {
	bus    *bussvc.Bus
	mux    *stdhttp.ServeMux
	apiKey string // empty ⇒ admin API not exposed (loopback only)
}

// NewHttpTransport creates an HTTP transport for a Bus.
// If apiKey is empty, control-plane endpoints are restricted to loopback only.
func NewHttpTransport(bus *bussvc.Bus, apiKey string) *HttpTransport {
	t := &HttpTransport{bus: bus, mux: stdhttp.NewServeMux(), apiKey: apiKey}
	t.mux.HandleFunc("/publish", t.handlePublish)
	t.mux.HandleFunc("/subscribe", t.handleSubscribe)
	t.mux.HandleFunc("/unsubscribe", t.handleUnsubscribe)
	t.mux.HandleFunc("/events", t.handleEvents)
	t.mux.HandleFunc("/register", t.handleRegister)
	t.mux.HandleFunc("/update-acl", t.handleUpdateACL)
	t.mux.HandleFunc("/unregister", t.handleUnregister)
	return t
}

// Handler returns the stdhttp.Handler for this transport.
func (t *HttpTransport) Handler() stdhttp.Handler { return t.mux }

// ── Trust helpers ──

func isLoopback(r *stdhttp.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// extractWorkerID returns the authenticated workerID.
// For loopback: self-declared from body.
// For remote: validated from token (placeholder for now).
func extractWorkerID(r *stdhttp.Request) (string, error) {
	var req struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", err
	}
	if !isLoopback(r) {
		return "", fmt.Errorf("remote worker auth not implemented (token required)")
	}
	if req.WorkerID == "" {
		return "", fmt.Errorf("worker_id is required")
	}
	return req.WorkerID, nil
}

// checkAdmin returns true if the request is authorized for control-plane operations.
func (t *HttpTransport) checkAdmin(r *stdhttp.Request) bool {
	if isLoopback(r) {
		return true
	}
	if t.apiKey == "" {
		return false
	}
	return r.Header.Get("Authorization") == "Bearer "+t.apiKey
}

// ── Data plane endpoints ──

// POST /publish  { worker_id, events: [...] }
func (t *HttpTransport) handlePublish(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if r.Method != stdhttp.MethodPost {
		stdhttp.Error(w, "method not allowed", stdhttp.StatusMethodNotAllowed)
		return
	}
	var req struct {
		WorkerID string         `json:"worker_id"`
		Events   []event.Event  `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		stdhttp.Error(w, "invalid body", stdhttp.StatusBadRequest)
		return
	}
	if req.WorkerID == "" {
		stdhttp.Error(w, "worker_id is required", stdhttp.StatusBadRequest)
		return
	}
	if !isLoopback(r) {
		stdhttp.Error(w, "remote not implemented", stdhttp.StatusUnauthorized)
		return
	}
	if err := t.bus.Publish(req.WorkerID, req.Events...); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusForbidden)
		return
	}
	w.WriteHeader(stdhttp.StatusOK)
}

// POST /subscribe  { worker_id, patterns: [...] }
func (t *HttpTransport) handleSubscribe(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if r.Method != stdhttp.MethodPost {
		stdhttp.Error(w, "method not allowed", stdhttp.StatusMethodNotAllowed)
		return
	}
	var req struct {
		WorkerID string                `json:"worker_id"`
		Patterns []event.EventPattern  `json:"patterns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		stdhttp.Error(w, "invalid body", stdhttp.StatusBadRequest)
		return
	}
	if req.WorkerID == "" {
		stdhttp.Error(w, "worker_id is required", stdhttp.StatusBadRequest)
		return
	}
	if !isLoopback(r) {
		stdhttp.Error(w, "remote not implemented", stdhttp.StatusUnauthorized)
		return
	}
	if err := t.bus.Subscribe(req.WorkerID, req.Patterns); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusForbidden)
		return
	}
	w.WriteHeader(stdhttp.StatusOK)
}

// POST /unsubscribe  { worker_id, patterns: [...] }
func (t *HttpTransport) handleUnsubscribe(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if r.Method != stdhttp.MethodPost {
		stdhttp.Error(w, "method not allowed", stdhttp.StatusMethodNotAllowed)
		return
	}
	var req struct {
		WorkerID string                `json:"worker_id"`
		Patterns []event.EventPattern  `json:"patterns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		stdhttp.Error(w, "invalid body", stdhttp.StatusBadRequest)
		return
	}
	if req.WorkerID == "" {
		stdhttp.Error(w, "worker_id is required", stdhttp.StatusBadRequest)
		return
	}
	if !isLoopback(r) {
		stdhttp.Error(w, "remote not implemented", stdhttp.StatusUnauthorized)
		return
	}
	if err := t.bus.Unsubscribe(req.WorkerID, req.Patterns); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusForbidden)
		return
	}
	w.WriteHeader(stdhttp.StatusOK)
}

// GET /events?worker_id=xxx&subscribe=a,b,c
// Opens an SSE stream. Creates a channel, binds it, subscribes to patterns,
// and forwards matching events.
func (t *HttpTransport) handleEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	q := r.URL.Query()
	workerID := q.Get("worker_id")
	if workerID == "" {
		stdhttp.Error(w, "worker_id query param required", stdhttp.StatusBadRequest)
		return
	}

	// Trust check: remote workers need token validation.
	if !isLoopback(r) {
		stdhttp.Error(w, "remote SSE not implemented (token required)", stdhttp.StatusUnauthorized)
		return
	}

	// Bind or reuse a channel for this SSE session.
	ch, err := t.bus.BindChannel(workerID)
	if err != nil && !strings.Contains(err.Error(), "already has a bound channel") {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	if err != nil {
		// Channel already bound — use it.
		ch = t.bus.Channel(workerID)
	}
	defer t.bus.UnbindChannel(workerID)

	// Subscribe to patterns if provided.
	if subs := q.Get("subscribe"); subs != "" {
		var patterns []event.EventPattern
		for _, s := range strings.Split(subs, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				patterns = append(patterns, event.NewPattern(s))
			}
		}
		if len(patterns) > 0 {
			if err := t.bus.Subscribe(workerID, patterns); err != nil {
				stdhttp.Error(w, err.Error(), stdhttp.StatusForbidden)
				return
			}
		}
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(stdhttp.Flusher)
	if !ok {
		stdhttp.Error(w, "streaming not supported", stdhttp.StatusInternalServerError)
		return
	}

	log.Printf("[httptransport] SSE stream started for worker=%s", workerID)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			log.Printf("[httptransport] SSE stream ended for worker=%s", workerID)
			return
		}
	}
}

// ── Control plane endpoints ──

// POST /register  { worker_id, publish_allow, subscribe_allow }
func (t *HttpTransport) handleRegister(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if r.Method != stdhttp.MethodPost {
		stdhttp.Error(w, "method not allowed", stdhttp.StatusMethodNotAllowed)
		return
	}
	if !t.checkAdmin(r) {
		stdhttp.Error(w, "unauthorized", stdhttp.StatusUnauthorized)
		return
	}
	var req struct {
		WorkerID       string   `json:"worker_id"`
		PublishAllow   []string `json:"publish_allow"`
		SubscribeAllow []string `json:"subscribe_allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		stdhttp.Error(w, "invalid body", stdhttp.StatusBadRequest)
		return
	}
	if err := t.bus.RegisterWorker(req.WorkerID, req.PublishAllow, req.SubscribeAllow); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusConflict)
		return
	}
	w.WriteHeader(stdhttp.StatusCreated)
}

// POST /update-acl  { worker_id, publish_allow, subscribe_allow }
func (t *HttpTransport) handleUpdateACL(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if r.Method != stdhttp.MethodPost {
		stdhttp.Error(w, "method not allowed", stdhttp.StatusMethodNotAllowed)
		return
	}
	if !t.checkAdmin(r) {
		stdhttp.Error(w, "unauthorized", stdhttp.StatusUnauthorized)
		return
	}
	var req struct {
		WorkerID       string   `json:"worker_id"`
		PublishAllow   []string `json:"publish_allow"`
		SubscribeAllow []string `json:"subscribe_allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		stdhttp.Error(w, "invalid body", stdhttp.StatusBadRequest)
		return
	}
	if err := t.bus.UpdateWorkerACL(req.WorkerID, req.PublishAllow, req.SubscribeAllow); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusNotFound)
		return
	}
	w.WriteHeader(stdhttp.StatusOK)
}

// POST /unregister  { worker_id }
func (t *HttpTransport) handleUnregister(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if r.Method != stdhttp.MethodPost {
		stdhttp.Error(w, "method not allowed", stdhttp.StatusMethodNotAllowed)
		return
	}
	if !t.checkAdmin(r) {
		stdhttp.Error(w, "unauthorized", stdhttp.StatusUnauthorized)
		return
	}
	var req struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		stdhttp.Error(w, "invalid body", stdhttp.StatusBadRequest)
		return
	}
	if err := t.bus.UnregisterWorker(req.WorkerID); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusNotFound)
		return
	}
	w.WriteHeader(stdhttp.StatusOK)
}
