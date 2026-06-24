package fs

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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

// TestEditFileDryRunReturnsDiffWithoutWriting verifies that preview mode uses
// the normal validation and diff path while leaving the file untouched.
func TestEditFileDryRunReturnsDiffWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "note.txt")
	writeFile(t, path, "alpha\nbeta\n")

	result, err := EditFile(context.Background(), EditRequest{
		Path:   "note.txt",
		DryRun: true,
		Edits: []Edit{{
			OldText: "beta",
			NewText: "gamma",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path, "alpha\nbeta\n")
	if !strings.Contains(result, "Previewed 1 edit") {
		t.Fatalf("unexpected result: %q", result)
	}
	if !strings.Contains(result, "-beta") ||
		!strings.Contains(result, "+gamma") {

		t.Fatalf("missing diff output: %q", result)
	}
}

// TestUnifiedDiffShowsOnlyChangedHunks verifies model-facing diffs avoid
// replaying unchanged file content outside nearby context.
func TestUnifiedDiffShowsOnlyChangedHunks(t *testing.T) {
	before := numberedLines(20)
	after := strings.Replace(before, "line 10\n", "changed 10\n", 1)

	got := unifiedDiff("note.txt", before, after, defaultEditDiffMaxBytes)
	if !strings.Contains(got, "@@ -7,7 +7,7 @@") {
		t.Fatalf("missing hunk range: %q", got)
	}
	if !strings.Contains(got, " line 07") ||
		!strings.Contains(got, "-line 10") ||
		!strings.Contains(got, "+changed 10") ||
		!strings.Contains(got, " line 13") {

		t.Fatalf("missing changed hunk content: %q", got)
	}
	if strings.Contains(got, "line 01") ||
		strings.Contains(got, "line 20") {

		t.Fatalf("diff included distant unchanged lines: %q", got)
	}
}

// TestUnifiedDiffSplitsDistantChanges verifies separated edits render as
// separate hunks rather than exposing the unchanged middle of the file.
func TestUnifiedDiffSplitsDistantChanges(t *testing.T) {
	before := numberedLines(30)
	after := strings.Replace(before, "line 05\n", "changed 05\n", 1)
	after = strings.Replace(after, "line 25\n", "changed 25\n", 1)

	got := unifiedDiff("note.txt", before, after, defaultEditDiffMaxBytes)
	if count := strings.Count(got, "@@ -"); count != 2 {
		t.Fatalf("hunk count = %d, want 2 in %q", count, got)
	}
	if strings.Contains(got, "line 15") {
		t.Fatalf("diff included unchanged middle content: %q", got)
	}
}

// TestUnifiedDiffShowsSmallChangeInLargeFile verifies a localized edit in a
// large file still produces a compact hunk instead of an omitted diff.
func TestUnifiedDiffShowsSmallChangeInLargeFile(t *testing.T) {
	before := numberedLines(1000)
	after := strings.Replace(before, "line 500\n", "changed 500\n", 1)

	got := unifiedDiff("large.txt", before, after, defaultEditDiffMaxBytes)
	if strings.Contains(got, "diff omitted") {
		t.Fatalf("small localized change was omitted: %q", got)
	}
	if !strings.Contains(got, "-line 500") ||
		!strings.Contains(got, "+changed 500") {

		t.Fatalf("missing localized change: %q", got)
	}
	if strings.Contains(got, "line 01\n") ||
		strings.Contains(got, "line 999") {

		t.Fatalf("diff included distant large-file context: %q", got)
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
		Path: ".etch/session.jsonl",
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

// numberedLines returns deterministic line-oriented content for diff tests.
func numberedLines(count int) string {
	var out strings.Builder
	for index := 1; index <= count; index++ {
		out.WriteString("line ")
		if index < 10 {
			out.WriteString("0")
		}
		out.WriteString(strconv.Itoa(index))
		out.WriteString("\n")
	}

	return out.String()
}
