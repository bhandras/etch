package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// DefaultListLimit caps directory listings when callers do not provide
	// a limit.
	DefaultListLimit = 500

	// EmptyDirectoryText is returned when a directory has no visible
	// entries.
	EmptyDirectoryText = "(empty directory)"
)

// ListRequest describes one bounded non-recursive directory listing.
type ListRequest struct {
	// Path is the directory to list. Empty means the current directory.
	Path string

	// Limit caps the number of rendered entries. Non-positive values use
	// DefaultListLimit.
	Limit int
}

// List returns a deterministic, model-friendly listing of one directory.
func List(ctx context.Context, req ListRequest) (string, error) {
	dir := req.Path
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}

	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("stat directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read directory: %w", err)
	}

	names, skipped := renderableNames(ctx, entries)
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})

	if len(names) == 0 {
		if skipped > 0 {
			return fmt.Sprintf("%s\n(skipped %d internal "+
				"entries)",
				EmptyDirectoryText, skipped), nil
		}

		return EmptyDirectoryText, nil
	}

	limit := req.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > len(names) {
		limit = len(names)
	}

	var out strings.Builder
	for i := 0; i < limit; i++ {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(names[i])
	}

	omitted := len(names) - limit
	if omitted > 0 || skipped > 0 {
		out.WriteByte('\n')
	}
	if omitted > 0 {
		fmt.Fprintf(&out, "(truncated %d entries)", omitted)
		if skipped > 0 {
			out.WriteByte('\n')
		}
	}
	if skipped > 0 {
		fmt.Fprintf(&out, "(skipped %d internal entries)", skipped)
	}

	return out.String(), nil
}

// renderableNames converts directory entries into displayed names and skips
// internal directories.
func renderableNames(ctx context.Context,
	entries []os.DirEntry) ([]string, int) {

	var names []string
	skipped := 0
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return names, skipped

		default:
		}

		name := entry.Name()
		if entry.IsDir() && skipDir(name) {
			skipped++
			continue
		}
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, filepath.ToSlash(name))
	}

	return names, skipped
}

// skipDir reports whether a directory should be hidden from builtin tools.
func skipDir(name string) bool {
	switch name {
	case ".git", ".harness", "bin", "node_modules", "vendor":
		return true

	default:
		return false
	}
}
