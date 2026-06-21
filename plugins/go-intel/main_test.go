package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunGoListSymbolsListsExportedSymbols verifies recursive symbol listing
// discovers top-level exported declarations from a fixture package.
func TestRunGoListSymbolsListsExportedSymbols(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoListSymbols(json.RawMessage(
		`{"path":` + jsonQuote(dir) + `,"groupBy":"package"}`,
	))
	if err != nil {
		t.Fatalf("list go symbols: %v", err)
	}
	for _, want := range []string{
		"package sample",
		"struct Widget",
		"func NewWidget",
		"method Widget.Name",
		"const Answer",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
	if strings.Contains(text, "hidden") {
		t.Fatalf("unexported symbol leaked without flag:\n%s", text)
	}
}

// TestRunGoFileSymbolsListsOneFile verifies file-scoped listings exclude
// symbols from sibling files.
func TestRunGoFileSymbolsListsOneFile(t *testing.T) {
	dir := goFixture(t)
	path := filepath.Join(dir, "sample", "extra.go")
	text, err := runGoFileSymbols(json.RawMessage(
		`{"path":` + jsonQuote(path) + `}`,
	))
	if err != nil {
		t.Fatalf("list file symbols: %v", err)
	}
	if !strings.Contains(text, "func Extra") {
		t.Fatalf("missing Extra in output:\n%s", text)
	}
	if strings.Contains(text, "Widget") {
		t.Fatalf("unexpected sibling symbol in file output:\n%s", text)
	}
}

// TestRunGoSymbolReturnsDocAndDeclaration verifies symbol lookup returns the
// doc comment and full source declaration for a struct.
func TestRunGoSymbolReturnsDocAndDeclaration(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoSymbol(json.RawMessage(
		`{"path":` + jsonQuote(dir) + `,"name":"Widget"}`,
	))
	if err != nil {
		t.Fatalf("lookup go symbol: %v", err)
	}
	for _, want := range []string{
		"doc:\nWidget stores a display name.",
		"type Widget struct",
		"Name string",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

// TestRunGoSymbolReportsAmbiguousMatches verifies duplicate names ask callers
// to refine the package or file filter.
func TestRunGoSymbolReportsAmbiguousMatches(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoSymbol(json.RawMessage(
		`{"path":` + jsonQuote(dir) + `,"name":"Duplicate"}`,
	))
	if err != nil {
		t.Fatalf("lookup ambiguous symbol: %v", err)
	}
	if !strings.Contains(text, "ambiguous") {
		t.Fatalf("expected ambiguity message, got:\n%s", text)
	}
}

// goFixture creates a small multi-package Go tree for plugin tests.
func goFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixture(
		t, filepath.Join(dir, "sample", "sample.go"), `package sample

// Answer is the test constant.
const Answer = 42

// Widget stores a display name.
type Widget struct {
	Name string
}

// NewWidget builds a Widget.
func NewWidget(name string) Widget {
	return Widget{Name: name}
}

// Name returns the widget name.
func (w Widget) NameText() string {
	return w.Name
}

func hidden() {}
`,
	)
	writeFixture(
		t, filepath.Join(dir, "sample", "extra.go"), `package sample

// Extra returns an extra value.
func Extra() string { return "extra" }

// Duplicate appears in one package.
func Duplicate() {}
`,
	)
	writeFixture(t, filepath.Join(dir, "other", "other.go"), `package other

// Duplicate appears in another package.
func Duplicate() {}
`)

	return dir
}

// writeFixture writes a fixture file and creates parent directories.
func writeFixture(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// jsonQuote returns text encoded as a JSON string literal.
func jsonQuote(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}

	return string(encoded)
}
