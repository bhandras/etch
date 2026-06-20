package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	openaiauth "harness/internal/auth/openai"
	harnessconfig "harness/internal/config"
	"harness/internal/core"
	"harness/internal/hooks"
	"harness/internal/model"
	"harness/internal/plugins"
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

	// commandAuth manages local OpenAI OAuth credentials.
	commandAuth = "auth"

	// commandHelp prints top-level or command-specific CLI help.
	commandHelp = "help"

	// providerEcho selects the dependency-free deterministic model client.
	providerEcho = "echo"

	// harnessUserAgent identifies this CLI on provider HTTP requests.
	harnessUserAgent = "harness"
)

// cliConfig stores parsed command-line options for one invocation.
type cliConfig struct {
	command             string
	prompt              string
	sessionDir          string
	jsonOutput          bool
	sessionID           string
	provider            string
	model               string
	baseURL             string
	apiKey              string
	openaiAPI           string
	openaiAPIExplicit   bool
	reasoningEffort     string
	reasoningSummary    string
	baseURLExplicit     bool
	toolName            string
	toolPath            string
	toolCommand         string
	toolContent         string
	toolOldText         string
	toolNewText         string
	toolQuery           string
	toolRawArguments    string
	toolOffset          int
	toolLimit           int
	toolTimeout         int
	toolIgnoreCase      bool
	toolDryRun          bool
	autoCompact         bool
	autoCompactLimit    int
	keepMessages        int
	keepRecentTokens    int
	compactInstructions string
	maxToolRounds       int
	authAction          string
	authPath            string
	authIssuer          string
	authClientID        string
	authCodexBaseURL    string
	hooks               []harnessconfig.HookConfig
	plugins             []harnessconfig.PluginConfig
}

// main runs the command and exits with the returned status code.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses flags, executes one agent turn, and renders the result.
func run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		printTopLevelHelp(stdout)

		return 0
	}
	if isHelpArg(args[0]) {
		printTopLevelHelp(stdout)

		return 0
	}
	if args[0] == commandHelp {
		return runHelp(args[1:], stdout, stderr)
	}

	cfg, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
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

	case commandAuth:
		return runAuth(cfg, stdout, stderr)

	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n", cfg.command)

		return 2
	}
}

// runHelp prints top-level or command-specific help text.
func runHelp(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		printTopLevelHelp(stdout)

		return 0
	}
	if len(args) > 2 {
		fmt.Fprintln(stderr, "error: help accepts at most two words")

		return 2
	}

	err := printCommandHelp(args, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 2
	}

	return 0
}

// printTopLevelHelp writes the command overview for the harness binary.
func printTopLevelHelp(stdout io.Writer) {
	fmt.Fprintln(stdout, "Harness is a minimal Go coding-agent harness.")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Usage:")
	fmt.Fprintln(stdout, `  harness -p "prompt" [flags]`)
	fmt.Fprintln(stdout, "  harness chat [flags]")
	fmt.Fprintln(stdout, "  harness auth <login|status|logout> [flags]")
	fmt.Fprintln(stdout, "  harness tool <name> [flags] [args]")
	fmt.Fprintln(stdout, "  harness sessions [flags]")
	fmt.Fprintln(stdout, "  harness show [flags] <session-id-prefix>")
	fmt.Fprintln(stdout, "  harness compact --session <id> [flags]")
	fmt.Fprintln(stdout, "  harness help [command]")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Commands:")
	fmt.Fprintln(stdout, "  chat      start an interactive chat session")
	fmt.Fprintln(stdout, "  auth      manage OpenAI OAuth credentials")
	fmt.Fprintln(stdout, "  tool      run a builtin tool directly")
	fmt.Fprintln(stdout, "  sessions  list local JSONL sessions")
	fmt.Fprintln(stdout, "  show      render one local session transcript")
	fmt.Fprintln(stdout, "  compact   summarize an existing session")
	fmt.Fprintln(stdout, "  help      show help for a command")
	fmt.Fprintln(stdout)
	fmt.Fprintln(
		stdout, "Use \"harness help <command>\" for command flags.",
	)
}

// printCommandHelp writes generated or custom help for one command.
func printCommandHelp(args []string, stdout io.Writer) error {
	command := args[0]
	switch command {
	case commandRun:
		_, err := parseRunFlags([]string{"-h"}, stdout)

		return ignoreHelpError(err)

	case commandChat:
		_, err := parseChatFlags([]string{"-h"}, stdout)

		return ignoreHelpError(err)

	case commandCompact:
		_, err := parseCompactFlags([]string{"-h"}, stdout)

		return ignoreHelpError(err)

	case commandSessions:
		_, err := parseSessionsFlags([]string{"-h"}, stdout)

		return ignoreHelpError(err)

	case commandShow:
		_, err := parseShowFlags([]string{"-h"}, stdout)

		return ignoreHelpError(err)

	case commandAuth:
		if len(args) == 2 {
			_, err := parseAuthFlags(
				[]string{args[1], "-h"}, stdout,
			)

			return ignoreHelpError(err)
		}
		printAuthHelp(stdout)

		return nil

	case commandTool:
		if len(args) == 2 {
			_, err := parseToolFlags(
				[]string{args[1], "-h"}, stdout,
			)

			return ignoreHelpError(err)
		}
		printToolHelp(stdout)

		return nil

	default:
		return fmt.Errorf("unknown help command %q", command)
	}
}

// printAuthHelp writes a compact auth command overview.
func printAuthHelp(stdout io.Writer) {
	fmt.Fprintln(stdout, "Usage:")
	fmt.Fprintln(stdout, "  harness auth login [flags]")
	fmt.Fprintln(stdout, "  harness auth status [flags]")
	fmt.Fprintln(stdout, "  harness auth logout [flags]")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Use \"harness help auth login\" for auth flags.")
}

// printToolHelp writes a compact direct-tool command overview.
func printToolHelp(stdout io.Writer) {
	fmt.Fprintln(stdout, "Usage:")
	fmt.Fprintln(stdout, "  harness tool <name> [flags] [args]")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Tools:")
	for _, spec := range tool.DefaultRegistry().Specs() {
		fmt.Fprintf(stdout, "  %s\n", spec.Name)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Use \"harness help tool <name>\" for tool flags.")
}

// ignoreHelpError treats flag package help as a successful help rendering.
func ignoreHelpError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}

	return err
}

// isHelpArg reports whether arg requests top-level help.
func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help"
}

// runAuth executes OpenAI OAuth credential management commands.
func runAuth(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	path, err := authStorePath(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	switch cfg.authAction {
	case "login":
		creds, err := openaiauth.LoginDevice(
			context.Background(), authOptions(cfg),
			func(event openaiauth.LoginProgress) {
				if event.DeviceCode.UserCode != "" {
					fmt.Fprintf(
						stdout, "Open %s\nEnter "+
							"code %s\n%s\n",
						event.DeviceCode.VerificationURL,
						event.DeviceCode.UserCode,
						"Waiting for authorization...",
					)

					return
				}
				if event.Message != "" {
					fmt.Fprintln(stdout, event.Message)
				}
			},
		)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		if err := openaiauth.Save(path, creds); err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		fmt.Fprintf(
			stdout, "saved OpenAI OAuth credentials to %s\n", path,
		)

		return 0

	case "status":
		return runAuthStatus(path, stdout, stderr)

	case "logout":
		removed, err := openaiauth.Logout(path)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		if removed {
			fmt.Fprintf(
				stdout, "removed OpenAI OAuth credentials "+
					"from %s\n", path,
			)
		} else {
			fmt.Fprintln(
				stdout, "no OpenAI OAuth credentials found",
			)
		}

		return 0

	default:
		fmt.Fprintf(
			stderr, "error: unknown auth action %q\n",
			cfg.authAction,
		)

		return 2
	}
}

// runAuthStatus renders non-secret OpenAI authentication state.
func runAuthStatus(path string, stdout io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stdout, "OpenAI Auth")
	if openaiauth.AccessTokenFromEnv() != "" {
		fmt.Fprintln(stdout, "- env token: CODEX_ACCESS_TOKEN")
	} else {
		fmt.Fprintln(stdout, "- env token: not set")
	}

	creds, err := openaiauth.Load(path)
	if err != nil {
		if errors.Is(err, openaiauth.ErrNotLoggedIn) {
			fmt.Fprintln(stdout, "- stored login: not found")

			return 0
		}
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	email, accountID := openaiauth.ParseChatGPTClaims(creds.Tokens.IDToken)
	fmt.Fprintln(stdout, "- stored login: ChatGPT/Codex OAuth")
	fmt.Fprintf(stdout, "- auth file: %s\n", path)
	fmt.Fprintf(stdout, "- backend: %s\n", creds.CodexBaseURL)
	if email != "" {
		fmt.Fprintf(stdout, "- email: %s\n", email)
	}
	if accountID == "" {
		accountID = creds.Tokens.AccountID
	}
	if accountID != "" {
		fmt.Fprintf(stdout, "- account: %s\n", accountID)
	}

	return 0
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
	projectContext, err := promptctx.LoadProjectContext(cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	hookRunner, err := hooks.New(cfg.hooks, cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	registry, closePlugins, err := configuredToolRegistry(
		context.Background(), cfg, cwd,
	)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	defer closePlugins()

	result, err := core.RunTurn(context.Background(), core.TurnRequest{
		Prompt:        cfg.prompt,
		SessionDir:    cfg.sessionDir,
		CWD:           cwd,
		SystemText:    projectContext.SystemText,
		Model:         modelClient,
		Tools:         registry,
		MaxToolRounds: cfg.maxToolRounds,
		Hooks:         hookRunner,
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

// configuredToolRegistry returns builtins plus configured plugin tools.
func configuredToolRegistry(ctx context.Context, cfg cliConfig, cwd string) (
	*tool.Registry, func(), error) {

	registry := tool.DefaultRegistry()
	clients, err := plugins.StartConfigured(ctx, cfg.plugins, cwd, registry)
	if err != nil {
		return nil, nil, err
	}
	closePlugins := func() {
		for _, client := range clients {
			_ = client.Close()
		}
	}

	return registry, closePlugins, nil
}

// runTool executes one registered tool directly for local smoke testing.
func runTool(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "error: get working directory:", err)

		return 1
	}
	registry, closePlugins, err := configuredToolRegistry(
		context.Background(), cfg, cwd,
	)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	defer closePlugins()

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
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "error: get working directory:", err)

		return 1
	}
	hookRunner, err := hooks.New(cfg.hooks, cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	result, err := core.CompactSession(context.Background(),
		core.CompactRequest{
			SessionPath:      entry.Path,
			Model:            modelClient,
			KeepMessages:     cfg.keepMessages,
			KeepRecentTokens: cfg.keepRecentTokens,
			ModelName:        cfg.model,
			Instructions:     cfg.compactInstructions,
			Hooks:            hookRunner,
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
	projectContext, err := promptctx.LoadProjectContext(cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	hookRunner, err := hooks.New(cfg.hooks, cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	registry, closePlugins, err := configuredToolRegistry(
		context.Background(), cfg, cwd,
	)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	defer closePlugins()

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
				cfg, line, sessionPath, modelClient, registry,
				stdout, stderr, hookRunner,
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
				SystemText:    projectContext.SystemText,
				Model:         modelClient,
				ModelName:     cfg.model,
				Tools:         registry,
				MaxToolRounds: cfg.maxToolRounds,
				AutoCompactThresholdTokens: autoCompactThreshold(
					cfg,
				),
				AutoCompactKeepMessages:     cfg.keepMessages,
				AutoCompactKeepRecentTokens: cfg.keepRecentTokens,
				Observer:                    observer,
				Hooks:                       hookRunner,
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

	// toolCalls counts local tool calls executed during this turn.
	toolCalls int

	// usage accumulates provider token counters reported during this turn.
	usage model.Usage
}

// EventAppended renders model-visible assistant and tool events.
func (o *chatObserver) EventAppended(event session.Event) {
	if event.Type == session.EventModelUsage {
		usage, err := decodeUsage(event)
		if err != nil {
			fmt.Fprintf(
				o.renderer.stdout, "render error: %v\n", err,
			)

			return
		}
		o.usage = o.usage.Add(model.Usage{
			InputTokens:           usage.InputTokens,
			CachedInputTokens:     usage.CachedInputTokens,
			OutputTokens:          usage.OutputTokens,
			ReasoningOutputTokens: usage.ReasoningOutputTokens,
			TotalTokens:           usage.TotalTokens,
		})

		return
	}
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
	o.toolCalls++
	o.renderer.renderToolCall(call)
}

// ReasoningCompleted renders one model-provided thinking summary block.
func (o *chatObserver) ReasoningCompleted(text string) {
	o.renderer.renderReasoning(text)
}

// AutoCompacted renders one automatic context maintenance notice.
func (o *chatObserver) AutoCompacted(result core.AutoCompactResult) {
	o.renderer.renderAutoCompact(result)
}

// Finish renders terminal-only end-of-turn decoration.
func (o *chatObserver) Finish(elapsed time.Duration) {
	o.renderer.finish(elapsed, liveTurnStats{
		ToolCalls: o.toolCalls,
		Usage:     o.usage,
	})
}

// handleChatCommand executes one slash command and returns whether to continue.
func handleChatCommand(cfg cliConfig, line string, sessionPath string,
	modelClient model.Client, registry *tool.Registry, stdout io.Writer,
	stderr io.Writer, hookRunner *hooks.Runner) (bool, string) {

	if line == "/compact" || strings.HasPrefix(line, "/compact ") {
		if sessionPath == "" {
			fmt.Fprintln(stdout, "no active session")

			return true, sessionPath
		}
		result, err := core.CompactSession(context.Background(),
			core.CompactRequest{
				SessionPath:      sessionPath,
				Model:            modelClient,
				KeepMessages:     cfg.keepMessages,
				KeepRecentTokens: cfg.keepRecentTokens,
				ModelName:        cfg.model,
				Instructions: strings.TrimSpace(
					strings.TrimPrefix(line, "/compact"),
				),
				Hooks: hookRunner,
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
	}

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
		if err := printContextStats(
			sessionPath, cfg, stdout,
		); err != nil {

			fmt.Fprintln(stderr, "error:", err)
		}

		return true, sessionPath

	case "/status":
		if sessionPath == "" {
			fmt.Fprintln(stdout, "no active session")

			return true, sessionPath
		}
		if err := printSessionStatus(sessionPath, stdout); err != nil {
			fmt.Fprintln(stderr, "error:", err)
		}

		return true, sessionPath

	case "/tools":
		for _, spec := range registry.Specs() {
			fmt.Fprintln(stdout, spec.Name)
		}

		return true, sessionPath

	case "/help":
		fmt.Fprintln(
			stdout, "/exit /quit /new /show /sessions /context "+
				"/status /compact /tools /help",
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

// printContextStats renders prompt context projection statistics for a session.
func printContextStats(path string, cfg cliConfig, stdout io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	projectContext, err := promptctx.LoadProjectContext(cwd)
	if err != nil {
		return err
	}
	events, err := session.ReadAll(path)
	if err != nil {
		return err
	}
	stats, err := promptctx.BuildStats(events, projectContext.SystemText)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, promptctx.FormatStats(stats))
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, formatAutoCompactConfig(cfg))
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, promptctx.FormatProjectContext(projectContext))

	return nil
}

// formatAutoCompactConfig renders chat context maintenance settings.
func formatAutoCompactConfig(cfg cliConfig) string {
	enabled := "false"
	if cfg.autoCompact {
		enabled = "true"
	}
	threshold := cfg.autoCompactLimit
	if threshold <= 0 {
		threshold = core.DefaultAutoCompactThresholdTokens
	}
	keepMessages := cfg.keepMessages
	if keepMessages <= 0 {
		keepMessages = core.DefaultCompactKeepMessages
	}
	keepRecentTokens := cfg.keepRecentTokens
	if keepRecentTokens <= 0 {
		keepRecentTokens = core.DefaultCompactKeepRecentTokens
	}

	return fmt.Sprintf("Auto Compact\n"+
		"- enabled: %s\n"+
		"- threshold: ~%d tokens\n"+
		"- keep recent: ~%d tokens\n"+
		"- fallback keep messages: %d",
		enabled, threshold, keepRecentTokens, keepMessages)
}

// printSessionStatus renders durable activity statistics for a session.
func printSessionStatus(path string, stdout io.Writer) error {
	events, err := session.ReadAll(path)
	if err != nil {
		return err
	}
	status, err := session.BuildStatus(events, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, session.FormatStatus(status))

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

		case commandAuth:
			return parseAuthFlags(args[1:], stderr)
		}
		if !strings.HasPrefix(args[0], "-") {
			return cliConfig{}, fmt.Errorf("unknown command %q; "+
				"use \"harness help\" to list commands",
				args[0])
		}
	}

	return parseRunFlags(args, stderr)
}

// parseAuthFlags converts auth subcommands into credential management config.
func parseAuthFlags(args []string, stderr io.Writer) (cliConfig, error) {
	if len(args) == 0 {
		return cliConfig{}, fmt.Errorf("auth requires login, status, " +
			"or logout")
	}
	cfg := cliConfig{
		command:           commandAuth,
		authAction:        args[0],
		authIssuer:        openaiauth.DefaultIssuer,
		authClientID:      openaiauth.DefaultClientID,
		authCodexBaseURL:  openaiauth.DefaultCodexBaseURL,
		baseURL:           openai.DefaultBaseURL,
		openaiAPI:         openai.APIResponses,
		openaiAPIExplicit: true,
	}
	fs := flag.NewFlagSet(
		commandAuth+" "+cfg.authAction, flag.ContinueOnError,
	)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.authPath, "auth-file", "", "OpenAI OAuth credential file",
	)
	fs.StringVar(
		&cfg.authIssuer, "issuer", cfg.authIssuer,
		"OpenAI OAuth issuer URL",
	)
	fs.StringVar(
		&cfg.authClientID, "client-id", cfg.authClientID,
		"OpenAI OAuth client id",
	)
	fs.StringVar(
		&cfg.authCodexBaseURL, "codex-base-url", cfg.authCodexBaseURL,
		"OpenAI Codex backend URL for OAuth tokens",
	)
	if err := fs.Parse(args[1:]); err != nil {
		return cliConfig{}, err
	}
	if fs.NArg() != 0 {
		return cliConfig{}, fmt.Errorf("auth %s accepts no positional "+
			"arguments", cfg.authAction)
	}
	switch cfg.authAction {
	case "login", "status", "logout":
		return cfg, nil

	default:
		return cliConfig{}, fmt.Errorf("auth requires login, status, " +
			"or logout")
	}
}

// authStorePath returns the active OpenAI OAuth credential file path.
func authStorePath(cfg cliConfig) (string, error) {
	if cfg.authPath != "" {
		return filepath.Abs(cfg.authPath)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	return openaiauth.DefaultStorePath(cwd)
}

// authOptions converts CLI auth flags into provider-specific OAuth options.
func authOptions(cfg cliConfig) openaiauth.Options {
	return openaiauth.Options{
		Issuer:       cfg.authIssuer,
		ClientID:     cfg.authClientID,
		CodexBaseURL: cfg.authCodexBaseURL,
	}
}

// parseToolFlags converts tool subcommand arguments into configuration.
func parseToolFlags(args []string, stderr io.Writer) (cliConfig, error) {
	if len(args) == 0 {
		return cliConfig{}, fmt.Errorf("provide a tool name")
	}
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	cfg := cliConfig{
		command:  commandTool,
		toolName: args[0],
		plugins:  defaults.Plugins,
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
	fs.BoolVar(
		&cfg.toolDryRun, "dry-run", false,
		"preview edit changes without modifying files",
	)
	fs.StringVar(
		&cfg.toolRawArguments, "args", "",
		"raw JSON object arguments for plugin tools",
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
		if cfg.toolRawArguments != "" && fs.NArg() != 0 {
			return cliConfig{}, fmt.Errorf("plugin tool %s "+
				"accepts --args or one positional JSON "+
				"argument, not both", cfg.toolName)
		}
		switch fs.NArg() {
		case 0:
			if cfg.toolRawArguments == "" {
				cfg.toolRawArguments = "{}"
			}

		case 1:
			cfg.toolRawArguments = fs.Arg(0)

		default:
			return cliConfig{}, fmt.Errorf("plugin tool %s "+
				"accepts at most one positional JSON argument",
				cfg.toolName)
		}
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
			DryRun bool `json:"dryRun,omitempty"`
		}{
			Path: cfg.toolPath,
			Edits: []struct {
				OldText string `json:"oldText"`
				NewText string `json:"newText"`
			}{{
				OldText: cfg.toolOldText,
				NewText: cfg.toolNewText,
			}},
			DryRun: cfg.toolDryRun,
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
		raw := strings.TrimSpace(cfg.toolRawArguments)
		if raw == "" {
			raw = "{}"
		}
		if err := validateRawJSONObject(raw); err != nil {
			return "", fmt.Errorf("plugin tool %s arguments: %w",
				cfg.toolName, err)
		}

		return raw, nil
	}
}

// validateRawJSONObject ensures direct plugin tool arguments are an object.
func validateRawJSONObject(raw string) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &value); err != nil ||
		value == nil {
		return fmt.Errorf("must be a JSON object")
	}

	return nil
}

// parseChatFlags converts chat subcommand flags into configuration.
func parseChatFlags(args []string, stderr io.Writer) (cliConfig, error) {
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	baseURLExplicit := defaults.OpenAI.BaseURL != ""
	openaiAPIExplicit := defaults.OpenAI.API != ""
	cfg := cliConfig{
		command:           commandChat,
		sessionDir:        configSessionDir(defaults),
		provider:          configProvider(defaults),
		model:             defaults.Provider.Model,
		baseURL:           configOpenAIBaseURL(defaults),
		apiKey:            apiKeyFromEnv(),
		openaiAPI:         configOpenAIAPI(defaults),
		openaiAPIExplicit: openaiAPIExplicit,
		reasoningEffort:   defaults.OpenAI.ReasoningEffort,
		reasoningSummary:  defaults.OpenAI.ReasoningSummary,
		maxToolRounds:     configMaxToolRounds(defaults),
		autoCompact:       defaults.Context.AutoCompact,
		autoCompactLimit:  configAutoCompactThreshold(defaults),
		keepMessages:      configKeepMessages(defaults),
		keepRecentTokens:  configKeepRecentTokens(defaults),
		baseURLExplicit:   baseURLExplicit,
		hooks:             defaults.Hooks,
		plugins:           defaults.Plugins,
	}
	fs := flag.NewFlagSet(commandChat, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", cfg.sessionDir,
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
		&cfg.keepMessages, "keep-messages", cfg.keepMessages,
		"fallback message count when token retention is disabled",
	)
	fs.IntVar(
		&cfg.keepRecentTokens, "keep-recent-tokens",
		cfg.keepRecentTokens,
		"approximate recent context tokens kept raw by compaction",
	)
	fs.BoolVar(
		&cfg.autoCompact, "auto-compact", cfg.autoCompact,
		"automatically compact large chat context before model calls",
	)
	fs.IntVar(
		&cfg.autoCompactLimit, "auto-compact-threshold-tokens",
		cfg.autoCompactLimit,
		"approximate token threshold for automatic compaction",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.maxToolRounds < 1 {
		return cliConfig{}, fmt.Errorf("max-tool-rounds must be " +
			"positive")
	}
	if cfg.autoCompact && cfg.autoCompactLimit < 1 {
		return cliConfig{}, fmt.Errorf(
			"auto-compact-threshold-tokens must be positive")
	}
	if cfg.keepRecentTokens < 1 {
		return cliConfig{}, fmt.Errorf("keep-recent-tokens must be " +
			"positive")
	}
	cfg.baseURLExplicit = cfg.baseURLExplicit || flagWasSet(fs, "base-url")
	cfg.openaiAPIExplicit = cfg.openaiAPIExplicit ||
		flagWasSet(fs, "openai-api")
	applyAPIKeyFlag(&cfg, *apiKeyFlag)

	return cfg, nil
}

// parseCompactFlags converts compact subcommand flags into configuration.
func parseCompactFlags(args []string, stderr io.Writer) (cliConfig, error) {
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	baseURLExplicit := defaults.OpenAI.BaseURL != ""
	openaiAPIExplicit := defaults.OpenAI.API != ""
	cfg := cliConfig{
		command:           commandCompact,
		sessionDir:        configSessionDir(defaults),
		provider:          configProvider(defaults),
		model:             defaults.Provider.Model,
		baseURL:           configOpenAIBaseURL(defaults),
		apiKey:            apiKeyFromEnv(),
		openaiAPI:         configOpenAIAPI(defaults),
		openaiAPIExplicit: openaiAPIExplicit,
		reasoningEffort:   defaults.OpenAI.ReasoningEffort,
		reasoningSummary:  defaults.OpenAI.ReasoningSummary,
		keepMessages:      configKeepMessages(defaults),
		keepRecentTokens:  configKeepRecentTokens(defaults),
		baseURLExplicit:   baseURLExplicit,
		hooks:             defaults.Hooks,
		plugins:           defaults.Plugins,
	}
	fs := flag.NewFlagSet(commandCompact, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", cfg.sessionDir,
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
		"fallback message count when token retention is disabled",
	)
	fs.IntVar(
		&cfg.keepRecentTokens, "keep-recent-tokens",
		cfg.keepRecentTokens,
		"approximate recent context tokens kept raw by compaction",
	)
	fs.StringVar(
		&cfg.compactInstructions, "instructions", "",
		"optional focus instructions for the compaction summary",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.sessionID == "" {
		return cliConfig{}, fmt.Errorf("compact requires --session")
	}
	if cfg.keepRecentTokens < 1 {
		return cliConfig{}, fmt.Errorf("keep-recent-tokens must be " +
			"positive")
	}
	cfg.baseURLExplicit = cfg.baseURLExplicit || flagWasSet(fs, "base-url")
	cfg.openaiAPIExplicit = cfg.openaiAPIExplicit ||
		flagWasSet(fs, "openai-api")
	applyAPIKeyFlag(&cfg, *apiKeyFlag)

	return cfg, nil
}

// parseRunFlags converts default command flags into a run configuration.
func parseRunFlags(args []string, stderr io.Writer) (cliConfig, error) {
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	baseURLExplicit := defaults.OpenAI.BaseURL != ""
	openaiAPIExplicit := defaults.OpenAI.API != ""
	cfg := cliConfig{
		command:           commandRun,
		sessionDir:        configSessionDir(defaults),
		provider:          configProvider(defaults),
		model:             defaults.Provider.Model,
		baseURL:           configOpenAIBaseURL(defaults),
		apiKey:            apiKeyFromEnv(),
		openaiAPI:         configOpenAIAPI(defaults),
		openaiAPIExplicit: openaiAPIExplicit,
		reasoningEffort:   defaults.OpenAI.ReasoningEffort,
		reasoningSummary:  defaults.OpenAI.ReasoningSummary,
		maxToolRounds:     configMaxToolRounds(defaults),
		baseURLExplicit:   baseURLExplicit,
		hooks:             defaults.Hooks,
		plugins:           defaults.Plugins,
	}
	fs := flag.NewFlagSet("harness", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.prompt, "p", "", "prompt to run non-interactively")
	fs.StringVar(
		&cfg.sessionDir, "session-dir", cfg.sessionDir,
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
	cfg.baseURLExplicit = cfg.baseURLExplicit || flagWasSet(fs, "base-url")
	cfg.openaiAPIExplicit = cfg.openaiAPIExplicit ||
		flagWasSet(fs, "openai-api")
	applyAPIKeyFlag(&cfg, *apiKeyFlag)

	return cfg, nil
}

// apiKeyFlagValue registers the API key flag without exposing env defaults.
func apiKeyFlagValue(fs *flag.FlagSet) *string {
	value := ""
	fs.StringVar(&value, "api-key", "", "OpenAI-compatible API key")

	return &value
}

// apiKeyFromEnv returns the configured OpenAI-compatible API key fallback.
func apiKeyFromEnv() string {
	if value := os.Getenv("OPENAI_API_KEY"); value != "" {
		return value
	}

	return os.Getenv("OPENROUTER_API_KEY")
}

// addOpenAIFlags registers provider-specific OpenAI controls.
func addOpenAIFlags(fs *flag.FlagSet, cfg *cliConfig) {
	fs.StringVar(
		&cfg.authPath, "auth-file", "", "OpenAI OAuth credential file",
	)
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

// flagWasSet reports whether fs parsed a flag explicitly from the CLI.
func flagWasSet(fs *flag.FlagSet, name string) bool {
	wasSet := false
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			wasSet = true
		}
	})

	return wasSet
}

// modelClient creates the provider selected by run command configuration.
func modelClient(cfg cliConfig) (model.Client, error) {
	switch cfg.provider {
	case "", providerEcho:
		return model.EchoClient{}, nil

	case openai.ProviderName:
		if cfg.model == "" {
			return nil, fmt.Errorf("openai provider requires " +
				"--model or provider.model config")
		}
		var oauthErr error
		token := ""
		baseURL := openaiauth.DefaultCodexBaseURL
		creds, err := loadOpenAIOAuthCredentials(cfg)
		if err == nil {
			token = creds.Tokens.AccessToken
			baseURL = creds.CodexBaseURL
		} else if errors.Is(err, openaiauth.ErrNotLoggedIn) {
			token = openaiauth.AccessTokenFromEnv()
		} else {
			oauthErr = err
		}

		if token == "" && cfg.apiKey != "" && oauthErr == nil {
			return &openai.Client{
				BaseURL:          cfg.baseURL,
				APIKey:           cfg.apiKey,
				Model:            cfg.model,
				API:              cfg.openaiAPI,
				ReasoningEffort:  cfg.reasoningEffort,
				ReasoningSummary: cfg.reasoningSummary,
				UserAgent:        harnessUserAgent,
			}, nil
		}
		if oauthErr != nil {
			return nil, oauthErr
		}
		if token == "" {
			return nil, fmt.Errorf("openai provider requires " +
				"harness auth login, CODEX_ACCESS_TOKEN, " +
				"--api-key, OPENAI_API_KEY, or " +
				"OPENROUTER_API_KEY")
		}

		apiMode := cfg.openaiAPI
		if !cfg.openaiAPIExplicit {
			apiMode = openai.APIResponses
		}
		if cfg.baseURLExplicit {
			baseURL = cfg.baseURL
		}

		return &openai.Client{
			BaseURL:          baseURL,
			APIKey:           token,
			Model:            cfg.model,
			API:              apiMode,
			ReasoningEffort:  cfg.reasoningEffort,
			ReasoningSummary: cfg.reasoningSummary,
			UserAgent:        harnessUserAgent,
		}, nil

	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.provider)
	}
}

// loadOpenAIOAuthCredentials loads and refreshes stored OAuth credentials.
func loadOpenAIOAuthCredentials(cfg cliConfig) (openaiauth.Credentials, error) {
	path, err := authStorePath(cfg)
	if err != nil {
		return openaiauth.Credentials{}, err
	}
	creds, err := openaiauth.EnsureAccessToken(
		context.Background(), path, authOptions(cfg),
	)
	if err != nil {
		return openaiauth.Credentials{}, err
	}

	return creds, nil
}

// loadConfigDefaults loads project TOML defaults for commands that honor them.
func loadConfigDefaults() (harnessconfig.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return harnessconfig.Config{}, fmt.Errorf("get working "+
			"directory: %w", err)
	}

	return harnessconfig.Load(cwd)
}

// configSessionDir returns the configured session directory or the CLI default.
func configSessionDir(cfg harnessconfig.Config) string {
	if cfg.Session.Dir != "" {
		return cfg.Session.Dir
	}

	return defaultSessionDir
}

// configProvider returns the configured provider or the offline default.
func configProvider(cfg harnessconfig.Config) string {
	if cfg.Provider.Name != "" {
		return cfg.Provider.Name
	}

	return providerEcho
}

// configOpenAIBaseURL returns the configured OpenAI endpoint or the default.
func configOpenAIBaseURL(cfg harnessconfig.Config) string {
	if cfg.OpenAI.BaseURL != "" {
		return cfg.OpenAI.BaseURL
	}

	return openai.DefaultBaseURL
}

// configOpenAIAPI returns the configured OpenAI API shape or the default.
func configOpenAIAPI(cfg harnessconfig.Config) string {
	if cfg.OpenAI.API != "" {
		return cfg.OpenAI.API
	}

	return openai.APIChatCompletions
}

// configMaxToolRounds returns the configured tool-loop limit or the default.
func configMaxToolRounds(cfg harnessconfig.Config) int {
	if cfg.Session.MaxToolRounds > 0 {
		return cfg.Session.MaxToolRounds
	}

	return core.DefaultMaxToolRounds
}

// configKeepMessages returns the configured compaction retention or default.
func configKeepMessages(cfg harnessconfig.Config) int {
	if cfg.Session.KeepMessages > 0 {
		return cfg.Session.KeepMessages
	}

	return core.DefaultCompactKeepMessages
}

// configKeepRecentTokens returns the configured raw retention token budget.
func configKeepRecentTokens(cfg harnessconfig.Config) int {
	if cfg.Context.KeepRecentTokens > 0 {
		return cfg.Context.KeepRecentTokens
	}

	return core.DefaultCompactKeepRecentTokens
}

// configAutoCompactThreshold returns the configured auto-compaction threshold.
func configAutoCompactThreshold(cfg harnessconfig.Config) int {
	if cfg.Context.AutoCompactThresholdTokens > 0 {
		return cfg.Context.AutoCompactThresholdTokens
	}

	return core.DefaultAutoCompactThresholdTokens
}

// autoCompactThreshold returns the active auto-compaction threshold or zero.
func autoCompactThreshold(cfg cliConfig) int {
	if !cfg.autoCompact {
		return 0
	}

	return cfg.autoCompactLimit
}

// parseSessionsFlags converts sessions subcommand flags into configuration.
func parseSessionsFlags(args []string, stderr io.Writer) (cliConfig, error) {
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	cfg := cliConfig{
		command:    commandSessions,
		sessionDir: configSessionDir(defaults),
	}
	fs := flag.NewFlagSet(commandSessions, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", cfg.sessionDir,
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
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	cfg := cliConfig{
		command:    commandShow,
		sessionDir: configSessionDir(defaults),
	}
	fs := flag.NewFlagSet(commandShow, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", cfg.sessionDir,
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

// decodeUsage decodes a durable model usage event.
func decodeUsage(event session.Event) (session.UsageData, error) {
	var data session.UsageData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return session.UsageData{}, fmt.Errorf("decode usage: %w", err)
	}

	return data, nil
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
