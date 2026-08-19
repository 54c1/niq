// niq — neural interface quantum.
//
// Usage:
//
//	niq                       — start with the default "dev" preset
//	niq swarm --config <file> — start from a YAML config file
//	niq swarm --preset <name> — start from a built-in preset
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/54c1/niq/internal/swarm"
)

// version is injected at build time via -ldflags:
//
//	-X main.version=v1.2.3
var version = "dev"

func main() {
	// Handle --version / -v / version before anything else.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			fmt.Println("niq", version)
			return
		case "--help", "-h", "help":
			printUsage()
			return
		}
	}

	// Detect subcommand: "niq swarm ..."
	if len(os.Args) > 1 && os.Args[1] == "swarm" {
		if err := runSwarm(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Default: no args, start with the dev preset.
	// Also support -config / -preset as top-level flags for convenience.
	if err := runSwarm(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`niq - neural interface quantum

Usage:
  niq                       start with the default "dev" preset
  niq swarm --config <file> start from a YAML config file
  niq swarm --preset <name> start from a built-in preset
  niq --version             print version
`)
}

func runSwarm(args []string) error {
	// Set up logging to ~/.niq/niq.log.
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".niq")
	os.MkdirAll(logDir, 0755)
	f, err := os.OpenFile(filepath.Join(logDir, "niq.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err == nil {
		log.SetOutput(f)
		defer f.Close()
	} else {
		log.SetOutput(io.Discard)
	}
	log.SetPrefix("[niq] ")

	fs := flag.NewFlagSet("niq", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to swarm YAML config")
	preset := fs.String("preset", "", "Built-in preset name (dev, test-headless, etc.)")
	webUIAddr := fs.String("webui", ":19763", "WebUI listen address (e.g. :19763)")
	programsRoot := fs.String("programs-root", "", "Program storage root directory (default: ~/.niq/programs)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return swarm.RunSwarm(swarm.RunOptions{
		ConfigPath:   *configPath,
		Preset:       *preset,
		WebUIAddr:    *webUIAddr,
		ProgramsRoot: *programsRoot,
	})
}
