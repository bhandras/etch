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
				Trigger: "auto",
			},
		),
		statusEvent(
			t, EventModelUsage, "7", "6",
			startedAt.Add(6*time.Second), UsageData{
				InputTokens:           100,
				CachedInputTokens:     64,
				OutputTokens:          20,
				ReasoningOutputTokens: 5,
				TotalTokens:           120,
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
	if status.ToolBatches != 1 || status.LargestToolBatch != 1 {
		t.Fatalf("unexpected tool batch counts: %#v", status)
	}
	if status.Compactions != 1 {
		t.Fatalf("unexpected compactions: %d", status.Compactions)
	}
	if status.AutoCompactions != 1 || status.ManualCompactions != 0 {
		t.Fatalf("unexpected compaction triggers: %#v", status)
	}
	if status.Usage.InputTokens != 100 ||
		status.Usage.CachedInputTokens != 64 ||
		status.Usage.TotalTokens != 120 {

		t.Fatalf("unexpected usage: %#v", status.Usage)
	}

	text := FormatStatus(status)
	if !strings.Contains(text, "- age: 2m 0s") {
		t.Fatalf("missing age: %q", text)
	}
	if !strings.Contains(text, "Actual Model Usage") {
		t.Fatalf("missing usage section: %q", text)
	}
	if !strings.Contains(text, "compactions: 1 (1 auto, 0 manual)") {
		t.Fatalf("missing compaction trigger counts: %q", text)
	}
	if !strings.Contains(text, "- tool batches: 1 (largest 1)") {
		t.Fatalf("missing tool batch counts: %q", text)
	}
	if !strings.Contains(text, "- model wait: 2s") {
		t.Fatalf("missing model wait: %q", text)
	}
	if !strings.Contains(text, "- tool result wait: 1s") {
		t.Fatalf("missing tool wait: %q", text)
	}
	if !strings.Contains(text, "- cached input: 64 tokens") {
		t.Fatalf("missing cached input usage: %q", text)
	}
}

// TestFormatStatusShowsUsagePlaceholder verifies sessions without provider
// usage make the missing telemetry explicit.
func TestFormatStatusShowsUsagePlaceholder(t *testing.T) {
	text := FormatStatus(Status{})
	if !strings.Contains(text, "- not recorded yet") {
		t.Fatalf("missing usage placeholder: %q", text)
	}
}

// TestBuildStatusUsesMetricRequestCounts verifies new logs report actual
// provider requests while old logs can still fall back to assistant events.
func TestBuildStatusUsesMetricRequestCounts(t *testing.T) {
	startedAt := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	events := []Event{
		statusEvent(
			t, EventSessionStarted, "1", "", startedAt,
			StartedData{
				CWD: "/work",
			},
		),
		statusEvent(
			t, EventAssistantMessage, "2", "1",
			startedAt.Add(time.Second),
			TextMessage(RoleAssistant, "thinking"),
		),
		statusEvent(
			t, EventModelMetrics, "3", "2",
			startedAt.Add(2*time.Second), MetricsData{
				Requests:              2,
				ContinuationRequests:  1,
				ContinuationFallbacks: 1,
				RequestBytes:          2048,
				ResponseBytes:         1024,
				InputMessages:         8,
				DeltaMessages:         2,
				ToolCount:             5,
			},
		),
	}

	status, err := BuildStatus(events, startedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if status.ModelCalls != 2 {
		t.Fatalf("unexpected model calls: %#v", status)
	}
	if status.Metrics.RequestBytes != 2048 ||
		status.Metrics.ContinuationRequests != 1 ||
		status.Metrics.ContinuationFallbacks != 1 {

		t.Fatalf("unexpected metrics: %#v", status.Metrics)
	}
	text := FormatStatus(status)
	if !strings.Contains(text, "Recorded Transport") {
		t.Fatalf("missing transport section: %q", text)
	}
	if !strings.Contains(
		text, "- requests: 2 (1 continuation attempts, 1 fallbacks)",
	) {

		t.Fatalf("missing request counts: %q", text)
	}
	if !strings.Contains(text, "- averages: 1.0KB up/request") {
		t.Fatalf("missing averages: %q", text)
	}
	if !strings.Contains(
		text, "8 input messages, 2 delta messages, 5 tools",
	) {

		t.Fatalf("missing request shape: %q", text)
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
