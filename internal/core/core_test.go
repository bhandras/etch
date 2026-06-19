package core

import (
	"context"
	"encoding/json"
	"testing"

	"harness/internal/model"
	"harness/internal/session"
)

// TestRunTurnPersistsEchoExchange verifies the first complete executable slice:
// prompt admission, model streaming, and JSONL session persistence.
func TestRunTurnPersistsEchoExchange(t *testing.T) {
	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "hello",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      model.EchoClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssistantText != "hello" {
		t.Fatalf("assistant text mismatch: %q", result.AssistantText)
	}

	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected three events, got %d", len(events))
	}
	if events[0].Type != session.EventSessionStarted {
		t.Fatalf("unexpected first event type: %q", events[0].Type)
	}
	if events[1].Type != session.EventUserMessage {
		t.Fatalf("unexpected user event type: %q", events[1].Type)
	}
	if events[2].Type != session.EventAssistantMessage {
		t.Fatalf("unexpected assistant event type: %q", events[2].Type)
	}
	if events[1].ParentID != events[0].ID ||
		events[2].ParentID != events[1].ID {

		t.Fatalf("events are not parent chained: %#v", events)
	}

	var assistant session.MessageData
	if err := json.Unmarshal(events[2].Data, &assistant); err != nil {
		t.Fatal(err)
	}
	if assistant.Role != session.RoleAssistant {
		t.Fatalf("assistant role mismatch: %q", assistant.Role)
	}
	if assistant.Content[0].Text != "hello" {
		t.Fatalf("assistant content mismatch: %#v", assistant.Content)
	}
}

// TestRunTurnRejectsEmptyPrompt keeps invalid CLI input from creating empty
// session files.
func TestRunTurnRejectsEmptyPrompt(t *testing.T) {
	_, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "   ",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      model.EchoClient{},
	})
	if err == nil {
		t.Fatal("expected empty prompt error")
	}
}
