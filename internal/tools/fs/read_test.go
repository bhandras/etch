package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadReturnsWholeFile verifies that small files are returned without
// continuation noise.
func TestReadReturnsWholeFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, filepath.Join(root, "note.txt"), "alpha\nbeta")

	got, err := Read(context.Background(), ReadRequest{Path: "note.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nbeta" {
		t.Fatalf("read mismatch:\nwant %q\ngot  %q", "alpha\nbeta", got)
	}
}

// TestReadHonorsOffsetAndLimit verifies Pi-style 1-indexed continuation
// slices for large files.
func TestReadHonorsOffsetAndLimit(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, filepath.Join(root, "note.txt"), "one\ntwo\nthree\nfour")

	got, err := Read(context.Background(), ReadRequest{
		Path:   "note.txt",
		Offset: 2,
		Limit:  2,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "two\nthree\n\n[1 more line in file. Use offset=4 to continue.]"
	if got != want {
		t.Fatalf("read mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestReadRejectsDirectory verifies that read remains a file operation rather
// than silently listing directories.
func TestReadRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	_, err := Read(context.Background(), ReadRequest{Path: "."})
	if err == nil {
		t.Fatal("expected directory error")
	}
}

// TestReadRejectsWorkspaceEscape verifies reads follow the same workspace
// boundary as write-capable filesystem tools.
func TestReadRejectsWorkspaceEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outside, "secret")
	t.Chdir(root)

	_, err := Read(context.Background(), ReadRequest{Path: outside})
	if err == nil {
		t.Fatal("expected workspace escape error")
	}
}

// TestReadRejectsInternalPath verifies read does not expose harness state that
// other builtin filesystem tools intentionally skip.
func TestReadRejectsInternalPath(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir(".harness", 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".harness", "secret.txt"), "secret")

	_, err := Read(
		context.Background(), ReadRequest{
			Path: ".harness/secret.txt",
		},
	)
	if err == nil {
		t.Fatal("expected internal path error")
	}
}

// TestReadRejectsOffsetPastEnd verifies that invalid continuation requests are
// reported to the model.
func TestReadRejectsOffsetPastEnd(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, filepath.Join(root, "note.txt"), "one\ntwo")

	_, err := Read(context.Background(), ReadRequest{
		Path:   "note.txt",
		Offset: 3,
	})
	if err == nil {
		t.Fatal("expected offset error")
	}
}

// TestReadTruncatesByBytes verifies that long first lines do not flood model
// context.
func TestReadTruncatesByBytes(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(
		t, filepath.Join(root, "long.txt"),
		strings.Repeat("x", DefaultReadMaxBytes+1),
	)

	got, err := Read(context.Background(), ReadRequest{Path: "long.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "exceeds 50.0KB limit") {
		t.Fatalf("missing byte warning: %q", got)
	}
}
