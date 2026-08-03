package swarm

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/pkg/service/bus"
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
// It parses the config, creates a bus, builds workers, and manages their lifecycle.
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

	// 2. Create in-memory bus (+ EventStore).
	memBus := bus.NewMemoryBus()

	// 3. Create WorkerService.
	engine := workerhost.New()

	// 4. Build build context — passes the shared Bus for control-plane access.
	buildCtx := BuildContext{
		Bus:       memBus.SharedBus(),
		Engine:    engine,
		Store:     memBus,
		WebUIAddr:    opts.WebUIAddr,
		ProgramsRoot: opts.ProgramsRoot,
	}

	// 5. Build workers from config.
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

	// 6. Register all workers with WorkerService.
	for _, reg := range regs {
		log.Printf("[swarm] registering worker: %s (type=%s)", reg.w.ID(), reg.typ)
		engine.Register(reg.w, reg.typ)
	}

	// 7. Publish initial worker.discover via Route (no identity needed for boot event).
	memBus.Route(event.New("worker.discover", "swarm", nil))

	// 8. Set up signal handling for graceful shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 9. (WebUI is started internally by the HIW worker if WebUIAddr is set).

	// 10. Run — starts all workers then blocks until ctx is cancelled.
	fmt.Println("niq swarm started. Press Ctrl+C to stop.")
	if err := engine.Run(ctx); err != nil && err != context.Canceled {
		return fmt.Errorf("swarm: %w", err)
	}

	fmt.Println("\nniq swarm stopped.")
	return nil
}
