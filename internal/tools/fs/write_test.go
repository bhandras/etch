package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteCreatesFile verifies that whole-file writes create missing files.
func TestWriteCreatesFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	result, err := Write(context.Background(), WriteRequest{
		Path:    "notes/hello.txt",
		Content: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(
		t, filepath.Join(dir, "notes", "hello.txt"),
		"hello\n",
	)
	if !strings.Contains(result, "Successfully wrote 6 bytes") {
		t.Fatalf("unexpected result: %q", result)
	}
}

// TestWriteOverwritesFile verifies that write is a complete file replacement
// rather than an append operation.
func TestWriteOverwritesFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "old")

	_, err := Write(context.Background(), WriteRequest{
		Path:    "note.txt",
		Content: "new",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(dir, "note.txt"), "new")
}

// TestWriteRejectsDirectory verifies that write does not replace directories.
func TestWriteRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("target", 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Write(context.Background(), WriteRequest{
		Path:    "target",
		Content: "new",
	})
	if err == nil {
		t.Fatal("expected directory error")
	}
}

// TestWriteRejectsOutsideWorkingDirectory verifies that mutation tools stay
// anchored to the process working directory.
func TestWriteRejectsOutsideWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	_, err := Write(context.Background(), WriteRequest{
		Path:    filepath.Join(filepath.Dir(dir), "outside.txt"),
		Content: "nope",
	})
	if err == nil {
		t.Fatal("expected outside-cwd error")
	}
}

// TestWriteRejectsInternalPath verifies that local repository and harness
// state are not modified through model-facing mutation tools.
func TestWriteRejectsInternalPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	_, err := Write(context.Background(), WriteRequest{
		Path:    ".harness/session.jsonl",
		Content: "nope",
	})
	if err == nil {
		t.Fatal("expected internal path error")
	}
}

// assertFileContent verifies a fixture file's exact bytes.
func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("file content mismatch:\nwant %q\ngot  %q", want,
			string(got))
	}
}
