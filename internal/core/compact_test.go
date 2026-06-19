package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harness/internal/model"
	"harness/internal/prompt"
	"harness/internal/session"
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

// staticSummaryClient returns one fixed compaction summary.
type staticSummaryClient struct {
	// summary is the text emitted by the fake model.
	summary string
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
