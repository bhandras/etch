package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

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
		if padded {
			fmt.Fprintln(stdout)
		}
		keepGoing, nextPath = handleChatCommand(
			cfg, line, sessionPath, modelClient, registry, stdout,
			stderr, hookRunner,
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
	showTerminalCursor(stdout)
	renderChatPrompt(stdout)
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
