package fs

import (
	"fmt"
	"strings"
)

const (
	// defaultEditDiffMaxBytes caps model-facing diff output for edit
	// results.
	defaultEditDiffMaxBytes = 20 * 1024

	// defaultDiffContextLines is the unchanged line count shown around
	// each change hunk.
	defaultDiffContextLines = 3

	// maxDiffLineProduct bounds the simple LCS renderer for large files.
	maxDiffLineProduct = 250000
)

// unifiedDiff returns a compact line diff for model-visible edit results.
func unifiedDiff(path string, before string, after string,
	maxBytes int) string {

	if before == after {
		return ""
	}

	beforeLines := splitDiffLines(before)
	afterLines := splitDiffLines(after)
	lines, oldBase, newBase, ok := compactDiffLines(
		beforeLines, afterLines, defaultDiffContextLines,
	)
	if !ok {
		return "[diff omitted: file is too large for inline diff]"
	}

	hunks := diffHunks(lines, defaultDiffContextLines)
	var out strings.Builder
	out.WriteString("--- " + path + "\n")
	out.WriteString("+++ " + path + "\n")
	for _, hunk := range hunks {
		part := renderDiffHunk(lines, hunk, oldBase, newBase)
		if out.Len()+len(part) > maxBytes {
			out.WriteString(
				"[diff truncated: output exceeded " +
					formatBytes(maxBytes) + "]\n",
			)

			break
		}
		out.WriteString(part)
	}

	return strings.TrimRight(out.String(), "\n")
}

// compactDiffLines trims identical edges before running the bounded LCS diff.
func compactDiffLines(before []string, after []string, context int) ([]diffLine,
	int, int, bool) {

	prefix := commonPrefixLines(before, after)
	suffix := commonSuffixLines(before, after, prefix)
	beforeEnd := len(before) - suffix
	afterEnd := len(after) - suffix
	beforeMid := before[prefix:beforeEnd]
	afterMid := after[prefix:afterEnd]
	if len(beforeMid)*len(afterMid) > maxDiffLineProduct {
		return nil, 1, 1, false
	}

	start := max(0, prefix-context)
	lines := make([]diffLine, 0, len(beforeMid)+len(afterMid)+2*context)
	for _, line := range before[start:prefix] {
		lines = append(lines, diffLine{Prefix: ' ', Text: line})
	}
	lines = append(lines, diffLines(beforeMid, afterMid)...)
	suffixContext := min(context, suffix)
	for _, line := range after[afterEnd : afterEnd+suffixContext] {
		lines = append(lines, diffLine{Prefix: ' ', Text: line})
	}

	return lines, start + 1, start + 1, true
}

// commonPrefixLines counts matching leading lines.
func commonPrefixLines(before []string, after []string) int {
	limit := min(len(before), len(after))
	for index := 0; index < limit; index++ {
		if before[index] != after[index] {
			return index
		}
	}

	return limit
}

// commonSuffixLines counts matching trailing lines after the shared prefix.
func commonSuffixLines(before []string, after []string, prefix int) int {
	limit := min(len(before)-prefix, len(after)-prefix)
	for count := 0; count < limit; count++ {
		beforeIndex := len(before) - count - 1
		afterIndex := len(after) - count - 1
		if before[beforeIndex] != after[afterIndex] {
			return count
		}
	}

	return limit
}

// diffHunk describes an inclusive-exclusive range in a diff line stream.
type diffHunk struct {
	// Start is the first diff line index included in the hunk.
	Start int

	// End is the first diff line index after the hunk.
	End int
}

// diffLine describes one rendered line in a simple line diff.
type diffLine struct {
	// Prefix is ' ', '-', or '+' for context, deletion, or insertion.
	Prefix byte

	// Text is the original line text without any diff prefix.
	Text string
}

// splitDiffLines splits text while preserving line-oriented records.
func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}

	return strings.SplitAfter(text, "\n")
}

// diffLines produces a stable LCS-based line diff.
func diffLines(before []string, after []string) []diffLine {
	table := diffTable(before, after)
	var lines []diffLine
	i := 0
	j := 0
	for i < len(before) || j < len(after) {
		if i < len(before) && j < len(after) && before[i] == after[j] {
			lines = append(
				lines, diffLine{
					Prefix: ' ',
					Text:   before[i],
				},
			)
			i++
			j++

			continue
		}
		if j < len(after) &&
			(i == len(before) || table[i][j+1] > table[i+1][j]) {

			lines = append(
				lines, diffLine{
					Prefix: '+',
					Text:   after[j],
				},
			)
			j++

			continue
		}

		lines = append(lines, diffLine{Prefix: '-', Text: before[i]})
		i++
	}

	return lines
}

// diffHunks groups changed lines with bounded surrounding context.
func diffHunks(lines []diffLine, context int) []diffHunk {
	if context < 0 {
		context = 0
	}
	var hunks []diffHunk
	for index, line := range lines {
		if line.Prefix == ' ' {
			continue
		}
		start := max(0, index-context)
		end := min(len(lines), index+context+1)
		if len(hunks) > 0 && start <= hunks[len(hunks)-1].End {
			if end > hunks[len(hunks)-1].End {
				hunks[len(hunks)-1].End = end
			}

			continue
		}
		hunks = append(hunks, diffHunk{Start: start, End: end})
	}

	return hunks
}

// diffTable builds the dynamic-programming table used by diffLines.
func diffTable(before []string, after []string) [][]int {
	table := make([][]int, len(before)+1)
	for i := range table {
		table[i] = make([]int, len(after)+1)
	}

	for i := len(before) - 1; i >= 0; i-- {
		for j := len(after) - 1; j >= 0; j-- {
			if before[i] == after[j] {
				table[i][j] = table[i+1][j+1] + 1

				continue
			}
			table[i][j] = max(table[i+1][j], table[i][j+1])
		}
	}

	return table
}

// renderDiffHunk renders one git-style unified diff hunk.
func renderDiffHunk(lines []diffLine, hunk diffHunk, oldBase int,
	newBase int) string {

	oldStart, newStart := diffHunkStarts(
		lines, hunk.Start, oldBase, newBase,
	)
	oldCount, newCount := diffHunkCounts(lines[hunk.Start:hunk.End])
	var out strings.Builder
	fmt.Fprintf(
		&out, "@@ -%s +%s @@\n", formatDiffRange(oldStart, oldCount),
		formatDiffRange(newStart, newCount),
	)
	for _, line := range lines[hunk.Start:hunk.End] {
		out.WriteString(renderDiffLine(line))
	}

	return out.String()
}

// diffHunkStarts returns one-based old and new line starts for a hunk.
func diffHunkStarts(lines []diffLine, hunkStart int, oldBase int,
	newBase int) (int, int) {

	oldStart := oldBase
	newStart := newBase
	for _, line := range lines[:hunkStart] {
		if line.Prefix != '+' {
			oldStart++
		}
		if line.Prefix != '-' {
			newStart++
		}
	}

	return oldStart, newStart
}

// diffHunkCounts counts old and new lines represented in a hunk.
func diffHunkCounts(lines []diffLine) (int, int) {
	oldCount := 0
	newCount := 0
	for _, line := range lines {
		if line.Prefix != '+' {
			oldCount++
		}
		if line.Prefix != '-' {
			newCount++
		}
	}

	return oldCount, newCount
}

// formatDiffRange renders the hunk line range using unified diff syntax.
func formatDiffRange(start int, count int) string {
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	if count == 0 {
		return fmt.Sprintf("%d,0", max(0, start-1))
	}

	return fmt.Sprintf("%d,%d", start, count)
}

// renderDiffLine prefixes one diff line and ensures exactly one output newline.
func renderDiffLine(line diffLine) string {
	text := strings.TrimSuffix(line.Text, "\n")

	return fmt.Sprintf("%c%s\n", line.Prefix, text)
}
