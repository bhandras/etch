package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"harness/internal/core"
	"harness/internal/model"
	"harness/internal/session"
	"harness/internal/tool"
)

// TestChatObserverRendersToolEvents verifies that live chat feedback pairs each
// local tool call with its result instead of batching call headers first.
func TestChatObserverRendersToolEvents(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: newLiveChatRenderer(&stdout, false),
	}

	observer.EventAppended(messageEvent(t,
		session.EventAssistantMessage,
		session.AssistantToolCallMessage("", []session.ToolCallData{{
			ID:        "call_1",
			Name:      "bash",
			Arguments: `{"command":"go test ./..."}`,
		}}),
	))
	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"go test ./..."}`,
	})
	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage,
			session.ToolMessage("call_1", "bash", "exit code: 0\n"),
		),
	)

	got := stdout.String()
	want := "• Ran bash go test ./...\n\n   exit code: 0\n"
	if got != want {
		t.Fatalf("render mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestChatObserverSeparatesAssistantReplies verifies that final assistant text
// is visually separated from prior tool output.
func TestChatObserverSeparatesAssistantReplies(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: newLiveChatRenderer(&stdout, false),
	}

	observer.ReasoningCompleted("checking the README")
	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_1",
		Name:      "read",
		Arguments: `{"path":"README.md"}`,
	})
	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage,
			session.ToolMessage("call_1", "read", "hello\n"),
		),
	)
	observer.EventAppended(
		messageEvent(
			t, session.EventAssistantMessage,
			session.TextMessage(session.RoleAssistant, "done"),
		),
	)

	got := stdout.String()
	if !strings.Contains(
		got, "• checking the README\n\n• Ran read README.md\n\n   "+
			"read 1 lines\n\n• done\n",
	) {

		t.Fatalf("assistant reply was not separated: %q", got)
	}
}

// TestChatObserverRendersFinalAfterNoiseOnlyDeltas verifies filtered live
// stream fragments do not suppress the durable final assistant answer.
func TestChatObserverRendersFinalAfterNoiseOnlyDeltas(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: newLiveChatRenderer(&stdout, false),
	}

	observer.ModelTextDelta(".\n:\n`.\n")
	observer.EventAppended(
		messageEvent(
			t, session.EventAssistantMessage, session.TextMessage(
				session.RoleAssistant, "real final answer",
			),
		),
	)

	got := stdout.String()
	if strings.Contains(got, "\n  .") ||
		strings.Contains(got, "\n  :") ||
		strings.Contains(got, "\n  `.") {

		t.Fatalf("noise-only delta became visible: %q", got)
	}
	if !strings.Contains(got, "• real final answer\n") {
		t.Fatalf("final assistant answer was suppressed: %q", got)
	}
}

// TestChatObserverTracksActiveSubagents verifies task calls update the quiet
// working-status activity counter until matching tool results arrive.
func TestChatObserverTracksActiveSubagents(t *testing.T) {
	var stdout bytes.Buffer
	renderer := newLiveChatRenderer(&stdout, false)
	observer := &chatObserver{
		renderer: renderer,
	}

	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_1",
		Name:      "task",
		Arguments: `{"profile":"explore","task":"map the repo"}`,
	})
	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_2",
		Name:      "task",
		Arguments: `{"profile":"review","task":"review the diff"}`,
	})
	if renderer.activeSubagents != 2 {
		t.Fatalf("active subagents after starts = %d",
			renderer.activeSubagents)
	}
	if renderer.subagentStatuses["call_1"].Codename !=
		subagentCodename("call_1") ||
		renderer.subagentStatuses["call_1"].Profile != "explore" ||
		renderer.subagentStatuses["call_1"].Task != "map the repo" {

		t.Fatalf("subagent row metadata was not recorded: %#v",
			renderer.subagentStatuses["call_1"])
	}
	observer.ToolProgress(tool.ProgressEvent{
		ToolCallID: "call_1",
		Message:    "read README.md",
	})
	if renderer.subagentStatuses["call_1"].Message != "read README.md" {
		t.Fatalf("subagent progress was not recorded: %#v",
			renderer.subagentStatuses["call_1"])
	}

	observer.ToolCallFinished(model.ToolCall{
		ID:   "call_1",
		Name: "task",
	})
	if renderer.activeSubagents != 1 {
		t.Fatalf("active subagents after first finish = %d",
			renderer.activeSubagents)
	}
	if _, ok := renderer.subagentStatuses["call_1"]; ok {
		t.Fatalf("completed subagent status was not removed")
	}

	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage,
			session.ToolMessage("call_1", "task", "Task done."),
		),
	)
	if renderer.activeSubagents != 1 {
		t.Fatalf("active subagents after appended result = %d",
			renderer.activeSubagents)
	}

	observer.ToolCallFinished(model.ToolCall{
		ID:   "call_2",
		Name: "task",
	})
	if renderer.activeSubagents != 0 {
		t.Fatalf("active subagents after all finishes = %d",
			renderer.activeSubagents)
	}
}

// TestChatObserverRendersPartialSubagentResultOnce verifies a completed child
// result can appear before ordered durable replay without duplicate output.
func TestChatObserverRendersPartialSubagentResultOnce(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: newLiveChatRenderer(&stdout, false),
	}
	result := tool.Result{Text: strings.Join(
		[]string{
			"Task review completed.",
			"",
			"Profile: review",
			"Session: child-1",
			"Duration: 2s",
			"Model calls: 3",
			"Tool calls: 5",
			"",
			"Result:",
			"Found a concrete issue.",
		}, "\n",
	)}

	observer.ToolResultCompleted(
		model.ToolCall{
			ID:   "call_1",
			Name: tool.NameTask,
		},
		result,
	)
	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage, session.ToolMessage(
				"call_1", tool.NameTask, result.Text,
			),
		),
	)

	got := stdout.String()
	if count := strings.Count(got, "Subagent "); count != 1 {
		t.Fatalf("expected one rendered subagent result, got %d:\n%s",
			count, got)
	}
	if !strings.Contains(got, "Found a concrete issue.") {
		t.Fatalf("partial subagent result was not rendered:\n%s", got)
	}
}

// TestChatObserverAddsSubagentUsage verifies child-agent token counters appear
// in the parent footer and final turn stats after a task result arrives.
func TestChatObserverAddsSubagentUsage(t *testing.T) {
	child, _, err := session.Create(t.TempDir(), ".", "child")
	if err != nil {
		t.Fatalf("create child session: %v", err)
	}
	if _, err := child.Append(
		session.EventModelUsage, "", session.UsageData{
			InputTokens:       1000,
			CachedInputTokens: 400,
			OutputTokens:      200,
			TotalTokens:       1200,
		},
	); err != nil {

		t.Fatalf("append child usage: %v", err)
	}
	if _, err := child.Append(
		session.EventAssistantMessage, "", session.AssistantToolCallMessage(
			"", []session.ToolCallData{{
				ID:   "call_child",
				Name: tool.NameRead,
			}},
		),
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
		t.Fatalf("close child session: %v", err)
	}

	var stdout bytes.Buffer
	composer := &terminalChatInput{stdout: &stdout}
	chrome := newChatChrome(
		cliConfig{
			model: "gpt-test",
		}, ".",
		chatChromeStatus{},
	)
	renderer := newLiveChatRenderer(&stdout, false)
	renderer.composer = composer
	observer := &chatObserver{
		renderer: renderer,
		chrome:   chrome,
	}
	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage, session.ToolMessage(
				"call_1", tool.NameTask,
				"Task review completed.\n\nProfile: review\n"+
					"Session: child\nSession path: "+child.Path()+
					"\nDuration: 1s\nModel calls: 1\n"+
					"Tool calls: 0\n\nResult:\ndone\n",
			),
		),
	)

	if observer.usage.InputTokens != 1000 ||
		observer.usage.CachedInputTokens != 400 ||
		observer.usage.OutputTokens != 200 {

		t.Fatalf("subagent usage was not recorded: %#v", observer.usage)
	}
	if observer.toolCalls != 1 {
		t.Fatalf("subagent tool calls were not recorded: %d",
			observer.toolCalls)
	}
	if observer.timing.ModelCalls != 2 ||
		observer.timing.RequestBytes != 2048 ||
		observer.timing.ResponseBytes != 1024 {

		t.Fatalf("subagent timing was not recorded: %#v",
			observer.timing)
	}
	observer.TurnTiming(core.TurnTiming{
		ModelDuration: time.Second,
	})
	if observer.timing.ModelCalls != 2 ||
		observer.timing.RequestBytes != 2048 ||
		observer.timing.ModelDuration != time.Second {

		t.Fatalf("parent timing overwrote subagent timing: %#v",
			observer.timing)
	}
	if !strings.Contains(composer.footerText, "1,000 in") ||
		!strings.Contains(composer.footerText, "400 cached") ||
		!strings.Contains(composer.footerText, "200 out") ||
		!strings.Contains(composer.footerText, "2 req") ||
		!strings.Contains(composer.footerText, "2.0KB up") ||
		!strings.Contains(composer.footerText, "1.0KB down") {

		t.Fatalf("subagent counters missing from footer: %q",
			composer.footerText)
	}
}

// TestChatObserverAddsModelMetrics verifies provider metrics refresh the live
// prompt footer as each model call is persisted.
func TestChatObserverAddsModelMetrics(t *testing.T) {
	var stdout bytes.Buffer
	composer := &terminalChatInput{stdout: &stdout}
	chrome := newChatChrome(
		cliConfig{
			model: "gpt-test",
		}, ".",
		chatChromeStatus{},
	)
	renderer := newLiveChatRenderer(&stdout, false)
	renderer.composer = composer
	observer := &chatObserver{
		renderer: renderer,
		chrome:   chrome,
	}

	observer.EventAppended(
		metricsEvent(
			t, session.MetricsData{
				Requests:      3,
				RequestBytes:  4096,
				ResponseBytes: 2048,
			},
		),
	)

	for _, want := range []string{"3 req", "4.0KB up", "2.0KB down"} {
		if !strings.Contains(composer.footerText, want) {
			t.Fatalf("footer missing %q: %q", want,
				composer.footerText)
		}
	}
}

// messageEvent creates one durable message event for CLI rendering tests.
func messageEvent(t *testing.T, eventType string,
	message session.MessageData) session.Event {

	t.Helper()
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}

	return session.Event{
		Type: eventType,
		ID:   "event_1",
		Time: time.Now().UTC(),
		Data: raw,
	}
}

// metricsEvent creates one durable metrics event for CLI rendering tests.
func metricsEvent(t *testing.T, metrics session.MetricsData) session.Event {
	t.Helper()
	raw, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}

	return session.Event{
		Type: session.EventModelMetrics,
		ID:   "metrics_1",
		Time: time.Now().UTC(),
		Data: raw,
	}
}
