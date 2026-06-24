package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRenderPromptAssistantRendersMarkdownTables verifies prompt mode applies
// structural markdown rendering before writing the assistant transcript.
func TestRenderPromptAssistantRendersMarkdownTables(t *testing.T) {
	var stdout bytes.Buffer
	renderPromptAssistant(
		&stdout,
		strings.Join(
			[]string{
				"| Fruit | Color |",
				"|---|---|",
				"| Apple | Red |",
				"| Banana | Yellow |",
			}, "\n",
		),
	)

	got := stdout.String()
	if strings.Contains(got, "|---") {
		t.Fatalf("prompt output kept raw markdown delimiter: %q", got)
	}
	if !strings.HasPrefix(got, "assistant:\n") {
		t.Fatalf("prompt output lost assistant label: %q", got)
	}
	if !strings.Contains(got, "Fruit   Color") ||
		!strings.Contains(got, "Banana  Yellow") {

		t.Fatalf("prompt output did not align table: %q", got)
	}
}
