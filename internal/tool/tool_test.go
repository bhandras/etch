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
	if len(specs) != 2 {
		t.Fatalf("expected two specs, got %d", len(specs))
	}
	if specs[0].Name != NameLS {
		t.Fatalf("unexpected tool name: %q", specs[0].Name)
	}
	if specs[1].Name != NameRead {
		t.Fatalf("unexpected tool name: %q", specs[1].Name)
	}
}
