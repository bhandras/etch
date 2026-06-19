package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Status summarizes durable activity recorded in a session log.
type Status struct {
	// StartedAt is the timestamp of the first recorded session event.
	StartedAt time.Time

	// LastEventAt is the timestamp of the newest recorded session event.
	LastEventAt time.Time

	// Age is the elapsed wall time since the session started.
	Age time.Duration

	// EventCount is the total number of JSONL events in the session.
	EventCount int

	// UserTurns is the number of user message events in the session.
	UserTurns int

	// ModelCalls is the number of assistant message events, which maps to
	// completed model passes in the current harness event shape.
	ModelCalls int

	// ToolCalls is the number of tool calls requested by assistant events.
	ToolCalls int

	// ToolResults is the number of tool result messages recorded.
	ToolResults int

	// Compactions is the number of context summary events recorded.
	Compactions int

	// MessageBytes is the total byte length of model-visible message text.
	MessageBytes int

	// SummaryBytes is the total byte length of compacted summaries.
	SummaryBytes int
}

// BuildStatus computes session activity counters from durable events.
func BuildStatus(events []Event, now time.Time) (Status, error) {
	var status Status
	status.EventCount = len(events)
	if len(events) == 0 {
		return status, nil
	}

	status.StartedAt = events[0].Time
	status.LastEventAt = events[len(events)-1].Time
	status.Age = now.Sub(status.StartedAt)
	if status.Age < 0 {
		status.Age = status.LastEventAt.Sub(status.StartedAt)
	}

	for _, event := range events {
		switch event.Type {
		case EventUserMessage:
			message, err := decodeStatusMessage(event)
			if err != nil {
				return Status{}, err
			}
			status.UserTurns++
			status.MessageBytes += len(statusMessageText(message))

		case EventAssistantMessage:
			message, err := decodeStatusMessage(event)
			if err != nil {
				return Status{}, err
			}
			status.ModelCalls++
			status.ToolCalls += len(message.ToolCalls)
			status.MessageBytes += len(statusMessageText(message))

		case EventToolMessage:
			message, err := decodeStatusMessage(event)
			if err != nil {
				return Status{}, err
			}
			status.ToolResults++
			status.MessageBytes += len(statusMessageText(message))

		case EventContextSummary:
			summary, err := decodeStatusSummary(event)
			if err != nil {
				return Status{}, err
			}
			status.Compactions++
			status.SummaryBytes += len(summary.Summary)
		}
	}

	return status, nil
}

// FormatStatus returns a compact human-readable session status report.
func FormatStatus(status Status) string {
	var out strings.Builder
	fmt.Fprintf(&out, "session age: %s\n", FormatDuration(status.Age))
	if !status.StartedAt.IsZero() {
		fmt.Fprintf(
			&out, "started: %s\n",
			status.StartedAt.Format(time.RFC3339),
		)
	}
	if !status.LastEventAt.IsZero() {
		fmt.Fprintf(
			&out, "last event: %s\n",
			status.LastEventAt.Format(time.RFC3339),
		)
	}
	fmt.Fprintf(&out, "events: %d\n", status.EventCount)
	fmt.Fprintf(&out, "turns: %d\n", status.UserTurns)
	fmt.Fprintf(&out, "model calls: %d\n", status.ModelCalls)
	fmt.Fprintf(
		&out, "tool calls: %d requested, %d results\n",
		status.ToolCalls, status.ToolResults,
	)
	fmt.Fprintf(&out, "compactions: %d\n", status.Compactions)
	fmt.Fprintf(&out, "message text: %d bytes\n", status.MessageBytes)
	fmt.Fprintf(&out, "summary text: %d bytes\n", status.SummaryBytes)
	fmt.Fprint(&out, "actual model usage: not recorded yet")

	return out.String()
}

// FormatDuration renders a compact approximate wall-clock duration.
func FormatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	seconds := int(duration.Round(time.Second).Seconds())
	if seconds < 1 {
		return "<1s"
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60

	return fmt.Sprintf("%dh %dm", hours, minutes)
}

// decodeStatusMessage decodes a message event for status counters.
func decodeStatusMessage(event Event) (MessageData, error) {
	var message MessageData
	if err := json.Unmarshal(event.Data, &message); err != nil {
		return MessageData{}, fmt.Errorf("decode message %s: %w",
			event.ID, err)
	}

	return message, nil
}

// decodeStatusSummary decodes a summary event for status counters.
func decodeStatusSummary(event Event) (SummaryData, error) {
	var summary SummaryData
	if err := json.Unmarshal(event.Data, &summary); err != nil {
		return SummaryData{}, fmt.Errorf("decode summary %s: %w",
			event.ID, err)
	}

	return summary, nil
}

// statusMessageText joins text content parts for session status counters.
func statusMessageText(message MessageData) string {
	var text string
	for _, part := range message.Content {
		if part.Type == ContentText {
			text += part.Text
		}
	}

	return text
}
