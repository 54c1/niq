package swarm

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/pkg/service/eventbus"
	"github.com/54c1/niq/pkg/service/eventbus/transport/inprocess"
	"github.com/54c1/niq/pkg/service/workerhost"
)

// RunOptions controls the swarm command's behaviour.
type RunOptions struct {
	ConfigPath string // --config
	Preset     string // --preset
	WebUIAddr  string // --webui
	ProgramsRoot string // --programs-root
}

// RunSwarm is the core entry point for the `niq swarm` command.
// It parses the config, creates the event bus, builds workers, and manages their lifecycle.
func RunSwarm(opts RunOptions) error {
	// 1. Parse config.
	var cfg *SwarmConfig
	var err error
	switch {
	case opts.ConfigPath != "":
		cfg, err = ParseConfig(opts.ConfigPath)
	case opts.Preset != "":
		cfg, err = LoadPreset(opts.Preset)
	default:
		cfg, err = LoadPreset("dev")
	}
	if err != nil {
		return err
	}

	// 2. Create identity registry (file-backed).
	homeDir, _ := os.UserHomeDir()
	idDir := filepath.Join(homeDir, ".niq", "id")
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(idDir, "identities.json"))
	if err != nil {
		return fmt.Errorf("swarm: create registry: %w", err)
	}

	// 3. Create event bus engine.
	engine := eventbus.NewEngine(registry, nil)

	// 4. Create in-process listener and start accepting connections.
	listener := inprocess.NewInProcListener()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		for {
			ch, err := listener.Accept(ctx)
			if err != nil {
				return
			}
			eventbus.Attach(ctx, engine, ch.WorkerID(), ch)
		}
	}()

	// 5. Create WorkerService (control-plane lifecycle manager).
	workerSvc := workerhost.New()

	// 6. Build build context.
	buildCtx := BuildContext{
		Registry:     registry,
		Listener:     listener,
		Engine:       engine,
		WorkerSvc:    workerSvc,
		WebUIAddr:    opts.WebUIAddr,
		ProgramsRoot: opts.ProgramsRoot,
	}

	// 7. Build workers from config.
	type wReg struct {
		w   worker.ManagedWorker
		typ string
	}
	var regs []wReg

	for _, wc := range cfg.Workers {
		log.Printf("[swarm] building worker: %s (type=%s)", wc.ID, wc.Type)

		w, err := BuildWorker(buildCtx, wc)
		if err != nil {
			return fmt.Errorf("swarm: build worker %q: %w", wc.ID, err)
		}
		regs = append(regs, wReg{w: w, typ: wc.Type})
	}

	// 8. Register all workers with WorkerService.
	for _, reg := range regs {
		log.Printf("[swarm] registering worker: %s (type=%s)", reg.w.ID(), reg.typ)
		workerSvc.Register(reg.w, reg.typ)
	}

	// 9. Run — starts all workers then blocks until ctx is cancelled.
	fmt.Println("niq swarm started. Press Ctrl+C to stop.")
	if err := workerSvc.Run(ctx); err != nil && err != context.Canceled {
		return fmt.Errorf("swarm: %w", err)
	}

	fmt.Println("\nniq swarm stopped.")
	return nil
}
