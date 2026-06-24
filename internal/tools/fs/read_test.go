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
	want := "1 | alpha\n2 | beta"
	if got != want {
		t.Fatalf("read mismatch:\nwant %q\ngot  %q", want, got)
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

	want := "2 | two\n3 | three\n\n" +
		"[1 more line in file. Use offset=4 to continue.]"
	if got != want {
		t.Fatalf("read mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestReadCanDisableLineNumbers verifies callers can request raw slices when
// line prefixes would be undesirable.
func TestReadCanDisableLineNumbers(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, filepath.Join(root, "note.txt"), "alpha\nbeta")
	lineNumbers := false

	got, err := Read(context.Background(), ReadRequest{
		Path:        "note.txt",
		LineNumbers: &lineNumbers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nbeta" {
		t.Fatalf("read mismatch:\nwant %q\ngot  %q", "alpha\nbeta", got)
	}
}

// TestReadCanBatchIndependentRanges verifies a single read call can retrieve
// several known file slices without adding another model-facing tool.
func TestReadCanBatchIndependentRanges(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, filepath.Join(root, "one.txt"), "alpha\nbeta\ngamma")
	writeFile(t, filepath.Join(root, "two.txt"), "red\ngreen\nblue")

	got, err := Read(context.Background(), ReadRequest{
		Files: []ReadRange{{
			Path:  "one.txt",
			Limit: 2,
		}, {
			Path:   "two.txt",
			Offset: 2,
			Limit:  1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--- one.txt (limit=2) ---",
		"1 | alpha",
		"[1 more line in file. Use offset=3 to continue.]",
		"--- two.txt (offset=2, limit=1) ---",
		"2 | green",
		"[1 more line in file. Use offset=3 to continue.]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("batched read missing %q:\n%s", want, got)
		}
	}
}

// TestReadBatchKeepsPerFileErrors verifies one bad path does not discard
// successful neighboring reads.
func TestReadBatchKeepsPerFileErrors(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, filepath.Join(root, "ok.txt"), "alpha")

	got, err := Read(context.Background(), ReadRequest{
		Files: []ReadRange{{
			Path: "ok.txt",
		}, {
			Path: "missing.txt",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "1 | alpha") ||
		!strings.Contains(
			got, "Read batch completed with 1 per-file error",
		) ||
		!strings.Contains(got, "--- missing.txt ---") ||
		!strings.Contains(got, "error: stat file:") {

		t.Fatalf("unexpected partial batch output:\n%s", got)
	}
}

// TestReadBatchPrefersFilesOverSinglePath verifies model-filled batch requests
// stay useful even when legacy single-file fields are also present.
func TestReadBatchPrefersFilesOverSinglePath(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFile(t, filepath.Join(root, "one.txt"), "one")
	writeFile(t, filepath.Join(root, "two.txt"), "two")

	got, err := Read(context.Background(), ReadRequest{
		Path: "one.txt",
		Files: []ReadRange{{
			Path: "two.txt",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "one") || !strings.Contains(got, "two") {
		t.Fatalf("read did not prefer files over path:\n%s", got)
	}
}

// TestReadBatchCapsFileCount verifies batched reads stay intentionally small.
func TestReadBatchCapsFileCount(t *testing.T) {
	req := ReadRequest{
		Files: make([]ReadRange, DefaultReadMaxFiles+1),
	}
	for index := range req.Files {
		req.Files[index].Path = "file.txt"
	}
	_, err := Read(context.Background(), req)
	if err == nil {
		t.Fatal("expected too many files error")
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
	if err := os.Mkdir(".etch", 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".etch", "secret.txt"), "secret")

	_, err := Read(
		context.Background(), ReadRequest{
			Path: ".etch/secret.txt",
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
