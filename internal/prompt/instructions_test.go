package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSystemTextLoadsAncestorInstructions verifies that project instructions
// are loaded from parent directories before child directories.
func TestSystemTextLoadsAncestorInstructions(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules\n")
	writeFile(t, filepath.Join(child, "AGENTS.md"), "child rules\n")

	text, err := SystemText(child)
	if err != nil {
		t.Fatal(err)
	}
	rootIndex := strings.Index(text, "root rules")
	childIndex := strings.Index(text, "child rules")
	if rootIndex < 0 || childIndex < 0 {
		t.Fatalf("missing instructions: %q", text)
	}
	if rootIndex > childIndex {
		t.Fatalf("instructions out of order: %q", text)
	}
}

// TestLoadInstructionFilesTruncatesLargeFiles verifies that large instruction
// files cannot dominate the first prompt context.
func TestLoadInstructionFilesTruncatesLargeFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(
		t, filepath.Join(dir, "AGENTS.md"),
		strings.Repeat("x", MaxInstructionFileBytes+10),
	)

	files, err := LoadInstructionFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one file, got %d", len(files))
	}
	if !strings.Contains(files[0].Text, "[truncated 10 bytes]") {
		t.Fatalf("missing truncation marker: %q", files[0].Text)
	}
}

// writeFile writes one test fixture file.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
