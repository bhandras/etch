package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"harness/internal/model"
	"harness/internal/session"
)

const (
	// DefaultCompactKeepMessages is the number of latest message events
	// kept raw after manual compaction.
	DefaultCompactKeepMessages = 12

	// compactToolResultLimit caps serialized tool results in summary
	// prompts.
	compactToolResultLimit = 2048
)

// CompactRequest contains everything needed to append a session summary.
type CompactRequest struct {
	// SessionPath is the JSONL session log to compact.
	SessionPath string

	// Model is the provider-neutral client used to summarize older history.
	Model model.Client

	// KeepMessages is the number of latest message events to keep raw.
	KeepMessages int

	// ModelName records the summarization model name in the summary event.
	ModelName string
}

// CompactResult reports the summary event appended by compaction.
type CompactResult struct {
	// SessionPath is the compacted JSONL session log.
	SessionPath string

	// SummaryEventID is the appended context.summary event identifier.
	SummaryEventID string

	// FirstKeptEventID is the first raw event retained after the summary.
	FirstKeptEventID string

	// Summary is the model-written checkpoint.
	Summary string
}

// CompactSession summarizes older session history and appends a summary event.
func CompactSession(ctx context.Context,
	req CompactRequest) (*CompactResult, error) {

	if strings.TrimSpace(req.SessionPath) == "" {
		return nil, fmt.Errorf("session path must not be empty")
	}
	if req.Model == nil {
		return nil, fmt.Errorf("model client must not be nil")
	}
	keep := req.KeepMessages
	if keep <= 0 {
		keep = DefaultCompactKeepMessages
	}

	store, events, err := session.Open(req.SessionPath)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	cut, firstKeptID, err := compactionCut(events, keep)
	if err != nil {
		return nil, err
	}
	if cut == 0 {
		return nil, fmt.Errorf("not enough history to compact")
	}

	summary, err := summarizeEvents(ctx, req.Model, events[:cut])
	if err != nil {
		return nil, err
	}
	event, err := store.Append(
		session.EventContextSummary, store.LastID(),
		session.SummaryData{
			Summary:          summary,
			RangeStartID:     events[0].ID,
			RangeEndID:       events[cut-1].ID,
			FirstKeptEventID: firstKeptID,
			Model:            req.ModelName,
		},
	)
	if err != nil {
		return nil, err
	}

	return &CompactResult{
		SessionPath:      req.SessionPath,
		SummaryEventID:   event.ID,
		FirstKeptEventID: firstKeptID,
		Summary:          summary,
	}, nil
}

// compactionCut returns the first event index retained after compaction.
func compactionCut(events []session.Event, keepMessages int) (int, string,
	error) {

	messageIndexes := make([]int, 0, len(events))
	for i, event := range events {
		if compactMessageEvent(event.Type) {
			messageIndexes = append(messageIndexes, i)
		}
	}
	if len(messageIndexes) <= keepMessages {
		return 0, "", nil
	}

	keepStartMessage := messageIndexes[len(messageIndexes)-keepMessages]
	if keepStartMessage == 0 {
		return 0, "", nil
	}

	return keepStartMessage, events[keepStartMessage].ID, nil
}

// compactMessageEvent reports whether an event should count toward raw
// recency retention.
func compactMessageEvent(eventType string) bool {
	return eventType == session.EventUserMessage ||
		eventType == session.EventAssistantMessage ||
		eventType == session.EventToolMessage
}

// summarizeEvents asks the model to summarize serialized session events.
func summarizeEvents(ctx context.Context, client model.Client,
	events []session.Event) (string, error) {

	stream, err := client.Stream(ctx, model.Request{
		Messages: []model.Message{
			{
				Role: model.RoleSystem,
				Content: "Summarize older coding-agent session history " +
					"as a concise checkpoint. Use sections: Goal, " +
					"Constraints and Preferences, Progress, Key " +
					"Decisions, Next Steps, Critical Context.",
			},
			{
				Role:    model.RoleUser,
				Content: serializeEventsForSummary(events),
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("start compaction model stream: %w", err)
	}

	response, err := collectStream(ctx, stream)
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(response.Text)
	if summary == "" {
		return "", fmt.Errorf("compaction summary was empty")
	}

	return summary, nil
}

// serializeEventsForSummary converts session events into a compact transcript.
func serializeEventsForSummary(events []session.Event) string {
	var out strings.Builder
	for _, event := range events {
		if !compactMessageEvent(event.Type) {
			continue
		}

		var message session.MessageData
		if err := json.Unmarshal(event.Data, &message); err != nil {
			continue
		}
		switch message.Role {
		case session.RoleUser:
			writeSummaryLine(
				&out, "User", summaryMessageText(message),
			)

		case session.RoleAssistant:
			if len(message.ToolCalls) > 0 {
				for _, call := range message.ToolCalls {
					writeSummaryLine(
						&out, "Assistant tool call",
						call.Name+" "+call.Arguments,
					)
				}
			} else {
				writeSummaryLine(
					&out, "Assistant",
					summaryMessageText(message),
				)
			}

		case session.RoleTool:
			writeSummaryLine(
				&out, "Tool "+message.Name,
				limitSummaryText(
					summaryMessageText(message),
				),
			)
		}
	}

	return out.String()
}

// summaryMessageText joins text parts from a session message.
func summaryMessageText(message session.MessageData) string {
	var text string
	for _, part := range message.Content {
		if part.Type == session.ContentText {
			text += part.Text
		}
	}

	return text
}

// writeSummaryLine appends one labelled transcript line.
func writeSummaryLine(out *strings.Builder, label string, text string) {
	fmt.Fprintf(out, "[%s]: %s\n", label, strings.TrimSpace(text))
}

// limitSummaryText caps large tool results before summarization.
func limitSummaryText(text string) string {
	if len(text) <= compactToolResultLimit {
		return text
	}

	return text[:compactToolResultLimit] + "\n[truncated]"
}
