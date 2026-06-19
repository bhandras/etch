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

// TestFormatProjectContextReportsPinnedInputs verifies context stats include
// the durable project layer kept outside compaction.
func TestFormatProjectContextReportsPinnedInputs(t *testing.T) {
	project := ProjectContext{
		InstructionFiles: []InstructionFile{{
			Path: "AGENTS.md",
			Text: "repo rules",
		}},
		Skills: []Skill{{
			Name:        "go-style",
			Description: "Use for Go edits.",
			Path:        ".harness/skills/go-style/SKILL.md",
		}},
	}

	text := FormatProjectContext(project)
	if !strings.Contains(text, "pinned instruction files: 1") {
		t.Fatalf("missing instruction count: %q", text)
	}
	if !strings.Contains(text, "available skills: 1") {
		t.Fatalf("missing skill count: %q", text)
	}
	if !strings.Contains(text, "go-style: Use for Go edits.") {
		t.Fatalf("missing skill summary: %q", text)
	}
}
