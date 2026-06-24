package fs

import (
	"path/filepath"
	"strings"
)

const (
	// defaultWalkMaxDepth caps recursive builtin tool traversal in very
	// deep trees.
	defaultWalkMaxDepth = 20
)

// skipDir reports whether a directory should be hidden from builtin tools.
func skipDir(name string) bool {
	switch name {
	case ".git", ".etch", "bin", "node_modules", "vendor":
		return true

	default:
		return false
	}
}

// walkDepthExceeded reports whether path is deeper than the builtin limit.
func walkDepthExceeded(root string, path string) bool {
	depth, ok := walkDepth(root, path)
	if !ok {
		return false
	}

	return depth > defaultWalkMaxDepth
}

// walkDepth returns the number of path elements below root.
func walkDepth(root string, path string) (int, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0, err == nil
	}

	return len(strings.Split(rel, string(filepath.Separator))), true
}
