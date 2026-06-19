package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBuildStatusCountsSessionActivity verifies status counters derive from
// durable JSONL events rather than transient chat state.
func TestBuildStatusCountsSessionActivity(t *testing.T) {
	startedAt := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	events := []Event{
		statusEvent(
			t, EventSessionStarted, "1", "", startedAt,
			StartedData{
				CWD: "/work",
			},
		),
		statusEvent(
			t, EventUserMessage, "2", "1",
			startedAt.Add(time.Second),
			TextMessage(RoleUser, "hello"),
		),
		statusEvent(t, EventAssistantMessage, "3", "2",
			startedAt.Add(2*time.Second),
			AssistantToolCallMessage("", []ToolCallData{{
				ID:        "call_1",
				Name:      "ls",
				Arguments: `{"path":"."}`,
			}})),
		statusEvent(
			t, EventToolMessage, "4", "3",
			startedAt.Add(3*time.Second),
			ToolMessage("call_1", "ls", "file.go\n"),
		),
		statusEvent(
			t, EventAssistantMessage, "5", "4",
			startedAt.Add(4*time.Second),
			TextMessage(RoleAssistant, "done"),
		),
		statusEvent(
			t, EventContextSummary, "6", "5",
			startedAt.Add(5*time.Second), SummaryData{
				Summary: "older turns",
			},
		),
	}

	status, err := BuildStatus(events, startedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if status.UserTurns != 1 {
		t.Fatalf("unexpected turns: %d", status.UserTurns)
	}
	if status.ModelCalls != 2 {
		t.Fatalf("unexpected model calls: %d", status.ModelCalls)
	}
	if status.ToolCalls != 1 || status.ToolResults != 1 {
		t.Fatalf("unexpected tool counts: %#v", status)
	}
	if status.Compactions != 1 {
		t.Fatalf("unexpected compactions: %d", status.Compactions)
	}

	text := FormatStatus(status)
	if !strings.Contains(text, "session age: 2m 0s") {
		t.Fatalf("missing age: %q", text)
	}
	if !strings.Contains(text, "actual model usage: not recorded yet") {
		t.Fatalf("missing usage placeholder: %q", text)
	}
}

// TestFormatDurationCompactsLongDurations verifies status output stays short
// for sessions that have been open for hours.
func TestFormatDurationCompactsLongDurations(t *testing.T) {
	got := FormatDuration(3*time.Hour + 12*time.Minute + 30*time.Second)
	if got != "3h 12m" {
		t.Fatalf("unexpected duration: %q", got)
	}
}

// statusEvent builds one deterministic event fixture.
func statusEvent(t *testing.T, eventType string, id string, parent string,
	at time.Time, data any) Event {

	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}

	return Event{
		Type:     eventType,
		ID:       id,
		ParentID: parent,
		Time:     at,
		Data:     raw,
	}
}
