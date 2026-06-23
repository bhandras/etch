package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

[context]
auto_compact = true
auto_compact_threshold_tokens = 1000
keep_recent_tokens = 500

[prompt]
system_prompt = '''
Prefer go_inspect for Go code.
'''

[provider]
name = "openai"
model = "gpt-5.5"

[openai]
base_url = "https://api.example.test/v1"
api = "responses"
transport = "auto"
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
env = ["GIT_CONFIG_GLOBAL"]
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
	if cfg.Prompt.SystemPrompt != "Prefer go_inspect for Go code.\n" {
		t.Fatalf("unexpected prompt config: %#v", cfg.Prompt)
	}
	if cfg.Provider.Name != "openai" || cfg.Provider.Model != "gpt-5.5" {
		t.Fatalf("unexpected provider config: %#v", cfg.Provider)
	}
	if cfg.OpenAI.API != "responses" ||
		cfg.OpenAI.Transport != "auto" ||
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
		cfg.Plugins[0].TimeoutSeconds != 5 ||
		len(cfg.Plugins[0].Env) != 1 ||
		cfg.Plugins[0].Env[0] != "GIT_CONFIG_GLOBAL" {

		t.Fatalf("unexpected first plugin: %#v", cfg.Plugins[0])
	}
	if cfg.Plugins[1].Name != "disabled" || !cfg.Plugins[1].Disabled {
		t.Fatalf("unexpected second plugin: %#v", cfg.Plugins[1])
	}
}

// TestParseReadsSubagentProfiles verifies configured child-agent profiles map
// through the dependency-free TOML subset.
func TestParseReadsSubagentProfiles(t *testing.T) {
	cfg, err := Parse(`
[subagents]
enabled = true
max_per_turn = 3
max_concurrent = 2

[[subagents.profile]]
name = "review"
description = "Read-only reviewer."
provider = "openai"
model = "gpt-5.5"
openai_api = "responses"
reasoning_effort = "medium"
reasoning_summary = "auto"
system_prompt = '''
Review carefully.

Return findings first.
'''
allowed_tools = ["ls", "read", "grep"]
max_tool_rounds = 12
auto_compact = true
auto_compact_threshold_tokens = 80000
keep_messages = 8
keep_recent_tokens = 12000

[[subagents.profile]]
name = "disabled"
description = "Hidden profile."
allowed_tools = ["ls"]
disabled = true
`)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if !cfg.Subagents.Enabled ||
		cfg.Subagents.MaxPerTurn != 3 ||
		cfg.Subagents.MaxConcurrent != 2 {

		t.Fatalf("unexpected subagent config: %#v", cfg.Subagents)
	}
	if len(cfg.Subagents.Profiles) != 2 {
		t.Fatalf("expected two profiles, got %d",
			len(cfg.Subagents.Profiles))
	}
	review := cfg.Subagents.Profiles[0]
	if review.Name != "review" || review.Model != "gpt-5.5" ||
		review.OpenAIAPI != "responses" ||
		review.ReasoningSummary != "auto" ||
		review.SystemPrompt != "Review carefully.\n\n"+
			"Return findings first.\n" {

		t.Fatalf("unexpected review profile: %#v", review)
	}
	if strings.Join(review.AllowedTools, ",") != "ls,read,grep" {
		t.Fatalf("unexpected allowed tools: %#v", review.AllowedTools)
	}
	if !review.AutoCompact ||
		review.AutoCompactThresholdTokens != 80000 ||
		review.KeepMessages != 8 ||
		review.KeepRecentTokens != 12000 {

		t.Fatalf("unexpected compaction config: %#v", review)
	}
	if !cfg.Subagents.Profiles[1].Disabled {
		t.Fatalf("disabled profile flag was not parsed: %#v",
			cfg.Subagents.Profiles[1])
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

// TestParseArrayTableAssignmentsNeedArrayScope verifies repeatable config
// sections can use normal tables as namespaces but not as scalar targets.
func TestParseArrayTableAssignmentsNeedArrayScope(t *testing.T) {
	tests := []struct {
		// name identifies the invalid namespace assignment.
		name string

		// text is the config fragment expected to fail.
		text string

		// want is the stable diagnostic fragment expected from schema.
		want string
	}{
		{
			name: "plugins",
			text: "[plugins]\nname = \"bad\"\n",
			want: "plugins setting \"name\" must be inside " +
				"[[plugins]]",
		},
		{
			name: "hooks",
			text: "[hooks]\ncommand = \"bad\"\n",
			want: "hooks setting \"command\" must be inside " +
				"[[hooks.*]]",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(test.text)
			if err == nil {
				t.Fatal("expected parse error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q in error %q", test.want,
					err.Error())
			}
		})
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

// TestLoadMergesHomeAndProjectConfigs verifies user defaults are merged before
// the nearest project config.
func TestLoadMergesHomeAndProjectConfigs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTestConfig(
		t,
		filepath.Join(home, ProjectConfigDir, ConfigFileName),
		`
[session]
max_tool_rounds = 100

[context]
auto_compact = true
keep_recent_tokens = 20000

[provider]
name = "openai"
model = "gpt-home"

[[plugins]]
name = "home"
command = "home-plugin"

[subagents]
enabled = true

[[subagents.profile]]
name = "home-review"
description = "home profile"
allowed_tools = ["read"]
`,
	)

	project := t.TempDir()
	writeTestConfig(
		t,
		filepath.Join(project, ProjectConfigDir, ConfigFileName),
		`
[session]
max_tool_rounds = 250

[provider]
model = "gpt-project"

[[plugins]]
name = "project"
command = "project-plugin"

[[subagents.profile]]
name = "project-review"
description = "project profile"
allowed_tools = ["grep"]
`,
	)

	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Path != filepath.Join(project, ProjectConfigDir, ConfigFileName) {
		t.Fatalf("unexpected high-precedence path: %q", cfg.Path)
	}
	if len(cfg.Paths) != 2 {
		t.Fatalf("unexpected merged paths: %#v", cfg.Paths)
	}
	if cfg.Provider.Name != "openai" ||
		cfg.Provider.Model != "gpt-project" ||
		cfg.Session.MaxToolRounds != 250 ||
		!cfg.Context.AutoCompact ||
		cfg.Context.KeepRecentTokens != 20000 {

		t.Fatalf("unexpected merged scalars: %#v", cfg)
	}
	if len(cfg.Plugins) != 2 ||
		cfg.Plugins[0].Name != "home" ||
		cfg.Plugins[1].Name != "project" {

		t.Fatalf("unexpected merged plugins: %#v", cfg.Plugins)
	}
	if !cfg.Subagents.Enabled ||
		len(cfg.Subagents.Profiles) != 2 ||
		cfg.Subagents.Profiles[0].Name != "home-review" ||
		cfg.Subagents.Profiles[1].Name != "project-review" {

		t.Fatalf("unexpected merged subagents: %#v", cfg.Subagents)
	}
}

// TestLoadUsesHomeConfigWithoutProject verifies global config works from an
// arbitrary directory.
func TestLoadUsesHomeConfigWithoutProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTestConfig(
		t, filepath.Join(home, ProjectConfigDir, ConfigFileName),
		"[provider]\nname = \"openai\"\n",
	)
	project := t.TempDir()

	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ProjectConfigDir, ConfigFileName)
	if cfg.Path != want || len(cfg.Paths) != 1 || cfg.Paths[0] != want {
		t.Fatalf("unexpected home config paths: %#v path=%q", cfg.Paths,
			cfg.Path)
	}
	if cfg.Provider.Name != "openai" {
		t.Fatalf("home config was not loaded: %#v", cfg.Provider)
	}
}

// TestLoadValidatesAfterMerging verifies partial config layers may combine
// into one semantically valid effective config.
func TestLoadValidatesAfterMerging(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTestConfig(
		t,
		filepath.Join(home, ProjectConfigDir, ConfigFileName),
		`
[provider]
name = "openai"
model = "gpt-home"

[subagents]
enabled = true
`,
	)

	project := t.TempDir()
	writeTestConfig(
		t,
		filepath.Join(project, ProjectConfigDir, ConfigFileName),
		`
[[subagents.profile]]
name = "project-review"
description = "project profile"
allowed_tools = ["read"]
`,
	)

	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Subagents.Enabled || len(cfg.Subagents.Profiles) != 1 {
		t.Fatalf("unexpected merged subagents: %#v", cfg.Subagents)
	}
	if cfg.Subagents.Profiles[0].Name != "project-review" {
		t.Fatalf("unexpected merged profile: %#v",
			cfg.Subagents.Profiles[0])
	}
}

// TestLoadAllowsExplicitFalseOverrides verifies boolean scalar assignments
// keep enough presence metadata for project configs to disable home defaults.
func TestLoadAllowsExplicitFalseOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTestConfig(
		t,
		filepath.Join(home, ProjectConfigDir, ConfigFileName),
		`
[provider]
name = "openai"
model = "gpt-home"

[context]
auto_compact = true

[subagents]
enabled = true

[[subagents.profile]]
name = "home-review"
description = "home profile"
allowed_tools = ["read"]
`,
	)

	project := t.TempDir()
	writeTestConfig(
		t,
		filepath.Join(project, ProjectConfigDir, ConfigFileName),
		`
[context]
auto_compact = false

[subagents]
enabled = false
`,
	)

	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Context.AutoCompact {
		t.Fatalf("project auto_compact=false did not override: %#v",
			cfg.Context)
	}
	if cfg.Subagents.Enabled {
		t.Fatalf("project subagents.enabled=false did not "+
			"override: %#v", cfg.Subagents)
	}
}

// TestParseRejectsUnknownKeys keeps the config language intentionally small.
func TestParseRejectsUnknownKeys(t *testing.T) {
	_, err := Parse("[provider]\nunknown = \"x\"\n")
	if err == nil {
		t.Fatal("expected unknown key error")
	}
	if !strings.Contains(err.Error(), "unknown key provider.unknown") ||
		!strings.Contains(err.Error(), "known keys:") {

		t.Fatalf("unexpected unknown key error: %v", err)
	}
}

// TestParseRejectsInvalidSemanticConfig verifies schema validation catches
// values that parse but would fail later at runtime.
func TestParseRejectsInvalidSemanticConfig(t *testing.T) {
	tests := []struct {
		// name identifies the invalid config case.
		name string

		// text is the config fragment expected to fail.
		text string

		// want is the stable diagnostic fragment expected in the error.
		want string
	}{
		{
			name: "provider",
			text: "[provider]\nname = \"mystery\"\n",
			want: "provider.name must be one of",
		},
		{
			name: "openai api",
			text: "[openai]\napi = \"mystery\"\n",
			want: "openai.api must be one of",
		},
		{
			name: "openai transport",
			text: "[openai]\ntransport = \"pipe\"\n",
			want: "openai.transport must be one of",
		},
		{
			name: "reasoning effort",
			text: "[openai]\nreasoning_effort = \"maximum\"\n",
			want: "openai.reasoning_effort must be one of",
		},
		{
			name: "hook event",
			text: "[[hooks]]\nevent = \"Nope\"\ncommand = \"cat\"\n",
			want: "hooks[1].event must be one of",
		},
		{
			name: "hook command",
			text: "[[hooks.PreToolUse]]\n",
			want: "hooks[1].command must not be empty",
		},
		{
			name: "hook matcher",
			text: "[[hooks.PreToolUse]]\nmatcher = \"[\"\ncommand = \"cat\"\n",
			want: "hooks[1].matcher:",
		},
		{
			name: "plugin name",
			text: "[[plugins]]\ncommand = \"cat\"\n",
			want: "plugins[1].name must not be empty",
		},
		{
			name: "enabled subagents need profile",
			text: "[subagents]\nenabled = true\n",
			want: "subagents.enabled requires at least one enabled",
		},
		{
			name: "subagent model",
			text: "[subagents]\nenabled = true\n" +
				"[[subagents.profile]]\nname = \"review\"\n" +
				"description = \"Review.\"\n" +
				"provider = \"openai\"\n" +
				"allowed_tools = [\"read\"]\n",
			want: "subagents.profile[1].model must not be empty",
		},
		{
			name: "subagent blank model",
			text: "[subagents]\nenabled = true\n" +
				"[provider]\nmodel = \"gpt-test\"\n" +
				"[[subagents.profile]]\nname = \"review\"\n" +
				"description = \"Review.\"\n" +
				"model = \"  \"\n" +
				"allowed_tools = [\"read\"]\n",
			want: "subagents.profile[1].model must not be blank",
		},
		{
			name: "subagent base url",
			text: "[subagents]\nenabled = true\n" +
				"[provider]\nmodel = \"gpt-test\"\n" +
				"[[subagents.profile]]\nname = \"review\"\n" +
				"description = \"Review.\"\n" +
				"base_url = \"://bad\"\n" +
				"allowed_tools = [\"read\"]\n",
			want: "subagents.profile[1].base_url:",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(test.text)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q in error %q", test.want,
					err.Error())
			}
		})
	}
}

// TestParseRejectsTrailingMultilineContent verifies literal multiline strings
// do not silently discard invalid trailing text after the closing delimiter.
func TestParseRejectsTrailingMultilineContent(t *testing.T) {
	_, err := Parse("[provider]\nname = '''\nopenai''' trailing\n")
	if err == nil {
		t.Fatal("expected trailing literal multiline error")
	}
	if !strings.Contains(err.Error(), "unexpected trailing content") {
		t.Fatalf("unexpected multiline error: %v", err)
	}
}

// TestParseAllowsDisabledIncompleteExtensions verifies disabled hook and plugin
// placeholders may omit runtime-required fields.
func TestParseAllowsDisabledIncompleteExtensions(t *testing.T) {
	_, err := Parse(`
[[hooks.PreToolUse]]
disabled = true

[[plugins]]
disabled = true
`)
	if err != nil {
		t.Fatalf("parse disabled extension placeholders: %v", err)
	}
}

// TestParseRejectsInvalidPluginEnv verifies plugin env allowlists only accept
// environment variable identifiers.
func TestParseRejectsInvalidPluginEnv(t *testing.T) {
	_, err := Parse(`
[[plugins]]
name = "bad"
command = "cat"
env = ["OPEN AI KEY"]
`)
	if err == nil || !strings.Contains(err.Error(), "plugins[1].env[0]") {
		t.Fatalf("expected invalid env error, got %v", err)
	}
}

// TestParseRejectsPromptTextAndFile verifies project prompt config has one
// source of truth.
func TestParseRejectsPromptTextAndFile(t *testing.T) {
	_, err := Parse(`
[prompt]
system_prompt = "inline"
system_prompt_file = ".harness/SYSTEM.md"
`)
	if err == nil || !strings.Contains(
		err.Error(),
		"prompt must set only one",
	) {

		t.Fatalf("expected prompt source conflict, got %v", err)
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

// writeTestConfig writes a config fixture and creates parent directories.
func writeTestConfig(t *testing.T, path string, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("make config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
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
	for _, field := range Fields() {
		keys[sampleConfigKey{
			Table: field.Table,
			Key:   field.Key,
		}] = true
	}

	return keys
}

// documentedSampleConfigKeys scans the sample file for commented or active TOML
// settings and returns a parseable config built from the examples.
func documentedSampleConfigKeys(text string) (map[sampleConfigKey]bool,
	string) {

	documented := make(map[sampleConfigKey]bool)
	var materialized strings.Builder
	var table string
	arrayTable := false
	inMultilineString := false
	for _, raw := range strings.Split(text, "\n") {
		line := uncommentSampleConfigLine(raw)
		if line == "" {
			continue
		}
		if inMultilineString {
			materialized.WriteString(line)
			materialized.WriteByte('\n')
			if sampleConfigLineEndsMultilineString(line) {
				inMultilineString = false
			}

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
		if sampleConfigLineStartsMultilineString(line) {
			inMultilineString = true
		}
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

// sampleConfigLineStartsMultilineString reports whether a materialized sample
// key opens a TOML multiline string that continues onto later sample lines.
func sampleConfigLineStartsMultilineString(line string) bool {
	return sampleConfigTripleQuoteCount(line)%2 == 1
}

// sampleConfigLineEndsMultilineString reports whether a materialized sample
// line closes the current TOML multiline string.
func sampleConfigLineEndsMultilineString(line string) bool {
	return sampleConfigTripleQuoteCount(line)%2 == 1
}

// sampleConfigTripleQuoteCount counts TOML multiline string delimiters used by
// the sample file.
func sampleConfigTripleQuoteCount(line string) int {
	return strings.Count(line, "'''") + strings.Count(line, "\"\"\"")
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
		table == "prompt" || table == "provider" || table == "openai":
		return table, true

	case table == "plugins":
		return "plugins", true

	case table == "subagents":
		return "subagents", true

	case table == "subagents.profile":
		return "subagents.profile", true

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
