package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"harness/internal/hooks"
	"harness/internal/model"
	promptctx "harness/internal/prompt"
	"harness/internal/session"
)

// runChat starts a line-oriented interactive chat session.
func runChat(cfg cliConfig, stdin io.Reader, stdout io.Writer,
	stderr io.Writer) int {

	defer showTerminalCursor(stdout)

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
	initialUsage := model.Usage{}
	if cfg.sessionID != "" {
		entry, err := session.Resolve(cfg.sessionDir, cfg.sessionID)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		sessionPath = entry.Path
		initialUsage, err = chatSessionUsage(sessionPath)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		fmt.Fprintf(
			stdout, "continuing session %s\n", shortID(entry.ID),
		)
	}

	input := newChatInput(stdin, stdout)
	defer func() {
		_ = input.Close()
	}()
	composer := terminalComposer(input)
	chrome := newChatChrome(cfg, cwd, initialUsage)
	if composer != nil {
		composer.SetFooter(chrome.Footer())
		if sessionPath != "" {
			history, err := chatPromptHistory(sessionPath)
			if err != nil {
				fmt.Fprintln(stderr, "error:", err)

				return 1
			}
			composer.SetHistory(history)
		}
	}
	results := readChatLines(input)
	pendingResults := []chatLineResult{}
	inputDone := false
	for {
		result, ok := nextChatLine(results, &pendingResults, inputDone)
		if !ok {
			break
		}
		if result.Err != nil {
			if errors.Is(result.Err, errChatInputCanceled) {
				continue
			}
			if errors.Is(result.Err, errChatInputInterrupted) {
				return 0
			}
			fmt.Fprintln(
				stderr, "error: read chat input:", result.Err,
			)

			return 1
		}
		if !result.OK {
			break
		}
		line := result.Line
		commandLine := strings.TrimSpace(line)
		if commandLine == "" {
			continue
		}
		if strings.HasPrefix(commandLine, "/") {
			keepGoing, nextPath := runChatCommandWithOutput(
				composer, cfg, commandLine, sessionPath,
				modelClient, registry, stdout, stderr,
				hookRunner,
			)
			sessionPath = nextPath
			if composer != nil && commandLine == "/new" {
				composer.SetHistory(nil)
			}
			if !keepGoing {
				return 0
			}

			continue
		}

		turn, err := runChatTurnWithSteering(
			cfg, line, sessionPath, cwd, projectContext.SystemText,
			modelClient, registry, hookRunner, chrome, composer,
			results, &inputDone, &pendingResults, stdout,
		)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				renderChatCancelNotice(composer, stdout)

				continue
			}
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		sessionPath = turn.SessionPath
	}

	return 0
}
