package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"harness/internal/core"
	"harness/internal/hooks"
	"harness/internal/model"
	promptctx "harness/internal/prompt"
	"harness/internal/session"
	"harness/internal/tool"
)

// runChatCommandWithOutput clears the live prompt around slash-command output.
func runChatCommandWithOutput(composer *terminalChatInput, cfg cliConfig,
	line string, sessionPath string, modelClient model.Client,
	registry *tool.Registry, stdout io.Writer, stderr io.Writer,
	hookRunner *hooks.Runner) (bool, string) {

	keepGoing := true
	nextPath := sessionPath
	write := func() {
		padded := chatCommandOutputPadded(line)
		keepGoing, nextPath = handleChatCommand(
			cfg, line, sessionPath, modelClient, registry, stdout,
			stderr, hookRunner, composer,
		)
		if padded {
			fmt.Fprintln(stdout)
		}
	}
	if composer != nil {
		composer.WithOutput(write)
	} else {
		write()
	}

	return keepGoing, nextPath
}

// chatCommandOutputPadded reports whether a slash command writes visible text.
func chatCommandOutputPadded(line string) bool {
	return line != "/exit" && line != "/quit"
}

// handleChatCommand executes one slash command and returns whether to continue.
func handleChatCommand(cfg cliConfig, line string, sessionPath string,
	modelClient model.Client, registry *tool.Registry, stdout io.Writer,
	stderr io.Writer, hookRunner *hooks.Runner,
	composer *terminalChatInput) (bool, string) {

	if line == "/compact" || strings.HasPrefix(line, "/compact ") {
		if sessionPath == "" {
			fmt.Fprintln(stdout, "no active session")

			return true, sessionPath
		}
		result, err := compactWithFeedback(
			cfg, line, sessionPath, modelClient, stdout, hookRunner,
			composer,
		)
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
	if line == "/tool" || strings.HasPrefix(line, "/tool ") ||
		strings.HasPrefix(line, "/tools ") {

		name := strings.TrimSpace(strings.TrimPrefix(line, "/tool"))
		if strings.HasPrefix(line, "/tools ") {
			name = strings.TrimSpace(
				strings.TrimPrefix(line, "/tools"),
			)
		}
		if name == "" {
			for _, spec := range registry.Specs() {
				fmt.Fprintln(stdout, spec.Name)
			}

			return true, sessionPath
		}
		if err := printToolSpec(registry, name, stdout); err != nil {
			fmt.Fprintln(stderr, "error:", err)
		}

		return true, sessionPath
	}
	if line == "/context dump" ||
		strings.HasPrefix(line, "/context dump ") {

		if sessionPath == "" {
			fmt.Fprintln(stdout, "no active session")

			return true, sessionPath
		}
		path := strings.TrimSpace(
			strings.TrimPrefix(line, "/context "+
				"dump"),
		)
		written, err := dumpContext(
			sessionPath, cfg, registry, path,
		)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
		} else {
			fmt.Fprintf(stdout, "context dump: %s\n", written)
		}

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
			sessionPath, cfg, registry, stdout,
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
		fmt.Fprintln(stdout, chatHelpText())

		return true, sessionPath

	default:
		fmt.Fprintf(stdout, "unknown command %s\n", line)

		return true, sessionPath
	}
}

// compactWithFeedback runs manual compaction with live terminal status.
func compactWithFeedback(cfg cliConfig, line string, sessionPath string,
	modelClient model.Client, stdout io.Writer, hookRunner *hooks.Runner,
	composer *terminalChatInput) (*core.CompactResult, error) {

	renderer := newLiveChatRenderer(stdout, !shouldStyle(stdout))
	renderer.composer = composer
	renderer.startStatus("Compacting")
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
	renderer.stopStatus()
	if err != nil {
		return nil, err
	}

	return result, nil
}

// chatHelpText returns readable help for interactive slash commands.
func chatHelpText() string {
	return strings.TrimSpace(`
Chat Commands
- /help
  Show this help.

- /status
  Show session age, event counts, model usage, tool calls, and compactions.

- /context
  Show approximate context statistics and pinned context layers.

- /context dump [path]
  Write the logical model context to a plain-text file. Without path, harness
  writes context-YYYYMMDD-HHMMSS.txt in the current directory.

- /compact [instructions]
  Summarize older session history into a compact context checkpoint.

- /tools
  List tools available to the model.

- /tool <name>
  Show one tool description and JSON parameter schema.

- /show
  Render the current session transcript.

- /sessions
  List recent sessions.

- /new
  Start a fresh session.

- /exit or /quit
  Leave chat.
`)
}

// printToolSpec renders the model-facing schema for one registered tool.
func printToolSpec(registry *tool.Registry, name string,
	stdout io.Writer) error {

	specs := registry.Specs()
	for _, spec := range specs {
		if spec.Name != name {
			continue
		}
		fmt.Fprintf(stdout, "Tool: %s\n\n", spec.Name)
		fmt.Fprintf(stdout, "Description:\n%s\n", spec.Description)
		if len(spec.Parameters) > 0 {
			formatted, err := formatJSON(spec.Parameters)
			if err != nil {
				return fmt.Errorf("format %s schema: %w", name,
					err)
			}
			fmt.Fprintf(
				stdout, "\nParameters:\n```json\n%s\n```\n",
				formatted,
			)
		}

		return nil
	}

	return fmt.Errorf("unknown tool %q", name)
}

// formatJSON returns stable indented JSON for command output.
func formatJSON(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}

	return string(encoded), nil
}

// printChatPrompt writes the fixed line-mode prompt.
func printChatPrompt(stdout io.Writer) {
	showTerminalCursor(stdout)
	renderChatPrompt(stdout)
}

// printContextStats renders prompt context projection statistics for a session.
func printContextStats(path string, cfg cliConfig, registry *tool.Registry,
	stdout io.Writer) error {

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	projectContext, err := promptctx.LoadProjectContextWithOptions(
		cwd, projectContextOptions(cfg),
	)
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
	var specs []model.ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}
	fmt.Fprintln(
		stdout, promptctx.FormatProjectContext(projectContext, specs),
	)

	return nil
}

// dumpContext writes a plain-text logical context dump and returns its path.
func dumpContext(sessionPath string, cfg cliConfig, registry *tool.Registry,
	requestedPath string) (string, error) {

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	projectContext, err := promptctx.LoadProjectContextWithOptions(
		cwd, projectContextOptions(cfg),
	)
	if err != nil {
		return "", err
	}
	events, err := session.ReadAll(sessionPath)
	if err != nil {
		return "", err
	}
	var specs []model.ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}
	now := time.Now()
	text, err := promptctx.DumpText(promptctx.DumpRequest{
		CreatedAt:   now,
		CWD:         cwd,
		SessionPath: sessionPath,
		ModelName:   cfg.model,
		Events:      events,
		Project:     projectContext,
		Tools:       specs,
	})
	if err != nil {
		return "", err
	}
	path, err := contextDumpPath(requestedPath, now)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create context dump directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("write context dump: %w", err)
	}

	return path, nil
}

// contextDumpPath resolves a requested dump path or creates a default one.
func contextDumpPath(requested string, now time.Time) (string, error) {
	path := strings.TrimSpace(requested)
	if path == "" {
		path = "context-" + now.Format("20060102-150405") + ".txt"
	} else {
		path = expandHomePath(path)
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(
			path, "context-"+now.Format("20060102-150405")+".txt",
		)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat context dump path: %w", err)
	}
	if filepath.IsAbs(path) {
		return path, nil
	}

	return filepath.Abs(path)
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
