package main

import (
	"bytes"
	"encoding/json"
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

// TestRenderSubagentToolCallShowsFullPrompt verifies start blocks display the
// complete delegated task and context instead of truncating them.
func TestRenderSubagentToolCallShowsFullPrompt(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
	}
	longTask := strings.TrimSpace(
		strings.Repeat("review architecture carefully ", 8),
	)
	renderer.renderToolCall(model.ToolCall{
		ID:   "call_1",
		Name: "task",
		Arguments: `{"profile":"review","task":` +
			quoteJSONString(longTask) +
			`,"context":"Focus on concurrency and session order."}`,
	})

	got := stdout.String()
	for _, want := range []string{
		"• Started subagent review",
		"  Task:",
		"  " + longTask,
		"  Context:",
		"  Focus on concurrency and session order.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subagent start block missing %q:\n%s", want,
				got)
		}
	}
	if strings.Contains(got, "…") {
		t.Fatalf("subagent task was truncated:\n%s", got)
	}
}

// TestRenderSubagentToolBatchShowsEachPrompt verifies parallel task batches
// make each child assignment visible before results arrive.
func TestRenderSubagentToolBatchShowsEachPrompt(t *testing.T) {
	var stdout bytes.Buffer
	renderer := &liveChatRenderer{
		stdout: &stdout,
	}

	renderer.renderToolBatch([]model.ToolCall{
		{
			ID:        "call_1",
			Name:      "task",
			Arguments: `{"profile":"review","task":"Review core."}`,
		},
		{
			ID:        "call_2",
			Name:      "task",
			Arguments: `{"profile":"explore","task":"Map CLI."}`,
		},
	})

	got := stdout.String()
	for _, want := range []string{
		"• Starting 2 subagents",
		"• Started subagent review",
		"  Review core.",
		"• Started subagent explore",
		"  Map CLI.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subagent batch missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Running 2 tools") {
		t.Fatalf("subagent-only batch kept generic tool header:\n%s",
			got)
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

// quoteJSONString returns text as a JSON string literal for test arguments.
func quoteJSONString(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}

	return string(encoded)
}
