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

	// Subagents stores named child-agent profiles available to the parent
	// model through the task delegation tool.
	Subagents SubagentConfig
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

	// Transport selects the provider transport, such as http, websocket, or
	// auto.
	Transport string

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

// SubagentConfig stores global controls and configured child-agent profiles.
type SubagentConfig struct {
	// Enabled exposes the task delegation tool when true and at least one
	// enabled profile exists.
	Enabled bool

	// MaxPerTurn caps how many subagents the parent may request in one
	// turn. Zero means the runtime uses its default.
	MaxPerTurn int

	// MaxConcurrent caps how many child agents may run at the same time.
	// Zero means the runtime uses its default.
	MaxConcurrent int

	// Profiles stores named child-agent configurations in file order.
	Profiles []SubagentProfileConfig
}

// SubagentProfileConfig describes one configured child-agent flavor.
type SubagentProfileConfig struct {
	// Name is the model-facing profile identifier.
	Name string

	// Description explains when the parent model should choose this
	// profile.
	Description string

	// Provider optionally overrides the parent provider for this profile.
	Provider string

	// Model optionally overrides the parent model for this profile.
	Model string

	// BaseURL optionally overrides the OpenAI-compatible base URL.
	BaseURL string

	// OpenAIAPI optionally overrides the OpenAI API shape.
	OpenAIAPI string

	// ReasoningEffort optionally overrides OpenAI reasoning effort.
	ReasoningEffort string

	// ReasoningSummary optionally overrides OpenAI reasoning summaries.
	ReasoningSummary string

	// SystemPrompt stores profile-specific child-agent instructions.
	SystemPrompt string

	// SystemPromptFile stores a path to profile-specific instructions.
	SystemPromptFile string

	// AllowedTools lists model-facing tools this child may call.
	AllowedTools []string

	// MaxToolRounds caps model/tool exchange rounds for this child.
	MaxToolRounds int

	// AutoCompact enables automatic compaction for this child profile.
	AutoCompact bool

	// AutoCompactThresholdTokens controls child automatic compaction.
	AutoCompactThresholdTokens int

	// KeepMessages controls child compaction message retention.
	KeepMessages int

	// KeepRecentTokens controls child compaction raw context retention.
	KeepRecentTokens int

	// Disabled keeps the profile configured without advertising it.
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

	lines, err := configLogicalLines(text)
	if err != nil {
		return Config{}, err
	}
	for _, logical := range lines {
		lineNumber := logical.line
		line := strings.TrimSpace(stripComment(logical.text))
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

// configLine stores one logical TOML line and its original start line number.
type configLine struct {
	// line is the one-indexed source line where text begins.
	line int

	// text is a parseable logical TOML statement.
	text string
}

// configLogicalLines joins literal multiline string assignments.
func configLogicalLines(text string) ([]configLine, error) {
	rawLines := strings.Split(text, "\n")
	lines := make([]configLine, 0, len(rawLines))
	for i := 0; i < len(rawLines); i++ {
		raw := rawLines[i]
		lineNumber := i + 1
		line := strings.TrimSpace(stripComment(raw))
		if !assignmentStartsLiteralMultiline(line) {
			lines = append(lines, configLine{
				line: lineNumber,
				text: raw,
			})

			continue
		}
		joined, next, err := collectLiteralMultiline(
			rawLines, i,
		)
		if err != nil {
			return nil, lineError(lineNumber, err)
		}
		lines = append(lines, configLine{
			line: lineNumber,
			text: joined,
		})
		i = next
	}

	return lines, nil
}

// assignmentStartsLiteralMultiline reports whether line begins a TOML literal
// multiline string assignment.
func assignmentStartsLiteralMultiline(line string) bool {
	_, value, err := parseAssignment(line)
	if err != nil {
		return false
	}

	return strings.HasPrefix(value, "'''") && !strings.HasSuffix(
		strings.TrimPrefix(value, "'''"),
		"'''",
	)
}

// collectLiteralMultiline joins one TOML literal multiline assignment.
func collectLiteralMultiline(lines []string, start int) (string, int, error) {
	key, value, err := parseAssignment(strings.TrimSpace(lines[start]))
	if err != nil {
		return "", start, err
	}
	body := strings.TrimPrefix(value, "'''")
	var out strings.Builder
	if body != "" {
		out.WriteString(body)
		out.WriteByte('\n')
	}
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "'''" {
			return key + " = " + strconv.Quote(out.String()), i, nil
		}
		if strings.Contains(line, "'''") {
			before, after, _ := strings.Cut(line, "'''")
			if strings.TrimSpace(after) != "" {
				return "", start, fmt.Errorf("unexpected " +
					"trailing content after literal " +
					"multiline string")
			}
			out.WriteString(before)

			return key + " = " + strconv.Quote(out.String()), i, nil
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}

	return "", start, fmt.Errorf("unterminated literal multiline string")
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

// parseStringList parses a single-line TOML array of quoted string values.
func parseStringList(value string) ([]string, error) {
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil, fmt.Errorf("expected string array")
	}
	body := strings.TrimSpace(
		strings.TrimSuffix(
			strings.TrimPrefix(value, "["),
			"]",
		),
	)
	if body == "" {
		return nil, nil
	}

	var values []string
	for len(body) > 0 {
		body = strings.TrimSpace(body)
		text, rest, err := parseStringListItem(body)
		if err != nil {
			return nil, err
		}
		values = append(values, text)
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, ",") {
			return nil, fmt.Errorf("expected comma between strings")
		}
		body = strings.TrimSpace(strings.TrimPrefix(rest, ","))
		if body == "" {
			return nil, fmt.Errorf("trailing comma is not " +
				"supported")
		}
	}

	return values, nil
}

// parseStringListItem parses one quoted string from the start of value.
func parseStringListItem(value string) (string, string, error) {
	if strings.HasPrefix(value, `"`) {
		escaped := false
		for i := 1; i < len(value); i++ {
			if escaped {
				escaped = false

				continue
			}
			if value[i] == '\\' {
				escaped = true

				continue
			}
			if value[i] == '"' {
				text, err := parseString(value[:i+1])

				return text, value[i+1:], err
			}
		}

		return "", "", fmt.Errorf("unterminated string")
	}
	if strings.HasPrefix(value, "'") {
		index := strings.Index(value[1:], "'")
		if index < 0 {
			return "", "", fmt.Errorf("unterminated string")
		}
		end := index + 2
		text, err := parseString(value[:end])

		return text, value[end:], err
	}

	return "", "", fmt.Errorf("expected quoted string")
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
