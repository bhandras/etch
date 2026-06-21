package main

import (
	"bufio"
	"bytes"
	"strings"
	"syscall"
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

// TestTerminalChatInputSubmitSkipsBlankPrompt verifies blank submissions are
// erased instead of being preserved as empty prompt islands in scrollback.
func TestTerminalChatInputSubmitSkipsBlankPrompt(t *testing.T) {
	t.Setenv("COLUMNS", "16")
	for _, submitted := range []string{"", "   "} {
		var stdout bytes.Buffer
		input := &terminalChatInput{
			stdout: &stdout,
			input:  []rune(submitted),
		}
		if err := input.renderLocked(); err != nil {
			t.Fatalf("initial render failed: %v", err)
		}
		stdout.Reset()

		line := input.submitLocked()
		if line != submitted {
			t.Fatalf("blank submit returned %q, want %q", line,
				submitted)
		}

		got := stdout.String()
		if strings.Contains(got, ">") {
			t.Fatalf("blank prompt was committed: %q", got)
		}
		if input.rendered {
			t.Fatalf("composer stayed rendered after blank submit")
		}
		if len(input.input) != 0 {
			t.Fatalf("input was not cleared: %q",
				string(input.input))
		}
	}
}

// TestTerminalChatInputCancelDiscardsPrompt verifies cancellations clear the
// live composer without preserving partially typed input in scrollback.
func TestTerminalChatInputCancelDiscardsPrompt(t *testing.T) {
	t.Setenv("COLUMNS", "16")
	var stdout bytes.Buffer
	input := &terminalChatInput{
		stdout: &stdout,
		input:  []rune("partial"),
	}
	if err := input.renderLocked(); err != nil {
		t.Fatalf("initial render failed: %v", err)
	}
	stdout.Reset()

	input.cancelLocked()

	got := stdout.String()
	if strings.Contains(got, "> partial") {
		t.Fatalf("interrupted prompt was committed: %q", got)
	}
	if input.rendered {
		t.Fatalf("composer stayed rendered after cancel")
	}
	if len(input.input) != 0 {
		t.Fatalf("input was not cleared: %q", string(input.input))
	}
}

// TestTerminalChatInputConsumesEscapeSequences verifies arrow-key sequences do
// not trigger standalone escape cancellation.
func TestTerminalChatInputConsumesEscapeSequences(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("[A"))
	input := &terminalChatInput{}

	if !input.consumeEscapeSequence(reader) {
		t.Fatalf("escape sequence was not consumed")
	}
	if reader.Buffered() != 0 {
		t.Fatalf("escape sequence left %d buffered bytes",
			reader.Buffered())
	}
}

// TestTerminalChatInputIgnoresStandaloneEscapeConsumption verifies a bare ESC
// remains available for cancellation.
func TestTerminalChatInputIgnoresStandaloneEscapeConsumption(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("x"))
	input := &terminalChatInput{}

	if input.consumeEscapeSequence(reader) {
		t.Fatalf("standalone escape was consumed as a sequence")
	}
	if reader.Buffered() != 1 {
		t.Fatalf("standalone escape consumed buffered input")
	}
}

// TestStatusComposerLineShowsEscapeCancelHint verifies active work points to
// the escape-key cancellation path instead of Ctrl+C.
func TestStatusComposerLineShowsEscapeCancelHint(t *testing.T) {
	line := statusComposerLine(0, "Working", time.Now(), 80)
	if !strings.Contains(line, "ESC to cancel") {
		t.Fatalf("missing escape cancel hint: %q", line)
	}
	if strings.Contains(line, "Ctrl+C") {
		t.Fatalf("status kept Ctrl+C hint: %q", line)
	}
}

// TestRawTerminalStateDisablesInterruptSignal verifies Ctrl+C reaches the
// prompt editor as input instead of terminating the process through SIGINT.
func TestRawTerminalStateDisablesInterruptSignal(t *testing.T) {
	state := syscall.Termios{
		Lflag: syscall.ECHO | syscall.ICANON | syscall.ISIG,
	}

	got := rawTerminalState(state)
	if got.Lflag&syscall.ISIG != 0 {
		t.Fatalf("raw mode kept ISIG enabled: %#v", got.Lflag)
	}
}
