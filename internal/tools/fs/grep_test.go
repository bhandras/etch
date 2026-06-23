package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepFindsLiteralMatches verifies compact path:line:text output for
// recursive literal search.
func TestGrepFindsLiteralMatches(t *testing.T) {
	dir := t.TempDir()
	mkdir(t, filepath.Join(dir, "cmd"))
	writeFile(t, filepath.Join(dir, "cmd", "main.go"), "alpha\nneedle\n")
	writeFile(t, filepath.Join(dir, "README.md"), "needle again\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "cmd/main.go:2:needle\nREADME.md:1:needle again"
	if got != want {
		t.Fatalf("grep mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestGrepCanIgnoreCase verifies opt-in case-insensitive literal matching.
func TestGrepCanIgnoreCase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.txt"), "Needle\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:       dir,
		Pattern:    "needle",
		IgnoreCase: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "note.txt:1:Needle" {
		t.Fatalf("unexpected ignore-case grep output: %q", got)
	}
}

// TestGrepDefaultsToCaseSensitive verifies lowercase queries do not match
// uppercase text unless IgnoreCase is requested.
func TestGrepDefaultsToCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.txt"), "Needle\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != NoGrepMatchesText {
		t.Fatalf("case-sensitive grep matched unexpectedly: %q", got)
	}
}

// TestGrepSkipsInternalAndBinaryFiles verifies that search avoids internal
// directories and binary-looking files.
func TestGrepSkipsInternalAndBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	mkdir(t, filepath.Join(dir, ".git"))
	mkdir(t, filepath.Join(dir, "bin"))
	writeFile(t, filepath.Join(dir, ".git", "config"), "needle\n")
	writeFile(t, filepath.Join(dir, "bin", "harness"), "needle\n")
	if err := os.WriteFile(
		filepath.Join(dir, "image.bin"), []byte("needle\x00"), 0o644,
	); err != nil {

		t.Fatal(err)
	}

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, ".git") || strings.Contains(got, "bin/") ||
		strings.Contains(got, "image.bin") {

		t.Fatalf("grep included skipped content: %q", got)
	}
	if !strings.Contains(got, NoGrepMatchesText) {
		t.Fatalf("grep missing no-match marker: %q", got)
	}
	if !strings.Contains(got, "(skipped 2 directories)") {
		t.Fatalf("grep missing internal skip notice: %q", got)
	}
	if !strings.Contains(got, "(skipped 1 binary files)") {
		t.Fatalf("grep missing binary skip notice: %q", got)
	}
}

// TestGrepSkipsVeryDeepDirectories verifies recursive search has a simple
// depth guard against pathological repository trees.
func TestGrepSkipsVeryDeepDirectories(t *testing.T) {
	dir := t.TempDir()
	deep := dir
	for i := 0; i <= defaultWalkMaxDepth; i++ {
		deep = filepath.Join(deep, "deep")
	}
	mkdir(t, deep)
	writeFile(t, filepath.Join(deep, "note.txt"), "needle\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "note.txt") {
		t.Fatalf("grep included too-deep file: %q", got)
	}
	if !strings.Contains(got, "(skipped 1 directories)") {
		t.Fatalf("grep missing depth skip notice: %q", got)
	}
}

// TestGrepHonorsGlobFilter verifies grep can search only matching file paths.
func TestGrepHonorsGlobFilter(t *testing.T) {
	dir := t.TempDir()
	mkdir(t, filepath.Join(dir, "cmd"))
	writeFile(t, filepath.Join(dir, "cmd", "main.go"), "needle\n")
	writeFile(t, filepath.Join(dir, "cmd", "main.txt"), "needle\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: "needle",
		Glob:    "**/*.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "cmd/main.go:1:needle" {
		t.Fatalf("grep glob mismatch: %q", got)
	}
}

// TestGrepSearchesMultipleRoots verifies one grep call can search several
// known roots without treating their names as one filesystem path.
func TestGrepSearchesMultipleRoots(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	mkdir(t, filepath.Join(dir, "cmd"))
	mkdir(t, filepath.Join(dir, "internal"))
	writeFile(t, filepath.Join(dir, "cmd", "main.go"), "needle\n")
	writeFile(t, filepath.Join(dir, "internal", "core.go"), "needle\n")
	writeFile(t, filepath.Join(dir, "README.md"), "needle\n")

	got, err := Grep(context.Background(), GrepRequest{
		Paths:   []string{"cmd", "internal"},
		Pattern: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cmd/main.go:1:needle",
		"internal/core.go:1:needle",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("multi-root grep missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "README.md") {
		t.Fatalf("multi-root grep escaped requested roots:\n%s", got)
	}
}

// TestGrepSplitsWhitespacePathWhenRootsExist verifies a common model mistake
// is recovered only when every whitespace-separated root is valid.
func TestGrepSplitsWhitespacePathWhenRootsExist(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	mkdir(t, filepath.Join(dir, "cmd"))
	mkdir(t, filepath.Join(dir, "internal"))
	writeFile(t, filepath.Join(dir, "cmd", "main.go"), "needle\n")
	writeFile(t, filepath.Join(dir, "internal", "core.go"), "needle\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:    "cmd internal",
		Pattern: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "cmd/main.go:1:needle") ||
		!strings.Contains(got, "internal/core.go:1:needle") {

		t.Fatalf("whitespace path was not split into roots:\n%s", got)
	}
}

// TestGrepHonorsRootGitignore verifies root .gitignore rules remove noisy
// files and directories from recursive search.
func TestGrepHonorsRootGitignore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n*.log\n")
	mkdir(t, filepath.Join(dir, "dist"))
	mkdir(t, filepath.Join(dir, "src"))
	writeFile(t, filepath.Join(dir, "dist", "bundle.js"), "needle\n")
	writeFile(t, filepath.Join(dir, "debug.log"), "needle\n")
	writeFile(t, filepath.Join(dir, "src", "main.go"), "needle\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "dist") || strings.Contains(got, "debug.log") {
		t.Fatalf("grep included ignored paths: %q", got)
	}
	if !strings.Contains(got, "src/main.go:1:needle") {
		t.Fatalf("grep lost visible match: %q", got)
	}
}

// TestGrepSupportsRegex verifies regex mode uses Go RE2 syntax.
func TestGrepSupportsRegex(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha-123\nalpha-x\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: `alpha-\d+`,
		Regex:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "note.txt:1:alpha-123" {
		t.Fatalf("regex grep mismatch: %q", got)
	}
}

// TestGrepRejectsInvalidRegex verifies bad regex patterns fail clearly.
func TestGrepRejectsInvalidRegex(t *testing.T) {
	_, err := Grep(context.Background(), GrepRequest{
		Path:    t.TempDir(),
		Pattern: "[",
		Regex:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "compile regex") {
		t.Fatalf("unexpected regex error: %v", err)
	}
}

// TestGrepRendersContextLines verifies context rows use grep-like separators
// while match rows keep the colon separator.
func TestGrepRendersContextLines(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.txt"), "before\nneedle\nafter\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: "needle",
		Context: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "note.txt-1:before\nnote.txt:2:needle\nnote.txt-3:after"
	if got != want {
		t.Fatalf("context grep mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestGrepTruncatesLongLines verifies long matching lines stay bounded.
func TestGrepTruncatesLongLines(t *testing.T) {
	dir := t.TempDir()
	writeFile(
		t, filepath.Join(dir, "note.txt"),
		"needle "+strings.Repeat("x", DefaultGrepMaxLineBytes+10),
	)

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[line truncated]") ||
		!strings.Contains(got, "(truncated 1 long lines)") {

		t.Fatalf("grep missing long-line truncation notice: %q", got)
	}
}

// TestGrepCapsTotalMatches verifies bounded output with explicit truncation.
func TestGrepCapsTotalMatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.txt"), "needle\nneedle\nneedle\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: "needle",
		Limit:   2,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "note.txt:1:needle\nnote.txt:2:needle\n(truncated 1 matches)"
	if got != want {
		t.Fatalf("grep mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestGrepReportsNoMatches verifies the explicit empty search result marker.
func TestGrepReportsNoMatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha\n")

	got, err := Grep(context.Background(), GrepRequest{
		Path:    dir,
		Pattern: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != NoGrepMatchesText {
		t.Fatalf("unexpected empty grep output: %q", got)
	}
}
