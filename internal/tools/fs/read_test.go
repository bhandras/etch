package fs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadReturnsWholeFile verifies that small files are returned without
// continuation noise.
func TestReadReturnsWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	writeFile(t, path, "alpha\nbeta")

	got, err := Read(context.Background(), ReadRequest{Path: path})
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
	path := filepath.Join(t.TempDir(), "note.txt")
	writeFile(t, path, "one\ntwo\nthree\nfour")

	got, err := Read(context.Background(), ReadRequest{
		Path:   path,
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
	_, err := Read(context.Background(), ReadRequest{Path: t.TempDir()})
	if err == nil {
		t.Fatal("expected directory error")
	}
}

// TestReadRejectsOffsetPastEnd verifies that invalid continuation requests are
// reported to the model.
func TestReadRejectsOffsetPastEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	writeFile(t, path, "one\ntwo")

	_, err := Read(context.Background(), ReadRequest{
		Path:   path,
		Offset: 3,
	})
	if err == nil {
		t.Fatal("expected offset error")
	}
}

// TestReadTruncatesByBytes verifies that long first lines do not flood model
// context.
func TestReadTruncatesByBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.txt")
	writeFile(t, path, strings.Repeat("x", DefaultReadMaxBytes+1))

	got, err := Read(context.Background(), ReadRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "exceeds 50.0KB limit") {
		t.Fatalf("missing byte warning: %q", got)
	}
}
