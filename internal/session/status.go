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

	// ToolBatches is the number of assistant messages that requested at
	// least one tool call.
	ToolBatches int

	// LargestToolBatch is the largest number of tools requested by one
	// assistant message.
	LargestToolBatch int

	// Compactions is the number of context summary events recorded.
	Compactions int

	// AutoCompactions is the number of summaries triggered automatically.
	AutoCompactions int

	// ManualCompactions is the number of summaries triggered explicitly or
	// recorded before trigger metadata existed.
	ManualCompactions int

	// MessageBytes is the total byte length of model-visible message text.
	MessageBytes int

	// SummaryBytes is the total byte length of compacted summaries.
	SummaryBytes int

	// Usage is the total provider-reported model usage recorded so far.
	Usage UsageData

	// ModelWait is the approximate time between the previous event and
	// assistant messages.
	ModelWait time.Duration

	// ToolWait is the approximate time between the previous event and tool
	// result messages.
	ToolWait time.Duration
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

	previousTime := events[0].Time
	for i, event := range events {
		previousGap := time.Duration(0)
		if i > 0 {
			previousGap = event.Time.Sub(previousTime)
			if previousGap < 0 {
				previousGap = 0
			}
		}
		previousTime = event.Time
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
			if len(message.ToolCalls) > 0 {
				status.ToolBatches++
				if len(message.ToolCalls) > status.LargestToolBatch {
					status.LargestToolBatch = len(
						message.ToolCalls,
					)
				}
			}
			status.MessageBytes += len(statusMessageText(message))
			status.ModelWait += previousGap

		case EventToolMessage:
			message, err := decodeStatusMessage(event)
			if err != nil {
				return Status{}, err
			}
			status.ToolResults++
			status.MessageBytes += len(statusMessageText(message))
			status.ToolWait += previousGap

		case EventContextSummary:
			summary, err := decodeStatusSummary(event)
			if err != nil {
				return Status{}, err
			}
			status.Compactions++
			if summary.Trigger == "auto" {
				status.AutoCompactions++
			} else {
				status.ManualCompactions++
			}
			status.SummaryBytes += len(summary.Summary)

		case EventModelUsage:
			usage, err := decodeStatusUsage(event)
			if err != nil {
				return Status{}, err
			}
			status.Usage = status.Usage.Add(usage)
		}
	}

	return status, nil
}

// FormatStatus returns a compact human-readable session status report.
func FormatStatus(status Status) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Session\n")
	fmt.Fprintf(&out, "- age: %s\n", FormatDuration(status.Age))
	if !status.StartedAt.IsZero() {
		fmt.Fprintf(
			&out, "- started: %s\n",
			status.StartedAt.Format(time.RFC3339),
		)
	}
	if !status.LastEventAt.IsZero() {
		fmt.Fprintf(
			&out, "- last event: %s\n",
			status.LastEventAt.Format(time.RFC3339),
		)
	}

	fmt.Fprintf(&out, "\nActivity\n")
	fmt.Fprintf(&out, "- events: %d\n", status.EventCount)
	fmt.Fprintf(&out, "- turns: %d\n", status.UserTurns)
	fmt.Fprintf(&out, "- model calls: %d\n", status.ModelCalls)
	fmt.Fprintf(
		&out, "- tool calls: %d requested, %d results\n",
		status.ToolCalls, status.ToolResults,
	)
	fmt.Fprintf(
		&out, "- tool batches: %d (largest %d)\n", status.ToolBatches,
		status.LargestToolBatch,
	)
	fmt.Fprintf(
		&out, "- compactions: %d (%d auto, %d manual)\n",
		status.Compactions, status.AutoCompactions,
		status.ManualCompactions,
	)

	fmt.Fprintf(&out, "\nStored Text\n")
	fmt.Fprintf(&out, "- messages: %d bytes\n", status.MessageBytes)
	fmt.Fprintf(&out, "- summaries: %d bytes\n", status.SummaryBytes)

	fmt.Fprintf(&out, "\nRecorded Timing\n")
	fmt.Fprintf(
		&out, "- model wait: %s\n", FormatDuration(status.ModelWait),
	)
	fmt.Fprintf(
		&out, "- tool result wait: %s\n",
		FormatDuration(status.ToolWait),
	)

	fmt.Fprintf(&out, "\nActual Model Usage\n")
	if status.Usage.Empty() {
		fmt.Fprint(&out, "- not recorded yet")
	} else {
		fmt.Fprintf(
			&out, "- input: %d tokens\n", status.Usage.InputTokens,
		)
		fmt.Fprintf(
			&out, "- cached input: %d tokens\n",
			status.Usage.CachedInputTokens,
		)
		fmt.Fprintf(
			&out, "- output: %d tokens\n",
			status.Usage.OutputTokens,
		)
		fmt.Fprintf(
			&out, "- reasoning output: %d tokens\n",
			status.Usage.ReasoningOutputTokens,
		)
		fmt.Fprintf(
			&out, "- total: %d tokens", status.Usage.TotalTokens,
		)
	}

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

// decodeStatusUsage decodes a model usage event for status counters.
func decodeStatusUsage(event Event) (UsageData, error) {
	var usage UsageData
	if err := json.Unmarshal(event.Data, &usage); err != nil {
		return UsageData{}, fmt.Errorf("decode usage %s: %w", event.ID,
			err)
	}

	return usage, nil
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
