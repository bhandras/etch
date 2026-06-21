package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"harness/internal/core"
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

// TestMarkdownLinesRendersTables verifies pipe tables become aligned terminal
// text instead of raw markdown separators.
func TestMarkdownLinesRendersTables(t *testing.T) {
	lines := markdownLines(
		strings.Join([]string{
			"| Priority | Lines | Why |",
			"|---|---:|:---|",
			"| P0 | 970 | Core orchestration |",
			"| P1 | 598 | Flags |",
		}, "\n"),
		terminalStyle{
			enabled: true,
		},
	)
	got := strings.Join(lines, "\n")
	if strings.Contains(got, "|---") {
		t.Fatalf("table kept raw markdown delimiter: %q", got)
	}
	if !strings.Contains(got, ansiBold+"Priority"+ansiReset) {
		t.Fatalf("table did not style header row: %q", got)
	}
	if !strings.Contains(got, "P0          970  Core orchestration") {
		t.Fatalf("table did not align body rows: %q", got)
	}
	if !strings.Contains(
		got, ansiDim+"--------  -----  ------------------"+ansiReset,
	) {

		t.Fatalf("table did not render separator: %q", got)
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

// TestRenderReasoningFiltersStreamNoise verifies completed thinking summaries
// drop punctuation-only stream fragments before display.
func TestRenderReasoningFiltersStreamNoise(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
	}

	renderer.renderReasoning(
		"**Synthesizing project details**\n\n.\n:\n,\nDone",
	)

	got := stdout.String()
	if strings.Contains(got, "\n  .") ||
		strings.Contains(got, "\n  :") ||
		strings.Contains(got, "\n  ,") {

		t.Fatalf("reasoning kept punctuation noise: %q", got)
	}
	if !strings.Contains(got, "**Synthesizing project details**") ||
		!strings.Contains(got, "Done") {

		t.Fatalf("reasoning lost useful summary text: %q", got)
	}
}

// TestLiveStatusShowsEscapeCancelHint verifies non-composer status output
// points to the escape-key cancellation path instead of Ctrl+C.
func TestLiveStatusShowsEscapeCancelHint(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
		style: terminalStyle{
			enabled: true,
		},
		statusCancel:    make(chan struct{}),
		statusStartedAt: time.Now(),
		statusText:      "Working",
	}

	renderer.redrawStatusLocked()
	got := stdout.String()
	if !strings.Contains(got, "ESC to cancel") {
		t.Fatalf("missing escape cancel hint: %q", got)
	}
	if strings.Contains(got, "Ctrl+C") {
		t.Fatalf("status kept Ctrl+C hint: %q", got)
	}
}

// TestChatObserverBuffersReasoningDeltas verifies reasoning deltas only update
// status until the completed summary can be rendered as one markdown block.
func TestChatObserverBuffersReasoningDeltas(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: &liveChatRenderer{
			stdout:       &stdout,
			statusCancel: make(chan struct{}),
		},
		dynamicReasoningStatus: true,
	}

	observer.ModelReasoningDelta("**Thinking")
	if stdout.String() != "" {
		t.Fatalf("reasoning delta rendered early: %q", stdout.String())
	}
	if observer.renderer.statusText != "Thinking" {
		t.Fatalf("partial reasoning status = %q",
			observer.renderer.statusText)
	}

	observer.ModelReasoningDelta(" through project shape**")
	if observer.renderer.statusText != "Thinking through project shape" {
		t.Fatalf("dynamic reasoning status = %q",
			observer.renderer.statusText)
	}

	observer.ReasoningCompleted("**Thinking** clearly")

	got := stdout.String()
	if !strings.Contains(got, "• **Thinking** clearly") {
		t.Fatalf("completed reasoning was not rendered: %q", got)
	}
}

// TestReasoningStatusTextStripsMarkdown verifies reasoning headings become
// plain transient status labels.
func TestReasoningStatusTextStripsMarkdown(t *testing.T) {
	tests := map[string]string{
		"**Summarizing project analysis**\n\nDetails": "Summarizing project analysis",
		"### Considering `project` improvements":      "Considering project improvements",
		"- **Reviewing terminal UI**":                 "Reviewing terminal UI",
	}
	for input, want := range tests {
		if got := reasoningStatusText(input); got != want {
			t.Fatalf("status text mismatch:\ninput %q\nwant  "+
				"%q\ngot   %q",
				input, want, got)
		}
	}
}

// TestReasoningStatusSurvivesToolRunner verifies tool activity does not
// overwrite a model-provided reasoning status label.
func TestReasoningStatusSurvivesToolRunner(t *testing.T) {
	renderer := &liveChatRenderer{
		stdout:       &bytes.Buffer{},
		statusCancel: make(chan struct{}),
	}
	observer := &chatObserver{
		renderer:               renderer,
		dynamicReasoningStatus: true,
	}

	observer.ModelReasoningDelta("**Summarizing project analysis**")
	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_1",
		Name:      "read",
		Arguments: `{"path":"README.md"}`,
	})

	if renderer.statusText != "Summarizing project analysis" {
		t.Fatalf("tool runner overwrote reasoning status: %q",
			renderer.statusText)
	}
}

// TestReasoningStatusDefaultsToCannedLabels verifies generic providers do not
// turn streamed reasoning headings into terminal status labels.
func TestReasoningStatusDefaultsToCannedLabels(t *testing.T) {
	renderer := &liveChatRenderer{
		stdout:       &bytes.Buffer{},
		statusCancel: make(chan struct{}),
	}
	observer := &chatObserver{
		renderer: renderer,
	}

	observer.ModelReasoningDelta("**Summarizing project analysis**")
	if renderer.statusText != "Thinking" {
		t.Fatalf("generic reasoning status = %q", renderer.statusText)
	}

	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_1",
		Name:      "read",
		Arguments: `{"path":"README.md"}`,
	})
	if renderer.statusText != "Running tools" {
		t.Fatalf("generic reasoning blocked canned status: %q",
			renderer.statusText)
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

// TestRenderToolResultFormatsDiffWithLineNumbers verifies live edit and write
// output uses a compact line-numbered diff view.
func TestRenderToolResultFormatsDiffWithLineNumbers(t *testing.T) {
	t.Setenv("COLUMNS", "20")

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
				"hello.md\n@@ -1 +1 @@\n-old\n+new\n",
		),
	)

	got := stdout.String()
	if !strings.Contains(got, ansiBold+"• Edited hello.md (+1 -1)") {
		t.Fatalf("missing edited diff header: %q", got)
	}
	wantDelete := ansiDiffDeleteBackground + ansiRed +
		padPromptRow("  1 - old", 20) + ansiReset
	if !strings.Contains(
		got, wantDelete,
	) {

		t.Fatalf("missing padded red deletion: %q", got)
	}
	wantAdd := ansiDiffAddBackground + ansiGreen +
		padPromptRow("  1 + new", 20) + ansiReset
	if !strings.Contains(
		got, wantAdd,
	) {

		t.Fatalf("missing padded green insertion: %q", got)
	}
	if strings.Contains(got, ansiClearToEndOfLine) {
		t.Fatalf("diff rows should be padded, not clear-to-end: %q",
			got)
	}
}

// TestRenderToolResultExpandsDiffTabs verifies tab indentation does not leave
// uncolored terminal cells inside added or deleted diff rows.
func TestRenderToolResultExpandsDiffTabs(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
		style: terminalStyle{
			enabled: true,
		},
	}
	renderer.renderToolResult(
		session.ToolMessage(
			"call_1", "edit", "Updated.\n\n--- hello.go\n+++ "+
				"hello.go\n@@ -1 +1 @@\n-	old\n+	new\n",
		),
	)

	got := stdout.String()
	if strings.Contains(got, "\t") {
		t.Fatalf("diff renderer kept terminal tab cells: %q", got)
	}
	if !strings.Contains(got, "-     old") ||
		!strings.Contains(got, "+     new") {

		t.Fatalf("diff renderer did not expand tab indentation: %q",
			got)
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

// TestStatusRendererPulsesDotAndRestoresCursor verifies the transient working
// line uses the quiet dot animation and restores terminal cursor state.
func TestStatusRendererPulsesDotAndRestoresCursor(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
		style: terminalStyle{
			enabled: true,
		},
		statusCancel:    make(chan struct{}),
		statusStartedAt: time.Now(),
		statusText:      "Working",
	}

	renderer.hideCursorLocked()
	renderer.redrawStatusLocked()
	renderer.statusFrame = 4
	renderer.redrawStatusLocked()
	renderer.clearStatusLocked()
	renderer.showCursorLocked()

	got := stdout.String()
	if !strings.Contains(got, ansiCursorHide) {
		t.Fatalf("status did not hide cursor: %q", got)
	}
	if !strings.Contains(got, ansiCursorShow) {
		t.Fatalf("status did not restore cursor: %q", got)
	}
	if strings.Count(got, "•"+ansiReset+ansiDim) != 2 {
		t.Fatalf("status changed dot shape instead of intensity: %q",
			got)
	}
	if !strings.Contains(got, "\x1b[38;2;85;85;85m"+"•") ||
		!strings.Contains(got, ansiBrightWhite+"•") {

		t.Fatalf("status did not pulse dot intensity: %q", got)
	}
	if strings.Contains(got, "/ Working") ||
		strings.Contains(got, "| Working") ||
		strings.Contains(got, "\\ Working") ||
		strings.Contains(got, "·") ||
		strings.Contains(got, "●") {

		t.Fatalf("status kept rotating slash frames: %q", got)
	}
	if !strings.Contains(got, "\n"+ansiMoveUpOne) {
		t.Fatalf("status did not reserve trailing blank line: %q", got)
	}
}

// TestStatusPulseTiming verifies the dot breathes through one full brightness
// cycle every two seconds.
func TestStatusPulseTiming(t *testing.T) {
	if got := statusPulseInterval * statusPulseFrameCount; got !=
		2*time.Second {

		t.Fatalf("status pulse cycle = %s", got)
	}
	if statusPulseDot(0) != statusPulseDot(statusPulseFrameCount) {
		t.Fatalf("status pulse did not wrap after one cycle")
	}
	if statusPulseDot(0) == statusPulseDot(4) {
		t.Fatalf("status pulse peak matches the dimmest frame")
	}
}

// TestRenderChatPromptKeepsCapturedOutputPlain verifies non-terminal prompt
// output remains stable for tests, scripts, and redirected sessions.
func TestRenderChatPromptKeepsCapturedOutputPlain(t *testing.T) {
	var stdout bytes.Buffer

	renderChatPrompt(&stdout)
	resetChatPrompt(&stdout)

	if got := stdout.String(); got != "> " {
		t.Fatalf("plain prompt mismatch: %q", got)
	}
}

// TestPromptIslandRowsMatchWrappedInput verifies the prompt island keeps one
// shaded row above and one shaded row below wrapped input.
func TestPromptIslandRowsMatchWrappedInput(t *testing.T) {
	t.Setenv("COLUMNS", "16")

	row := promptIslandRow(&bytes.Buffer{})
	inputRows := wrappedPromptRows([]rune("hello hello hello hello"), 16)
	island := promptIslandRows(&bytes.Buffer{}, inputRows)

	if len(inputRows) != 2 {
		t.Fatalf("prompt input wrapped into %d rows", len(inputRows))
	}
	if !strings.Contains(row, ansiClearToEndOfLine) {
		t.Fatalf("terminal prompt row did not clear to end: %q", row)
	}
	wantRows := len(inputRows) + 2
	if strings.Count(island, promptIslandStyle()) != wantRows {
		t.Fatalf("terminal prompt island used wrong row count: %q",
			island)
	}
	if strings.Count(island, ansiClearToEndOfLine) != wantRows {
		t.Fatalf("terminal prompt island did not clear each row: %q",
			island)
	}
	if strings.Count(island, "\n") != wantRows-1 {
		t.Fatalf("terminal prompt island had wrong line breaks: %q",
			island)
	}
}

// TestRenderToolBatchShowsGroupedCalls verifies multi-tool model batches are
// made visible before individual tool results arrive.
func TestRenderToolBatchShowsGroupedCalls(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
	}

	renderer.renderToolBatch([]model.ToolCall{
		{
			ID:        "call_1",
			Name:      "read",
			Arguments: `{"path":"README.md"}`,
		},
		{
			ID:        "call_2",
			Name:      "grep",
			Arguments: `{"pattern":"TODO","path":"."}`,
		},
	})

	got := stdout.String()
	if !strings.Contains(got, "• Running 2 tools") ||
		!strings.Contains(got, "read README.md") ||
		!strings.Contains(got, "grep TODO .") {

		t.Fatalf("missing batch summary: %q", got)
	}
}

// TestLiveChatRendererFinishPadsFooter verifies the final turn footer leaves
// one empty row before the next prompt island can render.
func TestLiveChatRendererFinishPadsFooter(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout:  &stdout,
		printed: true,
		style: terminalStyle{
			enabled: true,
		},
	}

	renderer.finish(time.Second, liveTurnStats{})

	got := stdout.String()
	if !strings.Contains(got, "- Worked for 1s -") {
		t.Fatalf("finish did not render footer: %q", got)
	}
	if !strings.HasSuffix(got, ansiReset+"\n\n") {
		t.Fatalf("finish did not pad below footer: %q", got)
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
	want := " · 2 tools · 100 in · 64 cached · 20 out"
	if got != want {
		t.Fatalf("unexpected usage stats:\nwant %q\ngot  %q", want, got)
	}

	got = formatTurnStats(liveTurnStats{
		ToolCalls: 3,
		Timing: core.TurnTiming{
			ModelDuration:    2 * time.Second,
			ToolDuration:     time.Second,
			ToolBatches:      1,
			LargestToolBatch: 3,
		},
	})
	want = " · 3 tools · model 2s"
	if got != want {
		t.Fatalf("unexpected timing stats:\nwant %q\ngot  %q", want,
			got)
	}

	got = formatTurnStats(liveTurnStats{
		Timing: core.TurnTiming{
			ModelCalls:       2,
			RequestBytes:     1536,
			ResponseBytes:    768,
			TimeToHeaders:    time.Second,
			TimeToFirstEvent: 2 * time.Second,
		},
	})
	want = " · 2 requests · 1.5KB up · 768B down"
	if got != want {
		t.Fatalf("unexpected transport stats:\nwant %q\ngot  %q", want,
			got)
	}
}
