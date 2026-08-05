// Package webui provides a web-based human interface for niq.
//
// It serves a React SPA that communicates with the backend via HTTP and SSE.
// It is owned by the swarm binary, not by the HIW worker — the HTTP server
// directly references HIW (for sending input) and EventLog (for streaming).
package webui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"time"

	eventbusapi "github.com/54c1/niq/pkg/service/eventbus/api"
	"github.com/54c1/niq/pkg/service/eventbus"
	"github.com/54c1/niq/pkg/worker/hiw"

	"github.com/54c1/niq/core/event"
)

//go:embed assets/dist/*
var embeddedAssets embed.FS

// Server is the WebUI HTTP server.
type Server struct {
	hiw      *hiw.Worker
	server   *http.Server
	devMode  bool // when true, static assets are proxied to Vite dev server
	eventLog *eventbusapi.EventLog
	engine   *eventbus.Engine
}

// New creates a WebUI Server.
// devMode enables Vite-proxy mode (frontend runs on :5173, APIs stay on addr).
func New(h *hiw.Worker, el *eventbusapi.EventLog, engine *eventbus.Engine, addr string, devMode bool) *Server {
	s := &Server{hiw: h, eventLog: el, engine: engine, devMode: devMode}
	mux := http.NewServeMux()

	// ── API routes ──

	// SSE: real-time event stream (from EventLog, which gets all events via Engine.OnEvent).
	mux.HandleFunc("GET /api/stream", s.serveSSE)

	// Input: publish user input to the bus as HIW.
	mux.HandleFunc("POST /api/input", s.handleInput)

	// Workers: list online workers from the engine.
	mux.HandleFunc("GET /api/workers", s.handleWorkers)

	// Events pagination: load events before a given anchor.
	mux.HandleFunc("GET /api/events/before/{id}", s.handleLoadBefore)

	// Abort: interrupt a worker's current reasoning.
	mux.HandleFunc("POST /api/abort", s.handleAbort)

	// ── Static assets ──
	if devMode {
		log.Println("[webui] dev mode: static assets served by Vite on :5173")
	} else {
		sub, err := fs.Sub(embeddedAssets, "assets/dist")
		if err != nil {
			log.Fatalf("[webui] embed fs: %v", err)
		}
		mux.Handle("GET /", http.FileServer(http.FS(sub)))
	}

	s.server = &http.Server{Addr: addr, Handler: cors(mux)}
	return s
}

// Start begins serving HTTP. Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	log.Printf("[webui] listening on %s", s.server.Addr)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.server.Shutdown(shutdownCtx)
	}()

	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("webui: %w", err)
	}
	return nil
}

// ── SSE ──

func (s *Server) serveSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	filter := eventbusapi.Filter{
		WorkerID: r.URL.Query().Get("worker"),
		TraceID:  r.URL.Query().Get("trace"),
		Type:     r.URL.Query().Get("type"),
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, err := s.eventLog.Follow(r.Context(), filter, limit)
	if err != nil {
		log.Printf("[webui] follow error: %v", err)
		return
	}

	for evt := range ch {
		data, _ := json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
}

// ── API handlers ──

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text      string `json:"text"`
		Target    string `json:"target,omitempty"`
		InputMode string `json:"input_mode,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.hiw.SendInput(r.Context(), body.Text, body.Target, body.InputMode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	ids := s.engine.OnlineWorkers()
	type workerInfo struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	infos := make([]workerInfo, 0, len(ids))
	for _, id := range ids {
		typ := ""
		if identity, ok := s.engine.Lookup(id); ok {
			typ = identity.Type
		}
		infos = append(infos, workerInfo{ID: id, Type: typ})
	}
	json.NewEncoder(w).Encode(infos)
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	evt := event.New("worker.abort", "hiw", map[string]any{
		"worker_id": "hiw",
	})
	if body.Target != "" {
		_ = s.hiw.Channel.Send(r.Context(), evt, body.Target)
	} else {
		_ = s.hiw.Channel.Broadcast(r.Context(), evt)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleLoadBefore(w http.ResponseWriter, r *http.Request) {
	anchor := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	filter := eventbusapi.Filter{
		WorkerID: r.URL.Query().Get("worker"),
		TraceID:  r.URL.Query().Get("trace"),
		Type:     r.URL.Query().Get("type"),
	}

	events, err := s.eventLog.LoadBefore(r.Context(), filter, anchor, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(events)
}

// ── CORS ──

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}