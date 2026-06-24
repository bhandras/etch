package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"etch/internal/textutil"
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

	// Metrics is the total provider transport and request-shape metadata
	// recorded so far.
	Metrics MetricsData

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
	metricModelCalls := 0
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

		case EventModelMetrics:
			metrics, err := decodeStatusMetrics(event)
			if err != nil {
				return Status{}, err
			}
			status.Metrics = status.Metrics.Add(metrics)
			requests := metrics.Requests
			if requests == 0 && !metrics.Empty() {
				requests = 1
			}
			metricModelCalls += requests
		}
	}
	if metricModelCalls > 0 {
		status.ModelCalls = metricModelCalls
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
	fmt.Fprintf(
		&out, "- events: %s\n", textutil.FormatCount(status.EventCount),
	)
	fmt.Fprintf(
		&out, "- turns: %s\n", textutil.FormatCount(status.UserTurns),
	)
	fmt.Fprintf(
		&out, "- model calls: %s\n",
		textutil.FormatCount(status.ModelCalls),
	)
	fmt.Fprintf(
		&out, "- tool calls: %s requested, %s results\n",
		textutil.FormatCount(status.ToolCalls),
		textutil.FormatCount(status.ToolResults),
	)
	fmt.Fprintf(
		&out, "- tool batches: %s (largest %s)\n",
		textutil.FormatCount(status.ToolBatches),
		textutil.FormatCount(status.LargestToolBatch),
	)
	fmt.Fprintf(
		&out, "- compactions: %s (%s auto, %s manual)\n",
		textutil.FormatCount(status.Compactions),
		textutil.FormatCount(status.AutoCompactions),
		textutil.FormatCount(status.ManualCompactions),
	)

	fmt.Fprintf(&out, "\nStored Text\n")
	fmt.Fprintf(
		&out, "- messages: %s bytes\n",
		textutil.FormatCount(status.MessageBytes),
	)
	fmt.Fprintf(
		&out, "- summaries: %s bytes\n",
		textutil.FormatCount(status.SummaryBytes),
	)

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
			&out, "- input: %s tokens\n",
			textutil.FormatCount(status.Usage.InputTokens),
		)
		fmt.Fprintf(
			&out, "- cached input: %s tokens\n",
			textutil.FormatCount(status.Usage.CachedInputTokens),
		)
		fmt.Fprintf(
			&out, "- output: %s tokens\n",
			textutil.FormatCount(status.Usage.OutputTokens),
		)
		fmt.Fprintf(
			&out, "- reasoning output: %s tokens\n",
			textutil.FormatCount(
				status.Usage.ReasoningOutputTokens,
			),
		)
		fmt.Fprintf(
			&out, "- total: %s tokens",
			textutil.FormatCount(status.Usage.TotalTokens),
		)
	}
	if !status.Metrics.Empty() {
		fmt.Fprintf(&out, "\n\nRecorded Transport\n")
		if status.Metrics.Transport != "" {
			fmt.Fprintf(
				&out, "- latest transport: %s\n",
				status.Metrics.Transport,
			)
		}
		fmt.Fprintf(
			&out, "- requests: %s (%s continuation attempts, %s "+
				"fallbacks)\n",
			textutil.FormatCount(status.Metrics.Requests),
			textutil.FormatCount(
				status.Metrics.ContinuationRequests,
			),
			textutil.FormatCount(
				status.Metrics.ContinuationFallbacks,
			),
		)
		if status.Metrics.WebSocketConnections > 0 ||
			status.Metrics.WebSocketReuses > 0 {

			fmt.Fprintf(
				&out, "- websocket: %s connections, %s "+
					"reuses\n", textutil.FormatCount(
					status.Metrics.WebSocketConnections,
				),
				textutil.FormatCount(
					status.Metrics.WebSocketReuses,
				),
			)
		}
		if status.Metrics.ContinuationFallbackError != "" {
			fmt.Fprintf(
				&out, "- last continuation fallback: HTTP "+
					"%d, %s\n",
				status.Metrics.ContinuationFallbackStatus,
				status.Metrics.ContinuationFallbackError,
			)
		}
		fmt.Fprintf(
			&out, "- bytes: %s up, %s down\n",
			textutil.FormatBytes(status.Metrics.RequestBytes),
			textutil.FormatBytes(status.Metrics.ResponseBytes),
		)
		fmt.Fprintf(
			&out, "- averages: %s up/request, %s down/request\n",
			textutil.FormatBytes(
				averageInt(
					status.Metrics.RequestBytes,
					status.Metrics.Requests,
				),
			),
			textutil.FormatBytes(
				averageInt(
					status.Metrics.ResponseBytes,
					status.Metrics.Requests,
				),
			),
		)
		fmt.Fprintf(
			&out, "- request shape: %s input messages, %s delta "+
				"messages, %s tools",
			textutil.FormatCount(status.Metrics.InputMessages),
			textutil.FormatCount(status.Metrics.DeltaMessages),
			textutil.FormatCount(status.Metrics.ToolCount),
		)
	}

	return out.String()
}

// averageInt returns a rounded integer average while tolerating zero counts.
func averageInt(total int, count int) int {
	if count <= 0 {
		return 0
	}

	return (total + count/2) / count
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

// decodeStatusMetrics decodes a model metrics event for status counters.
func decodeStatusMetrics(event Event) (MetricsData, error) {
	var metrics MetricsData
	if err := json.Unmarshal(event.Data, &metrics); err != nil {
		return MetricsData{}, fmt.Errorf("decode metrics %s: %w",
			event.ID, err)
	}

	return metrics, nil
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
