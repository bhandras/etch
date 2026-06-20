package main

import (
	"bytes"
	"strings"
	"testing"

	"harness/internal/model"
	"harness/internal/session"
)

// TestCappedToolResultLinesLimitsVerboseOutput verifies live chat does not
// flood the terminal with full tool results.
func TestCappedToolResultLinesLimitsVerboseOutput(t *testing.T) {
	message := session.ToolMessage(
		"call_1", "grep",
		"one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n",
	)

	lines := cappedToolResultLines(message)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "one") || !strings.Contains(got, "six") {
		t.Fatalf("missing retained output: %q", got)
	}
	if strings.Contains(got, "seven") || strings.Contains(got, "eight") {
		t.Fatalf("output was not capped: %q", got)
	}
	if !strings.Contains(got, "... 2 more lines") {
		t.Fatalf("missing truncation notice: %q", got)
	}
}

// TestMarkdownLinesKeepsPlainOutputStable verifies non-terminal rendering keeps
// markdown text unstyled and predictable.
func TestMarkdownLinesKeepsPlainOutputStable(t *testing.T) {
	lines := markdownLines(
		"# Title\n\nThis is **bold**.\n```go\nx := 1\n```",
		terminalStyle{},
	)
	got := strings.Join(lines, "\n")
	want := "# Title\n\nThis is **bold**.\n```go\nx := 1\n```"
	if got != want {
		t.Fatalf("markdown mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestMarkdownLinesStylesTerminalOutput verifies the tiny markdown renderer
// handles headers, bold spans, and fenced code when ANSI is enabled.
func TestMarkdownLinesStylesTerminalOutput(t *testing.T) {
	lines := markdownLines(
		"# Title\nThis is **bold**.\n```go\nx := 1\n```",
		terminalStyle{
			enabled: true,
		},
	)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, ansiBold+"Title"+ansiReset) {
		t.Fatalf("missing styled header: %q", got)
	}
	if !strings.Contains(got, ansiBold+"bold"+ansiReset) {
		t.Fatalf("missing styled bold span: %q", got)
	}
	if strings.Contains(got, "```") {
		t.Fatalf("fence markers should not render: %q", got)
	}
	if !strings.Contains(got, ansiDim+"x := 1"+ansiReset) {
		t.Fatalf("missing muted code line: %q", got)
	}
}

// TestRenderReasoningStylesMarkdown verifies thinking blocks keep their muted
// tone while still rendering lightweight markdown.
func TestRenderReasoningStylesMarkdown(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
		style: terminalStyle{
			enabled: true,
		},
	}

	renderer.renderReasoning("**Analyzing** answer suggestions")

	got := stdout.String()
	if strings.Contains(got, "**") {
		t.Fatalf("reasoning kept markdown markers: %q", got)
	}
	if !strings.Contains(
		got, ansiDim+ansiItalic+"• "+ansiBold+
			"Analyzing"+ansiReset+ansiDim+ansiItalic,
	) {

		t.Fatalf("reasoning did not combine tone and markdown: %q", got)
	}
}

// TestRenderToolCallKeepsHeaderActive verifies tool call headers are not muted
// like reasoning summaries.
func TestRenderToolCallKeepsHeaderActive(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
		style: terminalStyle{
			enabled: true,
		},
	}

	renderer.renderToolCall(model.ToolCall{
		ID:        "call_1",
		Name:      "find",
		Arguments: `{"query":".go","path":"."}`,
	})

	got := stdout.String()
	if strings.Contains(got, ansiDim+"• Ran find") {
		t.Fatalf("tool call header was muted: %q", got)
	}
	if !strings.Contains(got, "• Ran find .go .") {
		t.Fatalf("missing active tool call header: %q", got)
	}
}

// TestRenderToolResultColorsDiffLines verifies live edit and write output uses
// conventional red and green coloring for unified diff changes.
func TestRenderToolResultColorsDiffLines(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
		style: terminalStyle{
			enabled: true,
		},
	}
	renderer.renderToolResult(
		session.ToolMessage(
			"call_1", "edit", "Updated.\n\n--- hello.md\n+++ "+
				"hello.md\n@@\n-old\n+new\n",
		),
	)

	got := stdout.String()
	if !strings.Contains(got, ansiRed+"   -old"+ansiReset) {
		t.Fatalf("missing red deletion: %q", got)
	}
	if !strings.Contains(got, ansiGreen+"   +new"+ansiReset) {
		t.Fatalf("missing green insertion: %q", got)
	}
	if !strings.Contains(got, ansiDim+"   --- hello.md"+ansiReset) {
		t.Fatalf("missing muted diff header: %q", got)
	}
}

// TestRenderToolResultMutesNonDiffOutput verifies ordinary tool result output
// stays visually subordinate in live chat.
func TestRenderToolResultMutesNonDiffOutput(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
		style: terminalStyle{
			enabled: true,
		},
	}
	renderer.renderToolResult(session.ToolMessage("call_1", "bash", "ok\n"))

	got := stdout.String()
	if !strings.Contains(got, ansiDim+"   ok"+ansiReset) {
		t.Fatalf("missing muted non-diff output: %q", got)
	}
}

// TestFormatTurnStatsSummarizesToolCounts verifies the footer keeps per-turn
// counters compact and grammatical.
func TestFormatTurnStatsSummarizesToolCounts(t *testing.T) {
	if got := formatTurnStats(liveTurnStats{}); got != "" {
		t.Fatalf("unexpected empty stats: %q", got)
	}
	if got := formatTurnStats(liveTurnStats{ToolCalls: 1}); got !=
		" · 1 tool" {

		t.Fatalf("unexpected singular stats: %q", got)
	}
	if got := formatTurnStats(liveTurnStats{ToolCalls: 3}); got !=
		" · 3 tools" {

		t.Fatalf("unexpected plural stats: %q", got)
	}
	got := formatTurnStats(liveTurnStats{
		ToolCalls: 2,
		Usage: model.Usage{
			InputTokens:           100,
			CachedInputTokens:     64,
			OutputTokens:          20,
			ReasoningOutputTokens: 5,
		},
	})
	want := " · 2 tools · 100 in · 64 cached · 20 out · 5 reasoning"
	if got != want {
		t.Fatalf("unexpected usage stats:\nwant %q\ngot  %q", want, got)
	}
}
