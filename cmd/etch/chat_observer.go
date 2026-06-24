package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"etch/internal/core"
	"etch/internal/model"
	"etch/internal/render"
	"etch/internal/session"
	"etch/internal/tool"
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

	// partialSubagentResults stores task results already rendered from
	// terminal-only parallel completion callbacks.
	partialSubagentResults map[string]bool

	// liveSubagentUsage stores task calls whose child usage counters were
	// already folded into the parent footer through progress events.
	liveSubagentUsage map[string]bool

	// liveSubagentMetrics stores task calls whose child transport counters
	// were already folded into the parent footer through progress events.
	liveSubagentMetrics map[string]bool

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
		o.addUsage(eventUsage)

		return
	}
	if event.Type == session.EventModelMetrics {
		metrics, err := decodeMetrics(event)
		if err != nil {
			fmt.Fprintf(
				o.renderer.stdout, "render error: %v\n", err,
			)

			return
		}
		o.addTiming(turnTimingFromMetrics(metrics))

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
		if o.skipPartialSubagentResult(message) {
			return
		}
		o.finishSubagentTool(message)
		o.recordSubagentActivity(message)
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

// ToolResultCompleted renders task results that finish before their ordered
// durable tool event can be appended to the parent session.
func (o *chatObserver) ToolResultCompleted(call model.ToolCall,
	result tool.Result) {

	if call.Name != tool.NameTask {
		return
	}
	message := session.ToolMessage(call.ID, call.Name, result.Text)
	o.finishSubagentTool(message)
	o.recordSubagentActivity(message)
	o.renderer.renderToolResult(message)

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.partialSubagentResults == nil {
		o.partialSubagentResults = make(map[string]bool)
	}
	o.partialSubagentResults[call.ID] = true
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
			call.ID, display.Profile, display.Task, "starting",
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

// skipPartialSubagentResult reports whether a durable task result was already
// rendered through a terminal-only completion callback.
func (o *chatObserver) skipPartialSubagentResult(
	message session.MessageData) bool {

	if message.Name != tool.NameTask || message.ToolCallID == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.partialSubagentResults[message.ToolCallID] {
		return false
	}
	delete(o.partialSubagentResults, message.ToolCallID)

	return true
}

// recordSubagentActivity adds child-session counters to parent-visible stats.
func (o *chatObserver) recordSubagentActivity(message session.MessageData) {
	if message.Name != tool.NameTask {
		return
	}
	display, ok := parseSubagentResult(render.MessageText(message))
	if !ok || strings.TrimSpace(display.SessionPath) == "" {
		return
	}
	status, err := readSessionStatus(display.SessionPath)
	if err != nil {
		return
	}
	usage := modelUsageFromSessionStatus(status)
	if !usage.Empty() &&
		!o.subagentUsageAlreadyRecorded(message.ToolCallID) {

		o.addUsage(usage)
	}
	o.addSubagentStats(
		status, o.subagentMetricsAlreadyRecorded(message.ToolCallID),
	)
}

// addUsage records provider usage and refreshes the prompt footer totals.
func (o *chatObserver) addUsage(usage model.Usage) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.usage = o.usage.Add(usage)
	if o.renderer.composer != nil && o.chrome != nil {
		o.renderer.composer.SetFooter(o.chrome.AddUsage(usage))
	}
}

// addSubagentStats records child-agent transport and tool counters for the
// current parent turn footer.
func (o *chatObserver) addSubagentStats(status session.Status,
	skipTiming bool) {

	timing := turnTimingFromSessionStatus(status)
	o.mu.Lock()
	defer o.mu.Unlock()

	o.toolCalls += status.ToolCalls
	if skipTiming {
		return
	}
	o.timing = addTurnTiming(o.timing, timing)
	if o.renderer.composer != nil && o.chrome != nil {
		o.renderer.composer.SetFooter(o.chrome.AddTiming(timing))
	}
}

// subagentUsageAlreadyRecorded reports whether callID contributed live usage
// counters and clears that marker for the final task result.
func (o *chatObserver) subagentUsageAlreadyRecorded(callID string) bool {
	if strings.TrimSpace(callID) == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	recorded := o.liveSubagentUsage[callID]
	delete(o.liveSubagentUsage, callID)

	return recorded
}

// subagentMetricsAlreadyRecorded reports whether callID contributed live
// transport counters and clears that marker for the final task result.
func (o *chatObserver) subagentMetricsAlreadyRecorded(callID string) bool {
	if strings.TrimSpace(callID) == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	recorded := o.liveSubagentMetrics[callID]
	delete(o.liveSubagentMetrics, callID)

	return recorded
}

// addTiming records provider transport counters and refreshes the footer.
func (o *chatObserver) addTiming(timing core.TurnTiming) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.timing = addTurnTiming(o.timing, timing)
	if o.renderer.composer != nil && o.chrome != nil {
		o.renderer.composer.SetFooter(o.chrome.AddTiming(timing))
	}
}

// ToolProgress updates live progress rows for long-running tools.
func (o *chatObserver) ToolProgress(event tool.ProgressEvent) {
	if !event.Usage.Empty() {
		o.recordLiveSubagentUsage(event.ToolCallID, event.Usage)
	}
	if !event.Metrics.Empty() {
		o.recordLiveSubagentMetrics(event.ToolCallID, event.Metrics)
	}
	if strings.TrimSpace(event.Message) != "" {
		o.renderer.updateSubagentStatus(event.ToolCallID, event.Message)
	}
}

// recordLiveSubagentUsage folds child-agent token counters into the live parent
// footer as soon as the child reports them.
func (o *chatObserver) recordLiveSubagentUsage(callID string,
	usage model.Usage) {

	if strings.TrimSpace(callID) != "" {
		o.mu.Lock()
		if o.liveSubagentUsage == nil {
			o.liveSubagentUsage = make(map[string]bool)
		}
		o.liveSubagentUsage[callID] = true
		o.mu.Unlock()
	}
	o.addUsage(usage)
}

// recordLiveSubagentMetrics folds child-agent transport counters into the live
// parent footer as soon as the child reports them.
func (o *chatObserver) recordLiveSubagentMetrics(callID string,
	metrics model.Metrics) {

	timing := turnTimingFromModelMetrics(metrics)
	if strings.TrimSpace(callID) != "" {
		o.mu.Lock()
		if o.liveSubagentMetrics == nil {
			o.liveSubagentMetrics = make(map[string]bool)
		}
		o.liveSubagentMetrics[callID] = true
		o.mu.Unlock()
	}
	o.addTiming(timing)
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
	o.mu.Lock()
	defer o.mu.Unlock()

	o.timing.ModelDuration = timing.ModelDuration
	o.timing.ToolDuration = timing.ToolDuration
	o.timing.ToolBatches = timing.ToolBatches
	o.timing.LargestToolBatch = timing.LargestToolBatch
}

// Finish renders terminal-only end-of-turn decoration.
func (o *chatObserver) Finish(elapsed time.Duration) {
	o.mu.Lock()
	stats := liveTurnStats{
		ToolCalls: o.toolCalls,
		Usage:     o.usage,
		Timing:    o.timing,
	}
	o.mu.Unlock()

	o.renderer.finish(elapsed, stats)
}

// addTurnTiming returns the additive merge of two turn timing values.
func addTurnTiming(left core.TurnTiming,
	right core.TurnTiming) core.TurnTiming {

	merged := core.TurnTiming{
		ModelCalls:       left.ModelCalls + right.ModelCalls,
		ModelDuration:    left.ModelDuration + right.ModelDuration,
		RequestBytes:     left.RequestBytes + right.RequestBytes,
		ResponseBytes:    left.ResponseBytes + right.ResponseBytes,
		TimeToHeaders:    left.TimeToHeaders + right.TimeToHeaders,
		TimeToFirstEvent: left.TimeToFirstEvent + right.TimeToFirstEvent,
		ToolDuration:     left.ToolDuration + right.ToolDuration,
		ToolBatches:      left.ToolBatches + right.ToolBatches,
		LargestToolBatch: left.LargestToolBatch,
	}
	if right.LargestToolBatch > merged.LargestToolBatch {
		merged.LargestToolBatch = right.LargestToolBatch
	}

	return merged
}
