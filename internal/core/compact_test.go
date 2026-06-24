package core

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"etch/internal/model"
	"etch/internal/prompt"
	"etch/internal/session"
	"etch/internal/tool"
)

// TestCompactSessionAppendsSummary verifies that manual compaction stores a
// durable append-only summary event.
func TestCompactSessionAppendsSummary(t *testing.T) {
	first, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "one",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      model.EchoClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RunTurn(context.Background(), TurnRequest{
		Prompt:      "two",
		SessionPath: first.SessionPath,
		CWD:         "/work/project",
		Model:       model.EchoClient{},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := CompactSession(context.Background(), CompactRequest{
		SessionPath:  second.SessionPath,
		Model:        staticSummaryClient{summary: "Goal: keep going"},
		KeepMessages: 2,
		ModelName:    "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "Goal: keep going" {
		t.Fatalf("unexpected summary: %q", result.Summary)
	}

	events, err := session.ReadAll(second.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Type != session.EventContextSummary {
		t.Fatalf("expected summary event, got %q", last.Type)
	}

	var data session.SummaryData
	if err := json.Unmarshal(last.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Model != "test-model" {
		t.Fatalf("unexpected model: %q", data.Model)
	}

	messages, err := prompt.BuildHistoryMessages(prompt.HistoryRequest{
		Events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(messages[0].Content, "Goal: keep going") {
		t.Fatalf("summary missing from context: %#v", messages)
	}
}

// TestCompactSessionUpdatesPreviousSummary verifies repeated compaction uses
// the prior checkpoint as explicit update context.
func TestCompactSessionUpdatesPreviousSummary(t *testing.T) {
	first, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     strings.Repeat("alpha ", 40),
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      model.EchoClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RunTurn(context.Background(), TurnRequest{
		Prompt:      strings.Repeat("beta ", 40),
		SessionPath: first.SessionPath,
		CWD:         "/work/project",
		Model:       model.EchoClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &recordingSummaryClient{summary: "updated summary"}
	result, err := CompactSession(context.Background(), CompactRequest{
		SessionPath:      second.SessionPath,
		Model:            client,
		KeepRecentTokens: 20,
		ModelName:        "test-model",
		Instructions:     "preserve beta details",
	})
	if err != nil {
		t.Fatal(err)
	}

	third, err := RunTurn(context.Background(), TurnRequest{
		Prompt:      strings.Repeat("gamma ", 40),
		SessionPath: result.SessionPath,
		CWD:         "/work/project",
		Model:       model.EchoClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	client.summary = "second update"
	_, err = CompactSession(context.Background(), CompactRequest{
		SessionPath:      third.SessionPath,
		Model:            client,
		KeepRecentTokens: 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	last := client.requests[len(client.requests)-1].Messages[1].Content
	if !strings.Contains(last, "<previous-summary>") ||
		!strings.Contains(last, "updated summary") {

		t.Fatalf("missing previous summary update context: %q", last)
	}
	if !strings.Contains(
		client.requests[0].Messages[1].Content,
		"Additional focus: preserve beta details",
	) {

		t.Fatalf("missing compact instructions: %q",
			client.requests[0].Messages[1].Content)
	}
}

// TestCompactSessionTracksFileOperations verifies compaction stores file lists
// derived from read and mutation tool calls.
func TestCompactSessionTracksFileOperations(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.txt"), "hello\n")
	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:        "call_read",
						Name:      "read",
						Arguments: `{"path":"note.txt"}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:   "call_edit",
						Name: "edit",
						Arguments: `{"path":"note.txt","edits":[` +
							`{"oldText":"hello","newText":"hi"}]}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "done",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}
	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:        "inspect and edit",
		SessionDir:    filepath.Join(dir, "sessions"),
		CWD:           dir,
		Model:         client,
		Tools:         tool.DefaultRegistry(),
		MaxToolRounds: 4,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = CompactSession(context.Background(), CompactRequest{
		SessionPath:      result.SessionPath,
		Model:            staticSummaryClient{summary: "summary"},
		KeepRecentTokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var data session.SummaryData
	if err := json.Unmarshal(
		events[len(events)-1].Data, &data,
	); err != nil {

		t.Fatal(err)
	}
	if len(data.ReadFiles) != 0 ||
		len(data.ModifiedFiles) != 1 ||
		data.ModifiedFiles[0] != "note.txt" {

		t.Fatalf("unexpected file metadata: %#v", data)
	}
	if !strings.Contains(data.Summary, "## Files") ||
		!strings.Contains(data.Summary, "note.txt") {

		t.Fatalf("missing file summary block: %q", data.Summary)
	}
}

// staticSummaryClient returns one fixed compaction summary.
type staticSummaryClient struct {
	// summary is the text emitted by the fake model.
	summary string
}

// recordingSummaryClient records summary requests and emits configurable text.
type recordingSummaryClient struct {
	// summary is the text emitted by the fake model.
	summary string

	// requests stores all summarization requests in call order.
	requests []model.Request
}

// Stream records the request and returns the configured summary text.
func (c *recordingSummaryClient) Stream(ctx context.Context,
	req model.Request) (<-chan model.Event, error) {

	c.requests = append(c.requests, req)
	events := make(chan model.Event, 2)
	events <- model.Event{Type: model.EventTextDelta, Text: c.summary}
	events <- model.Event{Type: model.EventDone}
	close(events)

	return events, nil
}

// Stream returns the configured summary as one model response.
func (c staticSummaryClient) Stream(ctx context.Context, req model.Request) (
	<-chan model.Event, error) {

	events := make(chan model.Event, 2)
	events <- model.Event{Type: model.EventTextDelta, Text: c.summary}
	events <- model.Event{Type: model.EventDone}
	close(events)

	return events, nil
}
