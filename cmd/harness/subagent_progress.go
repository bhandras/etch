package main

import (
	"fmt"
	"sync"
	"time"

	"harness/internal/core"
	"harness/internal/model"
	"harness/internal/render"
	"harness/internal/session"
	"harness/internal/tool"
)

// subagentProgressObserver forwards child-agent activity to the parent tool.
type subagentProgressObserver struct {
	// sink receives parent-visible progress events.
	sink tool.ProgressSink

	// toolCallID is the parent task call represented by this child.
	toolCallID string

	// mu protects last.
	mu sync.Mutex

	// last stores the most recent emitted message for deduplication.
	last string
}

// newSubagentProgressObserver creates a child observer when progress is wired.
func newSubagentProgressObserver(sink tool.ProgressSink,
	toolCallID string) *subagentProgressObserver {

	if sink == nil {
		return nil
	}

	return &subagentProgressObserver{
		sink:       sink,
		toolCallID: toolCallID,
	}
}

// EventAppended forwards child usage and transport counters as they are
// persisted so the parent footer can update before the task finishes.
func (o *subagentProgressObserver) EventAppended(event session.Event) {
	switch event.Type {
	case session.EventModelUsage:
		usage, err := decodeUsage(event)
		if err != nil {
			return
		}
		o.emitUsage(model.Usage{
			InputTokens:           usage.InputTokens,
			CachedInputTokens:     usage.CachedInputTokens,
			OutputTokens:          usage.OutputTokens,
			ReasoningOutputTokens: usage.ReasoningOutputTokens,
			TotalTokens:           usage.TotalTokens,
		})

	case session.EventModelMetrics:
		metrics, err := decodeMetrics(event)
		if err != nil {
			return
		}
		o.emitMetrics(modelMetricsFromSessionMetrics(metrics))
	}
}

// ToolBatchStarted reports a compact child tool batch summary.
func (o *subagentProgressObserver) ToolBatchStarted(calls []model.ToolCall) {
	if len(calls) == 0 {
		return
	}
	if len(calls) == 1 {
		o.emit(toolCallProgressText(calls[0]))

		return
	}
	o.emit(fmt.Sprintf("running %d tools", len(calls)))
}

// ToolCallStarted reports one child tool invocation.
func (o *subagentProgressObserver) ToolCallStarted(call model.ToolCall) {
	o.emit(toolCallProgressText(call))
}

// ToolCallFinished ignores child completion because the next child event or
// final task result is a better parent-visible activity signal.
func (o *subagentProgressObserver) ToolCallFinished(call model.ToolCall) {
}

// ModelTextDelta reports that the child is writing a response.
func (o *subagentProgressObserver) ModelTextDelta(text string) {
	o.emit("responding")
}

// ModelReasoningDelta reports child thinking without exposing partial stream
// fragments as live status labels.
func (o *subagentProgressObserver) ModelReasoningDelta(text string) {
	o.emit("thinking")
}

// ReasoningCompleted reports the completed child thinking summary heading.
func (o *subagentProgressObserver) ReasoningCompleted(text string) {
	status := reasoningStatusText(text)
	if status == "" {
		status = "thinking"
	}
	o.emit(status)
}

// AutoCompacted reports child context maintenance.
func (o *subagentProgressObserver) AutoCompacted(
	result core.AutoCompactResult) {

	o.emit("compacting context")
}

// TurnTiming ignores coarse child timing because live metrics and the final
// task result carry the counters shown in parent chat chrome.
func (o *subagentProgressObserver) TurnTiming(timing core.TurnTiming) {
}

// emitUsage sends provider token counters to the parent observer.
func (o *subagentProgressObserver) emitUsage(usage model.Usage) {
	if o == nil || o.sink == nil || usage.Empty() {
		return
	}
	o.sink(tool.ProgressEvent{
		ToolCallID: o.toolCallID,
		Usage:      usage,
	})
}

// emitMetrics sends provider transport counters to the parent observer.
func (o *subagentProgressObserver) emitMetrics(metrics model.Metrics) {
	if o == nil || o.sink == nil || metrics.Empty() {
		return
	}
	o.sink(tool.ProgressEvent{
		ToolCallID: o.toolCallID,
		Metrics:    metrics,
	})
}

// emit sends one deduplicated progress message to the parent.
func (o *subagentProgressObserver) emit(message string) {
	if o == nil || o.sink == nil {
		return
	}
	message = compactStatusText(message)
	if message == "" {
		return
	}
	o.mu.Lock()
	if o.last == message {
		o.mu.Unlock()

		return
	}
	o.last = message
	o.mu.Unlock()

	o.sink(tool.ProgressEvent{
		ToolCallID: o.toolCallID,
		Message:    message,
	})
}

// toolCallProgressText renders one child tool call for live status.
func toolCallProgressText(call model.ToolCall) string {
	return render.ToolCallText(session.ToolCallData{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: call.Arguments,
	})
}

// modelMetricsFromSessionMetrics converts durable metric payloads into the
// neutral model metric shape used by live progress events.
func modelMetricsFromSessionMetrics(metrics session.MetricsData) model.Metrics {
	return model.Metrics{
		Transport:                  metrics.Transport,
		Requests:                   metrics.Requests,
		WebSocketConnections:       metrics.WebSocketConnections,
		WebSocketReuses:            metrics.WebSocketReuses,
		ContinuationRequests:       metrics.ContinuationRequests,
		ContinuationFallbacks:      metrics.ContinuationFallbacks,
		ContinuationFallbackStatus: metrics.ContinuationFallbackStatus,
		ContinuationFallbackError:  metrics.ContinuationFallbackError,
		RequestBytes:               metrics.RequestBytes,
		ResponseBytes:              metrics.ResponseBytes,
		InputMessages:              metrics.InputMessages,
		DeltaMessages:              metrics.DeltaMessages,
		ToolCount:                  metrics.ToolCount,
		InstructionBytes:           metrics.InstructionBytes,
		InputBytes:                 metrics.InputBytes,
		ToolBytes:                  metrics.ToolBytes,
		TimeToHeaders: time.Duration(
			metrics.TimeToHeadersMillis,
		) * time.Millisecond,
		TimeToFirstEvent: time.Duration(
			metrics.TimeToFirstEventMillis,
		) * time.Millisecond,
	}
}
