package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// resumeRecentMessageLimit bounds the transcript tail shown before a
	// resumed chat prompt.
	resumeRecentMessageLimit = 8
)

// runChat starts a line-oriented interactive chat session.
func runChat(cfg cliConfig, stdin io.Reader, stdout io.Writer,
	stderr io.Writer) int {

	defer showTerminalCursor(stdout)
	warnImplicitEchoProvider(cfg, stderr)

	runtime, err := openChatRuntime(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	defer runtime.Close()
	sessionPath := runtime.sessionPath
	activeSessionID := runtime.resumeID
	code := 0
	defer func() {
		if code == 0 {
			printChatResumeHint(cfg, activeSessionID, stdout)
		}
	}()
	if runtime.resumeID != "" {
		fmt.Fprintf(
			stdout, "continuing session %s\n",
			shortID(runtime.resumeID),
		)
		if err := renderRecentSessionPath(
			runtime.sessionPath, resumeRecentMessageLimit, stdout,
		); err != nil {

			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
	}

	input := newChatInput(stdin, stdout)
	defer func() {
		_ = input.Close()
	}()
	composer := terminalComposer(input)
	chrome := newChatChrome(cfg, runtime.cwd, runtime.initialStatus)
	if composer != nil {
		composer.SetFooter(chrome.Footer())
		if sessionPath != "" {
			history, err := chatPromptHistory(sessionPath)
			if err != nil {
				fmt.Fprintln(stderr, "error:", err)

				code = 1

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
				code = 0

				return 0
			}
			fmt.Fprintln(
				stderr, "error: read chat input:", result.Err,
			)

			code = 1

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
				runtime.modelClient, runtime.registry, stdout,
				stderr, runtime.hookRunner,
			)
			sessionPath = nextPath
			if composer != nil && commandLine == "/new" {
				composer.SetHistory(nil)
			}
			if nextPath == "" {
				activeSessionID = ""
			}
			if !keepGoing {
				code = 0

				return 0
			}

			continue
		}

		turn, err := runChatTurnWithSteering(
			cfg, line, sessionPath, runtime.cwd, runtime.systemText,
			runtime.modelClient, runtime.registry,
			runtime.hookRunner, chrome, composer, results,
			&inputDone, &pendingResults, stdout,
		)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				renderChatCancelNotice(composer, stdout)

				continue
			}
			fmt.Fprintln(stderr, "error:", err)

			code = 1

			return 1
		}
		sessionPath = turn.SessionPath
		activeSessionID = turn.SessionID
	}

	code = 0

	return 0
}

// printChatResumeHint writes the command that continues an active session.
func printChatResumeHint(cfg cliConfig, sessionID string, stdout io.Writer) {
	if sessionID == "" {
		return
	}
	fmt.Fprintf(stdout, "\nsession: %s\n", sessionID)
	fmt.Fprintf(stdout, "resume: %s\n", chatResumeCommand(cfg, sessionID))
}

// chatResumeCommand returns a copyable command for continuing sessionID.
func chatResumeCommand(cfg cliConfig, sessionID string) string {
	if cfg.sessionDir == "" || cfg.sessionDir == defaultSessionDir {
		return "etch resume " + sessionID
	}

	return "etch resume --session-dir " + shellQuote(cfg.sessionDir) +
		" " + sessionID
}

// shellQuote returns a conservative shell token for a display command.
func shellQuote(text string) string {
	if text == "" {
		return "''"
	}
	if strings.ContainsAny(text, " \t\n'\"\\$`!*?[]{}();&|<>") {
		return "'" + strings.ReplaceAll(text, "'", "'\"'\"'") + "'"
	}

	return text
}
