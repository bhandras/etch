package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestTerminalChatInputRedrawsComposerInFlow verifies ordinary typing keeps
// the live composer as a flow-relative block.
func TestTerminalChatInputRedrawsComposerInFlow(t *testing.T) {
	t.Setenv("COLUMNS", "16")
	var stdout bytes.Buffer
	input := &terminalChatInput{
		stdout: &stdout,
	}

	input.input = []rune("hello")
	if err := input.renderLocked(); err != nil {
		t.Fatalf("initial render failed: %v", err)
	}
	stdout.Reset()
	input.input = []rune("hello!")
	if err := input.renderLocked(); err != nil {
		t.Fatalf("second render failed: %v", err)
	}

	got := stdout.String()
	if strings.Contains(got, ansiMoveUp(2)) {
		t.Fatalf("same-height redraw performed a full clear: %q", got)
	}
	if !strings.Contains(got, "> hello!") {
		t.Fatalf("composer redraw missed updated input: %q", got)
	}
}

// TestTerminalChatInputTracksRenderedRows verifies prompt height is derived
// from status and wrapped input rather than absolute terminal position.
func TestTerminalChatInputTracksRenderedRows(t *testing.T) {
	t.Setenv("COLUMNS", "10")
	var stdout bytes.Buffer
	input := &terminalChatInput{
		stdout: &stdout,
	}

	input.input = []rune("hello")
	if err := input.renderLocked(); err != nil {
		t.Fatalf("single-line render failed: %v", err)
	}
	if input.lastRows != 3 {
		t.Fatalf("single-line composer rows = %d", input.lastRows)
	}

	input.statusText = "working"
	input.statusStartedAt = time.Now()
	input.input = []rune("hello hello hello hello")
	if err := input.renderLocked(); err != nil {
		t.Fatalf("multi-line render failed: %v", err)
	}
	if input.lastRows != 8 {
		t.Fatalf("status multi-line composer rows = %d", input.lastRows)
	}
}

// TestTerminalChatInputRedrawsAfterWidthChange verifies the composer reflows
// against the current terminal width on a fresh render.
func TestTerminalChatInputRedrawsAfterWidthChange(t *testing.T) {
	t.Setenv("COLUMNS", "24")
	var stdout bytes.Buffer
	input := &terminalChatInput{
		stdout: &stdout,
	}

	input.input = []rune("hello world")
	if err := input.renderLocked(); err != nil {
		t.Fatalf("wide render failed: %v", err)
	}
	stdout.Reset()
	t.Setenv("COLUMNS", "10")
	if err := input.renderLocked(); err != nil {
		t.Fatalf("narrow render failed: %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, ansiMoveUp(4)) {
		t.Fatalf("resize clear did not account for wrapped old "+
			"rows: %q", got)
	}
	if !strings.Contains(got, "> hello wo") ||
		!strings.Contains(got, "rld") {

		t.Fatalf("composer did not wrap at the narrower width: %q", got)
	}
	if input.lastWidth != 10 {
		t.Fatalf("composer width = %d", input.lastWidth)
	}
	if input.lastRows != 4 {
		t.Fatalf("wrapped composer rows = %d", input.lastRows)
	}
}

// TestTerminalChatInputPadsStatusAbovePrompt verifies the working indicator
// has a blank row above it while the prompt keeps its own padding below.
func TestTerminalChatInputPadsStatusAbovePrompt(t *testing.T) {
	t.Setenv("COLUMNS", "16")
	var stdout bytes.Buffer
	input := &terminalChatInput{
		stdout: &stdout,
	}

	input.statusText = "working"
	input.statusStartedAt = time.Now()
	if err := input.renderLocked(); err != nil {
		t.Fatalf("status render failed: %v", err)
	}

	lines := strings.Split(stdout.String(), "\n")
	if len(lines) < 5 {
		t.Fatalf("status composer rendered too few rows: %q",
			stdout.String())
	}
	if !strings.Contains(lines[0], ansiReset) {
		t.Fatalf("status composer did not start with padding: %q",
			stdout.String())
	}
	if !strings.Contains(lines[1], "working") {
		t.Fatalf("status line missing after padding: %q",
			stdout.String())
	}
}

// TestTerminalChatInputRendersFooterBelowPrompt verifies the prompt owns one
// metadata row below the island.
func TestTerminalChatInputRendersFooterBelowPrompt(t *testing.T) {
	t.Setenv("COLUMNS", "24")
	var stdout bytes.Buffer
	input := &terminalChatInput{
		stdout: &stdout,
	}

	input.SetFooter("gpt-5.5 high · ~/work")
	if err := input.renderLocked(); err != nil {
		t.Fatalf("footer render failed: %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "gpt-5.5 high") ||
		!strings.Contains(got, "~/work") {

		t.Fatalf("footer row missing metadata: %q", got)
	}
	if !strings.Contains(got, "\n\r"+ansiReset+ansiDim+"gpt-5.5") {
		t.Fatalf("footer row did not reset prompt background: %q", got)
	}
	if input.lastRows != 4 {
		t.Fatalf("footer composer rows = %d", input.lastRows)
	}
}

// TestTerminalChatInputWithOutputPreservesResult verifies output is written
// into scrollback before the active composer redraws.
func TestTerminalChatInputWithOutputPreservesResult(t *testing.T) {
	t.Setenv("COLUMNS", "16")
	t.Setenv("LINES", "8")
	var stdout bytes.Buffer
	input := &terminalChatInput{
		stdout: &stdout,
	}
	input.input = []rune("next")
	if err := input.renderLocked(); err != nil {
		t.Fatalf("initial render failed: %v", err)
	}
	stdout.Reset()

	input.WithOutput(func() {
		stdout.WriteString("RESULT\n")
	})

	got := stdout.String()
	resultAt := strings.Index(got, "RESULT")
	promptAt := strings.LastIndex(got, "> next")
	if resultAt < 0 {
		t.Fatalf("output result was not written: %q", got)
	}
	if promptAt < 0 {
		t.Fatalf("composer was not redrawn: %q", got)
	}
	if resultAt > promptAt {
		t.Fatalf("composer was redrawn before output: %q", got)
	}
	afterResult := got[resultAt:]
	if strings.Count(afterResult, ansiMoveUp(2)) > 0 {
		t.Fatalf("composer cleared again after writing output: %q", got)
	}
}

// TestTerminalChatInputFinishCommitsPromptOnly verifies a submitted composer
// writes the prompt into scrollback without transient footer chrome.
func TestTerminalChatInputFinishCommitsPromptOnly(t *testing.T) {
	t.Setenv("COLUMNS", "16")
	var stdout bytes.Buffer
	input := &terminalChatInput{
		stdout: &stdout,
	}
	input.SetFooter("gpt-5.5 high")
	input.input = []rune("hello")
	if err := input.renderLocked(); err != nil {
		t.Fatalf("initial render failed: %v", err)
	}
	stdout.Reset()

	input.finishLocked()

	got := stdout.String()
	if strings.Contains(got, ansiClearLine) {
		t.Fatalf("finish unexpectedly used line clearing: %q", got)
	}
	if !strings.Contains(got, "> hello") {
		t.Fatalf("submitted prompt was not committed: %q", got)
	}
	if strings.Contains(got, "gpt-5.5 high") {
		t.Fatalf("footer chrome was committed with prompt: %q", got)
	}
	if input.rendered {
		t.Fatalf("composer stayed rendered after submit")
	}
}
