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

	// Plugins stores explicitly configured plugin processes in file order.
	Plugins []PluginConfig
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

// PluginConfig describes one explicit out-of-process plugin command.
type PluginConfig struct {
	// Name is the human-readable plugin identifier used in diagnostics.
	Name string

	// Command starts the plugin process through the platform shell.
	Command string

	// TimeoutSeconds caps plugin initialization and tool calls. Zero means
	// the plugin client uses its default timeout.
	TimeoutSeconds int

	// Disabled leaves the plugin definition in config without starting it.
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
	scope := configScope{cfg: &cfg}

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
			scope, err = beginArrayConfigTable(&cfg, name)
			if err != nil {
				return Config{}, lineError(lineNumber, err)
			}

			continue
		}

		if strings.HasPrefix(line, "[") {
			name, err := parseTable(line)
			if err != nil {
				return Config{}, lineError(lineNumber, err)
			}
			scope, err = beginNormalConfigTable(&cfg, name)
			if err != nil {
				return Config{}, lineError(lineNumber, err)
			}

			continue
		}

		key, value, err := parseAssignment(line)
		if err != nil {
			return Config{}, lineError(lineNumber, err)
		}
		if err := applySchemaAssignment(scope, key, value); err != nil {
			return Config{}, lineError(lineNumber, err)
		}
	}

	if err := Validate(cfg); err != nil {
		return Config{}, err
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
