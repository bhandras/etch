package main

import (
	"fmt"
	"sync"

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

// EventAppended ignores durable child events; child JSONL stores the details.
func (o *subagentProgressObserver) EventAppended(event session.Event) {
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

// ModelTextDelta reports that the child is writing a response.
func (o *subagentProgressObserver) ModelTextDelta(text string) {
	o.emit("responding")
}

// ModelReasoningDelta reports child thinking progress when available.
func (o *subagentProgressObserver) ModelReasoningDelta(text string) {
	status := reasoningStatusText(text)
	if status == "" {
		status = "thinking"
	}
	o.emit(status)
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

// TurnTiming ignores child timing; the final task result carries counters.
func (o *subagentProgressObserver) TurnTiming(timing core.TurnTiming) {
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
