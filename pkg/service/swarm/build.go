package swarm

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	programpkg "github.com/54c1/niq/core/program"
	"github.com/54c1/niq/core/store"
	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/pkg/helper/openai"
	svcbus "github.com/54c1/niq/pkg/service/bus"
	inprocesspkg "github.com/54c1/niq/pkg/service/bus/transport/inprocess"
	"github.com/54c1/niq/pkg/service/pgbackend"
	"github.com/54c1/niq/pkg/service/ui/webui"
	"github.com/54c1/niq/pkg/service/workerhost"
	"github.com/54c1/niq/pkg/service/wsbackend"
	"github.com/54c1/niq/pkg/worker/hiw"
	"github.com/54c1/niq/pkg/worker/host"
	programworker "github.com/54c1/niq/pkg/worker/program"
	"github.com/54c1/niq/pkg/worker/reason"
	"github.com/54c1/niq/pkg/worker/timer"
	"github.com/54c1/niq/pkg/worker/workspace"
)

// BuildContext holds shared dependencies that worker factories need.
type BuildContext struct {
	Bus          *svcbus.Bus // shared bus for control-plane operations
	Engine       *workerhost.WorkerService
	Store        store.EventStore
	WebUIAddr    string // non-empty enables WebUI server in HIW
	ProgramsRoot string // root directory for program storage
}

// clientFor builds an InProcessClient for a worker, falling back to ["*"]
// when the worker config does not specify publish / subscription lists.
func clientFor(ctx BuildContext, cfg WorkerConfig) (*inprocesspkg.InProcessClient, error) {
	subAllow := cfg.Subscriptions
	if len(subAllow) == 0 {
		subAllow = []string{"*"}
	}
	pubAllow := cfg.Publish
	if len(pubAllow) == 0 {
		pubAllow = []string{"*"}
	}
	if err := ctx.Bus.RegisterWorker(cfg.ID, pubAllow, subAllow); err != nil {
		return nil, fmt.Errorf("swarm: register worker %q: %w", cfg.ID, err)
	}
	client, err := inprocesspkg.NewClient(ctx.Bus, cfg.ID)
	if err != nil {
		return nil, fmt.Errorf("swarm: client for %q: %w", cfg.ID, err)
	}
	log.Printf("[swarm] registered worker %s (pub=%v sub=%v)", cfg.ID, pubAllow, subAllow)
	return client, nil
}

// BuildWorker instantiates a single worker.ManagedWorker from a config entry.
func BuildWorker(ctx BuildContext, cfg WorkerConfig) (worker.ManagedWorker, error) {
	switch cfg.Type {
	case "reason":
		return buildReason(ctx, cfg)
	case "workspace":
		return buildWorkspace(ctx, cfg)
	case "host":
		return buildHost(ctx, cfg)
	case "timer":
		return buildTimer(ctx, cfg)
	case "hiw":
		return buildHIW(ctx, cfg)
	case "program":
		return buildProgram(ctx, cfg)
	default:
		return nil, fmt.Errorf("swarm: unknown worker type %q", cfg.Type)
	}
}

// ── reason ──

func buildReason(ctx BuildContext, cfg WorkerConfig) (worker.ManagedWorker, error) {
	client, err := clientFor(ctx, cfg)
	if err != nil {
		return nil, err
	}

	subs := cfg.Subscriptions
	handlers := make([]reason.EventConverter, 0, len(subs))
	for _, s := range subs {
		pat := s
		handlers = append(handlers, reason.EventConverter{
			Pattern: event.NewPattern(pat),
			Converter: func(evt event.Event) []llm.Message {
				text, _ := evt.Payload["text"].(string)
				if text == "" {
					text = fmt.Sprintf("Event: %s from %s", evt.Type, evt.WorkerId)
				}
				return []llm.Message{{
					Role:    llm.RoleUser,
					Content: []llm.ContentBlock{{Type: llm.ContentText, Text: text}},
				}}
			},
		})
	}

	provider := resolveProvider(cfg)
	reasoningEffort := "medium"

	var programs []programpkg.Program
	if cfg.Instruction != "" {
		programs = append(programs, programpkg.Program{
			Meta: programpkg.Meta{
				Name:        cfg.ID + "-instruction",
				ContentType: programpkg.ContentTypeInstruction,
			},
			EntryContent: programpkg.ProgramContent{
				Content: cfg.Instruction,
			},
		})
	}

	return reason.NewWorker(reason.Config{
		ID:              cfg.ID,
		Handlers:        handlers,
		Provider:        provider,
		Programs:        programs,
		Bus:             client,
		ReasoningEffort: &reasoningEffort,
	}), nil
}

// ── workspace ──

func buildWorkspace(ctx BuildContext, cfg WorkerConfig) (worker.ManagedWorker, error) {
	client, err := clientFor(ctx, cfg)
	if err != nil {
		return nil, err
	}

	dir := cfg.RootDir
	if dir == "" {
		return nil, fmt.Errorf("swarm: workspace worker %q: root_dir is required", cfg.ID)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("swarm: workspace worker %q: bad root_dir: %w", cfg.ID, err)
	}

	return workspace.New(workspace.Config{
		ID:      cfg.ID,
		Bus:     client,
		Backend: wsbackend.NewEmbeddedBackend(dir),
	}), nil
}

// ── host ──

func buildHost(ctx BuildContext, cfg WorkerConfig) (worker.ManagedWorker, error) {
	client, err := clientFor(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return host.New(host.Config{
		ID:     cfg.ID,
		Bus:    client,
		Engine: ctx.Engine,
		Shared: ctx.Bus,
	}), nil
}

// ── timer ──

func buildTimer(ctx BuildContext, cfg WorkerConfig) (worker.ManagedWorker, error) {
	client, err := clientFor(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return timer.New(timer.Config{
		ID:  cfg.ID,
		Bus: client,
	}), nil
}

// ── hiw ──

func buildHIW(ctx BuildContext, cfg WorkerConfig) (worker.ManagedWorker, error) {
	client, err := clientFor(ctx, cfg)
	if err != nil {
		return nil, err
	}

	h := hiw.New(hiw.Config{
		ID:    cfg.ID,
		Bus:   client,
		Store: ctx.Store,
	})

	if ctx.WebUIAddr != "" {
		s := webui.New(h, ctx.WebUIAddr, false)
		h.SetUI("webui", s)
	}

	return h, nil
}

// ── program ──

func buildProgram(ctx BuildContext, cfg WorkerConfig) (worker.ManagedWorker, error) {
	client, err := clientFor(ctx, cfg)
	if err != nil {
		return nil, err
	}

	root := cfg.RootDir
	if root == "" {
		root = ctx.ProgramsRoot
	}
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".niq", "programs")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("swarm: program worker %q: bad root_dir: %w", cfg.ID, err)
	}

	// Ensure the programs directory exists.
	os.MkdirAll(root, 0755)

	return programworker.New(programworker.Config{
		ID:      cfg.ID,
		Bus:     client,
		Backend: pgbackend.New(root),
	}), nil
}

// ── helpers ──

func resolveProvider(cfg WorkerConfig) llm.LLMProvider {
	apiKey := os.Getenv("OPENAI_API_KEY")
	baseURL := "https://api.openai.com/v1"
	model := "gpt-4o"

	if cfg.Provider == "deepseek" || cfg.Provider == "openai-compatible" {
		baseURL = "https://api.deepseek.com/v1"
	}
	if cfg.Model != "" {
		model = cfg.Model
	}

	return openai.New(openai.Config{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	})
}


