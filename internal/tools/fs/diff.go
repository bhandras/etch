package fs

import (
	"fmt"
	"strings"
)

const (
	// defaultEditDiffMaxBytes caps model-facing diff output for edit
	// results.
	defaultEditDiffMaxBytes = 20 * 1024

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
	if len(beforeLines)*len(afterLines) > maxDiffLineProduct {
		return "[diff omitted: file is too large for inline diff]"
	}

	lines := diffLines(beforeLines, afterLines)
	var out strings.Builder
	out.WriteString("--- " + path + "\n")
	out.WriteString("+++ " + path + "\n")
	out.WriteString("@@\n")
	for _, line := range lines {
		part := renderDiffLine(line)
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

// renderDiffLine prefixes one diff line and ensures exactly one output newline.
func renderDiffLine(line diffLine) string {
	text := strings.TrimSuffix(line.Text, "\n")

	return fmt.Sprintf("%c%s\n", line.Prefix, text)
}
