package fs

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// DefaultFindLimit caps recursive path search results when callers do
	// not provide a narrower limit.
	DefaultFindLimit = 500

	// NoFindMatchesText is returned when recursive path search finds no
	// matching files or directories.
	NoFindMatchesText = "(no matches)"
)

// FindRequest describes one bounded recursive path search.
type FindRequest struct {
	// Path is the file or directory where searching starts. Empty means the
	// current directory.
	Path string

	// Query is matched case-insensitively against slash-separated relative
	// paths. Empty means every non-internal path matches.
	Query string

	// Glob optionally filters slash-separated relative paths. Basename
	// patterns such as *.go and recursive patterns such as **/*_test.go
	// are supported.
	Glob string

	// Limit caps the number of rendered matches. Non-positive values use
	// DefaultFindLimit.
	Limit int
}

// Find returns deterministic paths matching a simple substring query.
func Find(ctx context.Context, req FindRequest) (string, error) {
	root := strings.TrimSpace(req.Path)
	if root == "" {
		root = "."
	}

	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("stat search root: %w", err)
	}

	var paths []string
	skipped := 0
	if info.IsDir() {
		paths, skipped, err = findInDirectory(ctx, root, req)
	} else {
		paths, err = findSingleFile(root, req)
	}
	if err != nil {
		return "", err
	}

	return renderFindResults(paths, skipped, req.Limit), nil
}

// findInDirectory recursively searches one directory tree.
func findInDirectory(ctx context.Context, root string, req FindRequest) (
	[]string, int, error) {

	var paths []string
	skipped := 0
	filter := findFilter{
		Query: strings.ToLower(req.Query),
		Glob:  req.Glob,
	}
	ignores := loadIgnoreMatcher(root)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry,
		walkErr error) error {

		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()

		default:
		}
		if path == root {
			return nil
		}
		rendered, err := relativeDisplayPath(root, path, entry.IsDir())
		if err != nil {
			return err
		}
		if entry.IsDir() && (skipDir(entry.Name()) ||
			walkDepthExceeded(root, path) ||
			ignores.Ignored(rendered, true)) {

			skipped++

			return filepath.SkipDir
		}
		if walkDepthExceeded(root, path) ||
			ignores.Ignored(rendered, false) {
			return nil
		}

		matches, err := pathMatchesFind(rendered, filter)
		if err != nil {
			return err
		}
		if matches {
			paths = append(paths, rendered)
		}

		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("walk search root: %w", err)
	}
	sortDisplayPaths(paths)

	return paths, skipped, nil
}

// findSingleFile matches a non-directory search root against the query.
func findSingleFile(path string, req FindRequest) ([]string, error) {
	rendered := filepath.ToSlash(filepath.Clean(path))
	matches, err := pathMatchesFind(rendered, findFilter{
		Query: strings.ToLower(req.Query),
		Glob:  req.Glob,
	})
	if err != nil || !matches {
		return nil, err
	}

	return []string{rendered}, nil
}

// findFilter stores normalized path search predicates.
type findFilter struct {
	// Query is the lowercase substring predicate.
	Query string

	// Glob is the optional slash-separated path glob predicate.
	Glob string
}

// pathMatchesFind reports whether rendered matches all find predicates.
func pathMatchesFind(rendered string, filter findFilter) (bool, error) {
	if filter.Query != "" &&
		!strings.Contains(strings.ToLower(rendered), filter.Query) {
		return false, nil
	}
	if filter.Glob == "" {
		return true, nil
	}

	return matchPathGlob(filter.Glob, rendered)
}

// relativeDisplayPath returns a slash-separated path relative to root.
func relativeDisplayPath(root string, path string, isDir bool) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("render relative path: %w", err)
	}
	rel = filepath.ToSlash(rel)
	if isDir {
		rel += "/"
	}

	return rel, nil
}

// sortDisplayPaths orders rendered paths case-insensitively and stably.
func sortDisplayPaths(paths []string) {
	sort.SliceStable(paths, func(i, j int) bool {
		left := strings.ToLower(paths[i])
		right := strings.ToLower(paths[j])
		if left == right {
			return paths[i] < paths[j]
		}

		return left < right
	})
}

// renderFindResults applies output limits and skipped-directory notices.
func renderFindResults(paths []string, skipped int, limit int) string {
	if limit <= 0 {
		limit = DefaultFindLimit
	}
	if limit > len(paths) {
		limit = len(paths)
	}

	var out strings.Builder
	if limit == 0 {
		out.WriteString(NoFindMatchesText)
	} else {
		for i := 0; i < limit; i++ {
			if i > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(paths[i])
		}
	}

	omitted := len(paths) - limit
	if omitted > 0 {
		out.WriteByte('\n')
		fmt.Fprintf(&out, "(truncated %d matches)", omitted)
	}
	if skipped > 0 {
		out.WriteByte('\n')
		fmt.Fprintf(&out, "(skipped %d directories)", skipped)
	}

	return out.String()
}
