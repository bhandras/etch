package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/model"
)

// TestDefaultRegistryExecutesLS verifies that the builtin registry exposes and
// dispatches the pure-Go directory listing tool.
func TestDefaultRegistryExecutesLS(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "")

	registry := DefaultRegistry()
	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID:        "call_1",
		Name:      NameLS,
		Arguments: `{"path":` + quoteJSON(dir) + `}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Text) != "go.mod" {
		t.Fatalf("unexpected ls result: %q", result.Text)
	}
}

// TestDefaultRegistryExecutesRead verifies that the registry exposes and
// dispatches the pure-Go text file reading tool.
func TestDefaultRegistryExecutesRead(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha\nbeta")

	registry := DefaultRegistry()
	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID:        "call_1",
		Name:      NameRead,
		Arguments: `{"path":"note.txt","limit":1}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Text, "alpha\n\n[") {
		t.Fatalf("unexpected read result: %q", result.Text)
	}
}

// TestDefaultRegistryExecutesFind verifies that the registry exposes and
// dispatches pure-Go recursive path discovery.
func TestDefaultRegistryExecutesFind(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "")

	registry := DefaultRegistry()
	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID:        "call_1",
		Name:      NameFind,
		Arguments: `{"path":` + quoteJSON(dir) + `,"query":"readme"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Text) != "README.md" {
		t.Fatalf("unexpected find result: %q", result.Text)
	}
}

// TestDefaultRegistryExecutesGrep verifies that the registry exposes and
// dispatches pure-Go literal text search.
func TestDefaultRegistryExecutesGrep(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha\nneedle\n")

	registry := DefaultRegistry()
	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID: "call_1", Name: NameGrep,
		Arguments: `{"path":` + quoteJSON(dir) +
			`,"pattern":"needle"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Text) != "note.txt:2:needle" {
		t.Fatalf("unexpected grep result: %q", result.Text)
	}
}

// TestDefaultRegistryExecutesWrite verifies that the registry exposes and
// dispatches the pure-Go whole-file writing tool.
func TestDefaultRegistryExecutesWrite(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	registry := DefaultRegistry()
	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID:        "call_1",
		Name:      NameWrite,
		Arguments: `{"path":"note.txt","content":"hello\n"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "Successfully wrote 6 bytes") {
		t.Fatalf("unexpected write result: %q", result.Text)
	}
	content, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello\n" {
		t.Fatalf("unexpected file content: %q", string(content))
	}
}

// TestDefaultRegistryExecutesEdit verifies that the registry exposes and
// dispatches the pure-Go exact replacement edit tool.
func TestDefaultRegistryExecutesEdit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "hello\n")

	registry := DefaultRegistry()
	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID:   "call_1",
		Name: NameEdit,
		Arguments: `{"path":"note.txt","edits":[{"oldText":"hello",` +
			`"newText":"goodbye"}]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "Successfully applied 1 edit") {
		t.Fatalf("unexpected edit result: %q", result.Text)
	}
	content, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "goodbye\n" {
		t.Fatalf("unexpected file content: %q", string(content))
	}
}

// TestDefaultRegistryExecutesBash verifies that the registry exposes and
// dispatches bounded local bash execution.
func TestDefaultRegistryExecutesBash(t *testing.T) {
	registry := DefaultRegistry()
	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID:        "call_1",
		Name:      NameBash,
		Arguments: `{"command":"printf hello"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "stdout:\nhello") {
		t.Fatalf("unexpected bash result: %q", result.Text)
	}
}

// quoteJSON returns a quoted JSON string literal for test arguments.
func quoteJSON(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}

	return string(encoded)
}

// writeFile creates a small file fixture for registry tests.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSpecsReturnsStableOrder verifies deterministic schema ordering for model
// requests.
func TestSpecsReturnsStableOrder(t *testing.T) {
	registry := DefaultRegistry()
	specs := registry.Specs()
	if len(specs) != 7 {
		t.Fatalf("expected seven specs, got %d", len(specs))
	}
	if specs[0].Name != NameBash {
		t.Fatalf("unexpected tool name: %q", specs[0].Name)
	}
	if specs[1].Name != NameEdit {
		t.Fatalf("unexpected tool name: %q", specs[1].Name)
	}
	if specs[2].Name != NameFind {
		t.Fatalf("unexpected tool name: %q", specs[2].Name)
	}
	if specs[3].Name != NameGrep {
		t.Fatalf("unexpected tool name: %q", specs[3].Name)
	}
	if specs[4].Name != NameLS {
		t.Fatalf("unexpected tool name: %q", specs[4].Name)
	}
	if specs[5].Name != NameRead {
		t.Fatalf("unexpected tool name: %q", specs[5].Name)
	}
	if specs[6].Name != NameWrite {
		t.Fatalf("unexpected tool name: %q", specs[6].Name)
	}
}
