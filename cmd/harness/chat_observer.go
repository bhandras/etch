package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"harness/internal/core"
	"harness/internal/model"
	"harness/internal/render"
	"harness/internal/session"
	"harness/internal/tool"
)

// chatObserver renders appended assistant and tool messages during a turn.
type chatObserver struct {
	// mu protects transient observer state that can be updated by parallel
	// tool completions.
	mu sync.Mutex

	// renderer owns transient terminal formatting for one chat turn.
	renderer *liveChatRenderer

	// chrome owns prompt footer state shared across turns.
	chrome *chatChrome

	// toolCalls counts local tool calls executed during this turn.
	toolCalls int

	// batchedCalls stores tool IDs already shown in a batch summary.
	batchedCalls map[string]bool

	// activeSubagents stores child-agent task calls that have not returned.
	activeSubagents map[string]bool

	// streamedReasoning reports whether reasoning deltas were received.
	streamedReasoning bool

	// reasoningStatus stores streamed reasoning text for status extraction.
	reasoningStatus strings.Builder

	// dynamicReasoningStatus reports whether model summaries may label
	// status.
	dynamicReasoningStatus bool

	// modelStatus reports whether statusText came from model reasoning.
	modelStatus bool

	// usage accumulates provider token counters reported during this turn.
	usage model.Usage

	// timing stores coarse timing reported by the core after the turn.
	timing core.TurnTiming
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
		eventUsage := model.Usage{
			InputTokens:           usage.InputTokens,
			CachedInputTokens:     usage.CachedInputTokens,
			OutputTokens:          usage.OutputTokens,
			ReasoningOutputTokens: usage.ReasoningOutputTokens,
			TotalTokens:           usage.TotalTokens,
		}
		o.usage = o.usage.Add(eventUsage)
		if o.renderer.composer != nil && o.chrome != nil {
			o.renderer.composer.SetFooter(
				o.chrome.AddUsage(eventUsage),
			)
		}

		return
	}
	if event.Type == session.EventUserMessage {
		return
	}
	if !session.IsMessageEvent(event.Type) {
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
		o.finishSubagentTool(message)
		o.renderer.renderToolResult(message)

	default:
		o.renderer.renderAssistant(render.MessageText(message))
	}
}

// ToolBatchStarted renders one live summary for multi-tool model batches.
func (o *chatObserver) ToolBatchStarted(calls []model.ToolCall) {
	if len(calls) <= 1 {
		return
	}
	if o.batchedCalls == nil {
		o.batchedCalls = make(map[string]bool)
	}
	for _, call := range calls {
		o.batchedCalls[call.ID] = true
	}
	o.updateCannedStatus("Running tools")
	o.renderer.renderToolBatch(calls)
}

// ToolCallStarted renders one live tool call immediately before execution.
func (o *chatObserver) ToolCallStarted(call model.ToolCall) {
	o.toolCalls++
	o.startSubagentTool(call)
	o.updateCannedStatus("Running tools")
	if o.batchedCalls[call.ID] {
		return
	}
	o.renderer.renderToolCall(call)
}

// ToolCallFinished clears transient state for tools that complete before their
// durable result is appended to the transcript.
func (o *chatObserver) ToolCallFinished(call model.ToolCall) {
	o.finishSubagentCall(call.ID, call.Name)
}

// startSubagentTool records a running child-agent task for status display.
func (o *chatObserver) startSubagentTool(call model.ToolCall) {
	if call.Name != tool.NameTask {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.activeSubagents == nil {
		o.activeSubagents = make(map[string]bool)
	}
	o.activeSubagents[call.ID] = true
	o.renderer.setActiveSubagents(len(o.activeSubagents))
	if display, ok := parseSubagentCall(call.Arguments); ok {
		o.renderer.startSubagentStatus(
			call.ID, subagentLiveLabel(display),
			"starting",
		)
	}
}

// finishSubagentTool clears a completed child-agent task from status display.
func (o *chatObserver) finishSubagentTool(message session.MessageData) {
	o.finishSubagentCall(message.ToolCallID, message.Name)
}

// finishSubagentCall clears a completed child-agent task by call id.
func (o *chatObserver) finishSubagentCall(callID string, name string) {
	if name != tool.NameTask {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	if len(o.activeSubagents) == 0 {
		return
	}
	removedID := callID
	if callID != "" {
		delete(o.activeSubagents, callID)
	} else {
		for id := range o.activeSubagents {
			delete(o.activeSubagents, id)
			removedID = id

			break
		}
	}
	o.renderer.setActiveSubagents(len(o.activeSubagents))
	o.renderer.removeSubagentStatus(removedID)
}

// ToolProgress updates live progress rows for long-running tools.
func (o *chatObserver) ToolProgress(event tool.ProgressEvent) {
	o.renderer.updateSubagentStatus(event.ToolCallID, event.Message)
}

// ModelTextDelta records assistant stream progress without rendering raw
// partial deltas in the line-oriented chat UI.
func (o *chatObserver) ModelTextDelta(text string) {
	o.updateCannedStatus("Responding")
}

// ModelReasoningDelta records streamed reasoning progress without rendering
// partial summary fragments.
func (o *chatObserver) ModelReasoningDelta(text string) {
	o.streamedReasoning = true
	o.reasoningStatus.WriteString(text)
	if o.dynamicReasoningStatus {
		status := reasoningStatusText(o.reasoningStatus.String())
		if status != "" {
			o.modelStatus = true
			o.renderer.updateStatus(status)

			return
		}
	}
	o.updateCannedStatus("Thinking")
}

// ReasoningCompleted renders one model-provided thinking summary block.
func (o *chatObserver) ReasoningCompleted(text string) {
	if o.dynamicReasoningStatus {
		status := reasoningStatusText(text)
		if status != "" {
			o.modelStatus = true
			o.renderer.updateStatus(status)
		} else if o.streamedReasoning {
			o.updateCannedStatus("Working")
		}
	} else if o.streamedReasoning {
		o.updateCannedStatus("Working")
	}
	o.renderer.renderReasoning(text)
}

// updateCannedStatus changes status unless reasoning supplied a better label.
func (o *chatObserver) updateCannedStatus(text string) {
	if o.modelStatus {
		return
	}
	o.renderer.updateStatus(text)
}

// AutoCompacted renders one automatic context maintenance notice.
func (o *chatObserver) AutoCompacted(result core.AutoCompactResult) {
	o.renderer.renderAutoCompact(result)
}

// TurnTiming records coarse timing for the turn footer.
func (o *chatObserver) TurnTiming(timing core.TurnTiming) {
	o.timing = timing
}

// Finish renders terminal-only end-of-turn decoration.
func (o *chatObserver) Finish(elapsed time.Duration) {
	o.renderer.finish(elapsed, liveTurnStats{
		ToolCalls: o.toolCalls,
		Usage:     o.usage,
		Timing:    o.timing,
	})
}
