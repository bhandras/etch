package fs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

	// NoGrepMatchesText is returned when literal search finds no matches.
	NoGrepMatchesText = "(no matches)"
)

// GrepRequest describes one bounded literal text search.
type GrepRequest struct {
	// Path is the file or directory where searching starts. Empty means the
	// current directory.
	Path string

	// Pattern is the non-empty literal text to find.
	Pattern string

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
	root := strings.TrimSpace(req.Path)
	if root == "" {
		root = "."
	}

	files, skippedDirs, err := grepFiles(ctx, root)
	if err != nil {
		return "", err
	}

	stats := grepStats{SkippedDirs: skippedDirs}
	matches, err := grepMatches(ctx, root, files, req, &stats)
	if err != nil {
		return "", err
	}

	return renderGrepResults(matches, stats), nil
}

// grepMatch stores one rendered literal search match.
type grepMatch struct {
	// Path is the display path for the matching file.
	Path string

	// Line is the 1-indexed line number that matched.
	Line int

	// Text is the matched line without its trailing newline.
	Text string
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
		if path != root && entry.IsDir() &&
			walkDepthExceeded(root, path) {

			skippedDirs++

			return filepath.SkipDir
		}
		if walkDepthExceeded(root, path) {
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
	req GrepRequest, stats *grepStats) ([]grepMatch, error) {

	limit := req.Limit
	if limit <= 0 {
		limit = DefaultGrepLimit
	}

	var matches []grepMatch
	for _, path := range files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		default:
		}
		fileMatches, skipped, err := grepFile(path, root, req)
		if err != nil {
			return nil, err
		}
		stats.SkippedBinaryFiles += skipped.BinaryFiles
		stats.SkippedLargeFiles += skipped.LargeFiles
		if len(fileMatches) > DefaultGrepPerFileLimit {
			stats.PerFileTruncated++
			fileMatches = fileMatches[:DefaultGrepPerFileLimit]
		}

		for _, match := range fileMatches {
			if len(matches) >= limit {
				stats.TruncatedMatches++
				continue
			}
			matches = append(matches, match)
		}
	}

	return matches, nil
}

// grepFile searches one file and reports whether it was skipped.
func grepFile(path string, root string, req GrepRequest) ([]grepMatch,
	grepFileSkip, error) {

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

	return grepContent(displayPath(root, path), string(content), req), grepFileSkip{}, nil
}

// grepFileSkip reports why a file was not searched.
type grepFileSkip struct {
	// BinaryFiles counts files skipped because they contain a NUL byte.
	BinaryFiles int

	// LargeFiles counts files skipped because they exceed the byte cap.
	LargeFiles int
}

// grepContent returns every literal match from one file.
func grepContent(path string, content string, req GrepRequest) []grepMatch {
	pattern := req.Pattern
	if req.IgnoreCase {
		pattern = strings.ToLower(pattern)
	}

	lines := strings.Split(content, "\n")
	var matches []grepMatch
	for i, line := range lines {
		haystack := line
		if req.IgnoreCase {
			haystack = strings.ToLower(line)
		}
		if strings.Contains(haystack, pattern) {
			matches = append(matches, grepMatch{
				Path: path,
				Line: i + 1,
				Text: lines[i],
			})
		}
	}

	return matches
}

// displayPath returns a slash-separated path for model-visible output.
func displayPath(root string, path string) string {
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
				&out, "%s:%d:%s", match.Path, match.Line,
				match.Text,
			)
		}
	}

	appendGrepNotice(&out, stats.TruncatedMatches, "truncated", "matches")
	appendGrepNotice(
		&out, stats.PerFileTruncated, "truncated",
		"files by per-file match cap",
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

// appendGrepNotice appends a parenthesized notice when count is non-zero.
func appendGrepNotice(out *strings.Builder, count int, verb string,
	unit string) {

	if count == 0 {
		return
	}
	out.WriteByte('\n')
	fmt.Fprintf(out, "(%s %d %s)", verb, count, unit)
}
