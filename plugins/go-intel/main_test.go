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

// TestRunGoSearchSymbolsFindsNameMatches verifies substring search finds
// symbols by exact and partial names before callers know the precise lookup.
func TestRunGoSearchSymbolsFindsNameMatches(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoSearchSymbols(json.RawMessage(
		`{"path":` + jsonQuote(dir) + `,"query":"NameText"}`,
	))
	if err != nil {
		t.Fatalf("search go symbols: %v", err)
	}
	for _, want := range []string{
		"query: NameText",
		"symbols: 1",
		"method Widget.NameText sample/sample.go:17",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

// TestRunGoSearchSymbolsFindsDocMatches verifies search covers godoc so agents
// can find concepts even when names are not obvious.
func TestRunGoSearchSymbolsFindsDocMatches(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoSearchSymbols(json.RawMessage(
		`{"path":` + jsonQuote(dir) + `,"query":"display name"}`,
	))
	if err != nil {
		t.Fatalf("search go symbols by doc: %v", err)
	}
	if !strings.Contains(text, "struct Widget sample/sample.go:7") {
		t.Fatalf("missing doc-matched Widget in output:\n%s", text)
	}
	if strings.Contains(text, "func hidden") {
		t.Fatalf("unexported symbol leaked without flag:\n%s", text)
	}
}

// TestRunGoSearchSymbolsRequiresQuery verifies empty searches fail before
// parsing the project.
func TestRunGoSearchSymbolsRequiresQuery(t *testing.T) {
	if _, err := runGoSearchSymbols(
		json.RawMessage(`{"query":"   "}`),
	); err == nil {

		t.Fatal("expected empty query to fail")
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
		"file: sample/sample.go:7-9",
		"godoc:\nWidget stores a display name.",
		"6 | // Widget stores a display name.",
		"7 | type Widget struct",
		"8 | \tName string",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

// TestRunGoSymbolReturnsFunctionSignature verifies function lookups include a
// standalone signature, godoc, and the default full declaration.
func TestRunGoSymbolReturnsFunctionSignature(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoSymbol(json.RawMessage(
		`{"path":` + jsonQuote(dir) + `,"name":"NewWidget"}`,
	))
	if err != nil {
		t.Fatalf("lookup go symbol: %v", err)
	}
	for _, want := range []string{
		"signature:\n```go\nfunc NewWidget(name string) Widget\n```",
		"godoc:\nNewWidget builds a Widget.",
		"declaration:",
		"return Widget{Name: name}",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

// TestRunGoSymbolReturnsMethodSignature verifies method lookups render the
// receiver as part of the structured signature block.
func TestRunGoSymbolReturnsMethodSignature(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoSymbol(json.RawMessage(
		`{"path":` + jsonQuote(dir) + `,"name":"NameText"}`,
	))
	if err != nil {
		t.Fatalf("lookup go symbol: %v", err)
	}
	want := "signature:\n```go\nfunc (w Widget) NameText() string\n```"
	if !strings.Contains(text, want) {
		t.Fatalf("missing method signature %q in output:\n%s", want,
			text)
	}
}

// TestRunGoSymbolSignatureModeSkipsFullDeclaration verifies callers can request
// API shape and godoc without sending the whole declaration body.
func TestRunGoSymbolSignatureModeSkipsFullDeclaration(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoSymbol(json.RawMessage(
		`{"path":` + jsonQuote(dir) +
			`,"name":"NewWidget","declaration":"signature"}`,
	))
	if err != nil {
		t.Fatalf("lookup go symbol: %v", err)
	}
	if !strings.Contains(text, "func NewWidget(name string) Widget") {
		t.Fatalf("missing function signature in output:\n%s", text)
	}
	if strings.Contains(text, "declaration:") ||
		strings.Contains(text, "return Widget{Name: name}") {

		t.Fatalf("signature mode leaked full declaration:\n%s", text)
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

// TestRunGoSymbolsReturnsBatchSignatures verifies batched lookups parse once
// and default to signature-sized detail instead of full declarations.
func TestRunGoSymbolsReturnsBatchSignatures(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoSymbols(json.RawMessage(
		`{"path":` + jsonQuote(dir) +
			`,"names":["Widget","NewWidget","NameText"]}`,
	))
	if err != nil {
		t.Fatalf("lookup go symbols: %v", err)
	}
	for _, want := range []string{
		"symbols requested: 3",
		"symbols resolved: 3",
		"declaration: signature",
		"symbol: Widget",
		"symbol: NewWidget",
		"func NewWidget(name string) Widget",
		"symbol: Widget.NameText",
		"func (w Widget) NameText() string",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
	if strings.Contains(text, "return Widget{Name: name}") {
		t.Fatalf("batch default leaked full declaration:\n%s", text)
	}
}

// TestRunGoSymbolsReportsPartialResults verifies missing and ambiguous names do
// not discard successful symbol details from the same batch.
func TestRunGoSymbolsReportsPartialResults(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoSymbols(json.RawMessage(
		`{"path":` + jsonQuote(dir) +
			`,"names":["NewWidget","Missing","Duplicate"]}`,
	))
	if err != nil {
		t.Fatalf("lookup partial go symbols: %v", err)
	}
	for _, want := range []string{
		"symbols requested: 3",
		"symbols resolved: 1",
		"missing:\n- Missing",
		"ambiguous:\n- Duplicate",
		"  - func Duplicate other/other.go:4",
		"  - func Duplicate sample/extra.go:7",
		"symbol: NewWidget",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

// TestRunGoSymbolsFullModeIncludesDeclarations verifies callers can explicitly
// request full declaration bodies for a focused batch.
func TestRunGoSymbolsFullModeIncludesDeclarations(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoSymbols(json.RawMessage(
		`{"path":` + jsonQuote(dir) +
			`,"names":["NewWidget"],"declaration":"full"}`,
	))
	if err != nil {
		t.Fatalf("lookup full go symbols: %v", err)
	}
	if !strings.Contains(text, "return Widget{Name: name}") {
		t.Fatalf("full mode omitted declaration body:\n%s", text)
	}
}

// TestRunGoSymbolsRequiresNames verifies empty batches fail before parsing the
// project.
func TestRunGoSymbolsRequiresNames(t *testing.T) {
	if _, err := runGoSymbols(
		json.RawMessage(`{"names":[" ",""]}`),
	); err == nil {

		t.Fatal("expected empty name batch to fail")
	}
}

// TestRunGoSymbolsLimitsBatchSize verifies a single detail call cannot request
// an unbounded number of symbols.
func TestRunGoSymbolsLimitsBatchSize(t *testing.T) {
	names := make([]string, maxBatchSymbolNames+1)
	for i := range names {
		names[i] = "Widget"
	}
	raw, err := json.Marshal(symbolsArgs{Names: names})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runGoSymbols(raw); err == nil {
		t.Fatal("expected oversized name batch to fail")
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
