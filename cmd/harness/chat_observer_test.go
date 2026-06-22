package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"harness/internal/model"
	"harness/internal/session"
)

// TestChatObserverRendersToolEvents verifies that live chat feedback pairs each
// local tool call with its result instead of batching call headers first.
func TestChatObserverRendersToolEvents(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: newLiveChatRenderer(&stdout, false),
	}

	observer.EventAppended(messageEvent(t,
		session.EventAssistantMessage,
		session.AssistantToolCallMessage("", []session.ToolCallData{{
			ID:        "call_1",
			Name:      "bash",
			Arguments: `{"command":"go test ./..."}`,
		}}),
	))
	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"go test ./..."}`,
	})
	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage,
			session.ToolMessage("call_1", "bash", "exit code: 0\n"),
		),
	)

	got := stdout.String()
	want := "• Ran bash go test ./...\n\n   exit code: 0\n"
	if got != want {
		t.Fatalf("render mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestChatObserverSeparatesAssistantReplies verifies that final assistant text
// is visually separated from prior tool output.
func TestChatObserverSeparatesAssistantReplies(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: newLiveChatRenderer(&stdout, false),
	}

	observer.ReasoningCompleted("checking the README")
	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_1",
		Name:      "read",
		Arguments: `{"path":"README.md"}`,
	})
	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage,
			session.ToolMessage("call_1", "read", "hello\n"),
		),
	)
	observer.EventAppended(
		messageEvent(
			t, session.EventAssistantMessage,
			session.TextMessage(session.RoleAssistant, "done"),
		),
	)

	got := stdout.String()
	if !strings.Contains(
		got, "• checking the README\n\n• Ran read README.md\n\n   "+
			"read 1 lines\n\n• done\n",
	) {

		t.Fatalf("assistant reply was not separated: %q", got)
	}
}

// TestChatObserverRendersFinalAfterNoiseOnlyDeltas verifies filtered live
// stream fragments do not suppress the durable final assistant answer.
func TestChatObserverRendersFinalAfterNoiseOnlyDeltas(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: newLiveChatRenderer(&stdout, false),
	}

	observer.ModelTextDelta(".\n:\n`.\n")
	observer.EventAppended(
		messageEvent(
			t, session.EventAssistantMessage, session.TextMessage(
				session.RoleAssistant, "real final answer",
			),
		),
	)

	got := stdout.String()
	if strings.Contains(got, "\n  .") ||
		strings.Contains(got, "\n  :") ||
		strings.Contains(got, "\n  `.") {

		t.Fatalf("noise-only delta became visible: %q", got)
	}
	if !strings.Contains(got, "• real final answer\n") {
		t.Fatalf("final assistant answer was suppressed: %q", got)
	}
}

// TestChatObserverTracksActiveSubagents verifies task calls update the quiet
// working-status activity counter until matching tool results arrive.
func TestChatObserverTracksActiveSubagents(t *testing.T) {
	var stdout bytes.Buffer
	renderer := newLiveChatRenderer(&stdout, false)
	observer := &chatObserver{
		renderer: renderer,
	}

	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_1",
		Name:      "task",
		Arguments: `{"profile":"explore","task":"map the repo"}`,
	})
	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_2",
		Name:      "task",
		Arguments: `{"profile":"review","task":"review the diff"}`,
	})
	if renderer.activeSubagents != 2 {
		t.Fatalf("active subagents after starts = %d",
			renderer.activeSubagents)
	}

	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage,
			session.ToolMessage("call_1", "task", "Task done."),
		),
	)
	if renderer.activeSubagents != 1 {
		t.Fatalf("active subagents after first result = %d",
			renderer.activeSubagents)
	}

	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage,
			session.ToolMessage("call_2", "task", "Task done."),
		),
	)
	if renderer.activeSubagents != 0 {
		t.Fatalf("active subagents after all results = %d",
			renderer.activeSubagents)
	}
}

// messageEvent creates one durable message event for CLI rendering tests.
func messageEvent(t *testing.T, eventType string,
	message session.MessageData) session.Event {

	t.Helper()
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}

	return session.Event{
		Type: eventType,
		ID:   "event_1",
		Time: time.Now().UTC(),
		Data: raw,
	}
}
