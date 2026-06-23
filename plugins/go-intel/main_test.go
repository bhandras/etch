package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunGoInspectListsExportedSymbols verifies the empty filter shape returns
// a compact symbol map while respecting exported-only defaults.
func TestRunGoInspectListsExportedSymbols(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) + `],"detail":"none"}`,
	))
	if err != nil {
		t.Fatalf("list go symbols: %v", err)
	}
	for _, want := range []string{
		"filters: none",
		"struct Widget sample/sample.go:7-9",
		"func NewWidget sample/sample.go:12-14",
		"method Widget.NameText sample/sample.go:17-19",
		"const Answer sample/sample.go:4",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
	if strings.Contains(text, "hidden") {
		t.Fatalf("unexported symbol leaked without flag:\n%s", text)
	}
}

// TestRunGoInspectFiltersByFileRegex verifies file regexes replace the old
// file-specific symbol tool.
func TestRunGoInspectFiltersByFileRegex(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) +
			`],"file":"extra\\.go$","detail":"none"}`,
	))
	if err != nil {
		t.Fatalf("filter go symbols by file: %v", err)
	}
	if !strings.Contains(text, "func Extra sample/extra.go:4") {
		t.Fatalf("missing Extra in output:\n%s", text)
	}
	if strings.Contains(text, "Widget") {
		t.Fatalf("unexpected sibling symbol in file output:\n%s", text)
	}
}

// TestRunGoInspectFiltersByWorkspaceFileRegex verifies file regexes match the
// displayed repo-relative path even when paths points at a nested package.
func TestRunGoInspectFiltersByWorkspaceFileRegex(t *testing.T) {
	dir := goFixture(t)
	t.Chdir(dir)
	text, err := runGoInspect(json.RawMessage(
		`{"paths":["sample"],"file":"sample/extra\\.go$",` +
			`"detail":"none"}`,
	))
	if err != nil {
		t.Fatalf("filter go symbols by workspace file: %v", err)
	}
	if !strings.Contains(text, "func Extra sample/extra.go:4") {
		t.Fatalf("missing Extra in output:\n%s", text)
	}
	if strings.Contains(text, "sample/sample.go") {
		t.Fatalf("unexpected sibling file in output:\n%s", text)
	}
}

// TestRunGoInspectFiltersByPackageRegex verifies package regexes can select a
// package by relative directory.
func TestRunGoInspectFiltersByPackageRegex(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) +
			`],"package":"other","detail":"none"}`,
	))
	if err != nil {
		t.Fatalf("filter go symbols by package: %v", err)
	}
	if !strings.Contains(text, "func Duplicate other/other.go:4") {
		t.Fatalf("missing other Duplicate in output:\n%s", text)
	}
	if strings.Contains(text, "sample/extra.go") {
		t.Fatalf("unexpected sample package symbol:\n%s", text)
	}
}

// TestRunGoInspectPackageDetailMapsFilesAndSymbols verifies package mode gives
// agents a compact package/file map before broad file reads.
func TestRunGoInspectPackageDetailMapsFilesAndSymbols(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) +
			`],"detail":"package","includeUnexported":true}`,
	))
	if err != nil {
		t.Fatalf("map go package symbols: %v", err)
	}
	for _, want := range []string{
		"detail: package",
		"package sample (sample): 2 files, 7 symbols",
		"files:\n- sample/extra.go\n- sample/sample.go",
		"symbols:\n- func Extra sample/extra.go:4",
		"- func hidden sample/sample.go:21",
		"package other (other): 1 files, 1 symbols",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

// TestRunGoInspectSearchesMultiplePaths verifies one call can inspect several
// roots while keeping result paths unambiguous.
func TestRunGoInspectSearchesMultiplePaths(t *testing.T) {
	dir := goFixture(t)
	sample := filepath.Join(dir, "sample")
	other := filepath.Join(dir, "other")
	text, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(sample) + `,` +
			jsonQuote(other) +
			`],"name":"Duplicate","detail":"none","limit":10}`,
	))
	if err != nil {
		t.Fatalf("search go symbols across paths: %v", err)
	}
	for _, want := range []string{
		"symbols: 2",
		"func Duplicate " + filepath.ToSlash(sample) + "/extra.go:7",
		"func Duplicate " + filepath.ToSlash(other) + "/other.go:4",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

// TestRunGoInspectSummaryReturnsCompactDeclarations verifies summary mode
// returns godoc and compact declarations without function bodies.
func TestRunGoInspectSummaryReturnsCompactDeclarations(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) +
			`],"name":"^(Widget|NewWidget|NameText)$",` +
			`"includeUnexported":true}`,
	))
	if err != nil {
		t.Fatalf("summarize go symbols: %v", err)
	}
	for _, want := range []string{
		"detail: summary",
		"symbol: Widget",
		"godoc:\nWidget stores a display name.",
		"declaration:\n```go\ntype Widget struct { ... }\n```",
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
		t.Fatalf("summary leaked full function body:\n%s", text)
	}
}

// TestRunGoInspectRendersConstDeclarations verifies const symbols include the
// declaration token in both compact and full detail modes.
func TestRunGoInspectRendersConstDeclarations(t *testing.T) {
	dir := goFixture(t)
	summary, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) +
			`],"name":"^Answer$","detail":"summary"}`,
	))
	if err != nil {
		t.Fatalf("summarize const symbol: %v", err)
	}
	if !strings.Contains(summary, "const Answer = 42") {
		t.Fatalf("summary const declaration missing token:\n%s",
			summary)
	}

	full, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) +
			`],"name":"^Answer$","detail":"full"}`,
	))
	if err != nil {
		t.Fatalf("fully render const symbol: %v", err)
	}
	for _, want := range []string{
		"3 | // Answer is the test constant.",
		"4 | const Answer = 42",
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("missing %q in full const output:\n%s", want,
				full)
		}
	}
}

// TestRunGoInspectFullModeIncludesOnlyFullDeclaration verifies full mode uses
// the source declaration as the only declaration-shaped output.
func TestRunGoInspectFullModeIncludesOnlyFullDeclaration(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) +
			`],"name":"^NewWidget$","detail":"full"}`,
	))
	if err != nil {
		t.Fatalf("lookup full go symbol: %v", err)
	}
	for _, want := range []string{
		"detail: full",
		"11 | // NewWidget builds a Widget.",
		"12 | func NewWidget(name string) Widget {",
		"13 | \treturn Widget{Name: name}",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\ngodoc:\n") {
		t.Fatalf("full mode duplicated godoc outside declaration:\n%s",
			text)
	}
	if strings.Count(text, "declaration:") != 1 {
		t.Fatalf("full mode should render one declaration block:\n%s",
			text)
	}
}

// TestRunGoInspectNameRegexIsCaseInsensitive verifies symbol regex matching is
// forgiving without adding a model-facing ignoreCase option.
func TestRunGoInspectNameRegexIsCaseInsensitive(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) +
			`],"name":"^newwidget$","detail":"none"}`,
	))
	if err != nil {
		t.Fatalf("case-insensitive go symbol search: %v", err)
	}
	if !strings.Contains(text, "func NewWidget sample/sample.go:12-14") {
		t.Fatalf("case-insensitive regex did not match:\n%s", text)
	}
}

// TestRunGoInspectIncludesUnexported verifies private helpers stay hidden
// until callers explicitly ask for package internals.
func TestRunGoInspectIncludesUnexported(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) +
			`],"name":"hidden","includeUnexported":true,` +
			`"detail":"none"}`,
	))
	if err != nil {
		t.Fatalf("include unexported symbols: %v", err)
	}
	if !strings.Contains(text, "func hidden sample/sample.go:21") {
		t.Fatalf("missing hidden helper:\n%s", text)
	}
}

// TestRunGoInspectReportsNoMatches verifies empty result sets are explicit and
// still include the active filter context.
func TestRunGoInspectReportsNoMatches(t *testing.T) {
	dir := goFixture(t)
	text, err := runGoInspect(json.RawMessage(
		`{"paths":[` + jsonQuote(dir) +
			`],"name":"PluginSupervisor"}`,
	))
	if err != nil {
		t.Fatalf("search missing go symbol: %v", err)
	}
	for _, want := range []string{
		"filters: name=/PluginSupervisor/i",
		"symbols: 0",
		"No Go symbols matched.",
		"Parsed symbols before filters: 8.",
		"Sample searchable files:",
		"- sample/extra.go",
		"Sample searchable symbols:",
		"- Answer",
		"Hint: file regexes match displayed repo-relative paths",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

// TestRunGoInspectRejectsInvalidRegex verifies malformed model regexes fail
// before any broad code parsing work.
func TestRunGoInspectRejectsInvalidRegex(t *testing.T) {
	if _, err := runGoInspect(
		json.RawMessage(`{"name":"Client("}`),
	); err == nil {

		t.Fatal("expected invalid regex to fail")
	}
}

// TestRunGoInspectRejectsTooManyPaths verifies one call cannot fan out across
// an unbounded number of roots.
func TestRunGoInspectRejectsTooManyPaths(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, maxSymbolPaths+1)
	for i := range paths {
		path := filepath.Join(dir, string(rune('a'+i)))
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		paths[i] = path
	}
	raw, err := json.Marshal(symbolsArgs{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runGoInspect(raw); err == nil {
		t.Fatal("expected oversized path list to fail")
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

// NameText returns the widget name.
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
