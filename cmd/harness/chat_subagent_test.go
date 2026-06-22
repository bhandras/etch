package main

import (
	"bytes"
	"strings"
	"testing"

	"harness/internal/model"
	"harness/internal/session"
)

// TestLiveToolCallLabelRendersTaskAsSubagent verifies task invocations are
// shown as delegated child-agent work instead of raw JSON.
func TestLiveToolCallLabelRendersTaskAsSubagent(t *testing.T) {
	label := liveToolCallLabel(model.ToolCall{
		ID:   "call_1",
		Name: "task",
		Arguments: `{"profile":"explore","task":"Read the repo\n` +
			`and summarize it."}`,
	})

	if label != "Started subagent explore: Read the repo and summarize it." {
		t.Fatalf("unexpected task label: %q", label)
	}
	if strings.Contains(label, "{") {
		t.Fatalf("task label exposed raw JSON: %q", label)
	}
}

// TestRenderSubagentToolResultSummarizesChildRun verifies task results keep the
// useful child outcome visible without dumping path-heavy metadata.
func TestRenderSubagentToolResultSummarizesChildRun(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
	}
	renderer.renderToolResult(
		session.ToolMessage(
			"call_1", "task",
			strings.Join(
				[]string{
					"Task review completed.",
					"",
					"Profile: review",
					"Session: child-123",
					"Session path: /tmp/noisy/session.jsonl",
					"Duration: 12s",
					"Model calls: 3",
					"Tool calls: 9",
					"",
					"Result:",
					"Found one issue.",
					"",
					"Inspect: harness show child-123",
					"Resume: harness resume child-123",
				}, "\n",
			),
		),
	)

	got := stdout.String()
	for _, want := range []string{
		"• Subagent review completed · 12s · 3 model calls · 9 tools",
		"  Session: child-123",
		"  Result:",
		"  Found one issue.",
		"  Inspect: harness show child-123",
		"  Resume: harness resume child-123",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subagent output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Session path") {
		t.Fatalf("subagent output kept noisy path metadata:\n%s", got)
	}
}
