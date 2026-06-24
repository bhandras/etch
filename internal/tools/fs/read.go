package fs

import (
	"context"
	"fmt"
	"os"
	"strings"

	"etch/internal/textutil"
)

const (
	// DefaultReadMaxLines caps text file output when callers do not provide
	// a narrower line limit.
	DefaultReadMaxLines = 2000

	// DefaultReadMaxBytes caps text file output by UTF-8 bytes before a
	// continuation notice is appended.
	DefaultReadMaxBytes = 50 * 1024

	// DefaultReadMaxFiles caps batched read requests so one call cannot fan
	// out across the whole repository.
	DefaultReadMaxFiles = 8

	// DefaultReadBatchMaxBytes caps the combined output of a batched read.
	DefaultReadBatchMaxBytes = 120 * 1024
)

// ReadRange describes one bounded text file range.
type ReadRange struct {
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

// ReadRequest describes one bounded text file read or a batch of independent
// file ranges.
type ReadRequest struct {
	// Path is the file to read in single-file mode.
	Path string

	// Offset is the 1-indexed line number to start reading from in
	// single-file mode. Non-positive values start at the first line.
	Offset int

	// Limit caps the number of lines to return in single-file mode before
	// the default truncation limit is considered. Non-positive values use
	// DefaultReadMaxLines.
	Limit int

	// LineNumbers controls whether output lines are prefixed with their
	// 1-indexed source line number. The zero value enables line numbers.
	LineNumbers *bool `json:"lineNumbers,omitempty"`

	// Files contains independent file ranges to read in one tool call.
	// Callers should use this instead of repeated read calls when they
	// already know several relevant ranges.
	Files []ReadRange `json:"files,omitempty"`
}

// Read returns a model-friendly text slice from a file.
func Read(ctx context.Context, req ReadRequest) (string, error) {
	if len(req.Files) > 0 {
		return readBatch(ctx, req)
	}
	if strings.TrimSpace(req.Path) == "" {
		return "", fmt.Errorf("read requires path or files")
	}

	return readOne(ctx, readRequestRange(req))
}

// readRequestRange converts a single-file request into one file range.
func readRequestRange(req ReadRequest) ReadRange {
	return ReadRange{
		Path:        req.Path,
		Offset:      req.Offset,
		Limit:       req.Limit,
		LineNumbers: req.LineNumbers,
	}
}

// readOne reads one file range after applying workspace path policy.
func readOne(ctx context.Context, req ReadRange) (string, error) {
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

// readBatch reads multiple independent file ranges into one bounded response.
func readBatch(ctx context.Context, req ReadRequest) (string, error) {
	if len(req.Files) > DefaultReadMaxFiles {
		return "", fmt.Errorf("read files accepts at most %d entries",
			DefaultReadMaxFiles)
	}

	var out strings.Builder
	errorCount := 0
	for index, file := range req.Files {
		if strings.TrimSpace(file.Path) == "" {
			return "", fmt.Errorf("read files[%d].path is required",
				index)
		}
		if file.LineNumbers == nil {
			file.LineNumbers = req.LineNumbers
		}
		block, failed := renderReadBatchBlock(ctx, file)
		if failed {
			errorCount++
		}
		if !appendReadBatchBlock(&out, block, len(req.Files)-index-1) {
			break
		}
	}
	if errorCount > 0 {
		content := out.String()
		prefix := fmt.Sprintf("[Read batch completed with %d per-file %s. "+
			"Successful blocks are still usable; inspect "+
			"error blocks before assuming a file exists.]\n\n",
			errorCount, plural(errorCount, "error", "errors"))
		out.Reset()
		out.WriteString(prefix)
		out.WriteString(content)
	}

	return out.String(), nil
}

// plural returns singular for one item and plural for every other count.
func plural(count int, singular string, plural string) string {
	if count == 1 {
		return singular
	}

	return plural
}

// renderReadBatchBlock renders one file range or a per-file error block.
func renderReadBatchBlock(ctx context.Context, req ReadRange) (string, bool) {
	text, err := readOne(ctx, req)

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s", req.Path)
	if req.Offset > 0 || req.Limit > 0 {
		fmt.Fprintf(&out, " (%s)", readRangeLabel(req))
	}
	fmt.Fprintln(&out, " ---")
	if err != nil {
		fmt.Fprintf(&out, "error: %v", err)

		return out.String(), true
	}
	out.WriteString(text)

	return out.String(), false
}

// appendReadBatchBlock appends block if it fits and reports whether callers
// should continue appending later blocks.
func appendReadBatchBlock(out *strings.Builder, block string,
	remainingFiles int) bool {

	separator := ""
	if out.Len() > 0 {
		separator = "\n\n"
	}
	next := separator + block
	if out.Len()+len(next) <= DefaultReadBatchMaxBytes {
		out.WriteString(next)

		return true
	}

	notice := readBatchTruncationNotice(remainingFiles)
	remainingBytes := DefaultReadBatchMaxBytes - out.Len() -
		len(separator) - len(notice)
	if remainingBytes > 0 {
		truncated, _ := textutil.TruncateUTF8Bytes(
			block, remainingBytes,
		)
		out.WriteString(separator)
		out.WriteString(strings.TrimRight(truncated, "\n"))
		out.WriteString("\n")
		out.WriteString(notice)

		return false
	}
	if out.Len() > 0 {
		out.WriteString("\n\n")
	}
	out.WriteString(notice)

	return false
}

// readRangeLabel formats a requested range for a batched block header.
func readRangeLabel(req ReadRange) string {
	var parts []string
	if req.Offset > 0 {
		parts = append(parts, fmt.Sprintf("offset=%d", req.Offset))
	}
	if req.Limit > 0 {
		parts = append(parts, fmt.Sprintf("limit=%d", req.Limit))
	}

	return strings.Join(parts, ", ")
}

// readBatchTruncationNotice explains why later batched ranges may be omitted.
func readBatchTruncationNotice(remainingFiles int) string {
	files := "files"
	if remainingFiles == 1 {
		files = "file"
	}
	if remainingFiles > 0 {
		return fmt.Sprintf("[Read batch truncated at %s; %d more %s "+
			"omitted. Use fewer files or smaller offsets/limits.]",
			textutil.FormatBytes(DefaultReadBatchMaxBytes),
			remainingFiles, files)
	}

	return fmt.Sprintf("[Read batch truncated at %s. Use fewer files or "+
		"smaller offsets/limits.]",
		textutil.FormatBytes(DefaultReadBatchMaxBytes))
}

// renderReadContent applies offset, line limits, byte limits, and continuation
// hints to already-loaded file content.
func renderReadContent(content string, req ReadRange) (string, error) {
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
func lineNumbersEnabled(req ReadRange) bool {
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
