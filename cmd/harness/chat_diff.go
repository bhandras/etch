package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"harness/internal/render"
	"harness/internal/session"
)

const (
	// ansiDiffDeleteBackground starts the deletion-row background color.
	ansiDiffDeleteBackground = "\x1b[48;2;72;29;27m"

	// ansiDiffAddBackground starts the addition-row background color.
	ansiDiffAddBackground = "\x1b[48;2;24;63;39m"
)

// liveMutationDiff stores a parsed unified diff for terminal presentation.
type liveMutationDiff struct {
	// Path is the edited path from the diff header.
	Path string

	// Additions counts changed addition lines across all hunks.
	Additions int

	// Deletions counts changed deletion lines across all hunks.
	Deletions int

	// Lines stores line-numbered rows ready for terminal formatting.
	Lines []liveDiffRenderLine
}

// liveDiffRenderLine is one guttered diff row for live terminal output.
type liveDiffRenderLine struct {
	// Number is the old or new file line number displayed in the gutter.
	Number int

	// NumberWidth is the padded display width for Number.
	NumberWidth int

	// Marker is '-', '+', or ' ' for delete, add, or context.
	Marker byte

	// Text is the source text without the diff marker.
	Text string
}

// renderMutationToolResult renders edit and write diffs with line gutters.
func (r *liveChatRenderer) renderMutationToolResult(
	message session.MessageData) bool {

	if message.Name != "edit" && message.Name != "write" {
		return false
	}
	diff, ok := parseLiveMutationDiff(render.MessageText(message))
	if !ok {
		return false
	}

	header := fmt.Sprintf("Edited %s (+%d -%d)", displayDiffPath(diff.Path),
		diff.Additions, diff.Deletions)
	fmt.Fprintln(r.stdout, r.style.diffHeader("• "+header))
	lines := diff.RenderLines()
	limit := liveDiffOutputLimit
	if len(lines) > limit {
		remaining := len(lines) - limit
		lines = append(
			append([]liveDiffRenderLine{}, lines[:limit]...),
			liveDiffRenderLine{
				Text: fmt.Sprintf(
					"... %d more diff lines", remaining,
				),
			},
		)
	}
	numberWidth := liveDiffNumberWidth(lines)
	width := terminalWidth(r.stdout)
	for _, line := range lines {
		line.NumberWidth = numberWidth
		fmt.Fprintln(
			r.stdout, r.style.liveDiffLine(line, width),
		)
	}

	return true
}

// parseLiveMutationDiff extracts a hunked unified diff from tool output.
func parseLiveMutationDiff(text string) (liveMutationDiff, bool) {
	lines := splitPlainLines(text)
	diffStart := -1
	for index, line := range lines {
		if strings.HasPrefix(line, "--- ") {
			diffStart = index

			break
		}
	}
	if diffStart < 0 || diffStart+1 >= len(lines) ||
		!strings.HasPrefix(lines[diffStart+1], "+++ ") {
		return liveMutationDiff{}, false
	}

	diff := liveMutationDiff{
		Path: strings.TrimSpace(
			strings.TrimPrefix(lines[diffStart+1],
				"+++ "),
		),
	}
	for index := diffStart + 2; index < len(lines); {
		line := lines[index]
		if strings.HasPrefix(line, "[diff ") {
			return liveMutationDiff{}, false
		}
		if !strings.HasPrefix(line, "@@ ") {
			index++

			continue
		}
		oldLine, newLine, ok := parseUnifiedHunkHeader(line)
		if !ok {
			return liveMutationDiff{}, false
		}
		index++
		for index < len(lines) &&
			!strings.HasPrefix(lines[index], "@@ ") {

			row := lines[index]
			if strings.HasPrefix(row, "[diff ") {
				return liveMutationDiff{}, false
			}
			if row == "" {
				row = " "
			}
			marker := row[0]
			content := ""
			if len(row) > 1 {
				content = row[1:]
			}
			switch marker {
			case '-':
				diff.Lines = append(
					diff.Lines, liveDiffRenderLine{
						Number: oldLine,
						Marker: marker,
						Text:   content,
					},
				)
				diff.Deletions++
				oldLine++

			case '+':
				diff.Lines = append(
					diff.Lines, liveDiffRenderLine{
						Number: newLine,
						Marker: marker,
						Text:   content,
					},
				)
				diff.Additions++
				newLine++

			case ' ':
				diff.Lines = append(
					diff.Lines, liveDiffRenderLine{
						Number: newLine,
						Marker: marker,
						Text:   content,
					},
				)
				oldLine++
				newLine++
			}
			index++
		}
	}
	if diff.Path == "" || len(diff.Lines) == 0 {
		return liveMutationDiff{}, false
	}

	return diff, true
}

// parseUnifiedHunkHeader returns old and new one-based hunk starts.
func parseUnifiedHunkHeader(line string) (int, int, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[0] != "@@" {
		return 0, 0, false
	}
	oldStart, ok := parseUnifiedRange(fields[1], '-')
	if !ok {
		return 0, 0, false
	}
	newStart, ok := parseUnifiedRange(fields[2], '+')
	if !ok {
		return 0, 0, false
	}

	return oldStart, newStart, true
}

// parseUnifiedRange parses one unified diff range token such as -7,3.
func parseUnifiedRange(token string, prefix byte) (int, bool) {
	if token == "" || token[0] != prefix {
		return 0, false
	}
	rangeText := token[1:]
	if comma := strings.IndexByte(rangeText, ','); comma >= 0 {
		rangeText = rangeText[:comma]
	}
	start, err := strconv.Atoi(rangeText)
	if err != nil {
		return 0, false
	}
	if start == 0 {
		start = 1
	}

	return start, true
}

// RenderLines returns gutter rows in their existing parsed order.
func (d liveMutationDiff) RenderLines() []liveDiffRenderLine {
	return d.Lines
}

// liveDiffNumberWidth returns the gutter width needed by visible line numbers.
func liveDiffNumberWidth(lines []liveDiffRenderLine) int {
	width := 1
	for _, line := range lines {
		if line.Number <= 0 {
			continue
		}
		digits := len(strconv.Itoa(line.Number))
		if digits > width {
			width = digits
		}
	}

	return width
}

// displayDiffPath collapses absolute diff paths under the current directory.
func displayDiffPath(path string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return path
	}

	return rel
}

// diffHeader styles the compact mutation-result header.
func (s terminalStyle) diffHeader(text string) string {
	if !s.enabled {
		return text
	}

	return ansiBold + text + ansiReset
}

// liveDiffLine formats a line-numbered mutation diff row.
func (s terminalStyle) liveDiffLine(line liveDiffRenderLine, width int) string {
	number := ""
	if line.Number > 0 {
		number = strconv.Itoa(line.Number)
	}
	if line.NumberWidth <= 0 {
		line.NumberWidth = len(number)
	}
	text := expandDiffTabs(line.Text)
	row := fmt.Sprintf("  %*s %c %s", line.NumberWidth, number, line.Marker,
		text)
	if line.Number == 0 {
		row = "  " + strings.TrimSpace(text)
	}
	if !s.enabled {
		return row
	}
	switch line.Marker {
	case '-':
		return ansiDiffDeleteBackground + ansiRed +
			padPromptRow(row, width) + ansiReset

	case '+':
		return ansiDiffAddBackground + ansiGreen +
			padPromptRow(row, width) + ansiReset

	default:
		return ansiDim + row + ansiReset
	}
}

// expandDiffTabs avoids terminal tab cells that do not inherit row background.
func expandDiffTabs(text string) string {
	return strings.ReplaceAll(text, "\t", "    ")
}
