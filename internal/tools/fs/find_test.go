package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindMatchesRelativePaths verifies recursive case-insensitive path
// discovery with stable slash-separated output.
func TestFindMatchesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	mkdir(t, filepath.Join(dir, "internal", "tool"))
	writeFile(t, filepath.Join(dir, "internal", "tool", "tool.go"), "")
	writeFile(t, filepath.Join(dir, "README.md"), "")

	got, err := Find(context.Background(), FindRequest{
		Path:  dir,
		Query: "TOOL",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "internal/tool/\ninternal/tool/tool.go"
	if got != want {
		t.Fatalf("find mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestFindSkipsInternalDirectories verifies that recursive discovery avoids
// repository metadata and local harness state.
func TestFindSkipsInternalDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".git", ".harness", "bin",
		"node_modules", "vendor"} {

		mkdir(t, filepath.Join(dir, name))
		writeFile(t, filepath.Join(dir, name, "needle.txt"), "")
	}
	writeFile(t, filepath.Join(dir, "needle.txt"), "")

	got, err := Find(context.Background(), FindRequest{
		Path:  dir,
		Query: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "needle.txt") {
		t.Fatalf("find missing visible result: %q", got)
	}
	if strings.Contains(got, ".git") || strings.Contains(got, ".harness") ||
		strings.Contains(got, "node_modules") ||
		strings.Contains(got, "vendor") {

		t.Fatalf("find included internal path: %q", got)
	}
	if !strings.Contains(got, "(skipped 5 internal directories)") {
		t.Fatalf("find missing skipped notice: %q", got)
	}
}

// TestFindCapsMatches verifies bounded result output with an explicit
// truncation notice.
func TestFindCapsMatches(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeFile(t, filepath.Join(dir, name), "")
	}

	got, err := Find(context.Background(), FindRequest{
		Path:  dir,
		Query: ".txt",
		Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "a.txt\nb.txt\n(truncated 1 matches)"
	if got != want {
		t.Fatalf("find mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestFindReportsNoMatches verifies the explicit empty search result marker.
func TestFindReportsNoMatches(t *testing.T) {
	got, err := Find(context.Background(), FindRequest{
		Path:  t.TempDir(),
		Query: "missing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != NoFindMatchesText {
		t.Fatalf("unexpected empty find output: %q", got)
	}
}

// mkdir creates a test fixture directory and all missing parents.
func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
