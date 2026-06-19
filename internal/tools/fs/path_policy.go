package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mutationPath resolves a caller path into an absolute path that may be
// modified by filesystem mutation tools.
func mutationPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}

	candidate := filepath.Clean(trimmed)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(cwd, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	if err := requireUnderRoot(cwd, candidate); err != nil {
		return "", err
	}
	if err := rejectInternalMutationPath(cwd, candidate); err != nil {
		return "", err
	}

	return candidate, nil
}

// requireUnderRoot rejects paths that escape the current working directory.
func requireUnderRoot(root string, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("compare path to working directory: %w", err)
	}
	if rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside the working directory", path)
	}

	return nil
}

// rejectInternalMutationPath rejects repository and harness state mutations.
func rejectInternalMutationPath(root string, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("compare path to working directory: %w", err)
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		switch part {
		case ".git", ".harness":
			return fmt.Errorf("refusing to modify internal path %s",
				path)
		}
	}

	return nil
}
