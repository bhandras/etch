package fs

import (
	"context"
	"fmt"
	"os"
)

// WriteRequest describes one whole-file create or replacement operation.
type WriteRequest struct {
	// Path is the file to create or replace.
	Path string

	// Content is the complete desired file content.
	Content string
}

// Write creates or replaces one file with complete caller-provided content.
func Write(ctx context.Context, req WriteRequest) (string, error) {
	path, err := mutationPath(req.Path)
	if err != nil {
		return "", err
	}

	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat file: %w", err)
	}

	content := []byte(req.Content)
	if err := atomicWriteFile(ctx, path, content); err != nil {
		return "", err
	}

	return fmt.Sprintf("Successfully wrote %d bytes to %s.",
		len(content), path), nil
}
