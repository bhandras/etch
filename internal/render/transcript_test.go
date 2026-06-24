package render

import (
	"strings"
	"testing"

	"etch/internal/session"
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

// TestToolCallLinesRendersSingleBatchedRead verifies a one-entry read batch
// renders like a normal ranged read instead of showing a missing path.
func TestToolCallLinesRendersSingleBatchedRead(t *testing.T) {
	lines := ToolCallLines(session.ToolCallData{
		Name: "read",
		Arguments: `{"files":[{"path":"internal/plugins/client.go",` +
			`"offset":49,"limit":20}]}`,
	})
	got := strings.Join(lines, "\n")
	want := "-> read internal/plugins/client.go lines 49-68"
	if got != want {
		t.Fatalf("render mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestToolCallLinesRendersBatchedReadSummary verifies multi-file read batches
// show a compact file list for live subagent progress rows.
func TestToolCallLinesRendersBatchedReadSummary(t *testing.T) {
	lines := ToolCallLines(session.ToolCallData{
		Name: "read",
		Arguments: `{"files":[{"path":"internal/plugins/client.go"},` +
			`{"path":"sdk/plugins.go"},{"path":"README.md"},` +
			`{"path":"docs/architecture.md"}]}`,
	})
	got := strings.Join(lines, "\n")
	want := "-> read 4 files: internal/plugins/client.go, " +
		"sdk/plugins.go, README.md, ... 1 more"
	if got != want {
		t.Fatalf("render mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestToolCallLinesRendersBatchedReadWithoutMissingPath verifies empty file
// entries do not leak a placeholder into otherwise useful batch labels.
func TestToolCallLinesRendersBatchedReadWithoutMissingPath(t *testing.T) {
	lines := ToolCallLines(session.ToolCallData{
		Name:      "read",
		Arguments: `{"files":[{"path":""},{"path":"README.md"}]}`,
	})
	got := strings.Join(lines, "\n")
	want := "-> read README.md"
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
