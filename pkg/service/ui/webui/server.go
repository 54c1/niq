// Package webui provides a web-based human interface for niq.
//
// It serves a React SPA that communicates with the backend via HTTP and SSE.
// The backend is a standard http.Server that wraps a *hiw.Worker, exposing
// its methods through REST endpoints and streaming events via SSE.
//
// Architecture:
//
//	Browser (React SPA)
//	   │ HTTP (POST /api/input, GET /api/workers, ...)
//	   │ SSE  (GET /api/stream)
//	   ▼
//	WebUI Server (Go, embedded or dev-proxied)
//	   │ holds *hiw.Worker
//	   ▼
//	HIW Worker → Event Bus
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
	hiw       hiw.WebUI
	server    *http.Server
	devMode   bool // when true, static assets are proxied to Vite dev server
	eventLog  *eventbusapi.EventLog
	engine    *eventbus.Engine
}

// New creates a WebUI Server that wraps the given HIW.
// devMode enables Vite-proxy mode (frontend runs on :5173, APIs stay on addr).
func New(h hiw.WebUI, addr string, devMode bool) *Server {
	s := &Server{hiw: h, devMode: devMode}
	mux := http.NewServeMux()

	// ── API routes ──

	// SSE: real-time event stream.
	mux.HandleFunc("GET /api/stream", s.serveSSE)

	// Input: publish user input to the bus.
	mux.HandleFunc("POST /api/input", s.handleInput)

	// Workers: list known workers.
	mux.HandleFunc("GET /api/workers", s.handleWorkers)

	// Decisions: list pending decisions.
	mux.HandleFunc("GET /api/decisions", s.handleDecisions)

	// Decision: make a decision.
	mux.HandleFunc("POST /api/decisions/{id}", s.handleDecision)

	// Events pagination: load events before a given anchor.
	// Initial load + realtime stream comes from the SSE endpoint.
	mux.HandleFunc("GET /api/events/before/{id}", s.handleLoadBefore)

	// Abort: interrupt a worker's current reasoning.
	mux.HandleFunc("POST /api/abort", s.handleAbort)

	// ── Static assets ──
	if devMode {
		// In dev mode, Vite serves static assets on :5173.
		// API requests hit Go directly. No static file serving from Go.
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

// SetEventLog wires the bus event stream (EventLog via Engine.OnEvent) into
// the WebUI so its SSE endpoint receives ALL events — including tool call
// events sent via Send — not just what HIW's bus channel sees.
func (s *Server) SetEventLog(el *eventbusapi.EventLog, engine *eventbus.Engine) {
	s.eventLog = el
	s.engine = engine
}
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

	filter := hiw.Filter{
		WorkerID: r.URL.Query().Get("worker"),
		TraceID:  r.URL.Query().Get("trace"),
		Type:     r.URL.Query().Get("type"),
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Use the EventLog (hooked to Engine.OnEvent) when available — it receives
	// ALL events (both Send and Broadcast), so tool call events will appear in
	// the real-time stream. Fall back to HIW's Follow for backward compatibility.
	var ch <-chan event.Event
	var err error
	if s.eventLog != nil {
		busFilter := eventbusapi.Filter{
			WorkerID: filter.WorkerID,
			TraceID:  filter.TraceID,
			Type:     filter.Type,
		}
		ch, err = s.eventLog.Follow(r.Context(), busFilter, limit)
	} else {
		ch, err = s.hiw.Follow(r.Context(), filter)
	}
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
	workers := s.hiw.Workers()
	json.NewEncoder(w).Encode(workers)
}

func (s *Server) handleDecisions(w http.ResponseWriter, r *http.Request) {
	decisions := s.hiw.PendingDecisions()
	json.NewEncoder(w).Encode(decisions)
}

func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	reqID := r.PathValue("id")
	var body struct {
		Decision  string `json:"decision"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.hiw.MakeDecision(r.Context(), reqID, body.Decision, body.Reasoning); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if err := s.hiw.Abort(r.Context(), body.Target); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleLoadBefore(w http.ResponseWriter, r *http.Request) {
	anchor := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	filter := hiw.Filter{
		WorkerID: r.URL.Query().Get("worker"),
		TraceID:  r.URL.Query().Get("trace"),
		Type:     r.URL.Query().Get("type"),
	}

	events, err := s.hiw.LoadBefore(r.Context(), filter, anchor, limit)
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
