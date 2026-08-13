package providercfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProviderConfigDefaultAndSwitch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", path)

	cfg := &Config{
		Providers: []Provider{
			{Name: "deepseek-responses", Type: "openai-responses", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash"},
			{Name: "deepseek-claude", Type: "claude", BaseURL: "https://api.deepseek.com/anthropic", Model: "deepseek-v4-flash"},
		},
	}
	if err := Write(cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}

	def, ok := Default()
	if !ok || def.Name != "deepseek-responses" {
		t.Fatalf("Default = %+v, %v", def, ok)
	}

	claude, ok := Find("deepseek-claude")
	if !ok || claude.Type != "claude" {
		t.Fatalf("Find = %+v, %v", claude, ok)
	}

	byType, ok := FindByType("claude")
	if !ok || byType.Name != "deepseek-claude" {
		t.Fatalf("FindByType = %+v, %v", byType, ok)
	}

	if err := Switch("deepseek-claude"); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	def, ok = Default()
	if !ok || def.Name != "deepseek-claude" {
		t.Fatalf("Default after switch = %+v, %v", def, ok)
	}

	if err := Switch("missing"); err == nil {
		t.Fatal("Switch(missing) should fail")
	}
}

func TestProviderConfigMissingFile(t *testing.T) {
	t.Setenv("NIQ_PROVIDER_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	if _, ok := Default(); ok {
		t.Fatal("Default should be false when file is missing")
	}
}

func TestProviderConfigActiveFallsBackToFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", path)

	cfg := &Config{
		Active: "stale",
		Providers: []Provider{
			{Name: "first", Type: "openai"},
			{Name: "second", Type: "claude"},
		},
	}
	if err := Write(cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}

	def, ok := Default()
	if !ok || def.Name != "first" {
		t.Fatalf("Default = %+v, %v", def, ok)
	}
}

func TestProviderConfigPathOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", path)
	if got := Path(); got != path {
		t.Fatalf("Path() = %q, want %q", got, path)
	}

	_ = os.Unsetenv("NIQ_PROVIDER_CONFIG")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".niq", "provider.json")
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}
