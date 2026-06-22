package main

import (
	"strings"
	"testing"

	"harness/internal/session"
	"harness/internal/tool"
)

// TestAggregateSessionStatusFoldsSubagentCounters verifies parent status
// includes completed child-agent work referenced by task results.
func TestAggregateSessionStatusFoldsSubagentCounters(t *testing.T) {
	dir := t.TempDir()
	child, _, err := session.Create(dir, ".", "child")
	if err != nil {
		t.Fatalf("create child session: %v", err)
	}
	if _, err := child.Append(
		session.EventModelUsage, "", session.UsageData{
			InputTokens:       1000,
			CachedInputTokens: 500,
			OutputTokens:      100,
			TotalTokens:       1100,
		},
	); err != nil {

		t.Fatalf("append child usage: %v", err)
	}
	if _, err := child.Append(
		session.EventAssistantMessage, "",
		session.AssistantToolCallMessage("", []session.ToolCallData{{
			ID:   "call_child",
			Name: tool.NameRead,
		}}),
	); err != nil {

		t.Fatalf("append child tool call: %v", err)
	}
	if _, err := child.Append(
		session.EventModelMetrics, "", session.MetricsData{
			Requests:      2,
			RequestBytes:  2048,
			ResponseBytes: 1024,
		},
	); err != nil {

		t.Fatalf("append child metrics: %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatalf("close child: %v", err)
	}

	parent, _, err := session.Create(dir, ".", "parent")
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	if _, err := parent.Append(
		session.EventToolMessage, "", session.ToolMessage(
			"call_parent", tool.NameTask,
			"Task review completed.\n\nProfile: review\n"+
				"Session: child\nSession path: "+child.Path()+
				"\nDuration: 1s\nModel calls: 2\n"+
				"Tool calls: 1\n\nResult:\ndone\n",
		),
	); err != nil {

		t.Fatalf("append parent task result: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatalf("close parent: %v", err)
	}

	status, err := aggregateSessionStatus(parent.Path())
	if err != nil {
		t.Fatal(err)
	}
	if status.ToolCalls != 1 || status.ModelCalls != 2 ||
		status.Metrics.RequestBytes != 2048 ||
		status.Usage.InputTokens != 1000 {

		t.Fatalf("child counters were not folded: %#v", status)
	}
	text := session.FormatStatus(status)
	if !strings.Contains(text, "- model calls: 2") ||
		!strings.Contains(text, "- input: 1,000 tokens") {

		t.Fatalf("aggregate status was not formatted: %q", text)
	}
}
