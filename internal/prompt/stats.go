package prompt

import (
	"fmt"
	"strings"

	"harness/internal/session"
)

// Stats describes the current prompt-context projection for a session.
type Stats struct {
	// EventCount is the total number of events in the session log.
	EventCount int

	// MessageEventCount is the total number of message events in the log.
	MessageEventCount int

	// SummaryActive reports whether a compaction summary affects replay.
	SummaryActive bool

	// SummaryEventID is the latest summary event identifier when active.
	SummaryEventID string

	// SummaryBytes is the byte length of the active summary text.
	SummaryBytes int

	// RawReplayEventCount is the number of events replayed after summary
	// selection.
	RawReplayEventCount int

	// ApproxContextBytes is the approximate bytes in projected model
	// message text.
	ApproxContextBytes int
}

// BuildStats computes context projection statistics for session events.
func BuildStats(events []session.Event, systemText string) (Stats, error) {
	var stats Stats
	stats.EventCount = len(events)
	for _, event := range events {
		if isMessageEvent(event.Type) {
			stats.MessageEventCount++
		}
	}

	summary, start, err := latestSummary(events)
	if err != nil {
		return Stats{}, err
	}
	stats.RawReplayEventCount = len(events) - start
	if summary != nil {
		stats.SummaryActive = true
		stats.SummaryBytes = len(summary.Summary)
		stats.SummaryEventID = summaryID(events)
	}

	messages, err := BuildHistoryMessages(HistoryRequest{
		Events:     events,
		SystemText: systemText,
	})
	if err != nil {
		return Stats{}, err
	}
	for _, message := range messages {
		stats.ApproxContextBytes += len(message.Content)
	}

	return stats, nil
}

// summaryID returns the identifier of the latest context summary event.
func summaryID(events []session.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == session.EventContextSummary {
			return events[i].ID
		}
	}

	return ""
}

// FormatStats returns a compact human-readable context stats report.
func FormatStats(stats Stats) string {
	summary := "inactive"
	if stats.SummaryActive {
		summary = fmt.Sprintf("active (%d bytes, event %s)",
			stats.SummaryBytes, stats.SummaryEventID)
	}

	return fmt.Sprintf("events: %d\nmessage events: %d\nsummary: %s\n"+
		"raw replay events: %d\napprox context bytes: %d",
		stats.EventCount, stats.MessageEventCount, summary,
		stats.RawReplayEventCount, stats.ApproxContextBytes)
}

// FormatProjectContext returns a compact report for pinned project context.
func FormatProjectContext(project ProjectContext) string {
	instructionBytes := 0
	for _, file := range project.InstructionFiles {
		instructionBytes += len(file.Text)
	}

	var out strings.Builder
	fmt.Fprintf(
		&out, "pinned instruction files: %d (%d bytes)\n",
		len(project.InstructionFiles), instructionBytes,
	)
	fmt.Fprintf(&out, "available skills: %d", len(project.Skills))
	for _, skill := range project.Skills {
		fmt.Fprintf(
			&out, "\n- %s: %s (%s)", skill.Name, skill.Description,
			skill.Path,
		)
	}

	return out.String()
}
