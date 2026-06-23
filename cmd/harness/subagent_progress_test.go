package main

import (
	"testing"

	"harness/internal/tool"
)

// TestSubagentProgressReasoningDeltaUsesStableStatus verifies child reasoning
// streams do not leak token-sized fragments into live status rows.
func TestSubagentProgressReasoningDeltaUsesStableStatus(t *testing.T) {
	var events []tool.ProgressEvent
	observer := newSubagentProgressObserver(
		func(event tool.ProgressEvent) {
			events = append(events, event)
		},
		"call_1",
	)

	observer.ModelReasoningDelta("Summar")
	observer.ModelReasoningDelta("izing")
	observer.ReasoningCompleted("**Summarizing plugin architecture**")

	if len(events) != 2 {
		t.Fatalf("progress event count = %d, want 2: %#v", len(events),
			events)
	}
	if events[0].Message != "thinking" {
		t.Fatalf("delta status = %q, want thinking", events[0].Message)
	}
	if events[1].Message != "Summarizing plugin architecture" {
		t.Fatalf("completed status = %q", events[1].Message)
	}
}
