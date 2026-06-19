package prompt

import (
	"strings"
	"testing"

	"harness/internal/session"
)

// TestBuildStatsReportsActiveSummary verifies that context stats explain the
// active compacted projection.
func TestBuildStatsReportsActiveSummary(t *testing.T) {
	old := messageEvent(
		t, "1", session.EventUserMessage,
		session.TextMessage(session.RoleUser, "old"),
	)
	kept := messageEvent(
		t, "2", session.EventUserMessage,
		session.TextMessage(session.RoleUser, "recent"),
	)
	summary := summaryEvent(t, "3", session.SummaryData{
		Summary:          "old summary",
		RangeStartID:     old.ID,
		RangeEndID:       old.ID,
		FirstKeptEventID: kept.ID,
	})

	stats, err := BuildStats([]session.Event{old, kept, summary}, "system")
	if err != nil {
		t.Fatal(err)
	}
	if !stats.SummaryActive {
		t.Fatal("expected active summary")
	}
	if stats.RawReplayEventCount != 2 {
		t.Fatalf("unexpected raw replay count: %d",
			stats.RawReplayEventCount)
	}

	text := FormatStats(stats)
	if !strings.Contains(text, "summary: active") {
		t.Fatalf("missing active summary: %q", text)
	}
	if !strings.Contains(text, "approx context bytes:") {
		t.Fatalf("missing context bytes: %q", text)
	}
}
