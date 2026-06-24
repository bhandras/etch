package fs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"etch/internal/textutil"
)

const (
	// DefaultGrepLimit caps total literal search matches when callers do
	// not provide a narrower limit.
	DefaultGrepLimit = 100

	// DefaultGrepPerFileLimit caps matches from one file so one noisy file
	// cannot dominate model context.
	DefaultGrepPerFileLimit = 20

	// DefaultGrepMaxFileBytes skips unusually large files in the first
	// dependency-free search implementation.
	DefaultGrepMaxFileBytes = 1024 * 1024

	// DefaultGrepMaxLineBytes caps each rendered search line.
	DefaultGrepMaxLineBytes = 500

	// DefaultGrepMaxContext caps before/after context lines around matches.
	DefaultGrepMaxContext = 5

	// DefaultGrepMaxPaths caps multi-root searches so one call cannot turn
	// into an unbounded project-wide scan with many roots.
	DefaultGrepMaxPaths = 8

	// NoGrepMatchesText is returned when literal search finds no matches.
	NoGrepMatchesText = "(no matches)"
)

// GrepRequest describes one bounded literal text search.
type GrepRequest struct {
	// Path is the file or directory where searching starts. Empty means the
	// current directory.
	Path string

	// Paths are files or directories to search in one call. When non-empty,
	// Paths take precedence over Path.
	Paths []string `json:"paths,omitempty"`

	// Pattern is the non-empty literal text to find.
	Pattern string

	// Regex treats Pattern as Go RE2 syntax instead of literal text.
	Regex bool

	// Glob optionally filters slash-separated relative paths before search.
	Glob string

	// Context includes this many surrounding lines around each match.
	Context int

	// Limit caps the total number of rendered matches. Non-positive values
	// use DefaultGrepLimit.
	Limit int

	// IgnoreCase enables case-insensitive literal matching.
	IgnoreCase bool
}

// Grep returns deterministic literal search matches with file and line numbers.
func Grep(ctx context.Context, req GrepRequest) (string, error) {
	pattern := req.Pattern
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	matcher, err := newGrepMatcher(req)
	if err != nil {
		return "", err
	}
	roots, err := grepRoots(req)
	if err != nil {
		return "", err
	}
	stats := grepStats{}
	var matches []grepMatch
	for _, root := range roots {
		files, skippedDirs, err := grepFiles(ctx, root)
		if err != nil {
			return "", err
		}
		stats.SkippedDirs += skippedDirs
		matches, err = grepMatches(
			ctx, root, files, req, matcher, &stats, matches,
			len(roots) > 1,
		)
		if err != nil {
			return "", err
		}
	}

	return renderGrepResults(matches, stats), nil
}

// grepRoots returns the caller-selected search roots, including a narrow
// compatibility path for whitespace-separated roots in Path.
func grepRoots(req GrepRequest) ([]string, error) {
	if len(req.Paths) > 0 {
		return normalizeGrepRoots(req.Paths)
	}
	root := strings.TrimSpace(req.Path)
	if root == "" {
		return []string{"."}, nil
	}
	if _, err := os.Stat(root); err == nil {
		return []string{root}, nil
	}
	fields := strings.Fields(root)
	if len(fields) <= 1 {
		return []string{root}, nil
	}
	roots, err := normalizeGrepRoots(fields)
	if err != nil {
		return []string{root}, nil
	}

	return roots, nil
}

// normalizeGrepRoots trims, validates, and caps multi-root grep requests.
func normalizeGrepRoots(paths []string) ([]string, error) {
	roots := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for index, path := range paths {
		root := strings.TrimSpace(path)
		if root == "" {
			return nil, fmt.Errorf("paths[%d] is empty", index)
		}
		if _, err := os.Stat(root); err != nil {
			return nil, fmt.Errorf("stat search root %q: %w", root,
				err)
		}
		clean := filepath.Clean(root)
		if seen[clean] {
			continue
		}
		roots = append(roots, root)
		seen[clean] = true
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("paths must contain at least one path")
	}
	if len(roots) > DefaultGrepMaxPaths {
		return nil, fmt.Errorf("paths accepts at most %d entries",
			DefaultGrepMaxPaths)
	}

	return roots, nil
}

// grepMatch stores one rendered literal search match.
type grepMatch struct {
	// Path is the display path for the matching file.
	Path string

	// Line is the 1-indexed line number that matched.
	Line int

	// Text is the matched line without its trailing newline.
	Text string

	// Match reports whether this row is the line that matched.
	Match bool

	// Truncated reports whether Text was shortened for display.
	Truncated bool
}

// grepStats stores skip and truncation notices for rendered output.
type grepStats struct {
	// SkippedDirs counts directories skipped during traversal.
	SkippedDirs int

	// SkippedBinaryFiles counts files skipped because they look binary.
	SkippedBinaryFiles int

	// SkippedLargeFiles counts files skipped because they exceed the size
	// cap.
	SkippedLargeFiles int

	// TruncatedMatches counts matches omitted by the total output limit.
	TruncatedMatches int

	// PerFileTruncated counts files whose matches exceeded the per-file
	// cap.
	PerFileTruncated int

	// TruncatedLines counts rendered lines shortened by the line cap.
	TruncatedLines int
}

// grepFiles returns text-search candidate files under root.
func grepFiles(ctx context.Context, root string) ([]string, int, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, 0, fmt.Errorf("stat search root: %w", err)
	}
	if !info.IsDir() {
		return []string{root}, 0, nil
	}

	var files []string
	skippedDirs := 0
	ignores := loadIgnoreMatcher(root)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry,
		walkErr error) error {

		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()

		default:
		}
		if path != root && entry.IsDir() && skipDir(entry.Name()) {
			skippedDirs++

			return filepath.SkipDir
		}
		rendered, err := relativeDisplayPath(root, path, entry.IsDir())
		if err != nil {
			return err
		}
		if path != root && entry.IsDir() &&
			(walkDepthExceeded(root, path) ||
				ignores.Ignored(rendered, true)) {

			skippedDirs++

			return filepath.SkipDir
		}
		if walkDepthExceeded(root, path) ||
			ignores.Ignored(rendered, false) {
			return nil
		}
		if entry.Type().IsRegular() {
			files = append(files, path)
		}

		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("walk search root: %w", err)
	}
	sortDisplayPaths(files)

	return files, skippedDirs, nil
}

// grepMatches searches candidate files and records bounded matches.
func grepMatches(ctx context.Context, root string, files []string,
	req GrepRequest, matcher grepMatcher, stats *grepStats,
	matches []grepMatch, displayFromCwd bool) ([]grepMatch, error) {

	limit := req.Limit
	if limit <= 0 {
		limit = DefaultGrepLimit
	}

	for _, path := range files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		default:
		}
		display := displayPath(root, path, displayFromCwd)
		if req.Glob != "" {
			ok, err := matchPathGlob(req.Glob, display)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		fileMatches, skipped, err := grepFile(
			path, display, req, matcher,
		)
		if err != nil {
			return nil, err
		}
		stats.SkippedBinaryFiles += skipped.BinaryFiles
		stats.SkippedLargeFiles += skipped.LargeFiles
		fileMatches, truncated := capGrepMatches(
			fileMatches, DefaultGrepPerFileLimit,
		)
		if truncated {
			stats.PerFileTruncated++
		}

		remaining := limit - grepMatchCount(matches)
		if remaining <= 0 {
			stats.TruncatedMatches += grepMatchCount(fileMatches)
			continue
		}
		beforeCount := grepMatchCount(fileMatches)
		fileMatches, truncated = capGrepMatches(fileMatches, remaining)
		if truncated {
			stats.TruncatedMatches += beforeCount -
				grepMatchCount(fileMatches)
		}
		for _, match := range fileMatches {
			if match.Truncated {
				stats.TruncatedLines++
			}
			matches = append(matches, match)
		}
	}

	return matches, nil
}

// grepFile searches one file and reports whether it was skipped.
func grepFile(path string, display string, req GrepRequest,
	matcher grepMatcher) ([]grepMatch, grepFileSkip, error) {

	file, err := os.Open(path)
	if err != nil {
		return nil, grepFileSkip{}, fmt.Errorf("open search file: %w",
			err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, grepFileSkip{}, fmt.Errorf("stat search file: %w",
			err)
	}
	if info.Size() > DefaultGrepMaxFileBytes {
		return nil, grepFileSkip{LargeFiles: 1}, nil
	}

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, grepFileSkip{}, fmt.Errorf("read search file: %w",
			err)
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, grepFileSkip{BinaryFiles: 1}, nil
	}

	return grepContent(display, string(content), req, matcher), grepFileSkip{}, nil
}

// grepFileSkip reports why a file was not searched.
type grepFileSkip struct {
	// BinaryFiles counts files skipped because they contain a NUL byte.
	BinaryFiles int

	// LargeFiles counts files skipped because they exceed the byte cap.
	LargeFiles int
}

// grepContent returns every literal match from one file.
func grepContent(path string, content string, req GrepRequest,
	matcher grepMatcher) []grepMatch {

	lines := strings.Split(content, "\n")
	var matches []grepMatch
	for i, line := range lines {
		if matcher.Match(line) {
			matches = appendContextRows(
				matches, path, lines, i,
				grepContext(req.Context),
			)
		}
	}

	return matches
}

// grepMatcher stores the compiled line matcher for one grep request.
type grepMatcher struct {
	// Pattern stores the normalized literal pattern.
	Pattern string

	// Regex stores the compiled regexp when regex mode is enabled.
	Regex *regexp.Regexp

	// IgnoreCase enables case-insensitive literal matching.
	IgnoreCase bool
}

// newGrepMatcher prepares a literal or regexp line matcher.
func newGrepMatcher(req GrepRequest) (grepMatcher, error) {
	if req.Regex {
		pattern := req.Pattern
		if req.IgnoreCase {
			pattern = "(?i:" + pattern + ")"
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return grepMatcher{}, fmt.Errorf("compile regex: %w",
				err)
		}

		return grepMatcher{Regex: compiled}, nil
	}

	pattern := req.Pattern
	if req.IgnoreCase {
		pattern = strings.ToLower(pattern)
	}

	return grepMatcher{Pattern: pattern, IgnoreCase: req.IgnoreCase}, nil
}

// Match reports whether one line satisfies the grep matcher.
func (m grepMatcher) Match(line string) bool {
	if m.Regex != nil {
		return m.Regex.MatchString(line)
	}
	haystack := line
	if m.IgnoreCase {
		haystack = strings.ToLower(line)
	}

	return strings.Contains(haystack, m.Pattern)
}

// grepContext clamps caller-requested context to the supported range.
func grepContext(context int) int {
	if context < 0 {
		return 0
	}
	if context > DefaultGrepMaxContext {
		return DefaultGrepMaxContext
	}

	return context
}

// appendContextRows appends one match and bounded surrounding context rows.
func appendContextRows(rows []grepMatch, path string, lines []string, index int,
	context int) []grepMatch {

	start := index - context
	if start < 0 {
		start = 0
	}
	end := index + context + 1
	if end > len(lines) {
		end = len(lines)
	}
	seen := grepRowsSeen(rows, path)
	for i := start; i < end; i++ {
		key := grepRowKey{Path: path, Line: i + 1}
		if seen[key] {
			continue
		}
		text, truncated := textutil.TruncateUTF8Bytes(
			lines[i], DefaultGrepMaxLineBytes,
		)
		rows = append(rows, grepMatch{
			Path:      path,
			Line:      i + 1,
			Text:      text,
			Match:     i == index,
			Truncated: truncated,
		})
		seen[key] = true
	}

	return rows
}

// grepRowKey identifies one rendered file line.
type grepRowKey struct {
	// Path is the rendered file path.
	Path string

	// Line is the one-based line number.
	Line int
}

// grepRowsSeen indexes rendered rows by path and line.
func grepRowsSeen(rows []grepMatch, path string) map[grepRowKey]bool {
	seen := make(map[grepRowKey]bool, len(rows))
	for _, row := range rows {
		if row.Path == path {
			seen[grepRowKey{Path: row.Path, Line: row.Line}] = true
		}
	}

	return seen
}

// capGrepMatches keeps at most limit matching rows plus their context rows.
func capGrepMatches(rows []grepMatch, limit int) ([]grepMatch, bool) {
	matches := 0
	for index, row := range rows {
		if !row.Match {
			continue
		}
		matches++
		if matches > limit {
			return rows[:index], true
		}
	}

	return rows, false
}

// grepMatchCount counts actual match rows, excluding context rows.
func grepMatchCount(rows []grepMatch) int {
	count := 0
	for _, row := range rows {
		if row.Match {
			count++
		}
	}

	return count
}

// displayPath returns a slash-separated path for model-visible output.
func displayPath(root string, path string, displayFromCwd bool) string {
	if displayFromCwd {
		return filepath.ToSlash(filepath.Clean(path))
	}
	if info, err := os.Stat(root); err == nil && info.IsDir() {
		if rel, err := filepath.Rel(root, path); err == nil {
			return filepath.ToSlash(rel)
		}
	}

	return filepath.ToSlash(filepath.Clean(path))
}

// renderGrepResults applies output formatting and skip notices.
func renderGrepResults(matches []grepMatch, stats grepStats) string {
	var out strings.Builder
	if len(matches) == 0 {
		out.WriteString(NoGrepMatchesText)
	} else {
		for i, match := range matches {
			if i > 0 {
				out.WriteByte('\n')
			}
			fmt.Fprintf(
				&out, "%s%s%d:%s", match.Path,
				grepLineSeparator(match), match.Line,
				match.Text,
			)
			if match.Truncated {
				out.WriteString(" ... [line truncated]")
			}
		}
	}

	appendGrepNotice(&out, stats.TruncatedMatches, "truncated", "matches")
	appendGrepNotice(
		&out, stats.PerFileTruncated, "truncated",
		"files by per-file match cap",
	)
	appendGrepNotice(
		&out, stats.TruncatedLines, "truncated", "long lines",
	)
	appendGrepNotice(
		&out, stats.SkippedDirs, "skipped", "directories",
	)
	appendGrepNotice(
		&out, stats.SkippedBinaryFiles, "skipped", "binary files",
	)
	appendGrepNotice(
		&out, stats.SkippedLargeFiles, "skipped", "large files",
	)

	return out.String()
}

// grepLineSeparator returns the match or context line separator.
func grepLineSeparator(match grepMatch) string {
	if match.Match {
		return ":"
	}

	return "-"
}

// appendGrepNotice appends a parenthesized notice when count is non-zero.
func appendGrepNotice(out *strings.Builder, count int, verb string,
	unit string) {

	if count == 0 {
		return
	}
	out.WriteByte('\n')
	fmt.Fprintf(out, "(%s %d %s)", verb, count, unit)
}
