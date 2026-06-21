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
