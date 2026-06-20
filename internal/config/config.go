package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// ProjectConfigDir is the project-local directory that owns harness
	// configuration and generated state.
	ProjectConfigDir = ".harness"

	// ConfigFileName is the TOML file name loaded from ProjectConfigDir.
	ConfigFileName = "config.toml"
)

// Config stores all settings loaded from a harness TOML file.
type Config struct {
	// Path is the absolute path to the loaded config file. Empty means no
	// config file was discovered.
	Path string

	// Session stores defaults for session-related CLI behavior.
	Session SessionConfig

	// Provider stores defaults for model provider selection.
	Provider ProviderConfig

	// OpenAI stores defaults for OpenAI-compatible provider options.
	OpenAI OpenAIConfig

	// Context stores defaults for prompt context management.
	Context ContextConfig

	// Hooks stores configured external process hooks in file order.
	Hooks []HookConfig
}

// SessionConfig stores defaults for local session handling.
type SessionConfig struct {
	// Dir is the directory where JSONL session logs are stored.
	Dir string

	// MaxToolRounds caps model/tool exchange loops within one user turn.
	MaxToolRounds int

	// KeepMessages is the number of recent message events preserved by
	// manual compaction.
	KeepMessages int
}

// ContextConfig stores defaults for model context maintenance.
type ContextConfig struct {
	// AutoCompact enables automatic session compaction before model calls.
	AutoCompact bool

	// AutoCompactThresholdTokens is the approximate context size that
	// triggers automatic compaction when AutoCompact is true.
	AutoCompactThresholdTokens int

	// KeepRecentTokens is the approximate amount of recent raw context to
	// retain after compaction.
	KeepRecentTokens int
}

// ProviderConfig stores provider defaults shared across provider backends.
type ProviderConfig struct {
	// Name is the provider identifier, such as "echo" or "openai".
	Name string

	// Model is the provider-specific model identifier.
	Model string
}

// OpenAIConfig stores defaults for the bundled OpenAI-compatible provider.
type OpenAIConfig struct {
	// BaseURL is the OpenAI-compatible API base URL.
	BaseURL string

	// API selects the provider API shape, such as "chat" or "responses".
	API string

	// ReasoningEffort requests a reasoning effort level when supported by
	// the selected model.
	ReasoningEffort string

	// ReasoningSummary requests displayable reasoning summaries when
	// supported by the selected model.
	ReasoningSummary string
}

// HookConfig describes one external command hook.
type HookConfig struct {
	// Event is the lifecycle event name that triggers the hook.
	Event string

	// Matcher filters event instances. The event decides what value is
	// matched; an empty matcher means all instances.
	Matcher string

	// Command is executed through the platform shell with hook JSON on
	// stdin and optional result JSON on stdout.
	Command string

	// TimeoutSeconds caps hook execution. Zero means the hook runner uses
	// its default timeout.
	TimeoutSeconds int

	// Disabled leaves the hook definition in config without running it.
	Disabled bool
}

// Load finds and parses the nearest project-local config for cwd.
func Load(cwd string) (Config, error) {
	path, err := Find(cwd)
	if err != nil {
		return Config{}, err
	}
	if path == "" {
		return Config{}, nil
	}

	cfg, err := ParseFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg.Path = path

	return cfg, nil
}

// Find returns the nearest .harness/config.toml at or above cwd.
func Find(cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return "", fmt.Errorf("config cwd must not be empty")
	}

	dir, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve config cwd: %w", err)
	}
	for {
		path := filepath.Join(dir, ProjectConfigDir, ConfigFileName)
		info, err := os.Stat(path)
		if err == nil {
			if info.IsDir() {
				return "", fmt.Errorf("config path is a "+
					"directory: %s", path)
			}

			return path, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat config %s: %w", path, err)
		}

		next := filepath.Dir(dir)
		if next == dir {
			return "", nil
		}
		dir = next
	}
}

// ParseFile parses one TOML config file.
func ParseFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg, err := Parse(string(data))
	if err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.Path = path

	return cfg, nil
}

// Parse parses the dependency-free TOML subset used by harness config files.
func Parse(text string) (Config, error) {
	var cfg Config
	var table string
	var activeHook *HookConfig

	lines := strings.Split(text, "\n")
	for i, raw := range lines {
		lineNumber := i + 1
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[[") {
			name, err := parseArrayTable(line)
			if err != nil {
				return Config{}, lineError(lineNumber, err)
			}
			hook, err := hookForArrayTable(name)
			if err != nil {
				return Config{}, lineError(lineNumber, err)
			}
			cfg.Hooks = append(cfg.Hooks, hook)
			activeHook = &cfg.Hooks[len(cfg.Hooks)-1]
			table = name

			continue
		}

		if strings.HasPrefix(line, "[") {
			name, err := parseTable(line)
			if err != nil {
				return Config{}, lineError(lineNumber, err)
			}
			if !knownTable(name) {
				return Config{}, lineError(
					lineNumber,
					fmt.Errorf("unknown table %q", name),
				)
			}
			table = name
			activeHook = nil

			continue
		}

		key, value, err := parseAssignment(line)
		if err != nil {
			return Config{}, lineError(lineNumber, err)
		}
		if err := applyAssignment(
			&cfg, activeHook, table, key, value,
		); err != nil {
			return Config{}, lineError(lineNumber, err)
		}
	}

	return cfg, nil
}

// parseArrayTable parses a TOML array table header.
func parseArrayTable(line string) (string, error) {
	if !strings.HasSuffix(line, "]]") {
		return "", fmt.Errorf("unterminated array table")
	}
	name := strings.TrimSpace(
		strings.TrimSuffix(
			strings.TrimPrefix(line, "[["),
			"]]",
		),
	)
	if name == "" {
		return "", fmt.Errorf("empty array table")
	}

	return name, nil
}

// parseTable parses a TOML table header.
func parseTable(line string) (string, error) {
	if !strings.HasSuffix(line, "]") {
		return "", fmt.Errorf("unterminated table")
	}
	name := strings.TrimSpace(
		strings.TrimSuffix(
			strings.TrimPrefix(line, "["),
			"]",
		),
	)
	if name == "" {
		return "", fmt.Errorf("empty table")
	}

	return name, nil
}

// hookForArrayTable creates the hook entry represented by an array table.
func hookForArrayTable(name string) (HookConfig, error) {
	if name == "hooks" {
		return HookConfig{}, nil
	}
	if !strings.HasPrefix(name, "hooks.") {
		return HookConfig{}, fmt.Errorf("unknown array table %q", name)
	}
	event := strings.TrimPrefix(name, "hooks.")
	if event == "" || strings.Contains(event, ".") {
		return HookConfig{}, fmt.Errorf("invalid hook event table %q",
			name)
	}

	return HookConfig{Event: event}, nil
}

// knownTable reports whether name is a supported normal table.
func knownTable(name string) bool {
	switch name {
	case "session", "provider", "openai", "context", "hooks":
		return true

	default:
		return false
	}
}

// parseAssignment splits a TOML scalar assignment into key and value text.
func parseAssignment(line string) (string, string, error) {
	index := strings.Index(line, "=")
	if index < 0 {
		return "", "", fmt.Errorf("expected key = value")
	}
	key := strings.TrimSpace(line[:index])
	value := strings.TrimSpace(line[index+1:])
	if key == "" {
		return "", "", fmt.Errorf("empty key")
	}
	if value == "" {
		return "", "", fmt.Errorf("empty value for %q", key)
	}

	return key, value, nil
}

// applyAssignment stores one parsed key/value pair into cfg.
func applyAssignment(cfg *Config, hook *HookConfig, table string, key string,
	value string) error {

	switch {
	case strings.HasPrefix(table, "hooks.") ||
		(table == "hooks" && hook != nil):

		if hook == nil {
			return fmt.Errorf("hook setting %q must be inside "+
				"[[hooks.*]]", key)
		}

		return applyHookAssignment(hook, key, value)

	case table == "session":
		return applySessionAssignment(&cfg.Session, key, value)

	case table == "provider":
		return applyProviderAssignment(&cfg.Provider, key, value)

	case table == "openai":
		return applyOpenAIAssignment(&cfg.OpenAI, key, value)

	case table == "context":
		return applyContextAssignment(&cfg.Context, key, value)

	case table == "hooks":
		return fmt.Errorf("unknown hooks key %q", key)

	case table == "":
		return fmt.Errorf("top-level key %q is not supported", key)

	default:
		return fmt.Errorf("unknown table %q", table)
	}
}

// applySessionAssignment stores one session table setting.
func applySessionAssignment(cfg *SessionConfig, key string,
	value string) error {

	switch key {
	case "dir":
		text, err := parseString(value)
		cfg.Dir = text

		return err

	case "max_tool_rounds":
		parsed, err := parsePositiveInt(value)
		cfg.MaxToolRounds = parsed

		return err

	case "keep_messages":
		parsed, err := parsePositiveInt(value)
		cfg.KeepMessages = parsed

		return err

	default:
		return fmt.Errorf("unknown session key %q", key)
	}
}

// applyContextAssignment stores one context table setting.
func applyContextAssignment(cfg *ContextConfig, key string,
	value string) error {

	switch key {
	case "auto_compact":
		parsed, err := parseBool(value)
		cfg.AutoCompact = parsed

		return err

	case "auto_compact_threshold_tokens":
		parsed, err := parsePositiveInt(value)
		cfg.AutoCompactThresholdTokens = parsed

		return err

	case "keep_recent_tokens":
		parsed, err := parsePositiveInt(value)
		cfg.KeepRecentTokens = parsed

		return err

	default:
		return fmt.Errorf("unknown context key %q", key)
	}
}

// applyProviderAssignment stores one provider table setting.
func applyProviderAssignment(cfg *ProviderConfig, key string,
	value string) error {

	text, err := parseString(value)
	if err != nil {
		return err
	}
	switch key {
	case "name":
		cfg.Name = text

	case "model":
		cfg.Model = text

	default:
		return fmt.Errorf("unknown provider key %q", key)
	}

	return nil
}

// applyOpenAIAssignment stores one OpenAI provider table setting.
func applyOpenAIAssignment(cfg *OpenAIConfig, key string, value string) error {
	text, err := parseString(value)
	if err != nil {
		return err
	}
	switch key {
	case "base_url":
		cfg.BaseURL = text

	case "api":
		cfg.API = text

	case "reasoning_effort":
		cfg.ReasoningEffort = text

	case "reasoning_summary":
		cfg.ReasoningSummary = text

	default:
		return fmt.Errorf("unknown openai key %q", key)
	}

	return nil
}

// applyHookAssignment stores one hook table setting.
func applyHookAssignment(cfg *HookConfig, key string, value string) error {
	switch key {
	case "event":
		text, err := parseString(value)
		cfg.Event = text

		return err

	case "matcher":
		text, err := parseString(value)
		cfg.Matcher = text

		return err

	case "command":
		text, err := parseString(value)
		cfg.Command = text

		return err

	case "timeout_seconds":
		parsed, err := parsePositiveInt(value)
		cfg.TimeoutSeconds = parsed

		return err

	case "disabled":
		parsed, err := parseBool(value)
		cfg.Disabled = parsed

		return err

	default:
		return fmt.Errorf("unknown hook key %q", key)
	}
}

// parseString parses a quoted TOML string value.
func parseString(value string) (string, error) {
	if len(value) < 2 {
		return "", fmt.Errorf("expected quoted string")
	}
	if strings.HasPrefix(value, `"`) {
		text, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("parse string: %w", err)
		}

		return text, nil
	}
	if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		return strings.TrimSuffix(strings.TrimPrefix(value, "'"), "'"), nil
	}

	return "", fmt.Errorf("expected quoted string")
}

// parsePositiveInt parses a positive integer value.
func parsePositiveInt(value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse integer: %w", err)
	}
	if parsed < 1 {
		return 0, fmt.Errorf("integer must be positive")
	}

	return parsed, nil
}

// parseBool parses a TOML boolean value.
func parseBool(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil

	case "false":
		return false, nil

	default:
		return false, fmt.Errorf("expected boolean")
	}
}

// stripComment removes TOML comments outside quoted strings.
func stripComment(line string) string {
	var quote rune
	escaped := false
	for i, r := range line {
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			continue
		}
		if r == '#' {
			return line[:i]
		}
	}

	return line
}

// lineError annotates a parse error with a line number.
func lineError(line int, err error) error {
	return fmt.Errorf("line %d: %w", line, err)
}
