package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseReadsProviderSessionAndHooks verifies the supported TOML subset maps
// cleanly into runtime configuration.
func TestParseReadsProviderSessionAndHooks(t *testing.T) {
	cfg, err := Parse(`
[session]
dir = ".data/sessions"
max_tool_rounds = 64
keep_messages = 20

[provider]
name = "openai"
model = "gpt-5.5"

[openai]
base_url = "https://api.example.test/v1"
api = "responses"
reasoning_effort = "minimal"
reasoning_summary = "auto"

[[hooks.PreToolUse]]
matcher = "bash"
command = "printf '{}'"
timeout_seconds = 2

[[hooks]]
event = "PostToolUse"
matcher = "*"
command = 'cat'
disabled = true
`)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.Session.Dir != ".data/sessions" ||
		cfg.Session.MaxToolRounds != 64 ||
		cfg.Session.KeepMessages != 20 {

		t.Fatalf("unexpected session config: %#v", cfg.Session)
	}
	if cfg.Provider.Name != "openai" || cfg.Provider.Model != "gpt-5.5" {
		t.Fatalf("unexpected provider config: %#v", cfg.Provider)
	}
	if cfg.OpenAI.API != "responses" ||
		cfg.OpenAI.ReasoningSummary != "auto" {

		t.Fatalf("unexpected openai config: %#v", cfg.OpenAI)
	}
	if len(cfg.Hooks) != 2 {
		t.Fatalf("expected two hooks, got %d", len(cfg.Hooks))
	}
	if cfg.Hooks[0].Event != "PreToolUse" ||
		cfg.Hooks[0].Command != "printf '{}'" {

		t.Fatalf("unexpected first hook: %#v", cfg.Hooks[0])
	}
	if cfg.Hooks[1].Event != "PostToolUse" || !cfg.Hooks[1].Disabled {
		t.Fatalf("unexpected second hook: %#v", cfg.Hooks[1])
	}
}

// TestParseAllowsHooksNamespace verifies [hooks] can group event hook arrays
// without creating a hook entry on its own.
func TestParseAllowsHooksNamespace(t *testing.T) {
	cfg, err := Parse(`
[hooks]

[[hooks.PreToolUse]]
matcher = "^bash$"
command = "first"

[[hooks.PreToolUse]]
matcher = "^write$"
command = "second"
`)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if len(cfg.Hooks) != 2 {
		t.Fatalf("expected two hooks, got %d", len(cfg.Hooks))
	}
	if cfg.Hooks[0].Command != "first" ||
		cfg.Hooks[1].Command != "second" {

		t.Fatalf("hooks are not in file order: %#v", cfg.Hooks)
	}
}

// TestFindWalksAncestors verifies project config discovery works from nested
// working directories.
func TestFindWalksAncestors(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ProjectConfigDir)
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("make config dir: %v", err)
	}
	path := filepath.Join(configDir, ConfigFileName)
	if err := os.WriteFile(
		path, []byte("[session]\ndir = \"x\"\n"), 0o644,
	); err != nil {

		t.Fatalf("write config: %v", err)
	}
	child := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("make child: %v", err)
	}

	got, err := Find(child)
	if err != nil {
		t.Fatalf("find config: %v", err)
	}
	if got != path {
		t.Fatalf("expected %s, got %s", path, got)
	}
}

// TestParseRejectsUnknownKeys keeps the config language intentionally small.
func TestParseRejectsUnknownKeys(t *testing.T) {
	_, err := Parse("[provider]\nunknown = \"x\"\n")
	if err == nil {
		t.Fatal("expected unknown key error")
	}
}
