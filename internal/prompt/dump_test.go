package prompt

import (
	"strings"
	"testing"
	"time"

	"etch/internal/model"
	"etch/internal/session"
)

// TestDumpTextRendersLayeredContext verifies the debug dump exposes project
// layers, compacted summary text, replayed messages, and tool schemas.
func TestDumpTextRendersLayeredContext(t *testing.T) {
	old := messageEvent(
		t, "1", session.EventUserMessage,
		session.TextMessage(session.RoleUser, "old prompt"),
	)
	kept := messageEvent(
		t, "2", session.EventUserMessage,
		session.TextMessage(session.RoleUser, "current prompt"),
	)
	summary := summaryEvent(t, "3", session.SummaryData{
		Summary:          "older work summary",
		RangeStartID:     old.ID,
		RangeEndID:       old.ID,
		FirstKeptEventID: kept.ID,
	})

	text, err := DumpText(DumpRequest{
		CreatedAt:   time.Date(2026, 6, 24, 8, 45, 0, 0, time.UTC),
		CWD:         "/tmp/project",
		SessionPath: "/tmp/session.jsonl",
		ModelName:   "gpt-test",
		Events:      []session.Event{old, kept, summary},
		Project: ProjectContext{
			SystemText:       BaseSystemPrompt,
			ConfigPrompt:     "prefer go_inspect",
			ConfigPromptPath: "/tmp/project/prompt.md",
			SystemFiles: []InstructionFile{{
				Path: "SYSTEM.md",
				Text: "system layer",
			}},
			InstructionFiles: []InstructionFile{{
				Path: "AGENTS.md",
				Text: "agent layer",
			}},
			Skills: []Skill{{
				Name:        "go-style",
				Description: "Use for Go edits.",
				Path:        ".etch/skills/go-style/SKILL.md",
			}},
		},
		Tools: []model.ToolSpec{{
			Name:        "read",
			Description: "Read files.",
			Parameters:  []byte(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"etch Context Dump",
		"created: 2026-06-24T08:45:00Z",
		"===== Base Prompt =====",
		"===== Config System Prompt (/tmp/project/prompt.md) =====",
		"prefer go_inspect",
		"===== Project System Prompt (SYSTEM.md) =====",
		"===== Project Instructions (AGENTS.md) =====",
		"===== Skill Catalog =====",
		"===== Conversation Summary =====",
		"older work summary",
		"===== Conversation Replay =====",
		"current prompt",
		"----- Tool: read -----",
		`{"type":"object"}`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in dump:\n%s", want, text)
		}
	}
	if strings.Contains(text, "old prompt") {
		t.Fatalf("summarized prompt leaked into replay:\n%s", text)
	}
}
