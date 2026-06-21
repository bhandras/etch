package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"harness/internal/core"
	"harness/internal/model"
	"harness/internal/render"
	"harness/internal/session"
)

const (
	// liveToolOutputLimit caps live terminal tool output blocks.
	liveToolOutputLimit = 6

	// liveDiffOutputLimit caps live terminal diff output blocks.
	liveDiffOutputLimit = 160

	// statusPulseFrameCount is the number of brightness steps in one status
	// pulse.
	statusPulseFrameCount = 8

	// ansiReset clears all terminal styling.
	ansiReset = "\x1b[0m"

	// ansiBold starts bold terminal styling.
	ansiBold = "\x1b[1m"

	// ansiDim starts dim terminal styling.
	ansiDim = "\x1b[2m"

	// ansiItalic starts italic terminal styling.
	ansiItalic = "\x1b[3m"

	// ansiRed starts red terminal styling for removed diff lines.
	ansiRed = "\x1b[31m"

	// ansiGreen starts green terminal styling for added diff lines.
	ansiGreen = "\x1b[32m"

	// ansiBrightWhite starts bright white terminal styling for peak status
	// frames.
	ansiBrightWhite = "\x1b[97m"

	// ansiPromptForeground starts the warm prompt text color.
	ansiPromptForeground = "\x1b[38;2;238;229;194m"

	// ansiPromptBackground starts the dark gray prompt island background.
	ansiPromptBackground = "\x1b[48;2;64;64;64m"

	// ansiClearLine clears the current terminal line.
	ansiClearLine = "\x1b[2K"

	// ansiCursorHide hides the terminal cursor during non-prompt work.
	ansiCursorHide = "\x1b[?25l"

	// ansiCursorShow restores the terminal cursor before prompt input.
	ansiCursorShow = "\x1b[?25h"

	// ansiMoveUpOne moves the terminal cursor up by one line.
	ansiMoveUpOne = "\x1b[1A"

	// ansiMoveDownOne moves the terminal cursor down by one line.
	ansiMoveDownOne = "\x1b[1B"

	// defaultTerminalWidth is used when terminal width detection is
	// unavailable.
	defaultTerminalWidth = 120
)

// statusPulseInterval controls how quickly the working dot breathes.
const statusPulseInterval = 250 * time.Millisecond

// liveChatRenderer owns transient terminal presentation for chat mode.
type liveChatRenderer struct {
	// mu serializes normal output with the animated status ticker.
	mu sync.Mutex

	// stdout receives rendered terminal output.
	stdout io.Writer

	// style applies optional ANSI formatting.
	style terminalStyle

	// prefixBlankLine requests a leading blank line before first output.
	prefixBlankLine bool

	// printed reports whether the current turn has rendered any block yet.
	printed bool

	// statusCancel stops the animated working status when non-nil.
	statusCancel chan struct{}

	// statusDone closes after the status goroutine exits.
	statusDone chan struct{}

	// statusStartedAt records when the working status began.
	statusStartedAt time.Time

	// statusText is the current canned working status label.
	statusText string

	// statusFrame is the current pulsing-dot animation frame index.
	statusFrame int

	// statusVisible reports whether the status line is currently painted.
	statusVisible bool

	// cursorHidden reports whether this renderer hid the terminal cursor.
	cursorHidden bool

	// composer owns the active bottom prompt while a turn is running.
	composer *terminalChatInput
}

// terminalStyle records whether ANSI styling should be emitted.
type terminalStyle struct {
	// enabled reports whether stdout looks like an interactive terminal.
	enabled bool
}

// terminalTone describes block-level styling for one rendered live block.
type terminalTone struct {
	// muted requests dim terminal text for less prominent blocks.
	muted bool

	// italic requests italic terminal text for reasoning summaries.
	italic bool
}

// liveTurnStats stores compact per-turn counters for the footer.
type liveTurnStats struct {
	// ToolCalls is the number of local tool calls executed in the turn.
	ToolCalls int

	// Usage stores provider-reported token counters for the turn.
	Usage model.Usage

	// Timing stores coarse model/tool timing for the turn.
	Timing core.TurnTiming
}

// newLiveChatRenderer creates the live renderer for one chat turn.
func newLiveChatRenderer(stdout io.Writer,
	prefixBlankLine bool) *liveChatRenderer {

	return &liveChatRenderer{
		stdout: stdout,
		style: terminalStyle{
			enabled: shouldStyle(stdout),
		},
		prefixBlankLine: prefixBlankLine,
	}
}

// renderAssistant renders final assistant text without an "assistant:" label.
func (r *liveChatRenderer) renderAssistant(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.renderWithOutputLocked(func() {
		r.closeStreamLocked()
		r.printSeparator()
		r.renderDotBlock(markdownLines(text, r.style), terminalTone{})
	})
}

// renderReasoning renders a model-provided thinking summary in a muted tone.
func (r *liveChatRenderer) renderReasoning(text string) {
	text = cleanReasoningSummary(text)
	if strings.TrimSpace(text) == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.renderWithOutputLocked(func() {
		r.closeStreamLocked()
		r.printSeparator()
		tone := terminalTone{
			muted:  true,
			italic: true,
		}
		r.renderDotBlock(
			markdownLinesWithTone(text, r.style, tone), tone,
		)
	})
}

// cleanReasoningSummary removes stream artifacts from displayable thinking
// summaries while preserving normal prose and markdown.
func cleanReasoningSummary(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	cleaned := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(cleaned) > 0 && !blank {
				cleaned = append(cleaned, "")
				blank = true
			}
			continue
		}
		if isStreamNoiseLine(trimmed) {
			continue
		}
		cleaned = append(cleaned, line)
		blank = false
	}

	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

// reasoningStatusText extracts a short plain-text status from reasoning.
func reasoningStatusText(text string) string {
	cleaned := cleanReasoningSummary(text)
	for _, line := range splitPlainLines(cleaned) {
		if status := plainMarkdownStatusLine(line); status != "" {
			return status
		}
	}

	return ""
}

// plainMarkdownStatusLine removes lightweight markdown from one status line.
func plainMarkdownStatusLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "- ")
	line = strings.TrimPrefix(line, "* ")
	line = strings.TrimPrefix(line, "• ")
	if header := markdownHeader(line); header != "" {
		line = header
	}
	line = strings.ReplaceAll(line, "**", "")
	line = strings.ReplaceAll(line, "__", "")
	line = strings.ReplaceAll(line, "`", "")
	line = strings.TrimSpace(line)
	if line == "" || isStreamNoiseLine(line) {
		return ""
	}

	return truncateStatusText(line)
}

// truncateStatusText keeps dynamic status text compact in narrow terminals.
func truncateStatusText(text string) string {
	const maxStatusRunes = 72
	runes := []rune(text)
	if len(runes) <= maxStatusRunes {
		return text
	}

	return string(runes[:maxStatusRunes-1]) + "…"
}

// isStreamNoiseLine reports whether a line is only short punctuation left
// behind by incremental model-stream deltas.
func isStreamNoiseLine(line string) bool {
	if len([]rune(line)) > 4 {
		return false
	}
	for _, char := range line {
		if !strings.ContainsRune(
			".,:;`'\"()[]{}-_*<>/\\|!?#$%^&=+~", char,
		) {
			return false
		}
	}

	return true
}

// renderToolCall renders one tool call immediately before local execution.
func (r *liveChatRenderer) renderToolCall(call model.ToolCall) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.renderWithOutputLocked(func() {
		r.closeStreamLocked()
		r.printSeparator()
		label := "Ran " + render.ToolCallText(session.ToolCallData{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
		r.renderDotBlock([]string{label}, terminalTone{})
	})
}

// renderToolBatch renders a compact summary for multi-tool model batches.
func (r *liveChatRenderer) renderToolBatch(calls []model.ToolCall) {
	if len(calls) <= 1 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.renderWithOutputLocked(func() {
		r.closeStreamLocked()
		r.printSeparator()
		lines := []string{fmt.Sprintf("Running %d tools", len(calls))}
		for _, call := range calls {
			lines = append(
				lines,
				"  "+render.ToolCallText(session.ToolCallData{
					ID:        call.ID,
					Name:      call.Name,
					Arguments: call.Arguments,
				}),
			)
		}
		r.renderDotBlock(lines, terminalTone{})
	})
}

// renderToolResult renders a bounded tool result block.
func (r *liveChatRenderer) renderToolResult(message session.MessageData) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.renderWithOutputLocked(func() {
		r.closeStreamLocked()
		r.printSeparator()
		for _, line := range cappedToolResultLines(message) {
			fmt.Fprintln(
				r.stdout,
				r.style.toolResultLine(message.Name, line),
			)
		}
	})
}

// renderAutoCompact renders one automatic context maintenance notice.
func (r *liveChatRenderer) renderAutoCompact(result core.AutoCompactResult) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.renderWithOutputLocked(func() {
		r.closeStreamLocked()
		r.printSeparator()
		line := fmt.Sprintf("Compacted context: ~%d -> ~%d tokens "+
			"(threshold ~%d, %s)", result.BeforeTokens,
			result.AfterTokens, result.ThresholdTokens,
			result.SummaryEventID)
		r.renderDotBlock([]string{line}, terminalTone{muted: true})
	})
}

// finish renders a terminal-only footer after a completed turn.
func (r *liveChatRenderer) finish(elapsed time.Duration, stats liveTurnStats) {
	r.stopStatus()
	r.mu.Lock()
	defer r.mu.Unlock()

	r.renderWithOutputLocked(func() {
		r.closeStreamLocked()
		if !r.style.enabled || !r.printed {
			return
		}
		fmt.Fprintf(
			r.stdout, "\n%s- Worked for %s%s -%s\n\n", ansiDim,
			formatElapsed(elapsed), formatTurnStats(stats),
			ansiReset,
		)
	})
}

// startStatus starts a terminal-only animated working status line.
func (r *liveChatRenderer) startStatus(text string) {
	if !r.style.enabled {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.statusCancel != nil {
		r.statusText = text
		if r.composer != nil {
			r.composer.SetStatus(text)

			return
		}
		r.redrawStatusLocked()

		return
	}

	r.statusStartedAt = time.Now()
	r.statusText = text
	r.statusCancel = make(chan struct{})
	r.statusDone = make(chan struct{})
	if r.composer != nil {
		r.composer.SetStatus(text)
		go r.runStatusTicker(r.statusCancel, r.statusDone)

		return
	}
	r.hideCursorLocked()
	r.redrawStatusLocked()

	go r.runStatusTicker(r.statusCancel, r.statusDone)
}

// updateStatus changes the animated working status label.
func (r *liveChatRenderer) updateStatus(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.statusCancel == nil {
		return
	}
	r.statusText = text
	if !r.style.enabled {
		return
	}
	if r.composer != nil {
		r.composer.SetStatus(text)

		return
	}
	r.redrawStatusLocked()
}

// stopStatus stops and clears the animated working status line.
func (r *liveChatRenderer) stopStatus() {
	r.mu.Lock()
	cancel := r.statusCancel
	done := r.statusDone
	r.statusCancel = nil
	r.statusDone = nil
	if r.composer != nil {
		r.composer.ClearStatus()
	} else {
		r.clearStatusLocked()
		r.showCursorLocked()
	}
	r.mu.Unlock()

	if cancel != nil {
		close(cancel)
		<-done
	}
}

// runStatusTicker repaints the working status until cancelled.
func (r *liveChatRenderer) runStatusTicker(cancel <-chan struct{},
	done chan<- struct{}) {

	defer close(done)
	ticker := time.NewTicker(statusPulseInterval)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return

		case <-ticker.C:
			r.mu.Lock()
			if r.statusCancel != nil {
				r.statusFrame++
				if r.composer != nil {
					r.composer.AdvanceStatus()
				} else {
					r.redrawStatusLocked()
				}
			}
			r.mu.Unlock()
		}
	}
}

// printSeparator inserts a blank line between live chat output blocks.
func (r *liveChatRenderer) printSeparator() {
	r.clearStatusLocked()
	if r.printed {
		fmt.Fprintln(r.stdout)
	} else if r.prefixBlankLine {
		fmt.Fprintln(r.stdout)
	}
	r.printed = true
}

// renderDotBlock renders lines with a leading dot on the first line.
func (r *liveChatRenderer) renderDotBlock(lines []string, tone terminalTone) {
	if len(lines) == 0 {
		lines = []string{""}
	}
	for i, line := range lines {
		prefix := "  "
		if i == 0 {
			prefix = "• "
		}
		fmt.Fprintln(r.stdout, r.style.wrapTone(prefix+line, tone))
	}
	r.redrawStatusLocked()
}

// renderWithOutputLocked lets an active composer move aside for normal output.
func (r *liveChatRenderer) renderWithOutputLocked(write func()) {
	if r.composer != nil {
		r.composer.WithOutput(write)

		return
	}
	write()
}

// closeStreamLocked is kept as a no-op for render paths that clear live status.
func (r *liveChatRenderer) closeStreamLocked() {
}

// redrawStatusLocked paints the current status line at the cursor position.
func (r *liveChatRenderer) redrawStatusLocked() {
	if !r.style.enabled || r.statusCancel == nil {
		return
	}
	if r.composer != nil {
		return
	}
	frame := statusPulseDot(r.statusFrame)
	elapsed := formatElapsed(time.Since(r.statusStartedAt))
	if !r.statusVisible {
		fmt.Fprint(r.stdout, "\n")
	}
	fmt.Fprintf(
		r.stdout, "\r%s%s%s %s (%s • ESC to cancel)%s", ansiClearLine,
		ansiDim, frame, r.statusText, elapsed, ansiReset,
	)
	if !r.statusVisible {
		fmt.Fprint(r.stdout, "\n", ansiMoveUpOne)
	}
	r.statusVisible = true
}

// statusPulseDot returns one stable dot with frame-dependent intensity.
func statusPulseDot(frame int) string {
	frames := []string{
		ansiDim + "\x1b[38;2;85;85;85m" + "•" + ansiReset + ansiDim,
		ansiDim + "\x1b[38;2;110;110;110m" + "•" + ansiReset + ansiDim,
		ansiDim + "\x1b[38;2;145;145;145m" + "•" + ansiReset + ansiDim,
		"\x1b[38;2;185;185;185m" + "•" + ansiReset + ansiDim,
		ansiBold + ansiBrightWhite + "•" + ansiReset + ansiDim,
		"\x1b[38;2;185;185;185m" + "•" + ansiReset + ansiDim,
		ansiDim + "\x1b[38;2;145;145;145m" + "•" + ansiReset + ansiDim,
		ansiDim + "\x1b[38;2;110;110;110m" + "•" + ansiReset + ansiDim,
	}

	return frames[frame%statusPulseFrameCount]
}

// clearStatusLocked removes the transient status line before normal output.
func (r *liveChatRenderer) clearStatusLocked() {
	if !r.statusVisible || !r.style.enabled {
		return
	}
	fmt.Fprintf(r.stdout, "\r%s%s\r", ansiClearLine, ansiMoveDownOne)
	r.statusVisible = false
}

// hideCursorLocked hides the cursor while the agent is producing output.
func (r *liveChatRenderer) hideCursorLocked() {
	if r.cursorHidden || !r.style.enabled {
		return
	}
	fmt.Fprint(r.stdout, ansiCursorHide)
	r.cursorHidden = true
}

// showCursorLocked restores the cursor before returning to prompt input.
func (r *liveChatRenderer) showCursorLocked() {
	if !r.cursorHidden || !r.style.enabled {
		return
	}
	fmt.Fprint(r.stdout, ansiCursorShow)
	r.cursorHidden = false
}

// showTerminalCursor restores the terminal cursor for chat exits.
func showTerminalCursor(stdout io.Writer) {
	if !shouldStyle(stdout) {
		return
	}
	fmt.Fprint(stdout, ansiCursorShow)
}

// renderChatPrompt writes the input prompt as a terminal island when possible.
func renderChatPrompt(stdout io.Writer) {
	if !shouldStyle(stdout) {
		fmt.Fprint(stdout, "> ")

		return
	}
	fmt.Fprintf(
		stdout, "\n%s%s%s\r%s",
		promptIslandRows(
			stdout, []string{"> "},
		),
		ansiMoveUpOne,
		ansiReset,
		terminalChatPrompt(),
	)
}

// terminalChatPrompt returns the styled prompt prefix for interactive chat.
func terminalChatPrompt() string {
	return promptIslandStyle() + " > "
}

// promptIslandStyle returns the open ANSI sequence for the input island.
func promptIslandStyle() string {
	return ansiPromptForeground + ansiPromptBackground
}

// promptIslandRows returns shaded rows around the current prompt input rows.
func promptIslandRows(stdout io.Writer, inputRows []string) string {
	rows := make([]string, 0, len(inputRows)+2)
	rows = append(rows, promptIslandRowWithText(stdout, ""))
	for _, row := range inputRows {
		rows = append(rows, promptIslandRowWithText(stdout, row))
	}
	rows = append(rows, promptIslandRowWithText(stdout, ""))

	return strings.Join(rows, "\n")
}

// promptIslandRow returns a full-width row for shaded prompt backgrounds.
func promptIslandRow(stdout io.Writer) string {
	return strings.Repeat(" ", terminalWidth(stdout))
}

// promptIslandRowWithText returns a shaded row padded to the terminal width.
func promptIslandRowWithText(stdout io.Writer, text string) string {
	return promptIslandRowWithTextWidth(text, terminalWidth(stdout))
}

// promptIslandRowWithTextWidth returns a shaded row padded to width.
func promptIslandRowWithTextWidth(text string, width int) string {
	return promptIslandStyle() + padPromptRow(text, width)
}

// ansiMoveUp returns a relative cursor-up movement for the requested rows.
func ansiMoveUp(rows int) string {
	if rows <= 0 {
		return ""
	}
	if rows == 1 {
		return ansiMoveUpOne
	}

	return fmt.Sprintf("\x1b[%dA", rows)
}

// ansiMoveRight returns a relative cursor-right movement for the requested
// columns.
func ansiMoveRight(columns int) string {
	if columns <= 0 {
		return ""
	}

	return fmt.Sprintf("\x1b[%dC", columns)
}

// terminalWidth returns the current terminal width using stdlib-only probes.
func terminalWidth(stdout io.Writer) int {
	if width, ok := terminalWidthFromFile(stdout); ok {
		return width
	}
	if width, ok := terminalWidthFromEnv(); ok {
		return width
	}

	return defaultTerminalWidth
}

// terminalWidthFromFile reads the terminal width through a TTY ioctl.
func terminalWidthFromFile(stdout io.Writer) (int, bool) {
	width, _, ok := terminalSizeFromFile(stdout)

	return width, ok
}

// terminalSizeFromFile reads terminal dimensions through a TTY ioctl.
func terminalSizeFromFile(stdout io.Writer) (int, int, bool) {
	file, ok := stdout.(*os.File)
	if !ok {
		return 0, 0, false
	}
	var size struct {
		rows uint16
		cols uint16
		x    uint16
		y    uint16
	}
	// #nosec G103 -- TIOCGWINSZ requires passing a pointer to a local
	// winsize struct so the kernel can write terminal dimensions.
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TIOCGWINSZ),
		uintptr(
			unsafe.Pointer(&size),
		),
	)
	if errno != 0 || size.cols == 0 {
		return 0, 0, false
	}

	return int(size.cols), int(size.rows), true
}

// terminalWidthFromEnv reads COLUMNS as a non-TTY fallback.
func terminalWidthFromEnv() (int, bool) {
	columns := os.Getenv("COLUMNS")
	if columns == "" {
		return 0, false
	}
	width, err := strconv.Atoi(columns)
	if err != nil || width <= 0 {
		return 0, false
	}

	return width, true
}

// resetChatPrompt restores normal styling after the submitted input line.
func resetChatPrompt(stdout io.Writer) {
	if !shouldStyle(stdout) {
		return
	}
	fmt.Fprint(stdout, ansiReset, "\n")
}

// cappedToolResultLines returns a compact live view of tool output.
func cappedToolResultLines(message session.MessageData) []string {
	text := render.MessageText(message)
	lines := render.ToolResultLines(message.Name, text)
	limit := liveToolOutputLimit
	if message.Name == "edit" || message.Name == "write" {
		limit = liveDiffOutputLimit
	}
	if len(lines) <= limit {
		return lines
	}

	remaining := len(lines) - limit
	out := append([]string{}, lines[:limit]...)
	out = append(out, fmt.Sprintf("   ... %d more lines", remaining))

	return out
}

// markdownLines applies a tiny terminal-only markdown rendering pass.
func markdownLines(text string, style terminalStyle) []string {
	return markdownLinesWithTone(text, style, terminalTone{})
}

// markdownLinesWithTone renders markdown while preserving a surrounding tone.
func markdownLinesWithTone(text string, style terminalStyle,
	tone terminalTone) []string {

	raw := splitPlainLines(text)
	if !style.enabled {
		return raw
	}

	baseOpen := style.openTone(tone)
	inFence := false
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(line, "    ") {
			lines = append(lines, style.muted(line))
			continue
		}
		if header := markdownHeader(line); header != "" {
			lines = append(
				lines,
				style.styleSpan(header, ansiBold, baseOpen),
			)
			continue
		}
		lines = append(
			lines, styleInlineMarkdown(line, style, baseOpen),
		)
	}

	return lines
}

// splitPlainLines trims trailing newlines and returns at least one line.
func splitPlainLines(text string) []string {
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return []string{""}
	}

	return strings.Split(trimmed, "\n")
}

// markdownHeader strips a leading markdown header marker when present.
func markdownHeader(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	count := 0
	for count < len(trimmed) && trimmed[count] == '#' {
		count++
	}
	if count == 0 || count > 6 {
		return ""
	}
	if len(trimmed) == count || trimmed[count] != ' ' {
		return ""
	}

	return strings.TrimSpace(trimmed[count:])
}

// styleInlineMarkdown renders a small subset of inline markdown.
func styleInlineMarkdown(line string, style terminalStyle,
	baseOpen string) string {

	line = styleDelimited(line, "**", ansiBold, style, baseOpen)
	line = styleDelimited(line, "`", ansiDim, style, baseOpen)

	return line
}

// styleDelimited applies one ANSI style to text between delimiter pairs.
func styleDelimited(line string, delimiter string, code string,
	style terminalStyle, baseOpen string) string {

	if !style.enabled {
		return line
	}

	var out strings.Builder
	remaining := line
	for {
		start := strings.Index(remaining, delimiter)
		if start < 0 {
			out.WriteString(remaining)

			return out.String()
		}
		end := strings.Index(
			remaining[start+len(delimiter):], delimiter,
		)
		if end < 0 {
			out.WriteString(remaining)

			return out.String()
		}
		end += start + len(delimiter)
		out.WriteString(remaining[:start])
		out.WriteString(
			style.styleSpan(
				remaining[start+len(delimiter):end], code,
				baseOpen,
			),
		)
		remaining = remaining[end+len(delimiter):]
	}
}

// shouldStyle reports whether ANSI terminal styling should be emitted.
func shouldStyle(stdout io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := stdout.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

// muted applies muted terminal styling when enabled.
func (s terminalStyle) muted(text string) string {
	if !s.enabled {
		return text
	}

	return ansiDim + text + ansiReset
}

// toolResultLine styles one live tool result line.
func (s terminalStyle) toolResultLine(toolName string, line string) string {
	if toolName == "edit" || toolName == "write" {
		return s.diffLine(line)
	}

	return s.muted(line)
}

// diffLine styles unified diff output using familiar red/green accents.
func (s terminalStyle) diffLine(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if strings.HasPrefix(trimmed, "+++") ||
		strings.HasPrefix(trimmed, "---") ||
		strings.HasPrefix(trimmed, "@@") ||
		strings.HasPrefix(trimmed, "[diff ") {
		return s.muted(line)
	}
	if strings.HasPrefix(trimmed, "+") {
		return s.colored(line, ansiGreen)
	}
	if strings.HasPrefix(trimmed, "-") {
		return s.colored(line, ansiRed)
	}

	return line
}

// colored applies an ANSI color when terminal styling is enabled.
func (s terminalStyle) colored(text string, color string) string {
	if !s.enabled {
		return text
	}

	return color + text + ansiReset
}

// openTone returns the ANSI prefix for a block tone.
func (s terminalStyle) openTone(tone terminalTone) string {
	if !s.enabled {
		return ""
	}

	var open strings.Builder
	if tone.muted {
		open.WriteString(ansiDim)
	}
	if tone.italic {
		open.WriteString(ansiItalic)
	}

	return open.String()
}

// wrapTone applies the block tone to text when terminal styling is enabled.
func (s terminalStyle) wrapTone(text string, tone terminalTone) string {
	open := s.openTone(tone)
	if open == "" {
		return text
	}

	return open + text + ansiReset
}

// styleSpan applies an inline style and then restores any surrounding tone.
func (s terminalStyle) styleSpan(text string, code string,
	baseOpen string) string {

	if !s.enabled {
		return text
	}

	return code + text + ansiReset + baseOpen
}

// formatElapsed returns a compact human duration for the turn footer.
func formatElapsed(elapsed time.Duration) string {
	seconds := int(elapsed.Round(time.Second).Seconds())
	if seconds < 1 {
		return "<1s"
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60

	return fmt.Sprintf("%dm %ds", minutes, seconds)
}

// formatTurnStats returns optional compact counters for the turn footer.
func formatTurnStats(stats liveTurnStats) string {
	var parts []string
	if stats.ToolCalls == 1 {
		parts = append(parts, "1 tool")
	} else if stats.ToolCalls > 1 {
		parts = append(parts, fmt.Sprintf("%d tools", stats.ToolCalls))
	}
	parts = append(parts, usageStatParts(stats.Usage)...)
	if stats.Timing.ModelDuration > 0 {
		parts = append(
			parts,
			"model "+formatElapsed(stats.Timing.ModelDuration),
		)
	}
	if len(parts) == 0 {
		return ""
	}

	return " · " + strings.Join(parts, " · ")
}

// formatUsageStats returns compact provider token counters without a leading
// separator.
func formatUsageStats(usage model.Usage) string {
	return strings.Join(usageStatParts(usage), " · ")
}

// usageStatParts returns compact token counter phrases for terminal chrome.
func usageStatParts(usage model.Usage) []string {
	if usage.Empty() {
		return nil
	}
	parts := []string{
		fmt.Sprintf("%d in", usage.InputTokens),
	}
	if usage.CachedInputTokens > 0 {
		parts = append(
			parts, fmt.Sprintf("%d cached",
				usage.CachedInputTokens),
		)
	}
	parts = append(parts, fmt.Sprintf("%d out", usage.OutputTokens))

	return parts
}
