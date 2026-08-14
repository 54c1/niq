package workerhost

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/54c1/niq/core/worker"
)

// WorkerRecord is the on-disk representation of a managed worker.
// Config and State are stored as separate files so a large snapshot does
// not bloat the small config.
type WorkerRecord struct {
	ID       string
	Type     string
	Params   map[string]any
	State    worker.WorkerState
	Snapshot []byte
}

// WorkerStore is the persistence backend for managed workers. WorkerService
// calls Save* on every state transition; the store writes one directory per
// worker holding config.json and state.json.
type WorkerStore interface {
	// SaveConfig writes the worker's serializable definition.
	SaveConfig(cfg worker.WorkerConfig) error
	// SaveState writes the worker's lifecycle state and latest snapshot.
	SaveState(id string, state worker.WorkerState, snapshot []byte) error
	// LoadAll returns every persisted worker record.
	LoadAll() ([]WorkerRecord, error)
	// Delete removes a worker's persisted directory.
	Delete(id string) error
}

// FileWorkerStore persists workers under a root directory, one subdirectory
// per worker:
//
//	<root>/<workerID>/config.json   — definition
//	<root>/<workerID>/state.json    — {"state": "...", "snapshot": "<base64>"}
type FileWorkerStore struct {
	root string
}

// NewFileWorkerStore creates a store rooted at root (created if missing).
func NewFileWorkerStore(root string) (*FileWorkerStore, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("workerhost: create store root: %w", err)
	}
	return &FileWorkerStore{root: root}, nil
}

func (s *FileWorkerStore) dir(id string) string {
	return filepath.Join(s.root, sanitizeDir(id))
}

func (s *FileWorkerStore) SaveConfig(cfg worker.WorkerConfig) error {
	dir := s.dir(cfg.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("workerhost: mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("workerhost: marshal config %s: %w", cfg.ID, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0644); err != nil {
		return fmt.Errorf("workerhost: write config %s: %w", cfg.ID, err)
	}
	return nil
}

func (s *FileWorkerStore) SaveState(id string, state worker.WorkerState, snapshot []byte) error {
	dir := s.dir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("workerhost: mkdir %s: %w", dir, err)
	}
	rec := stateFile{State: state, Snapshot: base64.StdEncoding.EncodeToString(snapshot)}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("workerhost: marshal state %s: %w", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0644); err != nil {
		return fmt.Errorf("workerhost: write state %s: %w", id, err)
	}
	return nil
}

func (s *FileWorkerStore) LoadAll() ([]WorkerRecord, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("workerhost: read store root: %w", err)
	}
	var recs []WorkerRecord
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		id := de.Name()
		cfg, err := readConfigFile(filepath.Join(s.root, id, "config.json"))
		if err != nil {
			// Skip unreadable workers rather than failing the whole load.
			continue
		}
		state, snapshot, _ := readStateFile(filepath.Join(s.root, id, "state.json"))
		recs = append(recs, WorkerRecord{
			ID:       cfg.ID,
			Type:     cfg.Type,
			Params:   cfg.Params,
			State:    state,
			Snapshot: snapshot,
		})
	}
	return recs, nil
}

func (s *FileWorkerStore) Delete(id string) error {
	return os.RemoveAll(s.dir(id))
}

type stateFile struct {
	State    worker.WorkerState `json:"state"`
	Snapshot string             `json:"snapshot,omitempty"`
}

func readConfigFile(path string) (worker.WorkerConfig, error) {
	var cfg worker.WorkerConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func readStateFile(path string) (worker.WorkerState, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return worker.StateRunning, nil, err
	}
	var rec stateFile
	if err := json.Unmarshal(data, &rec); err != nil {
		return worker.StateRunning, nil, err
	}
	snap, err := base64.StdEncoding.DecodeString(rec.Snapshot)
	if err != nil {
		snap = nil
	}
	return rec.State, snap, nil
}

// sanitizeDir keeps worker IDs filesystem-safe for the per-worker directory.
func sanitizeDir(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
