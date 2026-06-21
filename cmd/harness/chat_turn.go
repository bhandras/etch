package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"harness/internal/core"
	"harness/internal/hooks"
	"harness/internal/model"
	"harness/internal/provider/openai"
	"harness/internal/tool"
)

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
		renderer: newLiveChatRenderer(
			stdout, !shouldStyle(stdout),
		),
		chrome:                 chrome,
		dynamicReasoningStatus: chatDynamicReasoningStatus(cfg),
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

// chatDynamicReasoningStatus reports whether reasoning summaries are reliable
// enough to drive transient terminal status labels.
func chatDynamicReasoningStatus(cfg cliConfig) bool {
	return cfg.provider == openai.ProviderName &&
		cfg.openaiAPI == openai.APIResponses &&
		cfg.reasoningSummary != "" &&
		(!cfg.baseURLExplicit || cfg.baseURL == openai.DefaultBaseURL)
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
