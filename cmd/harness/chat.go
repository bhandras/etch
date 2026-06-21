package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// runChat starts a line-oriented interactive chat session.
func runChat(cfg cliConfig, stdin io.Reader, stdout io.Writer,
	stderr io.Writer) int {

	defer showTerminalCursor(stdout)

	runtime, err := openChatRuntime(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	defer runtime.Close()
	sessionPath := runtime.sessionPath
	if runtime.resumeID != "" {
		fmt.Fprintf(
			stdout, "continuing session %s\n",
			shortID(runtime.resumeID),
		)
	}

	input := newChatInput(stdin, stdout)
	defer func() {
		_ = input.Close()
	}()
	composer := terminalComposer(input)
	chrome := newChatChrome(cfg, runtime.cwd, runtime.initialUsage)
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
				runtime.modelClient, runtime.registry, stdout,
				stderr, runtime.hookRunner,
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

			return 1
		}
		sessionPath = turn.SessionPath
	}

	return 0
}
