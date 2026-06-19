package render

import (
	"strings"
	"testing"

	"harness/internal/session"
)

// TestMessageLinesRendersEditCall verifies that edit calls are shown as
// readable actions rather than raw assistant tool-call JSON.
func TestMessageLinesRendersEditCall(t *testing.T) {
	lines := MessageLines(session.MessageData{
		Role: session.RoleAssistant,
		ToolCalls: []session.ToolCallData{{
			ID: "call_1", Name: "edit",
			Arguments: `{"path":"hello.md","edits":[{},{}]}`,
		}},
	})
	got := strings.Join(lines, "\n")
	want := "-> edit hello.md (2 replacements)"
	if got != want {
		t.Fatalf("render mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestToolResultLinesRendersEditDiff verifies that edit results keep the diff
// visible for humans.
func TestToolResultLinesRendersEditDiff(t *testing.T) {
	lines := ToolResultLines(
		"edit", "Successfully applied 1 edit.\n\n--- hello.md\n+++ "+
			"hello.md\n@@\n-old\n+new\n",
	)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "   --- hello.md") {
		t.Fatalf("missing diff header: %q", got)
	}
	if !strings.Contains(got, "   +new") {
		t.Fatalf("missing diff insertion: %q", got)
	}
}

// TestToolResultLinesSummarizesRead verifies that read output does not flood
// human transcripts by default.
func TestToolResultLinesSummarizesRead(t *testing.T) {
	lines := ToolResultLines(
		"read",
		"one\ntwo\n\n[3 more lines in file. Use offset=3 to continue.]",
	)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "read 4 lines") {
		t.Fatalf("missing read summary: %q", got)
	}
	if !strings.Contains(got, "offset=3") {
		t.Fatalf("missing continuation hint: %q", got)
	}
}

// TestToolCallLinesFallsBackToRawArguments verifies future plugin tools still
// render with useful raw argument details.
func TestToolCallLinesFallsBackToRawArguments(t *testing.T) {
	lines := ToolCallLines(session.ToolCallData{
		Name:      "plugin_tool",
		Arguments: `{"value":1}`,
	})
	got := strings.Join(lines, "\n")
	want := `-> plugin_tool {"value":1}`
	if got != want {
		t.Fatalf("render mismatch:\nwant %q\ngot  %q", want, got)
	}
}
