package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"harness/internal/model"
	"harness/internal/render"
	"harness/internal/session"
)

const (
	// liveToolOutputLimit caps live terminal tool output blocks.
	liveToolOutputLimit = 6

	// ansiReset clears all terminal styling.
	ansiReset = "\x1b[0m"

	// ansiBold starts bold terminal styling.
	ansiBold = "\x1b[1m"

	// ansiDim starts dim terminal styling.
	ansiDim = "\x1b[2m"

	// ansiItalic starts italic terminal styling.
	ansiItalic = "\x1b[3m"
)

// liveChatRenderer owns transient terminal presentation for chat mode.
type liveChatRenderer struct {
	// stdout receives rendered terminal output.
	stdout io.Writer

	// style applies optional ANSI formatting.
	style terminalStyle

	// prefixBlankLine requests a leading blank line before first output.
	prefixBlankLine bool

	// printed reports whether the current turn has rendered any block yet.
	printed bool
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
	r.printSeparator()
	r.renderDotBlock(markdownLines(text, r.style), terminalTone{})
}

// renderReasoning renders a model-provided thinking summary in a muted tone.
func (r *liveChatRenderer) renderReasoning(text string) {
	r.printSeparator()
	tone := terminalTone{
		muted:  true,
		italic: true,
	}
	r.renderDotBlock(markdownLinesWithTone(text, r.style, tone), tone)
}

// renderToolCall renders one tool call immediately before local execution.
func (r *liveChatRenderer) renderToolCall(call model.ToolCall) {
	r.printSeparator()
	label := "Ran " + render.ToolCallText(session.ToolCallData{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: call.Arguments,
	})
	r.renderDotBlock([]string{label}, terminalTone{})
}

// renderToolResult renders a bounded tool result block.
func (r *liveChatRenderer) renderToolResult(message session.MessageData) {
	r.printSeparator()
	for _, line := range cappedToolResultLines(message) {
		fmt.Fprintln(r.stdout, r.style.muted(line))
	}
}

// finish renders a terminal-only footer after a completed turn.
func (r *liveChatRenderer) finish(elapsed time.Duration, stats liveTurnStats) {
	if !r.style.enabled || !r.printed {
		return
	}
	fmt.Fprintf(
		r.stdout, "\n%s- Worked for %s%s -%s\n", ansiDim,
		formatElapsed(elapsed), formatTurnStats(stats), ansiReset,
	)
}

// printSeparator inserts a blank line between live chat output blocks.
func (r *liveChatRenderer) printSeparator() {
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
}

// cappedToolResultLines returns a compact live view of tool output.
func cappedToolResultLines(message session.MessageData) []string {
	text := render.MessageText(message)
	lines := render.ToolResultLines(message.Name, text)
	if len(lines) <= liveToolOutputLimit {
		return lines
	}

	remaining := len(lines) - liveToolOutputLimit
	out := append([]string{}, lines[:liveToolOutputLimit]...)
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
	if stats.ToolCalls == 0 {
		return ""
	}
	if stats.ToolCalls == 1 {
		return " · 1 tool"
	}

	return fmt.Sprintf(" · %d tools", stats.ToolCalls)
}
