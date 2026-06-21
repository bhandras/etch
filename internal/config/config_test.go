package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// TestParseReadsProviderSessionAndHooks verifies the supported TOML subset maps
// cleanly into runtime configuration.
func TestParseReadsProviderSessionAndHooks(t *testing.T) {
	cfg, err := Parse(`
[session]
dir = ".data/sessions"
max_tool_rounds = 64
keep_messages = 20

[context]
auto_compact = true
auto_compact_threshold_tokens = 1000
keep_recent_tokens = 500

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

[[plugins]]
name = "git"
command = ".harness/plugins/git"
timeout_seconds = 5

[[plugins]]
name = "disabled"
command = "exit 1"
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
	if !cfg.Context.AutoCompact ||
		cfg.Context.AutoCompactThresholdTokens != 1000 ||
		cfg.Context.KeepRecentTokens != 500 {

		t.Fatalf("unexpected context config: %#v", cfg.Context)
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
	if len(cfg.Plugins) != 2 {
		t.Fatalf("expected two plugins, got %d", len(cfg.Plugins))
	}
	if cfg.Plugins[0].Name != "git" ||
		cfg.Plugins[0].Command != ".harness/plugins/git" ||
		cfg.Plugins[0].TimeoutSeconds != 5 {

		t.Fatalf("unexpected first plugin: %#v", cfg.Plugins[0])
	}
	if cfg.Plugins[1].Name != "disabled" || !cfg.Plugins[1].Disabled {
		t.Fatalf("unexpected second plugin: %#v", cfg.Plugins[1])
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

// TestSampleConfigDocumentsSupportedKeys verifies CI fails when a config field
// is not represented in the sample configuration file.
func TestSampleConfigDocumentsSupportedKeys(t *testing.T) {
	content, err := os.ReadFile(
		filepath.Join("..", "..", "sample-config.toml"),
	)
	if err != nil {
		t.Fatalf("read sample config: %v", err)
	}

	documented, materialized := documentedSampleConfigKeys(string(content))
	if _, err := Parse(materialized); err != nil {
		t.Fatalf("parse materialized sample config: %v\n%s", err,
			materialized)
	}

	supported := supportedSampleConfigKeys()
	missing := sampleConfigKeyDifference(supported, documented)
	if len(missing) > 0 {
		t.Fatalf("sample-config.toml is missing documented keys: %s",
			strings.Join(missing, ", "))
	}

	extra := sampleConfigKeyDifference(documented, supported)
	if len(extra) > 0 {
		t.Fatalf("sample-config.toml documents unknown keys: %s",
			strings.Join(extra, ", "))
	}
}

// sampleConfigKey identifies one table-qualified config setting in the sample.
type sampleConfigKey struct {
	// Table is the TOML table or array-table family that owns Key.
	Table string

	// Key is the scalar setting name documented under Table.
	Key string
}

// String returns the table-qualified form used in test failure messages.
func (k sampleConfigKey) String() string {
	return fmt.Sprintf("%s.%s", k.Table, k.Key)
}

// supportedSampleConfigKeys returns every configurable field that should appear
// in sample-config.toml.
func supportedSampleConfigKeys() map[sampleConfigKey]bool {
	keys := make(map[sampleConfigKey]bool)
	addConfigStructKeys(keys, "session", SessionConfig{})
	addConfigStructKeys(keys, "context", ContextConfig{})
	addConfigStructKeys(keys, "provider", ProviderConfig{})
	addConfigStructKeys(keys, "openai", OpenAIConfig{})
	addConfigStructKeys(keys, "hooks", HookConfig{})
	addConfigStructKeys(keys, "plugins", PluginConfig{})

	return keys
}

// addConfigStructKeys adds exported fields from cfg as snake-case config keys.
func addConfigStructKeys(keys map[sampleConfigKey]bool, table string, cfg any) {
	typ := reflect.TypeOf(cfg)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		keys[sampleConfigKey{
			Table: table,
			Key:   configFieldKey(field.Name),
		}] = true
	}
}

// configFieldKey converts a Go config field name into its TOML key spelling.
func configFieldKey(name string) string {
	var words []string
	var current []rune
	runes := []rune(name)
	for i, r := range runes {
		if shouldStartConfigWord(runes, i) {
			words = append(words, string(current))
			current = nil
		}
		current = append(current, unicode.ToLower(r))
	}
	if len(current) > 0 {
		words = append(words, string(current))
	}

	return strings.Join(words, "_")
}

// shouldStartConfigWord reports whether name[index] begins a new key word.
func shouldStartConfigWord(name []rune, index int) bool {
	if index == 0 || !unicode.IsUpper(name[index]) {
		return false
	}
	previous := name[index-1]
	if unicode.IsLower(previous) || unicode.IsDigit(previous) {
		return true
	}
	if index+1 < len(name) && unicode.IsLower(name[index+1]) {
		return true
	}

	return false
}

// documentedSampleConfigKeys scans the sample file for commented or active TOML
// settings and returns a parseable config built from the examples.
func documentedSampleConfigKeys(text string) (map[sampleConfigKey]bool,
	string) {

	documented := make(map[sampleConfigKey]bool)
	var materialized strings.Builder
	var table string
	arrayTable := false
	for _, raw := range strings.Split(text, "\n") {
		line := uncommentSampleConfigLine(raw)
		if line == "" {
			continue
		}

		if name, isArray, ok := sampleConfigTable(line); ok {
			table = name
			arrayTable = isArray
			if sampleConfigTableIsMaterialized(name, isArray) {
				materialized.WriteString(line)
				materialized.WriteByte('\n')
			}

			continue
		}

		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		group, ok := sampleConfigKeyGroup(table, arrayTable)
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		documented[sampleConfigKey{Table: group, Key: key}] = true
		materialized.WriteString(line)
		materialized.WriteByte('\n')
	}

	return documented, materialized.String()
}

// uncommentSampleConfigLine strips the sample's leading comment marker from
// example TOML while leaving active TOML unchanged.
func uncommentSampleConfigLine(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "#") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
	}

	return line
}

// sampleConfigTable returns the table name and array-table flag from a TOML
// table header.
func sampleConfigTable(line string) (string, bool, bool) {
	if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
		name := strings.TrimSpace(
			strings.TrimSuffix(
				strings.TrimPrefix(line, "[["),
				"]]",
			),
		)

		return name, true, name != ""
	}
	if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
		name := strings.TrimSpace(
			strings.TrimSuffix(
				strings.TrimPrefix(line, "["),
				"]",
			),
		)

		return name, false, name != ""
	}

	return "", false, false
}

// sampleConfigTableIsMaterialized reports whether table belongs in the
// generated sample config used to validate example values.
func sampleConfigTableIsMaterialized(table string, arrayTable bool) bool {
	if _, ok := sampleConfigKeyGroup(table, arrayTable); ok {
		return true
	}

	return table == "hooks" && !arrayTable
}

// sampleConfigKeyGroup maps concrete TOML table names into documented families.
func sampleConfigKeyGroup(table string, arrayTable bool) (string, bool) {
	switch {
	case table == "session" || table == "context" ||
		table == "provider" || table == "openai":
		return table, true

	case table == "plugins":
		return "plugins", true

	case strings.HasPrefix(table, "hooks.") ||
		(table == "hooks" && arrayTable):
		return "hooks", true

	default:
		return "", false
	}
}

// sampleConfigKeyDifference returns sorted keys present in left and absent from
// right.
func sampleConfigKeyDifference(left map[sampleConfigKey]bool,
	right map[sampleConfigKey]bool) []string {

	var diff []string
	for key := range left {
		if !right[key] {
			diff = append(diff, key.String())
		}
	}
	sort.Strings(diff)

	return diff
}
