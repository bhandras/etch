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

	// SummaryTokens is the approximate token count of the active summary
	// text.
	SummaryTokens int

	// RawReplayEventCount is the number of events replayed after summary
	// selection.
	RawReplayEventCount int

	// RawReplayBytes is the approximate bytes in replayed message text.
	RawReplayBytes int

	// RawReplayTokens is the approximate tokens in replayed message text.
	RawReplayTokens int

	// ApproxContextBytes is the approximate bytes in projected model
	// message text.
	ApproxContextBytes int

	// ApproxContextTokens is the approximate tokens in projected model
	// message text.
	ApproxContextTokens int
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
		stats.SummaryTokens = ApproxTokens(summary.Summary)
		stats.SummaryEventID = summaryID(events)
	}

	messages, err := BuildHistoryMessages(HistoryRequest{
		Events:     events,
		SystemText: systemText,
	})
	if err != nil {
		return Stats{}, err
	}
	summaryContextIndex := -1
	if summary != nil {
		summaryContextIndex = 0
		if systemText != "" {
			summaryContextIndex = 1
		}
	}
	for i, message := range messages {
		stats.ApproxContextBytes += len(message.Content)
		stats.ApproxContextTokens += ApproxTokens(message.Content)
		if systemText != "" && i == 0 {
			continue
		}
		if i == summaryContextIndex {
			continue
		}
		stats.RawReplayBytes += len(message.Content)
		stats.RawReplayTokens += ApproxTokens(message.Content)
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
		summary = fmt.Sprintf("active (%d bytes, ~%d tokens, event %s)",
			stats.SummaryBytes, stats.SummaryTokens,
			stats.SummaryEventID)
	}

	return fmt.Sprintf("Session Projection\n"+
		"- events: %d\n"+
		"- message events: %d\n"+
		"- summary: %s\n"+
		"- raw replay events: %d\n"+
		"- raw replay text: %d bytes, ~%d tokens\n"+
		"- approx context: %d bytes, ~%d tokens",
		stats.EventCount, stats.MessageEventCount, summary,
		stats.RawReplayEventCount, stats.RawReplayBytes,
		stats.RawReplayTokens, stats.ApproxContextBytes,
		stats.ApproxContextTokens)
}

// FormatProjectContext returns a compact report for pinned project context.
func FormatProjectContext(project ProjectContext) string {
	baseBytes := len(BaseSystemPrompt)
	baseTokens := ApproxTokens(BaseSystemPrompt)
	systemBytes := 0
	for _, file := range project.SystemFiles {
		systemBytes += len(file.Text)
	}
	instructionBytes := 0
	for _, file := range project.InstructionFiles {
		instructionBytes += len(file.Text)
	}
	catalog := skillCatalogText(project.Skills)

	var out strings.Builder
	fmt.Fprintf(&out, "Pinned Context\n")
	fmt.Fprintf(
		&out, "- base prompt: %d bytes, ~%d tokens\n", baseBytes,
		baseTokens,
	)
	fmt.Fprintf(
		&out, "- pinned system files: %d (%d bytes, ~%d tokens)\n",
		len(project.SystemFiles), systemBytes, ApproxTokensForFiles(
			project.SystemFiles,
		),
	)
	fmt.Fprintf(
		&out, "- pinned instruction files: %d (%d bytes, ~%d tokens)\n",
		len(project.InstructionFiles), instructionBytes,
		ApproxTokensForFiles(project.InstructionFiles),
	)
	fmt.Fprintf(
		&out, "- skill catalog: %d bytes, ~%d tokens\n", len(catalog),
		ApproxTokens(catalog),
	)
	fmt.Fprintf(&out, "\nAvailable Skills\n")
	fmt.Fprintf(&out, "- count: %d", len(project.Skills))
	for _, skill := range project.Skills {
		fmt.Fprintf(
			&out, "\n- %s: %s\n  %s", skill.Name, skill.Description,
			skill.Path,
		)
	}

	return out.String()
}

// ApproxTokensForFiles estimates token usage for instruction file content.
func ApproxTokensForFiles(files []InstructionFile) int {
	var tokens int
	for _, file := range files {
		tokens += ApproxTokens(file.Text)
	}

	return tokens
}

// ApproxTokens estimates context size without a provider tokenizer.
//
// It is intentionally approximate and should only drive coarse context-budget
// decisions such as layer stats and automatic compaction thresholds.
func ApproxTokens(text string) int {
	if text == "" {
		return 0
	}

	var tokens int
	var runesInWord int
	flushWord := func() {
		if runesInWord == 0 {
			return
		}
		tokens += (runesInWord + 3) / 4
		runesInWord = 0
	}

	for _, r := range text {
		switch {
		case r == '_' || r == '-' || r >= '0' && r <= '9' ||
			r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z':

			runesInWord++

		case r == ' ' || r == '\n' || r == '\t' || r == '\r':
			flushWord()

		default:
			flushWord()
			tokens++
		}
	}
	flushWord()

	return tokens
}
