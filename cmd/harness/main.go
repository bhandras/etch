package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"harness/internal/core"
	"harness/internal/model"
	"harness/internal/provider/openai"
	"harness/internal/session"
	"harness/internal/tool"
)

const (
	// defaultSessionDir is the local directory used for JSONL session logs
	// when the caller does not provide one.
	defaultSessionDir = ".harness/sessions"

	// commandRun is the implicit command used when the caller passes -p.
	commandRun = "run"

	// commandSessions lists known local session index entries.
	commandSessions = "sessions"

	// commandShow renders one local session transcript.
	commandShow = "show"

	// commandTool executes a builtin tool directly for smoke testing.
	commandTool = "tool"

	// providerEcho selects the dependency-free deterministic model client.
	providerEcho = "echo"
)

// cliConfig stores parsed command-line options for one invocation.
type cliConfig struct {
	command     string
	prompt      string
	sessionDir  string
	jsonOutput  bool
	sessionID   string
	provider    string
	model       string
	baseURL     string
	apiKey      string
	toolName    string
	toolPath    string
	toolContent string
	toolOffset  int
	toolLimit   int
}

// main runs the command and exits with the returned status code.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses flags, executes one agent turn, and renders the result.
func run(args []string, stdout io.Writer, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 2
	}

	switch cfg.command {
	case commandRun:
		return runPrompt(cfg, stdout, stderr)

	case commandSessions:
		return listSessions(cfg, stdout, stderr)

	case commandShow:
		return showSession(cfg, stdout, stderr)

	case commandTool:
		return runTool(cfg, stdout, stderr)

	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n", cfg.command)

		return 2
	}
}

// runPrompt executes the default non-interactive prompt path.
func runPrompt(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "error: get working directory:", err)

		return 1
	}

	modelClient, err := modelClient(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	result, err := core.RunTurn(context.Background(), core.TurnRequest{
		Prompt:     cfg.prompt,
		SessionDir: cfg.sessionDir,
		CWD:        cwd,
		Model:      modelClient,
		Tools:      tool.DefaultRegistry(),
	})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	if cfg.jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, "error: encode json output:", err)

			return 1
		}

		return 0
	}

	fmt.Fprintf(stdout, "assistant: %s\n", result.AssistantText)

	return 0
}

// runTool executes one builtin tool directly for local smoke testing.
func runTool(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	registry := tool.DefaultRegistry()
	arguments, err := toolArguments(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 2
	}

	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID:        "manual",
		Name:      cfg.toolName,
		Arguments: arguments,
	})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	if cfg.jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, "error: encode json output:", err)

			return 1
		}

		return 0
	}

	fmt.Fprintln(stdout, result.Text)

	return 0
}

// listSessions renders the local session index in text or JSON form.
func listSessions(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	entries, err := session.List(cfg.sessionDir)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	if cfg.jsonOutput {
		if err := json.NewEncoder(stdout).Encode(entries); err != nil {
			fmt.Fprintln(stderr, "error: encode json output:", err)

			return 1
		}

		return 0
	}

	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no sessions")

		return 0
	}

	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, entry := range entries {
		fmt.Fprintf(
			table, "%s	%s	%s\n",
			formatSessionTime(entry.CreatedAt), shortID(entry.ID),
			entry.Title,
		)
	}
	if err := table.Flush(); err != nil {
		fmt.Fprintln(stderr, "error: render session list:", err)

		return 1
	}

	return 0
}

// showSession renders one local session by exact ID or unambiguous ID prefix.
func showSession(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	entry, err := session.Resolve(cfg.sessionDir, cfg.sessionID)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	events, err := session.ReadAll(entry.Path)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	if cfg.jsonOutput {
		if err := json.NewEncoder(stdout).Encode(events); err != nil {
			fmt.Fprintln(stderr, "error: encode json output:", err)

			return 1
		}

		return 0
	}

	for _, event := range events {
		if !isMessageEvent(event.Type) {
			continue
		}
		message, err := decodeMessage(event)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		for _, line := range renderMessage(message) {
			fmt.Fprintln(stdout, line)
		}
	}

	return 0
}

// parseFlags converts CLI arguments into the command configuration.
func parseFlags(args []string, stderr io.Writer) (cliConfig, error) {
	if len(args) > 0 {
		switch args[0] {
		case commandSessions:
			return parseSessionsFlags(args[1:], stderr)

		case commandShow:
			return parseShowFlags(args[1:], stderr)

		case commandTool:
			return parseToolFlags(args[1:], stderr)
		}
	}

	return parseRunFlags(args, stderr)
}

// parseToolFlags converts tool subcommand arguments into configuration.
func parseToolFlags(args []string, stderr io.Writer) (cliConfig, error) {
	if len(args) == 0 {
		return cliConfig{}, fmt.Errorf("provide a tool name")
	}
	cfg := cliConfig{
		command:  commandTool,
		toolName: args[0],
	}
	fs := flag.NewFlagSet(
		commandTool+" "+cfg.toolName, flag.ContinueOnError,
	)
	fs.SetOutput(stderr)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false, "print the tool result as JSON",
	)
	fs.IntVar(
		&cfg.toolLimit, "limit", 0,
		"maximum entries or lines for tools that support limits",
	)
	fs.IntVar(
		&cfg.toolOffset, "offset", 0,
		"1-indexed line offset for tools that support offsets",
	)
	fs.StringVar(
		&cfg.toolContent, "content", "",
		"complete file content for tools that write files",
	)
	if err := fs.Parse(args[1:]); err != nil {
		return cliConfig{}, err
	}

	switch cfg.toolName {
	case tool.NameLS:
		if fs.NArg() > 1 {
			return cliConfig{}, fmt.Errorf("ls accepts at most " +
				"one path")
		}
		if fs.NArg() == 1 {
			cfg.toolPath = fs.Arg(0)
		}

	case tool.NameRead:
		if fs.NArg() != 1 {
			return cliConfig{}, fmt.Errorf("read accepts exactly " +
				"one path")
		}
		cfg.toolPath = fs.Arg(0)

	case tool.NameWrite:
		if fs.NArg() != 1 {
			return cliConfig{}, fmt.Errorf("write accepts " +
				"exactly one path")
		}
		cfg.toolPath = fs.Arg(0)

	default:
		return cliConfig{}, fmt.Errorf("unknown tool %q", cfg.toolName)
	}

	return cfg, nil
}

// toolArguments converts direct CLI tool flags into raw JSON arguments.
func toolArguments(cfg cliConfig) (string, error) {
	switch cfg.toolName {
	case tool.NameLS:
		args := struct {
			Path  string `json:"path,omitempty"`
			Limit int    `json:"limit,omitempty"`
		}{
			Path:  cfg.toolPath,
			Limit: cfg.toolLimit,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal ls arguments: %w", err)
		}

		return string(encoded), nil

	case tool.NameRead:
		args := struct {
			Path   string `json:"path"`
			Offset int    `json:"offset,omitempty"`
			Limit  int    `json:"limit,omitempty"`
		}{
			Path:   cfg.toolPath,
			Offset: cfg.toolOffset,
			Limit:  cfg.toolLimit,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal read arguments: %w", err)
		}

		return string(encoded), nil

	case tool.NameWrite:
		args := struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}{
			Path:    cfg.toolPath,
			Content: cfg.toolContent,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal write arguments: %w",
				err)
		}

		return string(encoded), nil

	default:
		return "", fmt.Errorf("unknown tool %q", cfg.toolName)
	}
}

// parseRunFlags converts default command flags into a run configuration.
func parseRunFlags(args []string, stderr io.Writer) (cliConfig, error) {
	var cfg cliConfig
	cfg.command = commandRun
	cfg.provider = envDefault("HARNESS_PROVIDER", providerEcho)
	cfg.model = envDefault("OPENAI_MODEL", "")
	cfg.baseURL = envDefault("OPENAI_BASE_URL", openai.DefaultBaseURL)
	cfg.apiKey = os.Getenv("OPENAI_API_KEY")
	fs := flag.NewFlagSet("harness", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.prompt, "p", "", "prompt to run non-interactively")
	fs.StringVar(
		&cfg.sessionDir, "session-dir", defaultSessionDir,
		"session log directory",
	)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false, "print the turn result as JSON",
	)
	fs.StringVar(
		&cfg.provider, "provider", cfg.provider,
		"model provider: echo or openai",
	)
	fs.StringVar(&cfg.model, "model", cfg.model, "provider model name")
	fs.StringVar(
		&cfg.baseURL, "base-url", cfg.baseURL,
		"OpenAI-compatible API base URL",
	)
	fs.StringVar(&cfg.apiKey, "api-key", cfg.apiKey, "OpenAI API key")
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.prompt == "" {
		return cliConfig{}, fmt.Errorf("provide a prompt with -p")
	}

	return cfg, nil
}

// modelClient creates the provider selected by run command configuration.
func modelClient(cfg cliConfig) (model.Client, error) {
	switch cfg.provider {
	case "", providerEcho:
		return model.EchoClient{}, nil

	case openai.ProviderName:
		if cfg.model == "" {
			return nil, fmt.Errorf("openai provider requires " +
				"--model or OPENAI_MODEL")
		}
		if cfg.apiKey == "" {
			return nil, fmt.Errorf("openai provider requires " +
				"--api-key or OPENAI_API_KEY")
		}

		return &openai.Client{
			BaseURL: cfg.baseURL,
			APIKey:  cfg.apiKey,
			Model:   cfg.model,
		}, nil

	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.provider)
	}
}

// envDefault returns an environment value or fallback when the value is unset.
func envDefault(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}

// parseSessionsFlags converts sessions subcommand flags into configuration.
func parseSessionsFlags(args []string, stderr io.Writer) (cliConfig, error) {
	cfg := cliConfig{
		command:    commandSessions,
		sessionDir: defaultSessionDir,
	}
	fs := flag.NewFlagSet(commandSessions, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", defaultSessionDir,
		"session log directory",
	)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false,
		"print the session list as JSON",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}

	return cfg, nil
}

// parseShowFlags converts show subcommand flags and arguments into
// configuration.
func parseShowFlags(args []string, stderr io.Writer) (cliConfig, error) {
	cfg := cliConfig{
		command:    commandShow,
		sessionDir: defaultSessionDir,
	}
	fs := flag.NewFlagSet(commandShow, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", defaultSessionDir,
		"session log directory",
	)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false,
		"print the raw session events as JSON",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if fs.NArg() != 1 {
		return cliConfig{}, fmt.Errorf("provide exactly one session " +
			"id or prefix")
	}
	cfg.sessionID = fs.Arg(0)

	return cfg, nil
}

// isMessageEvent reports whether an event contains user-visible message text.
func isMessageEvent(eventType string) bool {
	return eventType == session.EventUserMessage ||
		eventType == session.EventAssistantMessage ||
		eventType == session.EventToolMessage
}

// decodeMessage unmarshals a message event payload into its typed shape.
func decodeMessage(event session.Event) (session.MessageData, error) {
	var message session.MessageData
	if err := json.Unmarshal(event.Data, &message); err != nil {
		return session.MessageData{}, fmt.Errorf("decode message "+
			"event: %w", err)
	}

	return message, nil
}

// messageText joins text content parts for human transcript rendering.
func messageText(message session.MessageData) string {
	var parts []string
	for _, part := range message.Content {
		if part.Type == session.ContentText {
			parts = append(parts, part.Text)
		}
	}

	return strings.Join(parts, "")
}

// renderMessage returns human transcript lines for one session message.
func renderMessage(message session.MessageData) []string {
	text := messageText(message)
	switch message.Role {
	case session.RoleAssistant:
		if text != "" {
			return []string{"assistant: " + text}
		}

		var lines []string
		for _, call := range message.ToolCalls {
			lines = append(
				lines, fmt.Sprintf("assistant tool_call "+
					"%s: %s", call.Name, call.Arguments),
			)
		}

		return lines

	case session.RoleTool:
		name := message.Name
		if name == "" {
			name = "tool"
		}

		return []string{fmt.Sprintf("tool %s: %s", name, text)}

	default:
		return []string{message.Role + ": " + text}
	}
}

// formatSessionTime renders index timestamps for compact terminal lists.
func formatSessionTime(createdAt time.Time) string {
	return createdAt.Local().Format("2006-01-02 15:04")
}

// shortID returns the display prefix used in human session lists.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}

	return id[:8]
}
