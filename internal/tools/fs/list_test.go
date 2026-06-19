package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestListSortsEntriesAndMarksDirectories verifies the stable output shape
// expected by future model-facing wrappers.
func TestListSortsEntriesAndMarksDirectories(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b.txt"), "")
	writeFile(t, filepath.Join(dir, "A.txt"), "")
	if err := os.Mkdir(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := List(context.Background(), ListRequest{
		Path: dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "A.txt\nb.txt\ncmd/"
	if got != want {
		t.Fatalf("listing mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestListIncludesDotfiles verifies that ordinary hidden files remain visible
// to the agent while special internal directories are skipped separately.
func TestListIncludesDotfiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env.example"), "")

	got, err := List(context.Background(), ListRequest{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got != ".env.example" {
		t.Fatalf("unexpected listing: %q", got)
	}
}

// TestListSkipsInternalDirectories verifies that local repository metadata and
// session transcripts do not pollute model-facing listings.
func TestListSkipsInternalDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		".git",
		".harness",
		"node_modules",
		"vendor",
	} {
		if err := os.Mkdir(
			filepath.Join(dir, name), 0o755,
		); err != nil {

			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "README.md"), "")

	got, err := List(context.Background(), ListRequest{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "README.md") {
		t.Fatalf("listing missing real file: %q", got)
	}
	for _, name := range []string{
		".git",
		".harness",
		"node_modules",
		"vendor",
	} {
		if strings.Contains(got, name) {
			t.Fatalf("listing included skipped dir %q: %q", name,
				got)
		}
	}
	if !strings.Contains(got, "(skipped 4 internal entries)") {
		t.Fatalf("listing missing skipped notice: %q", got)
	}
}

// TestListCapsEntries verifies that large directories are bounded with an
// explicit truncation notice.
func TestListCapsEntries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(dir, name), "")
	}

	got, err := List(context.Background(), ListRequest{
		Path:  dir,
		Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "a\nb\n(truncated 1 entries)"
	if got != want {
		t.Fatalf("listing mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestListReportsEmptyDirectory verifies the explicit empty-directory marker.
func TestListReportsEmptyDirectory(t *testing.T) {
	got, err := List(context.Background(), ListRequest{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got != EmptyDirectoryText {
		t.Fatalf("unexpected empty output: %q", got)
	}
}

// writeFile creates a small test fixture file.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
