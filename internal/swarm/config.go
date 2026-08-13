// Package swarm provides the config-driven assembly for niq swarm.
//
// It reads a YAML config, instantiates workers, registers them with
// WorkerService, and manages the lifecycle. This is the only place where
// config files are parsed into worker instances — no new abstractions.
package swarm

import (
	"embed"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

//go:embed preset/*.yaml
var presetFS embed.FS

// SwarmConfig is the top-level structure of a swarm YAML config file.
type SwarmConfig struct {
	Workers []WorkerConfig `yaml:"workers"`
}

// WorkerConfig describes a single worker instance declaration.
type WorkerConfig struct {
	Type          string   `yaml:"type"` // reason / workspace / host / timer / hiw
	ID            string   `yaml:"id"`
	Instruction   string   `yaml:"instruction,omitempty"`
	Provider      string   `yaml:"provider,omitempty"`
	APIKey        string   `yaml:"api_key,omitempty"`
	BaseURL       string   `yaml:"base_url,omitempty"`
	Model         string   `yaml:"model,omitempty"`
	Subscriptions []string `yaml:"subscriptions,omitempty"`
	Publish       []string `yaml:"publish,omitempty"`
	RootDir       string   `yaml:"root_dir,omitempty"`
}

// ParseConfig reads and parses a swarm YAML config file.
func ParseConfig(path string) (*SwarmConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("swarm: read config: %w", err)
	}
	return parseYAML(raw)
}

// LoadPreset loads a built-in preset by name (without the .yaml suffix).
func LoadPreset(name string) (*SwarmConfig, error) {
	raw, err := presetFS.ReadFile("preset/" + name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("swarm: preset %q not found", name)
	}
	return parseYAML(raw)
}

func parseYAML(raw []byte) (*SwarmConfig, error) {
	var cfg SwarmConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("swarm: parse config: %w", err)
	}
	for i, w := range cfg.Workers {
		if w.Type == "" {
			return nil, fmt.Errorf("swarm: worker %d: type is required", i)
		}
		if w.ID == "" {
			return nil, fmt.Errorf("swarm: worker %d: id is required", i)
		}
	}
	return &cfg, nil
}
