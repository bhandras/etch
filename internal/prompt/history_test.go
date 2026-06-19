package prompt

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"harness/internal/model"
	"harness/internal/session"
)

// TestBuildHistoryMessagesReplaysSessionMessages verifies that durable
// messages become ordered model context.
func TestBuildHistoryMessagesReplaysSessionMessages(t *testing.T) {
	events := []session.Event{
		messageEvent(
			t, "1", session.EventUserMessage,
			session.TextMessage(session.RoleUser, "hello"),
		),
		messageEvent(t, "2", session.EventAssistantMessage,
			session.AssistantToolCallMessage("", []session.ToolCallData{{
				ID: "call_1", Name: "ls", Arguments: `{"path":"."}`,
			}})),
		messageEvent(
			t, "3", session.EventToolMessage,
			session.ToolMessage("call_1", "ls", "go.mod"),
		),
	}

	messages, err := BuildHistoryMessages(HistoryRequest{
		Events:     events,
		SystemText: "system rules",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected four messages, got %d", len(messages))
	}
	if messages[0].Role != model.RoleSystem ||
		messages[0].Content != "system rules" {

		t.Fatalf("unexpected system message: %#v", messages[0])
	}
	if messages[1].Role != model.RoleUser ||
		messages[1].Content != "hello" {

		t.Fatalf("unexpected user message: %#v", messages[1])
	}
	if len(messages[2].ToolCalls) != 1 ||
		messages[2].ToolCalls[0].Name != "ls" {

		t.Fatalf("unexpected tool call message: %#v", messages[2])
	}
	if messages[3].Role != model.RoleTool ||
		messages[3].ToolCallID != "call_1" ||
		messages[3].Name != "ls" ||
		messages[3].Content != "go.mod" {

		t.Fatalf("unexpected tool result message: %#v", messages[3])
	}
}

// TestBuildHistoryMessagesUsesLatestSummary verifies that compaction summaries
// replace older raw message history in model context.
func TestBuildHistoryMessagesUsesLatestSummary(t *testing.T) {
	first := messageEvent(
		t, "1", session.EventUserMessage,
		session.TextMessage(session.RoleUser, "old"),
	)
	kept := messageEvent(
		t, "2", session.EventUserMessage,
		session.TextMessage(session.RoleUser, "recent"),
	)
	summary := summaryEvent(t, "3", session.SummaryData{
		Summary:          "old summary",
		RangeStartID:     first.ID,
		RangeEndID:       first.ID,
		FirstKeptEventID: kept.ID,
	})

	messages, err := BuildHistoryMessages(HistoryRequest{
		Events: []session.Event{first, kept, summary},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected summary plus kept message, got %#v",
			messages)
	}
	if messages[0].Role != model.RoleSystem ||
		!strings.Contains(messages[0].Content, "old summary") {

		t.Fatalf("unexpected summary message: %#v", messages[0])
	}
	if messages[1].Content != "recent" {
		t.Fatalf("unexpected kept message: %#v", messages[1])
	}
}

// messageEvent creates one durable message event for prompt tests.
func messageEvent(t *testing.T, id string, eventType string,
	data session.MessageData) session.Event {

	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}

	return session.Event{
		Type: eventType,
		ID:   id,
		Time: time.Now().UTC(),
		Data: raw,
	}
}

// summaryEvent creates one durable summary event for prompt tests.
func summaryEvent(t *testing.T, id string,
	data session.SummaryData) session.Event {

	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}

	return session.Event{
		Type: session.EventContextSummary,
		ID:   id,
		Time: time.Now().UTC(),
		Data: raw,
	}
}
