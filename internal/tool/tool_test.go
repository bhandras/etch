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
	path := filepath.Join(t.TempDir(), "note.txt")
	writeFile(t, path, "alpha\nbeta")

	registry := DefaultRegistry()
	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID:        "call_1",
		Name:      NameRead,
		Arguments: `{"path":` + quoteJSON(path) + `,"limit":1}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Text, "alpha\n\n[") {
		t.Fatalf("unexpected read result: %q", result.Text)
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
	if len(specs) != 4 {
		t.Fatalf("expected four specs, got %d", len(specs))
	}
	if specs[0].Name != NameEdit {
		t.Fatalf("unexpected tool name: %q", specs[0].Name)
	}
	if specs[1].Name != NameLS {
		t.Fatalf("unexpected tool name: %q", specs[1].Name)
	}
	if specs[2].Name != NameRead {
		t.Fatalf("unexpected tool name: %q", specs[2].Name)
	}
	if specs[3].Name != NameWrite {
		t.Fatalf("unexpected tool name: %q", specs[3].Name)
	}
}
