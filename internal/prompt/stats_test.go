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
	if !strings.Contains(text, "approx context:") {
		t.Fatalf("missing context total: %q", text)
	}
	if !strings.Contains(text, "tokens") {
		t.Fatalf("missing token estimate: %q", text)
	}
	if stats.ApproxContextTokens == 0 {
		t.Fatal("expected context token estimate")
	}
	if stats.RawReplayTokens == 0 {
		t.Fatal("expected raw replay token estimate")
	}
}

// TestFormatProjectContextReportsPinnedInputs verifies context stats include
// the durable project layer kept outside compaction.
func TestFormatProjectContextReportsPinnedInputs(t *testing.T) {
	project := ProjectContext{
		SystemFiles: []InstructionFile{{
			Path: "SYSTEM.md",
			Text: "agent identity",
		}},
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
	if !strings.Contains(text, "base prompt:") {
		t.Fatalf("missing base prompt layer: %q", text)
	}
	if !strings.Contains(text, "pinned system files: 1") {
		t.Fatalf("missing system count: %q", text)
	}
	if !strings.Contains(text, "pinned instruction files: 1") {
		t.Fatalf("missing instruction count: %q", text)
	}
	if !strings.Contains(text, "Available Skills") ||
		!strings.Contains(text, "- count: 1") {

		t.Fatalf("missing skill count: %q", text)
	}
	if !strings.Contains(text, "skill catalog:") {
		t.Fatalf("missing skill catalog layer: %q", text)
	}
	if !strings.Contains(text, "tokens") {
		t.Fatalf("missing token estimates: %q", text)
	}
	if !strings.Contains(text, "go-style: Use for Go edits.") {
		t.Fatalf("missing skill summary: %q", text)
	}
}

// TestApproxTokensEstimatesText verifies the stdlib token estimator produces a
// stable, nonzero approximation for mixed prose and punctuation.
func TestApproxTokensEstimatesText(t *testing.T) {
	got := ApproxTokens("hello, world")
	if got != 5 {
		t.Fatalf("unexpected estimate: %d", got)
	}
	if ApproxTokens("") != 0 {
		t.Fatal("empty text should not consume tokens")
	}
}
