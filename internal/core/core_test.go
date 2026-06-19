package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"harness/internal/model"
	"harness/internal/session"
	"harness/internal/tool"
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

// TestRunTurnContinuesExistingSession verifies that a follow-up turn appends
// to the same JSONL log and replays prior messages into the model request.
func TestRunTurnContinuesExistingSession(t *testing.T) {
	first, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "first",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      model.EchoClient{},
	})
	if err != nil {
		t.Fatal(err)
	}

	client := &scriptedToolClient{
		events: [][]model.Event{{
			{
				Type: model.EventTextDelta,
				Text: "second",
			},
			{
				Type: model.EventDone,
			},
		}},
	}
	second, err := RunTurn(context.Background(), TurnRequest{
		Prompt:      "follow-up",
		SessionPath: first.SessionPath,
		CWD:         "/work/project",
		Model:       client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionPath != first.SessionPath {
		t.Fatalf("session path mismatch: want %q got %q",
			first.SessionPath, second.SessionPath)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one model request, got %d",
			len(client.requests))
	}

	messages := client.requests[0].Messages
	if len(messages) != 3 {
		t.Fatalf("expected three messages, got %#v", messages)
	}
	if messages[0].Role != model.RoleUser ||
		messages[0].Content != "first" {

		t.Fatalf("unexpected first history message: %#v", messages[0])
	}
	if messages[1].Role != model.RoleAssistant ||
		messages[1].Content != "first" {

		t.Fatalf("unexpected assistant history message: %#v",
			messages[1])
	}
	if messages[2].Role != model.RoleUser ||
		messages[2].Content != "follow-up" {

		t.Fatalf("unexpected current user message: %#v", messages[2])
	}
}

// TestRunTurnExecutesToolCalls verifies that the core can run one model
// requested tool call and feed the result back into a final model answer.
func TestRunTurnExecutesToolCalls(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "")

	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:   "call_1",
						Name: tool.NameLS,
						Arguments: `{"path":` +
							quoteJSON(dir) +
							`}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "I found go.mod.",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}

	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "list files",
		SessionDir: t.TempDir(),
		CWD:        dir,
		Model:      client,
		Tools:      tool.DefaultRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssistantText != "I found go.mod." {
		t.Fatalf("unexpected assistant text: %q", result.AssistantText)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected two model requests, got %d",
			len(client.requests))
	}
	if !hasToolSpec(client.requests[0].Tools, tool.NameLS) {
		t.Fatalf("expected first request to include ls tool: %#v",
			client.requests[0].Tools)
	}
	last := client.requests[1].Messages[len(client.requests[1].Messages)-1]
	if last.Role != model.RoleTool || last.Content != "go.mod" {
		t.Fatalf("expected tool result in second request, got %#v",
			last)
	}

	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("expected five session events, got %d", len(events))
	}
	if events[3].Type != session.EventToolMessage {
		t.Fatalf("expected tool message event, got %q", events[3].Type)
	}
}

// TestRunTurnFeedsToolErrorsBackToModel verifies that ordinary tool failures
// are persisted as tool results so the model can recover.
func TestRunTurnFeedsToolErrorsBackToModel(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "hello.md"), "hello\n")

	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:   "call_1",
						Name: tool.NameEdit,
						Arguments: `{"path":"hello.md","edits":[` +
							`{"oldText":"","newText":` +
							`"hello world\n"}]}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "I need a non-empty oldText.",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}

	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "edit hello.md",
		SessionDir: t.TempDir(),
		CWD:        dir,
		Model:      client,
		Tools:      tool.DefaultRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssistantText != "I need a non-empty oldText." {
		t.Fatalf("unexpected assistant text: %q", result.AssistantText)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected two model requests, got %d",
			len(client.requests))
	}
	last := client.requests[1].Messages[len(client.requests[1].Messages)-1]
	if last.Role != model.RoleTool {
		t.Fatalf("expected tool error message, got %#v", last)
	}
	if last.Content != "tool error: edit 1 oldText must not be empty" {
		t.Fatalf("unexpected tool error content: %q", last.Content)
	}
}

// TestRunTurnRequiresFinalAssistantResponse verifies that exhausting tool
// rounds after tool calls does not return an empty successful turn.
func TestRunTurnRequiresFinalAssistantResponse(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "")

	scripts := make([][]model.Event, 0, maxToolRounds)
	for i := 0; i < maxToolRounds; i++ {
		scripts = append(scripts, []model.Event{
			{
				Type: model.EventToolCall,
				ToolCall: model.ToolCall{
					ID:   "call_1",
					Name: tool.NameLS,
					Arguments: `{"path":` +
						quoteJSON(dir) +
						`}`,
				},
			},
			{
				Type: model.EventDone,
			},
		})
	}

	_, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "keep using tools",
		SessionDir: t.TempDir(),
		CWD:        dir,
		Model:      &scriptedToolClient{events: scripts},
		Tools:      tool.DefaultRegistry(),
	})
	if err == nil {
		t.Fatal("expected tool call limit error")
	}
}

// hasToolSpec reports whether a request advertised the named tool.
func hasToolSpec(specs []model.ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Name == name {
			return true
		}
	}

	return false
}

// scriptedToolClient returns predetermined event streams and records requests.
type scriptedToolClient struct {
	// events stores one event script per model request.
	events [][]model.Event

	// requests stores the model requests received by the fake client.
	requests []model.Request
}

// Stream returns the next scripted event stream.
func (c *scriptedToolClient) Stream(ctx context.Context, req model.Request) (
	<-chan model.Event, error) {

	c.requests = append(c.requests, req)
	events := make(chan model.Event, len(c.events[0]))
	for _, event := range c.events[0] {
		events <- event
	}
	close(events)
	c.events = c.events[1:]

	return events, nil
}

// quoteJSON returns a JSON string literal for test tool arguments.
func quoteJSON(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}

	return string(encoded)
}

// writeFile creates a small file fixture for core tests.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
