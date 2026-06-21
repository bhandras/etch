package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// workspacePath resolves a caller path into an absolute workspace path.
func workspacePath(path string) (string, error) {
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

	return candidate, nil
}

// readPath resolves a caller path into an absolute path safe for read tools.
func readPath(path string) (string, error) {
	candidate, err := workspacePath(path)
	if err != nil {
		return "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	if err := rejectInternalToolPath(cwd, candidate, "read"); err != nil {
		return "", err
	}

	return candidate, nil
}

// mutationPath resolves a caller path into an absolute path that may be
// modified by filesystem mutation tools.
func mutationPath(path string) (string, error) {
	candidate, err := workspacePath(path)
	if err != nil {
		return "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
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
	return rejectInternalToolPath(root, path, "modify")
}

// rejectInternalToolPath rejects repository and harness state access.
func rejectInternalToolPath(root string, path string, action string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("compare path to working directory: %w", err)
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		switch part {
		case ".git", ".harness":
			return fmt.Errorf("refusing to %s internal path %s",
				action, path)
		}
	}

	return nil
}
