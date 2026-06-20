package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunPluginEchoReportsTextStats verifies the smoke-test tool returns the
// original text plus dependency-free text statistics.
func TestRunPluginEchoReportsTextStats(t *testing.T) {
	text, err := runPluginEcho(json.RawMessage(`{"text":"hello world"}`))
	if err != nil {
		t.Fatalf("run plugin echo: %v", err)
	}
	if !strings.Contains(text, "hello world") {
		t.Fatalf("missing echo text: %q", text)
	}
	if !strings.Contains(text, "words: 2") {
		t.Fatalf("missing word count: %q", text)
	}
}

// TestRunProjectFilesSummarizesDirectory verifies the filesystem summary tool
// reports files, extensions, and sample paths from a small fixture.
func TestRunProjectFilesSummarizesDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "README.md"), "hello\n")
	writeFixture(t, filepath.Join(dir, "cmd", "main.go"), "package main\n")

	text, err := runProjectFiles(json.RawMessage(
		`{"path":` + strconvQuote(dir) + `,"limit":10}`,
	))
	if err != nil {
		t.Fatalf("run project files: %v", err)
	}
	for _, want := range []string{
		"files: 2",
		".go: 1",
		".md: 1",
		"README.md",
		"cmd/main.go",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

// TestNormalizeLimitBoundsWalkSize verifies project_files uses a bounded
// default and cap for directory walks.
func TestNormalizeLimitBoundsWalkSize(t *testing.T) {
	if got := normalizeLimit(0); got != defaultProjectFileLimit {
		t.Fatalf("unexpected default limit: %d", got)
	}
	if got := normalizeLimit(
		maxProjectFileLimit + 1,
	); got != maxProjectFileLimit {

		t.Fatalf("unexpected capped limit: %d", got)
	}
}

// writeFixture creates a small test file and any missing parent directories.
func writeFixture(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// strconvQuote returns a JSON-safe quoted string without adding extra imports
// to the tests that exercise raw plugin arguments.
func strconvQuote(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}

	return string(encoded)
}
