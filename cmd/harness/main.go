package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"harness/internal/core"
	"harness/internal/model"
	promptctx "harness/internal/prompt"
	"harness/internal/provider/openai"
	"harness/internal/render"
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

	// commandChat starts a minimal interactive line-oriented chat loop.
	commandChat = "chat"

	// commandCompact summarizes older session history into a context event.
	commandCompact = "compact"

	// providerEcho selects the dependency-free deterministic model client.
	providerEcho = "echo"
)

// cliConfig stores parsed command-line options for one invocation.
type cliConfig struct {
	command          string
	prompt           string
	sessionDir       string
	jsonOutput       bool
	sessionID        string
	provider         string
	model            string
	baseURL          string
	apiKey           string
	openaiAPI        string
	reasoningEffort  string
	reasoningSummary string
	toolName         string
	toolPath         string
	toolCommand      string
	toolContent      string
	toolOldText      string
	toolNewText      string
	toolQuery        string
	toolOffset       int
	toolLimit        int
	toolTimeout      int
	toolIgnoreCase   bool
	keepMessages     int
	maxToolRounds    int
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

	case commandChat:
		return runChat(cfg, os.Stdin, stdout, stderr)

	case commandCompact:
		return runCompact(cfg, stdout, stderr)

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
	systemText, err := promptctx.SystemText(cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	result, err := core.RunTurn(context.Background(), core.TurnRequest{
		Prompt:        cfg.prompt,
		SessionDir:    cfg.sessionDir,
		CWD:           cwd,
		SystemText:    systemText,
		Model:         modelClient,
		Tools:         tool.DefaultRegistry(),
		MaxToolRounds: cfg.maxToolRounds,
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

// runCompact appends a model-written summary to an existing session.
func runCompact(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	entry, err := session.Resolve(cfg.sessionDir, cfg.sessionID)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	modelClient, err := modelClient(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	result, err := core.CompactSession(context.Background(),
		core.CompactRequest{
			SessionPath:  entry.Path,
			Model:        modelClient,
			KeepMessages: cfg.keepMessages,
			ModelName:    cfg.model,
		})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	fmt.Fprintf(
		stdout, "compacted session %s\nsummary event: %s\n",
		shortID(entry.ID), result.SummaryEventID,
	)

	return 0
}

// runChat starts a line-oriented interactive chat session.
func runChat(cfg cliConfig, stdin io.Reader, stdout io.Writer,
	stderr io.Writer) int {

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
	systemText, err := promptctx.SystemText(cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	var sessionPath string
	if cfg.sessionID != "" {
		entry, err := session.Resolve(cfg.sessionDir, cfg.sessionID)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		sessionPath = entry.Path
		fmt.Fprintf(
			stdout, "continuing session %s\n", shortID(entry.ID),
		)
	}

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	printChatPrompt(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			printChatPrompt(stdout)
			continue
		}
		if strings.HasPrefix(line, "/") {
			keepGoing, nextPath := handleChatCommand(
				cfg, line, sessionPath, modelClient, stdout,
				stderr,
			)
			sessionPath = nextPath
			if !keepGoing {
				return 0
			}
			printChatPrompt(stdout)

			continue
		}

		observer := &chatObserver{
			renderer: newLiveChatRenderer(stdout, true),
		}
		startedAt := time.Now()
		result, err := core.RunTurn(
			context.Background(), core.TurnRequest{
				Prompt:        line,
				SessionDir:    cfg.sessionDir,
				SessionPath:   sessionPath,
				CWD:           cwd,
				SystemText:    systemText,
				Model:         modelClient,
				Tools:         tool.DefaultRegistry(),
				MaxToolRounds: cfg.maxToolRounds,
				Observer:      observer,
			},
		)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		observer.Finish(time.Since(startedAt))
		sessionPath = result.SessionPath
		printChatPrompt(stdout)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(stderr, "error: read chat input:", err)

		return 1
	}

	return 0
}

// chatObserver renders appended assistant and tool messages during a turn.
type chatObserver struct {
	// renderer owns transient terminal formatting for one chat turn.
	renderer *liveChatRenderer
}

// EventAppended renders model-visible assistant and tool events.
func (o *chatObserver) EventAppended(event session.Event) {
	if event.Type == session.EventUserMessage {
		return
	}
	if !isMessageEvent(event.Type) {
		return
	}

	message, err := decodeMessage(event)
	if err != nil {
		fmt.Fprintf(o.renderer.stdout, "render error: %v\n", err)

		return
	}
	if message.Role == session.RoleAssistant &&
		len(message.ToolCalls) > 0 &&
		render.MessageText(message) == "" {
		return
	}
	switch message.Role {
	case session.RoleAssistant:
		o.renderer.renderAssistant(render.MessageText(message))

	case session.RoleTool:
		o.renderer.renderToolResult(message)

	default:
		o.renderer.renderAssistant(render.MessageText(message))
	}
}

// ToolCallStarted renders one live tool call immediately before execution.
func (o *chatObserver) ToolCallStarted(call model.ToolCall) {
	o.renderer.renderToolCall(call)
}

// ReasoningCompleted renders one model-provided thinking summary block.
func (o *chatObserver) ReasoningCompleted(text string) {
	o.renderer.renderReasoning(text)
}

// Finish renders terminal-only end-of-turn decoration.
func (o *chatObserver) Finish(elapsed time.Duration) {
	o.renderer.finish(elapsed)
}

// handleChatCommand executes one slash command and returns whether to continue.
func handleChatCommand(cfg cliConfig, line string, sessionPath string,
	modelClient model.Client, stdout io.Writer,
	stderr io.Writer) (bool, string) {

	switch line {
	case "/exit", "/quit":
		return false, sessionPath

	case "/new":
		fmt.Fprintln(stdout, "started a new session")

		return true, ""

	case "/show":
		if sessionPath == "" {
			fmt.Fprintln(stdout, "no active session")

			return true, sessionPath
		}
		if err := renderSessionPath(sessionPath, stdout); err != nil {
			fmt.Fprintln(stderr, "error:", err)
		}

		return true, sessionPath

	case "/sessions":
		listSessions(cfg, stdout, stderr)

		return true, sessionPath

	case "/context":
		if sessionPath == "" {
			fmt.Fprintln(stdout, "no active session")

			return true, sessionPath
		}
		if err := printContextStats(sessionPath, stdout); err != nil {
			fmt.Fprintln(stderr, "error:", err)
		}

		return true, sessionPath

	case "/compact":
		if sessionPath == "" {
			fmt.Fprintln(stdout, "no active session")

			return true, sessionPath
		}
		result, err := core.CompactSession(context.Background(),
			core.CompactRequest{
				SessionPath:  sessionPath,
				Model:        modelClient,
				KeepMessages: cfg.keepMessages,
				ModelName:    cfg.model,
			})
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return true, sessionPath
		}
		fmt.Fprintf(
			stdout, "compacted context: %s\n",
			result.SummaryEventID,
		)

		return true, sessionPath

	case "/tools":
		for _, spec := range tool.DefaultRegistry().Specs() {
			fmt.Fprintln(stdout, spec.Name)
		}

		return true, sessionPath

	case "/help":
		fmt.Fprintln(
			stdout, "/exit /quit /new /show /sessions /context "+
				"/compact /tools /help",
		)

		return true, sessionPath

	default:
		fmt.Fprintf(stdout, "unknown command %s\n", line)

		return true, sessionPath
	}
}

// printChatPrompt writes the fixed line-mode prompt.
func printChatPrompt(stdout io.Writer) {
	fmt.Fprint(stdout, "> ")
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

	if err := renderEvents(events, stdout); err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	return 0
}

// renderSessionPath renders one session transcript from a JSONL path.
func renderSessionPath(path string, stdout io.Writer) error {
	events, err := session.ReadAll(path)
	if err != nil {
		return err
	}

	return renderEvents(events, stdout)
}

// renderSessionPathAfter renders transcript messages after the given event ID.
func renderSessionPathAfter(path string, afterID string,
	stdout io.Writer) error {

	events, err := session.ReadAll(path)
	if err != nil {
		return err
	}

	start := 0
	for i, event := range events {
		if event.ID == afterID {
			start = i + 1
			break
		}
	}

	return renderEvents(events[start:], stdout)
}

// printContextStats renders prompt context projection statistics for a session.
func printContextStats(path string, stdout io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	systemText, err := promptctx.SystemText(cwd)
	if err != nil {
		return err
	}
	events, err := session.ReadAll(path)
	if err != nil {
		return err
	}
	stats, err := promptctx.BuildStats(events, systemText)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, promptctx.FormatStats(stats))

	return nil
}

// renderEvents renders model-visible session messages as transcript lines.
func renderEvents(events []session.Event, stdout io.Writer) error {
	for _, event := range events {
		if !isMessageEvent(event.Type) {
			continue
		}
		message, err := decodeMessage(event)
		if err != nil {
			return err
		}
		for _, line := range render.MessageLines(message) {
			fmt.Fprintln(stdout, line)
		}
	}

	return nil
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

		case commandChat:
			return parseChatFlags(args[1:], stderr)

		case commandCompact:
			return parseCompactFlags(args[1:], stderr)
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
	fs.IntVar(
		&cfg.toolTimeout, "timeout", 0,
		"timeout in seconds for tools that run commands",
	)
	fs.StringVar(
		&cfg.toolContent, "content", "",
		"complete file content for tools that write files",
	)
	fs.StringVar(
		&cfg.toolOldText, "old", "",
		"exact original text for tools that edit files",
	)
	fs.StringVar(
		&cfg.toolNewText, "new", "",
		"exact replacement text for tools that edit files",
	)
	fs.BoolVar(
		&cfg.toolIgnoreCase, "ignore-case", false,
		"case-insensitive matching for tools that search text",
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

	case tool.NameFind:
		if fs.NArg() < 1 || fs.NArg() > 2 {
			return cliConfig{}, fmt.Errorf("find accepts a query " +
				"and optional path")
		}
		cfg.toolQuery = fs.Arg(0)
		if fs.NArg() == 2 {
			cfg.toolPath = fs.Arg(1)
		}

	case tool.NameGrep:
		if fs.NArg() < 1 || fs.NArg() > 2 {
			return cliConfig{}, fmt.Errorf("grep accepts a " +
				"pattern and optional path")
		}
		cfg.toolQuery = fs.Arg(0)
		if fs.NArg() == 2 {
			cfg.toolPath = fs.Arg(1)
		}

	case tool.NameWrite:
		if fs.NArg() != 1 {
			return cliConfig{}, fmt.Errorf("write accepts " +
				"exactly one path")
		}
		cfg.toolPath = fs.Arg(0)

	case tool.NameEdit:
		if fs.NArg() != 1 {
			return cliConfig{}, fmt.Errorf("edit accepts exactly " +
				"one path")
		}
		if cfg.toolOldText == "" {
			return cliConfig{}, fmt.Errorf("edit requires --old")
		}
		cfg.toolPath = fs.Arg(0)

	case tool.NameBash:
		if fs.NArg() == 0 {
			return cliConfig{}, fmt.Errorf("bash requires a " +
				"command")
		}
		cfg.toolCommand = strings.Join(fs.Args(), " ")

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

	case tool.NameFind:
		args := struct {
			Path  string `json:"path,omitempty"`
			Query string `json:"query,omitempty"`
			Limit int    `json:"limit,omitempty"`
		}{
			Path:  cfg.toolPath,
			Query: cfg.toolQuery,
			Limit: cfg.toolLimit,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal find arguments: %w", err)
		}

		return string(encoded), nil

	case tool.NameGrep:
		args := struct {
			Path       string `json:"path,omitempty"`
			Pattern    string `json:"pattern"`
			Limit      int    `json:"limit,omitempty"`
			IgnoreCase bool   `json:"ignoreCase,omitempty"`
		}{
			Path:       cfg.toolPath,
			Pattern:    cfg.toolQuery,
			Limit:      cfg.toolLimit,
			IgnoreCase: cfg.toolIgnoreCase,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal grep arguments: %w", err)
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

	case tool.NameEdit:
		args := struct {
			Path  string `json:"path"`
			Edits []struct {
				OldText string `json:"oldText"`
				NewText string `json:"newText"`
			} `json:"edits"`
		}{
			Path: cfg.toolPath,
			Edits: []struct {
				OldText string `json:"oldText"`
				NewText string `json:"newText"`
			}{{
				OldText: cfg.toolOldText,
				NewText: cfg.toolNewText,
			}},
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal edit arguments: %w", err)
		}

		return string(encoded), nil

	case tool.NameBash:
		args := struct {
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
		}{
			Command:        cfg.toolCommand,
			TimeoutSeconds: cfg.toolTimeout,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal bash arguments: %w", err)
		}

		return string(encoded), nil

	default:
		return "", fmt.Errorf("unknown tool %q", cfg.toolName)
	}
}

// parseChatFlags converts chat subcommand flags into configuration.
func parseChatFlags(args []string, stderr io.Writer) (cliConfig, error) {
	cfg := cliConfig{
		command:    commandChat,
		sessionDir: defaultSessionDir,
		provider:   envDefault("HARNESS_PROVIDER", providerEcho),
		model:      envDefault("OPENAI_MODEL", ""),
		baseURL:    envDefault("OPENAI_BASE_URL", openai.DefaultBaseURL),
		apiKey:     os.Getenv("OPENAI_API_KEY"),
		openaiAPI: envDefault(
			"HARNESS_OPENAI_API", openai.APIChatCompletions,
		),
		reasoningEffort:  envDefault("OPENAI_REASONING_EFFORT", ""),
		reasoningSummary: envDefault("OPENAI_REASONING_SUMMARY", ""),
		maxToolRounds: envIntDefault(
			"HARNESS_MAX_TOOL_ROUNDS", core.DefaultMaxToolRounds,
		),
	}
	fs := flag.NewFlagSet(commandChat, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", defaultSessionDir,
		"session log directory",
	)
	fs.StringVar(
		&cfg.sessionID, "session", "",
		"existing session id or prefix to continue",
	)
	fs.StringVar(
		&cfg.provider, "provider", cfg.provider,
		"model provider: echo or openai",
	)
	fs.StringVar(&cfg.model, "model", cfg.model, "provider model name")
	addOpenAIFlags(fs, &cfg)
	fs.StringVar(
		&cfg.baseURL, "base-url", cfg.baseURL,
		"OpenAI-compatible API base URL",
	)
	apiKeyFlag := apiKeyFlagValue(fs)
	fs.IntVar(
		&cfg.maxToolRounds, "max-tool-rounds", cfg.maxToolRounds,
		"maximum model/tool exchange rounds per user turn",
	)
	fs.IntVar(
		&cfg.keepMessages, "keep-messages",
		core.DefaultCompactKeepMessages,
		"recent message events kept raw by /compact",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.maxToolRounds < 1 {
		return cliConfig{}, fmt.Errorf("max-tool-rounds must be " +
			"positive")
	}
	applyAPIKeyFlag(&cfg, *apiKeyFlag)

	return cfg, nil
}

// parseCompactFlags converts compact subcommand flags into configuration.
func parseCompactFlags(args []string, stderr io.Writer) (cliConfig, error) {
	cfg := cliConfig{
		command:    commandCompact,
		sessionDir: defaultSessionDir,
		provider:   envDefault("HARNESS_PROVIDER", providerEcho),
		model:      envDefault("OPENAI_MODEL", ""),
		baseURL: envDefault(
			"OPENAI_BASE_URL", openai.DefaultBaseURL,
		),
		apiKey: os.Getenv("OPENAI_API_KEY"),
		openaiAPI: envDefault(
			"HARNESS_OPENAI_API", openai.APIChatCompletions,
		),
		reasoningEffort:  envDefault("OPENAI_REASONING_EFFORT", ""),
		reasoningSummary: envDefault("OPENAI_REASONING_SUMMARY", ""),
		keepMessages:     core.DefaultCompactKeepMessages,
	}
	fs := flag.NewFlagSet(commandCompact, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", defaultSessionDir,
		"session log directory",
	)
	fs.StringVar(
		&cfg.sessionID, "session", "",
		"existing session id or prefix to compact",
	)
	fs.StringVar(
		&cfg.provider, "provider", cfg.provider,
		"model provider: echo or openai",
	)
	fs.StringVar(&cfg.model, "model", cfg.model, "provider model name")
	addOpenAIFlags(fs, &cfg)
	fs.StringVar(
		&cfg.baseURL, "base-url", cfg.baseURL,
		"OpenAI-compatible API base URL",
	)
	apiKeyFlag := apiKeyFlagValue(fs)
	fs.IntVar(
		&cfg.keepMessages, "keep-messages", cfg.keepMessages,
		"recent message events kept raw",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.sessionID == "" {
		return cliConfig{}, fmt.Errorf("compact requires --session")
	}
	applyAPIKeyFlag(&cfg, *apiKeyFlag)

	return cfg, nil
}

// parseRunFlags converts default command flags into a run configuration.
func parseRunFlags(args []string, stderr io.Writer) (cliConfig, error) {
	var cfg cliConfig
	cfg.command = commandRun
	cfg.provider = envDefault("HARNESS_PROVIDER", providerEcho)
	cfg.model = envDefault("OPENAI_MODEL", "")
	cfg.baseURL = envDefault("OPENAI_BASE_URL", openai.DefaultBaseURL)
	cfg.apiKey = os.Getenv("OPENAI_API_KEY")
	cfg.openaiAPI = envDefault(
		"HARNESS_OPENAI_API", openai.APIChatCompletions,
	)
	cfg.reasoningEffort = envDefault("OPENAI_REASONING_EFFORT", "")
	cfg.reasoningSummary = envDefault("OPENAI_REASONING_SUMMARY", "")
	cfg.maxToolRounds = envIntDefault(
		"HARNESS_MAX_TOOL_ROUNDS", core.DefaultMaxToolRounds,
	)
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
	addOpenAIFlags(fs, &cfg)
	fs.StringVar(
		&cfg.baseURL, "base-url", cfg.baseURL,
		"OpenAI-compatible API base URL",
	)
	apiKeyFlag := apiKeyFlagValue(fs)
	fs.IntVar(
		&cfg.maxToolRounds, "max-tool-rounds", cfg.maxToolRounds,
		"maximum model/tool exchange rounds per user turn",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.maxToolRounds < 1 {
		return cliConfig{}, fmt.Errorf("max-tool-rounds must be " +
			"positive")
	}
	if cfg.prompt == "" {
		return cliConfig{}, fmt.Errorf("provide a prompt with -p")
	}
	applyAPIKeyFlag(&cfg, *apiKeyFlag)

	return cfg, nil
}

// apiKeyFlagValue registers the API key flag without exposing env defaults.
func apiKeyFlagValue(fs *flag.FlagSet) *string {
	value := ""
	fs.StringVar(&value, "api-key", "", "OpenAI API key")

	return &value
}

// addOpenAIFlags registers provider-specific OpenAI controls.
func addOpenAIFlags(fs *flag.FlagSet, cfg *cliConfig) {
	fs.StringVar(
		&cfg.openaiAPI, "openai-api", cfg.openaiAPI,
		"OpenAI API shape: chat or responses",
	)
	fs.StringVar(
		&cfg.reasoningEffort, "reasoning-effort", cfg.reasoningEffort,
		"OpenAI reasoning effort: none, minimal, low, medium, "+
			"high, or xhigh",
	)
	fs.StringVar(
		&cfg.reasoningSummary, "reasoning-summary",
		cfg.reasoningSummary,
		"OpenAI reasoning summary: auto, concise, or detailed",
	)
}

// applyAPIKeyFlag lets an explicit flag value override environment auth.
func applyAPIKeyFlag(cfg *cliConfig, apiKey string) {
	if apiKey != "" {
		cfg.apiKey = apiKey
	}
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
			BaseURL:          cfg.baseURL,
			APIKey:           cfg.apiKey,
			Model:            cfg.model,
			API:              cfg.openaiAPI,
			ReasoningEffort:  cfg.reasoningEffort,
			ReasoningSummary: cfg.reasoningSummary,
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

// envIntDefault returns a positive integer environment value or fallback.
func envIntDefault(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}

	return parsed
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
