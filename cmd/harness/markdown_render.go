package main

import "strings"

const (
	// markdownTableLeftAlign pads table cells on the right.
	markdownTableLeftAlign = "left"

	// markdownTableRightAlign pads table cells on the left.
	markdownTableRightAlign = "right"

	// markdownTableCenterAlign splits table cell padding across both sides.
	markdownTableCenterAlign = "center"
)

// markdownLines applies lightweight markdown rendering for chat output.
func markdownLines(text string, style terminalStyle) []string {
	return markdownLinesWithTone(text, style, terminalTone{})
}

// markdownLinesWithTone renders markdown while preserving a surrounding tone.
func markdownLinesWithTone(text string, style terminalStyle,
	tone terminalTone) []string {

	raw := splitPlainLines(text)
	if !style.enabled {
		return structuralMarkdownLines(raw, style)
	}

	baseOpen := style.openTone(tone)
	inFence := false
	lines := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		line := raw[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(line, "    ") {
			lines = append(lines, style.codeLine(line))
			continue
		}
		if table, next, ok := markdownTableLines(
			raw, i, style, baseOpen,
		); ok {

			lines = append(lines, table...)
			i = next - 1
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

// structuralMarkdownLines renders non-ANSI markdown structures in plain output.
func structuralMarkdownLines(raw []string, style terminalStyle) []string {
	inFence := false
	lines := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		line := raw[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			lines = append(lines, line)

			continue
		}
		if inFence || strings.HasPrefix(line, "    ") {
			lines = append(lines, line)

			continue
		}
		if table, next, ok := markdownTableLines(
			raw, i, style, "",
		); ok {

			lines = append(lines, table...)
			i = next - 1

			continue
		}
		lines = append(lines, line)
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

// markdownTableLines renders a pipe table beginning at start when one is found.
func markdownTableLines(raw []string, start int, style terminalStyle,
	baseOpen string) ([]string, int, bool) {

	if start+1 >= len(raw) || !strings.Contains(raw[start], "|") {
		return nil, start, false
	}
	delimiterCells, alignments, ok := markdownTableDelimiter(
		raw[start+1],
	)
	if !ok {
		return nil, start, false
	}

	rows := [][]string{splitMarkdownTableRow(raw[start])}
	rows = append(rows, delimiterCells)
	next := start + 2
	for next < len(raw) {
		line := raw[next]
		if strings.TrimSpace(line) == "" ||
			!strings.Contains(line, "|") {

			break
		}
		rows = append(rows, splitMarkdownTableRow(line))
		next++
	}

	return formatMarkdownTable(rows, alignments, style, baseOpen), next, true
}

// markdownTableDelimiter parses the alignment row for a markdown table.
func markdownTableDelimiter(line string) ([]string, []string, bool) {
	if !strings.Contains(line, "|") {
		return nil, nil, false
	}
	cells := splitMarkdownTableRow(line)
	if len(cells) == 0 {
		return nil, nil, false
	}

	alignments := make([]string, len(cells))
	for i, cell := range cells {
		alignment, ok := markdownTableAlignment(cell)
		if !ok {
			return nil, nil, false
		}
		alignments[i] = alignment
	}

	return cells, alignments, true
}

// markdownTableAlignment returns the column alignment declared by one cell.
func markdownTableAlignment(cell string) (string, bool) {
	trimmed := strings.TrimSpace(cell)
	if len(trimmed) < 3 {
		return "", false
	}
	leftColon := strings.HasPrefix(trimmed, ":")
	rightColon := strings.HasSuffix(trimmed, ":")
	dashes := strings.Trim(trimmed, ":")
	if len(dashes) < 3 || strings.Trim(dashes, "-") != "" {
		return "", false
	}
	if leftColon && rightColon {
		return markdownTableCenterAlign, true
	}
	if rightColon {
		return markdownTableRightAlign, true
	}

	return markdownTableLeftAlign, true
}

// splitMarkdownTableRow returns trimmed cells from a simple pipe table row.
func splitMarkdownTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}

	return cells
}

// formatMarkdownTable converts parsed markdown table rows into aligned text.
func formatMarkdownTable(rows [][]string, alignments []string,
	style terminalStyle, baseOpen string) []string {

	columnCount := markdownTableColumnCount(rows)
	widths := markdownTableWidths(rows, columnCount)
	lines := make([]string, 0, len(rows))
	for i, row := range rows {
		if i == 1 {
			lines = append(
				lines,
				style.styleSpan(
					markdownTableSeparator(widths), ansiDim,
					baseOpen,
				),
			)
			continue
		}

		bold := i == 0
		lines = append(
			lines, formatMarkdownTableRow(
				row, widths, alignments, bold, style, baseOpen,
			),
		)
	}

	return lines
}

// markdownTableColumnCount returns the widest row in a parsed table.
func markdownTableColumnCount(rows [][]string) int {
	columnCount := 0
	for _, row := range rows {
		if len(row) > columnCount {
			columnCount = len(row)
		}
	}

	return columnCount
}

// markdownTableWidths returns display widths for each table column.
func markdownTableWidths(rows [][]string, columnCount int) []int {
	widths := make([]int, columnCount)
	for i, row := range rows {
		if i == 1 {
			continue
		}
		for column := 0; column < columnCount; column++ {
			width := markdownTableCellWidth(
				markdownTableCell(row, column),
			)
			if width > widths[column] {
				widths[column] = width
			}
		}
	}
	for i, width := range widths {
		if width < 3 {
			widths[i] = 3
		}
	}

	return widths
}

// markdownTableCell returns one cell or an empty value for ragged rows.
func markdownTableCell(row []string, column int) string {
	if column >= len(row) {
		return ""
	}

	return row[column]
}

// markdownTableCellWidth estimates the terminal width of one markdown cell.
func markdownTableCellWidth(cell string) int {
	plain := strings.ReplaceAll(cell, "**", "")
	plain = strings.ReplaceAll(plain, "`", "")

	return len([]rune(plain))
}

// markdownTableSeparator returns a muted separator matching the table widths.
func markdownTableSeparator(widths []int) string {
	parts := make([]string, len(widths))
	for i, width := range widths {
		parts[i] = strings.Repeat("-", width)
	}

	return strings.Join(parts, "  ")
}

// formatMarkdownTableRow aligns and styles one visible table row.
func formatMarkdownTableRow(row []string, widths []int, alignments []string,
	bold bool, style terminalStyle, baseOpen string) string {

	cells := make([]string, len(widths))
	for column := range widths {
		raw := markdownTableCell(row, column)
		text := styleInlineMarkdown(raw, style, baseOpen)
		if bold {
			text = style.styleSpan(text, ansiBold, baseOpen)
		}
		alignment := markdownTableLeftAlign
		if column < len(alignments) {
			alignment = alignments[column]
		}
		cells[column] = padMarkdownTableCell(
			text, markdownTableCellWidth(raw), widths[column],
			alignment,
		)
	}

	return strings.Join(cells, "  ")
}

// padMarkdownTableCell pads styled table text according to one alignment.
func padMarkdownTableCell(text string, width int, target int,
	alignment string) string {

	padding := target - width
	if padding <= 0 {
		return text
	}

	switch alignment {
	case markdownTableRightAlign:
		return strings.Repeat(" ", padding) + text

	case markdownTableCenterAlign:
		left := padding / 2
		right := padding - left

		return strings.Repeat(" ", left) + text +
			strings.Repeat(" ", right)

	default:
		return text + strings.Repeat(" ", padding)
	}
}

// styleInlineMarkdown renders a small subset of inline markdown.
func styleInlineMarkdown(line string, style terminalStyle,
	baseOpen string) string {

	line = styleDelimited(line, "**", ansiBold, style, baseOpen)
	line = styleDelimited(line, "`", ansiCyan, style, baseOpen)

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

// styleSpan applies an inline style and then restores any surrounding tone.
func (s terminalStyle) styleSpan(text string, code string,
	baseOpen string) string {

	if !s.enabled {
		return text
	}

	return code + text + ansiReset + baseOpen
}
