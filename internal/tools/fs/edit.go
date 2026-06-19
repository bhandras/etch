package fs

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	// utf8BOM is the byte order mark preserved by edit operations when it
	// is present in an existing file.
	utf8BOM = "\ufeff"
)

// EditRequest describes exact replacements for one existing text file.
type EditRequest struct {
	// Path is the existing file to modify.
	Path string

	// Edits are exact replacements matched against the original file.
	Edits []Edit
}

// Edit describes one exact text replacement.
type Edit struct {
	// OldText is the exact original text to replace.
	OldText string `json:"oldText"`

	// NewText is the exact replacement text.
	NewText string `json:"newText"`
}

// EditFile applies exact non-overlapping replacements to one existing file.
func EditFile(ctx context.Context, req EditRequest) (string, error) {
	path, err := mutationPath(req.Path)
	if err != nil {
		return "", err
	}
	if len(req.Edits) == 0 {
		return "", fmt.Errorf("at least one edit is required")
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
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

	original := string(content)
	prefix, body := splitBOM(original)
	lineEnding := detectLineEnding(body)
	normalized := normalizeLineEndings(body)
	spans, err := findEditSpans(normalized, req.Edits)
	if err != nil {
		return "", err
	}

	edited := applyEditSpans(normalized, spans)
	if edited == normalized {
		return "", fmt.Errorf("edit produced no changes")
	}
	diff := unifiedDiff(path, normalized, edited, defaultEditDiffMaxBytes)
	edited = restoreLineEndings(edited, lineEnding)
	if err := atomicWriteFile(
		ctx, path, []byte(prefix+edited),
	); err != nil {
		return "", err
	}

	unit := "edits"
	if len(spans) == 1 {
		unit = "edit"
	}

	result := fmt.Sprintf("Successfully applied %d %s to %s.", len(spans),
		unit, path)
	if diff != "" {
		result += "\n\n" + diff
	}

	return result, nil
}

// editSpan records an exact replacement location in normalized content.
type editSpan struct {
	// Start is the inclusive byte offset of the match.
	Start int

	// End is the exclusive byte offset of the match.
	End int

	// NewText is the normalized replacement text.
	NewText string
}

// findEditSpans validates and locates all edits against original content.
func findEditSpans(content string, edits []Edit) ([]editSpan, error) {
	spans := make([]editSpan, 0, len(edits))
	for i, edit := range edits {
		oldText := normalizeLineEndings(edit.OldText)
		newText := normalizeLineEndings(edit.NewText)
		if oldText == "" {
			return nil, fmt.Errorf("edit %d oldText must not "+
				"be empty", i+1)
		}
		if strings.TrimSpace(oldText) == "" {
			return nil, fmt.Errorf("edit %d oldText must include "+
				"non-whitespace context", i+1)
		}

		start := strings.Index(content, oldText)
		if start < 0 {
			return nil, fmt.Errorf("edit %d oldText was not found",
				i+1)
		}
		if strings.LastIndex(content, oldText) != start {
			return nil, fmt.Errorf("edit %d oldText is ambiguous",
				i+1)
		}

		spans = append(spans, editSpan{
			Start:   start,
			End:     start + len(oldText),
			NewText: newText,
		})
	}

	sort.Slice(spans, func(i, j int) bool {
		return spans[i].Start < spans[j].Start
	})
	for i := 1; i < len(spans); i++ {
		if spans[i].Start < spans[i-1].End {
			return nil, fmt.Errorf("edits %d and %d overlap", i,
				i+1)
		}
	}

	return spans, nil
}

// applyEditSpans applies replacements from the end of the file backward.
func applyEditSpans(content string, spans []editSpan) string {
	edited := content
	for i := len(spans) - 1; i >= 0; i-- {
		span := spans[i]
		edited = edited[:span.Start] + span.NewText + edited[span.End:]
	}

	return edited
}

// splitBOM separates a UTF-8 byte order mark from text content.
func splitBOM(content string) (string, string) {
	if strings.HasPrefix(content, utf8BOM) {
		return utf8BOM, strings.TrimPrefix(content, utf8BOM)
	}

	return "", content
}

// detectLineEnding chooses the dominant line ending style to preserve.
func detectLineEnding(content string) string {
	if strings.Contains(content, "\r\n") {
		return "\r\n"
	}

	return "\n"
}

// normalizeLineEndings converts common text line endings to LF.
func normalizeLineEndings(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	return content
}

// restoreLineEndings converts normalized LF text back to the original style.
func restoreLineEndings(content string, lineEnding string) string {
	if lineEnding == "\n" {
		return content
	}

	return strings.ReplaceAll(content, "\n", lineEnding)
}
