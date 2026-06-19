package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEditFileAppliesExactReplacement verifies the basic surgical replacement
// behavior expected from the model-facing edit tool.
func TestEditFileAppliesExactReplacement(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha\nbeta\n")

	result, err := EditFile(context.Background(), EditRequest{
		Path: "note.txt",
		Edits: []Edit{{
			OldText: "beta",
			NewText: "gamma",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(dir, "note.txt"), "alpha\ngamma\n")
	if !strings.Contains(result, "Successfully applied 1 edit") {
		t.Fatalf("unexpected result: %q", result)
	}
	if !strings.Contains(result, "-beta") ||
		!strings.Contains(result, "+gamma") {

		t.Fatalf("missing diff output: %q", result)
	}
}

// TestEditFileAppliesMultipleEditsAgainstOriginal verifies that replacements
// are located before any mutation shifts offsets.
func TestEditFileAppliesMultipleEditsAgainstOriginal(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "one\ntwo\nthree\n")

	_, err := EditFile(context.Background(), EditRequest{
		Path: "note.txt",
		Edits: []Edit{
			{OldText: "one", NewText: "1"},
			{OldText: "three", NewText: "33333"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(dir, "note.txt"), "1\ntwo\n33333\n")
}

// TestEditFilePreservesCRLF verifies that exact matching accepts LF tool
// arguments while restoring the file's existing CRLF style.
func TestEditFilePreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha\r\nbeta\r\n")

	_, err := EditFile(context.Background(), EditRequest{
		Path: "note.txt",
		Edits: []Edit{{
			OldText: "alpha\nbeta\n",
			NewText: "alpha\ngamma\n",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(
		t, filepath.Join(dir, "note.txt"),
		"alpha\r\ngamma\r\n",
	)
}

// TestEditFileRejectsMissingText verifies that stale replacements fail rather
// than silently changing the wrong region.
func TestEditFileRejectsMissingText(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha\n")

	_, err := EditFile(context.Background(), EditRequest{
		Path: "note.txt",
		Edits: []Edit{{
			OldText: "beta",
			NewText: "gamma",
		}},
	})
	if err == nil {
		t.Fatal("expected missing text error")
	}
}

// TestEditFileRejectsWhitespaceOnlyText verifies that edits include visible
// context rather than anchoring on a fragile newline or indentation span.
func TestEditFileRejectsWhitespaceOnlyText(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha\n")

	_, err := EditFile(context.Background(), EditRequest{
		Path: "note.txt",
		Edits: []Edit{{
			OldText: "\n",
			NewText: "\nbeta\n",
		}},
	})
	if err == nil {
		t.Fatal("expected whitespace-only text error")
	}
}

// TestEditFileRejectsAmbiguousText verifies that oldText must identify one
// unique region in the original file.
func TestEditFileRejectsAmbiguousText(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "same\nsame\n")

	_, err := EditFile(context.Background(), EditRequest{
		Path: "note.txt",
		Edits: []Edit{{
			OldText: "same",
			NewText: "other",
		}},
	})
	if err == nil {
		t.Fatal("expected ambiguous text error")
	}
}

// TestEditFileRejectsOverlappingEdits verifies that independently unique edits
// still cannot rewrite intersecting ranges.
func TestEditFileRejectsOverlappingEdits(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "abcdef\n")

	_, err := EditFile(context.Background(), EditRequest{
		Path: "note.txt",
		Edits: []Edit{
			{OldText: "abc", NewText: "ABC"},
			{OldText: "bcd", NewText: "BCD"},
		},
	})
	if err == nil {
		t.Fatal("expected overlapping edit error")
	}
}

// TestEditFileRejectsInternalPath verifies that edit shares the mutation path
// policy used by write.
func TestEditFileRejectsInternalPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	_, err := EditFile(context.Background(), EditRequest{
		Path: ".harness/session.jsonl",
		Edits: []Edit{{
			OldText: "old",
			NewText: "new",
		}},
	})
	if err == nil {
		t.Fatal("expected internal path error")
	}
}

// TestEditFilePreservesBOM verifies that UTF-8 byte order marks survive
// successful edits.
func TestEditFilePreservesBOM(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "note.txt")
	writeFile(t, path, utf8BOM+"alpha\n")

	_, err := EditFile(context.Background(), EditRequest{
		Path: "note.txt",
		Edits: []Edit{{
			OldText: "alpha",
			NewText: "beta",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), utf8BOM) {
		t.Fatalf("missing BOM: %q", string(content))
	}
}

// TestUnifiedDiffOmitsLargeInputs verifies that diff rendering has a simple
// safety valve for large line-product comparisons.
func TestUnifiedDiffOmitsLargeInputs(t *testing.T) {
	oldText := strings.Repeat("old\n", 600)
	newText := strings.Repeat("new\n", 600)

	got := unifiedDiff(
		"large.txt", oldText, newText, defaultEditDiffMaxBytes,
	)
	if !strings.Contains(got, "diff omitted") {
		t.Fatalf("expected omitted diff, got %q", got)
	}
}
