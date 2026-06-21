package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"harness/internal/core"
	"harness/internal/hooks"
	"harness/internal/model"
	promptctx "harness/internal/prompt"
	"harness/internal/session"
	"harness/internal/tool"
)

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

// renderChatCancelNotice prints the cancellation notice around the live prompt.
func renderChatCancelNotice(composer *terminalChatInput, stdout io.Writer) {
	write := func() {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, chatCancelNotice(stdout))
		fmt.Fprintln(stdout)
	}
	if composer == nil {
		write()

		return
	}
	composer.WithOutput(write)
}

// chatCancelNotice returns the muted dot-led cancellation message.
func chatCancelNotice(stdout io.Writer) string {
	style := terminalStyle{
		enabled: shouldStyle(stdout),
	}

	return style.wrapTone("• Canceled", terminalTone{muted: true})
}

// nextChatLine returns queued chat input before reading from the live channel.
func nextChatLine(results <-chan chatLineResult, pending *[]chatLineResult,
	inputDone bool) (chatLineResult, bool) {

	if len(*pending) > 0 {
		result := (*pending)[0]
		*pending = (*pending)[1:]

		return result, true
	}
	if inputDone {
		return chatLineResult{}, false
	}
	result, ok := <-results

	return result, ok
}

// runChatTurnWithSteering runs one model turn while capturing steering input.
func runChatTurnWithSteering(cfg cliConfig, line string, sessionPath string,
	cwd string, systemText string, modelClient model.Client,
	registry *tool.Registry, hookRunner *hooks.Runner, chrome *chatChrome,
	composer *terminalChatInput, results <-chan chatLineResult,
	inputDone *bool, pendingResults *[]chatLineResult,
	stdout io.Writer) (*core.TurnResult, error) {

	observer := &chatObserver{
		renderer: newLiveChatRenderer(stdout, true),
		chrome:   chrome,
	}
	observer.renderer.composer = composer
	observer.renderer.startStatus("Working")
	startedAt := time.Now()
	busyInput := &chatBusyInput{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcomes := make(chan chatTurnOutcome, 1)
	go func() {
		result, err := core.RunTurn(
			ctx, core.TurnRequest{
				Prompt:        line,
				SessionDir:    cfg.sessionDir,
				SessionPath:   sessionPath,
				CWD:           cwd,
				SystemText:    systemText,
				Model:         modelClient,
				ModelName:     cfg.model,
				Tools:         registry,
				MaxToolRounds: cfg.maxToolRounds,
				AutoCompactThresholdTokens: autoCompactThreshold(
					cfg,
				),
				AutoCompactKeepMessages:     cfg.keepMessages,
				AutoCompactKeepRecentTokens: cfg.keepRecentTokens,
				DrainSteering:               busyInput.DrainSteering,
				Observer:                    observer,
				Hooks:                       hookRunner,
			},
		)
		outcomes <- chatTurnOutcome{Result: result, Err: err}
	}()

	liveResults := results
	if *inputDone {
		liveResults = nil
	}
	for {
		select {
		case outcome := <-outcomes:
			if drainReadyBusyChatInput(
				liveResults, inputDone, busyInput,
			) {

				observer.renderer.stopStatus()

				return nil, context.Canceled
			}
			if outcome.Err != nil {
				observer.renderer.stopStatus()
				*pendingResults = append(
					*pendingResults, busyInput.Pending()...,
				)

				return nil, outcome.Err
			}
			observer.Finish(time.Since(startedAt))
			*pendingResults = append(
				*pendingResults, busyInput.Pending()...,
			)

			return outcome.Result, nil

		case result, ok := <-liveResults:
			if !ok {
				*inputDone = true
				liveResults = nil

				continue
			}
			if collectBusyChatInput(result, busyInput) {
				cancel()
			}
		}
	}
}

// drainReadyBusyChatInput classifies submitted input already waiting locally.
func drainReadyBusyChatInput(liveResults <-chan chatLineResult, inputDone *bool,
	busyInput *chatBusyInput) bool {

	for liveResults != nil {
		select {
		case result, ok := <-liveResults:
			if !ok {
				*inputDone = true

				return false
			}
			if collectBusyChatInput(result, busyInput) {
				return true
			}

		default:
			return false
		}
	}

	return false
}

// chatTurnOutcome carries the asynchronous result of one core turn.
type chatTurnOutcome struct {
	// Result stores the successful turn result.
	Result *core.TurnResult

	// Err stores any turn failure.
	Err error
}

// collectBusyChatInput records active-turn input and reports cancellations.
func collectBusyChatInput(result chatLineResult,
	busyInput *chatBusyInput) bool {

	if errors.Is(result.Err, errChatInputCanceled) {
		return true
	}
	if result.Err != nil || !result.OK {
		busyInput.AddPending(result)

		return false
	}
	line := result.Line
	commandLine := strings.TrimSpace(line)
	if commandLine == "" {
		return false
	}
	if strings.HasPrefix(commandLine, "/") {
		busyInput.AddPending(chatLineResult{
			Line: commandLine,
			OK:   true,
		})

		return false
	}
	busyInput.AddSteering(line)

	return false
}

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
