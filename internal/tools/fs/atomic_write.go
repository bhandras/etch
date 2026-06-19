package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DefaultWriteMode is used when creating files that do not already
	// exist.
	DefaultWriteMode = 0o644
)

// atomicWriteFile replaces path with content through a temporary file in the
// same directory.
func atomicWriteFile(ctx context.Context, path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directories: %w", err)
	}

	mode := os.FileMode(DefaultWriteMode)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat existing file: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()

	default:
	}

	file, err := os.CreateTemp(
		filepath.Dir(path), "."+
			filepath.Base(path)+".tmp-*",
	)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempPath := file.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if _, err := file.Write(content); err != nil {
		_ = file.Close()

		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()

		return fmt.Errorf("set temporary file mode: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()

	default:
	}

	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	removeTemp = false

	return nil
}
