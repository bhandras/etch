package fs

import (
	"context"
	"fmt"
	"os"
	"strings"

	"harness/internal/textutil"
)

const (
	// DefaultReadMaxLines caps text file output when callers do not provide
	// a narrower line limit.
	DefaultReadMaxLines = 2000

	// DefaultReadMaxBytes caps text file output by UTF-8 bytes before a
	// continuation notice is appended.
	DefaultReadMaxBytes = 50 * 1024
)

// ReadRequest describes one bounded text file read.
type ReadRequest struct {
	// Path is the file to read.
	Path string

	// Offset is the 1-indexed line number to start reading from.
	// Non-positive values start at the first line.
	Offset int

	// Limit caps the number of lines to return before the default
	// truncation limit is considered. Non-positive values use
	// DefaultReadMaxLines.
	Limit int

	// LineNumbers controls whether output lines are prefixed with their
	// 1-indexed source line number. The zero value enables line numbers.
	LineNumbers *bool `json:"lineNumbers,omitempty"`
}

// Read returns a model-friendly text slice from a file.
func Read(ctx context.Context, req ReadRequest) (string, error) {
	path, err := readPath(req.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()

	default:
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()

	default:
	}

	return renderReadContent(string(content), req)
}

// renderReadContent applies offset, line limits, byte limits, and continuation
// hints to already-loaded file content.
func renderReadContent(content string, req ReadRequest) (string, error) {
	lines := strings.Split(content, "\n")
	start := req.Offset
	if start <= 0 {
		start = 1
	}
	startIndex := start - 1
	if startIndex >= len(lines) {
		return "", fmt.Errorf("offset %d is beyond end of file (%d "+
			"lines total)", req.Offset, len(lines))
	}

	limit := req.Limit
	if limit <= 0 {
		limit = DefaultReadMaxLines
	}

	selected := lines[startIndex:]
	if limit < len(selected) {
		selected = selected[:limit]
	}

	numbered := formatReadLines(selected, start, lineNumbersEnabled(req))
	truncated := truncateLines(numbered, DefaultReadMaxBytes)
	endLine := start + truncated.OutputLines - 1
	output := truncated.Text
	if truncated.FirstLineExceedsLimit {
		return fmt.Sprintf("[Line %d exceeds %s limit. Use a "+
			"smaller offset/limit or a specialized tool.]",
			start, textutil.FormatBytes(DefaultReadMaxBytes)), nil
	}
	if truncated.Truncated {
		nextOffset := endLine + 1
		reason := fmt.Sprintf("%s limit", truncated.Reason)
		if truncated.Reason == "bytes" {
			reason = textutil.FormatBytes(DefaultReadMaxBytes) +
				" limit"
		}
		output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s). "+
			"Use offset=%d to continue.]",
			start, endLine, len(lines), reason, nextOffset)

		return output, nil
	}

	if startIndex+limit < len(lines) {
		nextOffset := startIndex + limit + 1
		remaining := len(lines) - (startIndex + limit)
		unit := "lines"
		if remaining == 1 {
			unit = "line"
		}
		output += fmt.Sprintf("\n\n[%d more %s in file. Use offset=%d "+
			"to continue.]",
			remaining, unit, nextOffset)
	}

	return output, nil
}

// lineNumbersEnabled reports whether read output should include source lines.
func lineNumbersEnabled(req ReadRequest) bool {
	return req.LineNumbers == nil || *req.LineNumbers
}

// formatReadLines renders selected lines with optional source line numbers.
func formatReadLines(lines []string, start int, lineNumbers bool) []string {
	if !lineNumbers {
		return lines
	}
	width := len(fmt.Sprintf("%d", start+len(lines)-1))
	formatted := make([]string, 0, len(lines))
	for i, line := range lines {
		formatted = append(
			formatted,
			fmt.Sprintf("%*d | %s", width, start+i, line),
		)
	}

	return formatted
}

// truncatedText describes the result of byte-bound line rendering.
type truncatedText struct {
	// Text is the complete-line output that fits within the byte budget.
	Text string

	// Truncated reports whether at least one selected line was omitted.
	Truncated bool

	// Reason explains the first limit that caused truncation.
	Reason string

	// OutputLines is the number of complete lines included in Text.
	OutputLines int

	// FirstLineExceedsLimit reports whether no complete line can fit.
	FirstLineExceedsLimit bool
}

// truncateLines renders complete lines until the byte budget is exhausted.
func truncateLines(lines []string, maxBytes int) truncatedText {
	var out strings.Builder
	for i, line := range lines {
		part := line
		if i > 0 {
			part = "\n" + part
		}
		if out.Len()+len(part) > maxBytes {
			return truncatedText{
				Text:                  out.String(),
				Truncated:             true,
				Reason:                "bytes",
				OutputLines:           i,
				FirstLineExceedsLimit: i == 0,
			}
		}

		out.WriteString(part)
	}

	return truncatedText{
		Text:        out.String(),
		OutputLines: len(lines),
	}
}
