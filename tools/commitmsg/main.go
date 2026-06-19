package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const (
	// defaultSubjectWidth is the maximum subject width inherited from the
	// darepo-client commit message convention.
	defaultSubjectWidth = 69

	// defaultBodyWidth is the maximum body line width inherited from the
	// darepo-client commit message convention.
	defaultBodyWidth = 72
)

var (
	subjectPattern = regexp.MustCompile(
		`^([A-Za-z0-9_][A-Za-z0-9+_.\-/]*): (.+)$`,
	)
	listPattern    = regexp.MustCompile(`^(\s*)([-+*]|\d+[.)])(\s+)(.*)$`)
	trailerPattern = regexp.MustCompile(`^[A-Za-z0-9-]+:\s+\S.*$`)
	fencePattern   = regexp.MustCompile(`^\s*(` + "```" + `|~~~)`)
	quotePattern   = regexp.MustCompile(`^(\s*)>\s?(.*)$`)
)

// commitmsg is a pure-Go, stdlib-only reimplementation of the
// lightninglabs/darepo-client commit message linter conventions.
type options struct {
	// subjectWidth is the maximum allowed width for the first commit
	// message line.
	subjectWidth int

	// bodyWidth is the maximum allowed width for non-structured body lines.
	bodyWidth int
}

// lintIssue records one line-specific commit message violation.
type lintIssue struct {
	// line is the one-based line number where the linter found the issue.
	line int

	// msg explains the failed rule in user-facing language.
	msg string
}

// main wires the process streams into the testable command runner.
func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run parses the top-level command and dispatches to the selected subcommand.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts := options{
		subjectWidth: defaultSubjectWidth,
		bodyWidth:    defaultBodyWidth,
	}

	fs := flag.NewFlagSet("commitmsg", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(
		&opts.subjectWidth, "subject-width", defaultSubjectWidth,
		"max subject width",
	)
	fs.IntVar(
		&opts.bodyWidth, "body-width", defaultBodyWidth,
		"max body width",
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	rest := fs.Args()
	if len(rest) == 0 {
		printUsage(stderr)

		return 2
	}

	switch rest[0] {
	case "lint":
		return lintCmd(rest[1:], opts, stdin, stdout, stderr)

	case "fmt":
		return fmtCmd(rest[1:], opts, stdin, stdout, stderr)

	default:
		fmt.Fprintf(stderr, "unknown command %q\n", rest[0])
		printUsage(stderr)

		return 2
	}
}

// printUsage writes the compact command overview used for parse errors.
func printUsage(w io.Writer) {
	fmt.Fprintln(
		w, "usage: commitmsg [--subject-width N] [--body-width N] "+
			"{lint,fmt} ...",
	)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(
		w, "  lint  lint a message from --file, --commit, --range, "+
			"or stdin",
	)
	fmt.Fprintln(
		w, "  fmt   format a message from --file, --commit, or stdin",
	)
}

// lintCmd validates one commit message source or every commit in a revision
// range.
func lintCmd(args []string, opts options, stdin io.Reader,
	stdout, stderr io.Writer) int {

	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "path to commit message file")
	commit := fs.String("commit", "", "commit revision to lint")
	revRange := fs.String("range", "", "revision range to lint")
	includeMerges := fs.Bool(
		"include-merges", false, "include merge commits in --range",
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	if *revRange != "" {
		revs, err := collectRevs(*revRange, *includeMerges)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		if len(revs) == 0 {
			fmt.Fprintf(
				stdout, "no commits found in range: %s\n",
				*revRange,
			)

			return 0
		}

		ok := true
		for _, rev := range revs {
			if !*includeMerges {
				merge, err := commitIsMerge(rev)
				if err != nil {
					fmt.Fprintln(stderr, "error:", err)

					return 1
				}
				if merge {
					continue
				}
			}

			msg, err := getCommitMessage(rev)
			if err != nil {
				fmt.Fprintln(stderr, "error:", err)

				return 1
			}
			subject, err := commitSubject(rev)
			if err != nil {
				fmt.Fprintln(stderr, "error:", err)

				return 1
			}
			if !lintOne(
				stdout, rev[:min(7, len(rev))]+" "+subject, msg,
				opts,
			) {

				ok = false
			}
		}
		if ok {
			return 0
		}

		return 1
	}

	msg, label, err := readMessage(*file, *commit, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	if lintOne(stdout, label, msg, opts) {
		return 0
	}

	return 1
}

// fmtCmd normalizes one commit message source and writes the formatted result.
func fmtCmd(args []string, opts options, stdin io.Reader,
	stdout, stderr io.Writer) int {

	fs := flag.NewFlagSet("fmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "path to commit message file")
	commit := fs.String("commit", "", "commit revision to read")
	inPlace := fs.Bool(
		"in-place", false, "write formatted output back to --file",
	)
	check := fs.Bool(
		"check", false, "exit non-zero if formatting would change",
	)
	decode := fs.Bool(
		"decode-escaped-newlines", false,
		`decode literal "\n" body sequences`,
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	msg, _, err := readMessage(*file, *commit, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	formatted := formatMessage(msg, opts, *decode)
	if *check {
		if msg == formatted {
			return 0
		}
		fmt.Fprintln(stdout, "message is not properly formatted")

		return 1
	}

	if *inPlace {
		if *file == "" {
			fmt.Fprintln(
				stderr, "error: --in-place requires --file",
			)

			return 1
		}
		if err := os.WriteFile(
			*file, []byte(formatted), 0o644,
		); err != nil {

			fmt.Fprintln(stderr, "error:", err)

			return 1
		}

		return 0
	}

	fmt.Fprint(stdout, formatted)

	return 0
}

// lintOne prints the result for a single message and reports whether it passed.
func lintOne(w io.Writer, label, msg string, opts options) bool {
	issues := lintMessage(msg, opts)
	if len(issues) == 0 {
		fmt.Fprintf(w, "%s: OK\n", label)

		return true
	}

	fmt.Fprintf(w, "%s: FAIL\n", label)
	for _, issue := range issues {
		fmt.Fprintf(w, "  L%d: %s\n", issue.line, issue.msg)
	}

	return false
}

// readMessage loads commit message text from a file, a git revision, or stdin.
func readMessage(file, commit string, stdin io.Reader) (string, string, error) {
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", "", err
		}

		return string(data), file, nil
	}

	if commit != "" {
		msg, err := getCommitMessage(commit)
		if err != nil {
			return "", "", err
		}

		return msg, commit, nil
	}

	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", "", err
	}
	if len(data) == 0 {
		return "", "", errors.New("no input provided: use --file, " +
			"--commit, --range, or stdin")
	}

	return string(data), "<stdin>", nil
}

// runGit executes git and returns trimmed stdout for helpers that inspect
// commits.
func runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "),
			err, strings.TrimSpace(string(out)))
	}

	return strings.TrimRight(string(out), "\n"), nil
}

// getCommitMessage returns the full message body for one git revision.
func getCommitMessage(rev string) (string, error) {
	return runGit("show", "-s", "--format=%B", rev)
}

// commitSubject returns the subject line for one git revision.
func commitSubject(rev string) (string, error) {
	return runGit("show", "-s", "--format=%s", rev)
}

// commitIsMerge reports whether a git revision has more than one parent.
func commitIsMerge(rev string) (bool, error) {
	out, err := runGit("rev-list", "--parents", "-n", "1", rev)
	if err != nil {
		return false, err
	}

	return len(strings.Fields(out)) > 2, nil
}

// collectRevs expands a revision range into commits in chronological order.
func collectRevs(revRange string, includeMerges bool) ([]string, error) {
	args := []string{"rev-list", "--reverse", revRange}
	if !includeMerges {
		args = []string{
			"rev-list",
			"--no-merges",
			"--reverse",
			revRange,
		}
	}
	out, err := runGit(args...)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}

	return strings.Fields(out), nil
}

// lintMessage checks one complete commit message against the project
// convention.
func lintMessage(msg string, opts options) []lintIssue {
	subject, body := splitSubjectBody(msg)
	var issues []lintIssue

	if subject == "" {
		return []lintIssue{
			{
				line: 1,
				msg:  "empty commit message subject",
			},
		}
	}
	if len(subject) > opts.subjectWidth {
		issues = append(issues, lintIssue{
			line: 1,
			msg: fmt.Sprintf(
				"subject length %d exceeds %d chars",
				len(subject),
				opts.subjectWidth,
			),
		})
	}

	match := subjectPattern.FindStringSubmatch(subject)
	if match == nil {
		issues = append(issues, lintIssue{
			line: 1,
			msg:  `subject must match "<package>: <summary>"`,
		})
	} else if strings.HasSuffix(match[2], ".") {
		issues = append(issues, lintIssue{
			line: 1,
			msg:  "subject summary should not end with period",
		})
	}

	if len(body) > 0 {
		if strings.TrimSpace(body[0]) != "" {
			issues = append(issues, lintIssue{
				line: 2,
				msg:  "body must be separated from subject by one blank line",
			})
		} else {
			leading := 0
			for leading < len(body) &&
				strings.TrimSpace(body[leading]) == "" {

				leading++
			}
			if leading > 1 && leading < len(body) {
				issues = append(issues, lintIssue{
					line: 2,
					msg:  "use exactly one blank line between subject and body",
				})
			}
		}
	}

	inFence := false
	for i, line := range body {
		lineNo := i + 2
		if strings.Contains(line, `\n`) {
			issues = append(issues, lintIssue{
				line: lineNo,
				msg:  `found literal "\n"; use real newlines in commit body`,
			})
		}
		if fencePattern.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence || strings.TrimSpace(line) == "" {
			continue
		}
		if isIndentedCode(line) || isTrailer(line) {
			continue
		}
		if len(line) > opts.bodyWidth {
			issues = append(issues, lintIssue{
				line: lineNo,
				msg: fmt.Sprintf(
					"body line length %d exceeds "+
						"%d chars",
					len(line),
					opts.bodyWidth,
				),
			})
		}
	}

	return issues
}

// formatMessage normalizes subject spacing, body wrapping, and blank-line
// layout.
func formatMessage(msg string, opts options, decodeEscaped bool) string {
	subject, body := splitSubjectBody(msg)
	subject = strings.TrimSpace(subject)
	if decodeEscaped {
		body = decodeEscapedNewlines(body)
	}

	if match := subjectPattern.FindStringSubmatch(subject); match != nil {
		prefix := match[1] + ": "
		width := max(10, opts.subjectWidth-len(prefix))
		subject = prefix +
			strings.Join(
				wrapText(
					strings.TrimSpace(match[2]), width, "",
					"",
				),
				" ",
			)
	} else {
		subject = strings.Join(
			wrapText(subject, opts.subjectWidth, "", ""),
			" ",
		)
	}

	body = formatBody(body, opts.bodyWidth)
	if len(body) == 0 {
		return subject + "\n"
	}

	return subject + "\n\n" + strings.Join(body, "\n") + "\n"
}

// splitSubjectBody separates the first line from the remaining message body.
func splitSubjectBody(msg string) (string, []string) {
	msg = strings.ReplaceAll(msg, "\r\n", "\n")
	lines := strings.Split(msg, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return "", nil
	}

	return lines[0], lines[1:]
}

// formatBody rewrites body text while preserving markdown-like structured
// blocks.
func formatBody(lines []string, width int) []string {
	body := trimLeadingBlank(lines)
	body = trimTrailingBlank(body)
	if len(body) == 0 {
		return nil
	}

	var out []string
	for i := 0; i < len(body); {
		line := body[i]
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			i++
			continue
		}
		if fencePattern.MatchString(line) {
			next, block := consumeFence(body, i)
			out = append(out, block...)
			i = next
			continue
		}
		if isIndentedCode(line) {
			next, block := consumeIndented(body, i)
			out = append(out, block...)
			i = next
			continue
		}
		if isListItem(line) {
			for i < len(body) && strings.TrimSpace(body[i]) != "" &&
				isListItem(body[i]) {

				next, item := consumeListItem(body, i, width)
				out = append(out, item...)
				i = next
			}
			continue
		}
		if isQuoteLine(line) {
			next, quoted := consumeQuote(body, i, width)
			out = append(out, quoted...)
			i = next
			continue
		}
		if isTrailer(line) {
			out = append(out, strings.TrimRight(line, " \t"))
			i++
			continue
		}

		next, parts := consumeParagraph(body, i)
		out = append(out, formatParagraph(parts, width)...)
		i = next
	}

	return collapseBlankLines(out)
}

// consumeParagraph gathers adjacent plain-text lines for wrapping as one
// paragraph.
func consumeParagraph(body []string, start int) (int, []string) {
	var parts []string
	i := start
	for i < len(body) {
		line := body[i]
		if strings.TrimSpace(line) == "" ||
			fencePattern.MatchString(line) ||
			isListItem(line) ||
			isIndentedCode(line) ||
			isTrailer(line) ||
			isQuoteLine(line) {

			break
		}
		parts = append(parts, strings.TrimSpace(line))
		i++
	}

	return i, parts
}

// formatParagraph joins and wraps paragraph fragments after empty fragments are
// removed.
func formatParagraph(parts []string, width int) []string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		return nil
	}

	return wrapText(strings.Join(kept, " "), width, "", "")
}

// consumeQuote gathers adjacent markdown quote lines and wraps the quoted text.
func consumeQuote(body []string, start, width int) (int, []string) {
	i := start
	var quoted []string
	indent := ""
	for i < len(body) {
		line := body[i]
		if strings.TrimSpace(line) == "" {
			break
		}
		match := quotePattern.FindStringSubmatch(line)
		if match == nil {
			break
		}
		indent = match[1]
		quoted = append(quoted, strings.TrimSpace(match[2]))
		i++
	}

	text := strings.TrimSpace(strings.Join(quoted, " "))
	prefix := indent + "> "
	if text == "" {
		return i, []string{strings.TrimRight(prefix, " ")}
	}

	return i, wrapText(text, width, prefix, prefix)
}

// consumeListItem gathers one markdown list item and its continuation lines.
func consumeListItem(body []string, start, width int) (int, []string) {
	first := body[start]
	match := listPattern.FindStringSubmatch(first)
	if match == nil {
		return start + 1, []string{strings.TrimRight(first, " \t")}
	}

	indent := match[1]
	marker := match[2]
	content := []string{strings.TrimSpace(match[4])}
	i := start + 1
	for i < len(body) {
		line := body[i]
		if strings.TrimSpace(line) == "" {
			break
		}
		next := listPattern.FindStringSubmatch(line)
		if next != nil && len(next[1]) <= len(indent) {
			break
		}
		if fencePattern.MatchString(line) || isTrailer(line) {
			break
		}
		content = append(content, strings.TrimSpace(line))
		i++
	}

	prefix := indent + marker + " "
	text := strings.TrimSpace(strings.Join(content, " "))
	if text == "" {
		return i, []string{strings.TrimRight(prefix, " ")}
	}

	return i, wrapText(
		text, width, prefix,
		strings.Repeat(
			" ", len(prefix),
		),
	)
}

// consumeFence preserves a fenced code block exactly except for trailing
// whitespace.
func consumeFence(body []string, start int) (int, []string) {
	var out []string
	for i := start; i < len(body); i++ {
		line := strings.TrimRight(body[i], " \t")
		out = append(out, line)
		if i > start && fencePattern.MatchString(line) {
			return i + 1, out
		}
	}

	return len(body), out
}

// consumeIndented preserves an indented code block exactly except for trailing
// whitespace.
func consumeIndented(body []string, start int) (int, []string) {
	var out []string
	i := start
	for i < len(body) {
		line := body[i]
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			i++
			continue
		}
		if !isIndentedCode(line) {
			break
		}
		out = append(out, strings.TrimRight(line, " \t"))
		i++
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}

	return i, out
}

// wrapText breaks text into width-limited lines without splitting long tokens.
func wrapText(text string, width int, initialIndent,
	subsequentIndent string) []string {

	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return []string{strings.TrimRight(initialIndent, " ")}
	}

	var lines []string
	indent := initialIndent
	var current bytes.Buffer
	current.WriteString(indent)
	currentLen := len(indent)

	for _, word := range strings.Fields(text) {
		sep := 0
		if currentLen > len(indent) {
			sep = 1
		}
		if currentLen+sep+len(word) > width &&
			currentLen > len(indent) {

			lines = append(lines, current.String())
			indent = subsequentIndent
			current.Reset()
			current.WriteString(indent)
			currentLen = len(indent)
			sep = 0
		}
		if sep == 1 {
			current.WriteByte(' ')
			currentLen++
		}
		current.WriteString(word)
		currentLen += len(word)
	}

	if currentLen > len(indent) || len(lines) == 0 {
		lines = append(lines, current.String())
	}

	return lines
}

// decodeEscapedNewlines expands accidental literal newline escape sequences in
// body lines.
func decodeEscapedNewlines(lines []string) []string {
	var out []string
	for _, line := range lines {
		if !strings.Contains(line, `\n`) {
			out = append(out, line)
			continue
		}
		out = append(out, splitUnescapedNewlineEscapes(line)...)
	}

	return out
}

// splitUnescapedNewlineEscapes splits a single line at unescaped literal "\n"
// sequences.
func splitUnescapedNewlineEscapes(line string) []string {
	var out []string
	var b strings.Builder
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			if ch == 'n' {
				out = append(out, b.String())
				b.Reset()
			} else {
				b.WriteByte('\\')
				b.WriteByte(ch)
			}
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		b.WriteByte(ch)
	}
	if escaped {
		b.WriteByte('\\')
	}
	out = append(out, b.String())

	return out
}

// trimLeadingBlank removes blank lines before the meaningful body content.
func trimLeadingBlank(lines []string) []string {
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}

	return lines[i:]
}

// trimTrailingBlank removes blank lines after the meaningful body content.
func trimTrailingBlank(lines []string) []string {
	i := len(lines)
	for i > 0 && strings.TrimSpace(lines[i-1]) == "" {
		i--
	}

	return lines[:i]
}

// collapseBlankLines reduces repeated blank body lines to a single separator.
func collapseBlankLines(lines []string) []string {
	var out []string
	blank := false
	for _, line := range lines {
		isBlank := strings.TrimSpace(line) == ""
		if isBlank && blank {
			continue
		}
		out = append(out, line)
		blank = isBlank
	}

	return out
}

// isListItem reports whether a line starts a markdown-like list item.
func isListItem(line string) bool {
	return listPattern.MatchString(line)
}

// isTrailer reports whether a line has git trailer syntax.
func isTrailer(line string) bool {
	return trailerPattern.MatchString(line)
}

// isIndentedCode reports whether a line should be preserved as indented code.
func isIndentedCode(line string) bool {
	if strings.HasPrefix(line, "\t") {
		return true
	}
	if strings.TrimSpace(line) == "" {
		return false
	}
	spaces := 0
	for _, ch := range line {
		if ch != ' ' {
			break
		}
		spaces++
	}

	return spaces >= 4
}

// isQuoteLine reports whether a line starts markdown quote text.
func isQuoteLine(line string) bool {
	return quotePattern.MatchString(line)
}

// min returns the smaller integer and keeps revision label slicing readable.
func min(a, b int) int {
	if a < b {
		return a
	}

	return b
}

// max returns the larger integer and keeps width fallback calculations
// readable.
func max(a, b int) int {
	if a > b {
		return a
	}

	return b
}
