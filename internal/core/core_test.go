package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/hooks"
	"harness/internal/model"
	"harness/internal/prompt"
	"harness/internal/session"
	"harness/internal/tool"
)

// TestRunTurnPersistsEchoExchange verifies the first complete executable slice:
// prompt admission, model streaming, and JSONL session persistence.
func TestRunTurnPersistsEchoExchange(t *testing.T) {
	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "hello",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      model.EchoClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssistantText != "hello" {
		t.Fatalf("assistant text mismatch: %q", result.AssistantText)
	}

	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected three events, got %d", len(events))
	}
	if events[0].Type != session.EventSessionStarted {
		t.Fatalf("unexpected first event type: %q", events[0].Type)
	}
	if events[1].Type != session.EventUserMessage {
		t.Fatalf("unexpected user event type: %q", events[1].Type)
	}
	if events[2].Type != session.EventAssistantMessage {
		t.Fatalf("unexpected assistant event type: %q", events[2].Type)
	}
	if events[1].ParentID != events[0].ID ||
		events[2].ParentID != events[1].ID {

		t.Fatalf("events are not parent chained: %#v", events)
	}

	var assistant session.MessageData
	if err := json.Unmarshal(events[2].Data, &assistant); err != nil {
		t.Fatal(err)
	}
	if assistant.Role != session.RoleAssistant {
		t.Fatalf("assistant role mismatch: %q", assistant.Role)
	}
	if assistant.Content[0].Text != "hello" {
		t.Fatalf("assistant content mismatch: %#v", assistant.Content)
	}
}

// TestRunTurnEmitsLifecycleHooks verifies session and turn notification hooks
// fire in execution order around a simple turn.
func TestRunTurnEmitsLifecycleHooks(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "hooks.log")
	hookScript := filepath.Join(dir, "record-hook.sh")
	writeFile(
		t, hookScript, "#!/bin/sh\npayload=$(cat)\nprintf '%s\\n' "+
			"\"$payload\" >> \"$HOOK_LOG\"\nprintf '{}'\n",
	)
	if err := os.Chmod(hookScript, 0o755); err != nil {
		t.Fatalf("chmod hook: %v", err)
	}
	command := "HOOK_LOG=" + shellQuote(logPath) + " " +
		shellQuote(hookScript)
	runner, err := hooks.New([]config.HookConfig{
		{Event: hooks.EventSessionStart, Command: command},
		{Event: hooks.EventTurnStart, Command: command},
		{Event: hooks.EventTurnComplete, Command: command},
	}, dir)
	if err != nil {
		t.Fatalf("create hooks: %v", err)
	}

	_, err = RunTurn(context.Background(), TurnRequest{
		Prompt:     "hello",
		SessionDir: filepath.Join(dir, "sessions"),
		CWD:        dir,
		Model:      model.EchoClient{},
		Hooks:      runner,
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read hook log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	got := make([]string, 0, len(lines))
	for _, line := range lines {
		var envelope struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("decode hook envelope: %v", err)
		}
		got = append(got, envelope.Event)
	}
	want := []string{
		hooks.EventSessionStart,
		hooks.EventTurnStart,
		hooks.EventTurnComplete,
	}
	if !equalStrings(got, want) {
		t.Fatalf("hook event order mismatch:\nwant %#v\ngot  %#v", want,
			got)
	}
}

// TestRunTurnPersistsModelUsage verifies provider token counters are durable
// session events when a model stream reports them.
func TestRunTurnPersistsModelUsage(t *testing.T) {
	client := &scriptedToolClient{
		events: [][]model.Event{{
			{
				Type: model.EventTextDelta,
				Text: "hello",
			},
			{Type: model.EventUsage, Usage: model.Usage{
				InputTokens:           100,
				CachedInputTokens:     80,
				OutputTokens:          12,
				ReasoningOutputTokens: 3,
				TotalTokens:           112,
			}},
			{Type: model.EventResponseInfo, ResponseInfo: model.ResponseInfo{
				ProviderResponseID: "resp_123",
			}},
			{
				Type: model.EventDone,
			},
		}},
	}

	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "hello",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      client,
	})
	if err != nil {
		t.Fatal(err)
	}

	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("expected five events, got %#v", events)
	}
	if events[3].Type != session.EventModelUsage {
		t.Fatalf("expected usage event, got %q", events[3].Type)
	}
	var usage session.UsageData
	if err := json.Unmarshal(events[3].Data, &usage); err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 100 || usage.CachedInputTokens != 80 ||
		usage.OutputTokens != 12 || usage.ReasoningOutputTokens != 3 ||
		usage.TotalTokens != 112 {

		t.Fatalf("unexpected usage: %#v", usage)
	}
	if events[4].Type != session.EventModelResponse {
		t.Fatalf("expected response event, got %q", events[4].Type)
	}
	if events[4].ParentID != events[3].ID {
		t.Fatalf("response event parent mismatch: %#v", events)
	}
	var response session.ResponseData
	if err := json.Unmarshal(events[4].Data, &response); err != nil {
		t.Fatal(err)
	}
	if response.ProviderResponseID != "resp_123" {
		t.Fatalf("unexpected response identity: %#v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one model request, got %#v", client.requests)
	}
	if client.requests[0].SessionID != result.SessionID {
		t.Fatalf("session id was not sent to model: got %q want %q",
			client.requests[0].SessionID, result.SessionID)
	}
}

// TestRunTurnPersistsModelMetrics verifies provider transport measurements are
// durable session events when a model stream reports them.
func TestRunTurnPersistsModelMetrics(t *testing.T) {
	client := &scriptedToolClient{
		events: [][]model.Event{{
			{
				Type: model.EventTextDelta,
				Text: "hello",
			},
			{
				Type: model.EventMetrics,
				Metrics: model.Metrics{
					Requests:              1,
					ContinuationRequests:  1,
					ContinuationFallbacks: 1,
					RequestBytes:          2048,
					ResponseBytes:         1024,
					InputMessages:         2,
					DeltaMessages:         1,
					ToolCount:             4,
					InstructionBytes:      100,
					InputBytes:            500,
					ToolBytes:             700,
					TimeToHeaders:         3 * time.Millisecond,
					TimeToFirstEvent:      4 * time.Millisecond,
				},
			},
			{
				Type: model.EventDone,
			},
		}},
	}

	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "hello",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      client,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Type != session.EventModelMetrics {
		t.Fatalf("expected metrics event, got %#v", events)
	}
	var metrics session.MetricsData
	if err := json.Unmarshal(
		events[len(events)-1].Data, &metrics,
	); err != nil {

		t.Fatal(err)
	}
	if metrics.Requests != 1 || metrics.ContinuationRequests != 1 ||
		metrics.ContinuationFallbacks != 1 ||
		metrics.RequestBytes != 2048 || metrics.ResponseBytes != 1024 ||
		metrics.InputMessages != 2 || metrics.DeltaMessages != 1 ||
		metrics.ToolCount != 4 || metrics.InstructionBytes != 100 ||
		metrics.InputBytes != 500 || metrics.ToolBytes != 700 ||
		metrics.TimeToHeadersMillis != 3 ||
		metrics.TimeToFirstEventMillis != 4 {

		t.Fatalf("unexpected metrics: %#v", metrics)
	}
	if result.Timing.ModelCalls != 1 ||
		result.Timing.RequestBytes != 2048 ||
		result.Timing.ResponseBytes != 1024 {

		t.Fatalf("unexpected timing: %#v", result.Timing)
	}
}

// TestRunTurnRejectsEmptyPrompt keeps invalid CLI input from creating empty
// session files.
func TestRunTurnRejectsEmptyPrompt(t *testing.T) {
	_, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      model.EchoClient{},
	})
	if err == nil {
		t.Fatal("expected empty prompt error")
	}
}

// TestRunTurnRejectsWhitespacePrompt verifies blank prompts are rejected after
// trimming so they cannot create empty session files.
func TestRunTurnRejectsWhitespacePrompt(t *testing.T) {
	_, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "   ",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      model.EchoClient{},
	})
	if err == nil {
		t.Fatal("expected whitespace prompt error")
	}
}

// TestRunTurnContinuesExistingSession verifies that a follow-up turn appends
// to the same JSONL log and replays prior messages into the model request.
func TestRunTurnContinuesExistingSession(t *testing.T) {
	firstClient := &scriptedToolClient{
		events: [][]model.Event{{
			{
				Type: model.EventTextDelta,
				Text: "first",
			},
			{Type: model.EventResponseInfo, ResponseInfo: model.ResponseInfo{
				ProviderResponseID: "resp_first",
			}},
			{
				Type: model.EventDone,
			},
		}},
	}
	first, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "first",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      firstClient,
	})
	if err != nil {
		t.Fatal(err)
	}

	client := &scriptedToolClient{
		events: [][]model.Event{{
			{
				Type: model.EventTextDelta,
				Text: "second",
			},
			{
				Type: model.EventDone,
			},
		}},
	}
	second, err := RunTurn(context.Background(), TurnRequest{
		Prompt:      "follow-up",
		SessionPath: first.SessionPath,
		CWD:         "/work/project",
		Model:       client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionPath != first.SessionPath {
		t.Fatalf("session path mismatch: want %q got %q",
			first.SessionPath, second.SessionPath)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one model request, got %d",
			len(client.requests))
	}
	if client.requests[0].PreviousResponseID != "resp_first" {
		t.Fatalf("unexpected previous response id: %q",
			client.requests[0].PreviousResponseID)
	}
	if len(client.requests[0].DeltaMessages) != 1 ||
		client.requests[0].DeltaMessages[0].Content != "follow-up" {

		t.Fatalf("unexpected delta messages: %#v",
			client.requests[0].DeltaMessages)
	}

	messages := client.requests[0].Messages
	if len(messages) != 3 {
		t.Fatalf("expected three messages, got %#v", messages)
	}
	if messages[0].Role != model.RoleUser ||
		messages[0].Content != "first" {

		t.Fatalf("unexpected first history message: %#v", messages[0])
	}
	if messages[1].Role != model.RoleAssistant ||
		messages[1].Content != "first" {

		t.Fatalf("unexpected assistant history message: %#v",
			messages[1])
	}
	if messages[2].Role != model.RoleUser ||
		messages[2].Content != "follow-up" {

		t.Fatalf("unexpected current user message: %#v", messages[2])
	}
}

// TestRunTurnForksParentContext verifies child turns inherit parent messages
// before the task-call boundary without reusing parent provider continuation.
func TestRunTurnForksParentContext(t *testing.T) {
	dir := t.TempDir()
	parent, _, err := session.Create(dir, "/work/project", "parent")
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	user, err := parent.Append(
		session.EventUserMessage, parent.LastID(),
		session.TextMessage(session.RoleUser, "parent prompt"),
	)
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := parent.Append(
		session.EventAssistantMessage, user.ID,
		session.TextMessage(session.RoleAssistant, "parent finding"),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := parent.Append(
		session.EventModelResponse, assistant.ID, session.ResponseData{
			ProviderResponseID: "resp_parent",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := parent.Append(
		session.EventAssistantMessage, response.ID,
		session.AssistantToolCallMessage("", []session.ToolCallData{{
			ID:        "call_task",
			Name:      tool.NameTask,
			Arguments: `{"profile":"explore","task":"review"}`,
		}}),
	)
	if err != nil {
		t.Fatal(err)
	}

	client := &scriptedToolClient{events: [][]model.Event{{
		{
			Type: model.EventTextDelta,
			Text: "child done",
		},
		{
			Type: model.EventDone,
		},
	}}}
	result, err := RunTurn(context.Background(), TurnRequest{
		SessionDir:        dir,
		CWD:               "/work/project",
		Prompt:            "child task",
		ForkSessionPath:   parent.Path(),
		ForkBeforeEventID: boundary.ID,
		Model:             client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.requests[0].PreviousResponseID != "" {
		t.Fatalf("child reused parent continuation: %#v",
			client.requests[0])
	}
	messages := client.requests[0].Messages
	if len(messages) != 3 {
		t.Fatalf("unexpected child messages: %#v", messages)
	}
	if messages[0].Role != model.RoleUser ||
		messages[0].Content != "parent prompt" ||
		messages[1].Role != model.RoleAssistant ||
		messages[1].Content != "parent finding" ||
		messages[2].Role != model.RoleUser ||
		messages[2].Content != "child task" {

		t.Fatalf("unexpected forked messages: %#v", messages)
	}
	childEvents, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var started session.StartedData
	if err := json.Unmarshal(childEvents[0].Data, &started); err != nil {
		t.Fatal(err)
	}
	if started.ForkSessionPath != parent.Path() ||
		started.ForkBeforeEventID != boundary.ID {

		t.Fatalf("unexpected fork metadata: %#v", started)
	}

	resumeClient := &scriptedToolClient{events: [][]model.Event{{
		{
			Type: model.EventTextDelta,
			Text: "continued",
		},
		{
			Type: model.EventDone,
		},
	}}}
	_, err = RunTurn(context.Background(), TurnRequest{
		SessionPath: result.SessionPath,
		CWD:         "/work/project",
		Prompt:      "child follow-up",
		Model:       resumeClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed := resumeClient.requests[0].Messages
	if len(resumed) != 5 {
		t.Fatalf("unexpected resumed child messages: %#v", resumed)
	}
	if resumed[0].Content != "parent prompt" ||
		resumed[1].Content != "parent finding" ||
		resumed[2].Content != "child task" ||
		resumed[3].Content != "child done" ||
		resumed[4].Content != "child follow-up" {

		t.Fatalf("resumed child lost fork context: %#v", resumed)
	}
}

// TestRunTurnContinuesWithLifecycleHooks verifies non-context hooks do not
// force full-context provider requests.
func TestRunTurnContinuesWithLifecycleHooks(t *testing.T) {
	first := createContinuationSession(t, "first", "resp_first")
	cwd := t.TempDir()
	runner, err := hooks.New([]config.HookConfig{{
		Event:   hooks.EventTurnStart,
		Command: "printf '{}'",
	}}, cwd)
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedToolClient{
		events: [][]model.Event{{
			{
				Type: model.EventTextDelta,
				Text: "second",
			},
			{
				Type: model.EventDone,
			},
		}},
	}

	if _, err := RunTurn(context.Background(), TurnRequest{
		Prompt:      "follow-up",
		SessionPath: first.SessionPath,
		CWD:         cwd,
		Model:       client,
		Hooks:       runner,
	}); err != nil {

		t.Fatal(err)
	}
	if client.requests[0].PreviousResponseID != "resp_first" {
		t.Fatalf("continuation was disabled: %#v", client.requests[0])
	}
}

// TestRunTurnDisablesContinuationWithContextHooks verifies context-build hooks
// remain a full-context boundary because they can rewrite transient messages.
func TestRunTurnDisablesContinuationWithContextHooks(t *testing.T) {
	first := createContinuationSession(t, "first", "resp_first")
	cwd := t.TempDir()
	runner, err := hooks.New([]config.HookConfig{{
		Event:   hooks.EventContextBuild,
		Command: "printf '{}'",
	}}, cwd)
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedToolClient{
		events: [][]model.Event{{
			{
				Type: model.EventTextDelta,
				Text: "second",
			},
			{
				Type: model.EventDone,
			},
		}},
	}

	if _, err := RunTurn(context.Background(), TurnRequest{
		Prompt:      "follow-up",
		SessionPath: first.SessionPath,
		CWD:         cwd,
		Model:       client,
		Hooks:       runner,
	}); err != nil {

		t.Fatal(err)
	}
	if client.requests[0].PreviousResponseID != "" {
		t.Fatalf("context hook did not disable continuation: %#v",
			client.requests[0])
	}
	if len(client.requests[0].DeltaMessages) != 0 {
		t.Fatalf("unexpected delta messages: %#v",
			client.requests[0].DeltaMessages)
	}
}

// createContinuationSession writes a one-turn session with response identity.
func createContinuationSession(t *testing.T, text string,
	responseID string) *TurnResult {

	t.Helper()
	client := &scriptedToolClient{
		events: [][]model.Event{{
			{
				Type: model.EventTextDelta,
				Text: text,
			},
			{Type: model.EventResponseInfo, ResponseInfo: model.ResponseInfo{
				ProviderResponseID: responseID,
			}},
			{
				Type: model.EventDone,
			},
		}},
	}
	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     text,
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      client,
	})
	if err != nil {
		t.Fatal(err)
	}

	return result
}

// TestRunTurnExecutesToolCalls verifies that the core can run one model
// requested tool call and feed the result back into a final model answer.
func TestRunTurnExecutesToolCalls(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "")

	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:   "call_1",
						Name: tool.NameLS,
						Arguments: `{"path":` +
							quoteJSON(dir) +
							`}`,
					},
				},
				{Type: model.EventResponseInfo, ResponseInfo: model.ResponseInfo{
					ProviderResponseID: "resp_tools",
				}},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "I found go.mod.",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}

	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "list files",
		SessionDir: t.TempDir(),
		CWD:        dir,
		Model:      client,
		Tools:      tool.DefaultRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssistantText != "I found go.mod." {
		t.Fatalf("unexpected assistant text: %q", result.AssistantText)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected two model requests, got %d",
			len(client.requests))
	}
	if !hasToolSpec(client.requests[0].Tools, tool.NameLS) {
		t.Fatalf("expected first request to include ls tool: %#v",
			client.requests[0].Tools)
	}
	last := client.requests[1].Messages[len(client.requests[1].Messages)-1]
	if last.Role != model.RoleTool || last.Content != "go.mod" {
		t.Fatalf("expected tool result in second request, got %#v",
			last)
	}
	if client.requests[1].PreviousResponseID != "resp_tools" {
		t.Fatalf("unexpected previous response id: %q",
			client.requests[1].PreviousResponseID)
	}
	if len(client.requests[1].DeltaMessages) != 1 ||
		client.requests[1].DeltaMessages[0].Role != model.RoleTool ||
		client.requests[1].DeltaMessages[0].Content != "go.mod" {

		t.Fatalf("unexpected tool delta messages: %#v",
			client.requests[1].DeltaMessages)
	}

	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("expected six session events, got %d", len(events))
	}
	if events[4].Type != session.EventToolMessage {
		t.Fatalf("expected tool message event, got %q", events[4].Type)
	}
}

// TestToolExecutionGroupsKeepMutationBarriers verifies read-only calls batch
// together while mutating calls split the execution stream.
func TestToolExecutionGroupsKeepMutationBarriers(t *testing.T) {
	groups := toolExecutionGroups([]model.ToolCall{
		{ID: "call_1", Name: tool.NameRead},
		{ID: "call_2", Name: tool.NameGrep},
		{ID: "call_3", Name: tool.NameWrite},
		{ID: "call_4", Name: tool.NameRead},
	}, tool.DefaultRegistry())

	if len(groups) != 3 {
		t.Fatalf("expected three execution groups, got %#v", groups)
	}
	if len(groups[0]) != 2 || groups[0][0].ID != "call_1" ||
		groups[0][1].ID != "call_2" {

		t.Fatalf("unexpected first read-only group: %#v", groups[0])
	}
	if len(groups[1]) != 1 || groups[1][0].ID != "call_3" {
		t.Fatalf("unexpected mutation barrier group: %#v", groups[1])
	}
	if len(groups[2]) != 1 || groups[2][0].ID != "call_4" {
		t.Fatalf("unexpected trailing read group: %#v", groups[2])
	}
}

// TestToolExecutionGroupsHonorPerCallSafety verifies stateful tools can
// classify individual calls as parallel-safe or as serial barriers.
func TestToolExecutionGroupsHonorPerCallSafety(t *testing.T) {
	registry := tool.DefaultRegistry()
	registry.Register(callSafetyTool{})
	groups := toolExecutionGroups([]model.ToolCall{
		{ID: "call_1", Name: tool.NameRead},
		{
			ID:        "call_2",
			Name:      "maybe_parallel",
			Arguments: `{"safe":true}`,
		},
		{
			ID:        "call_3",
			Name:      "maybe_parallel",
			Arguments: `{"safe":false}`,
		},
		{ID: "call_4", Name: tool.NameRead},
	}, registry)

	if len(groups) != 3 {
		t.Fatalf("expected three execution groups, got %#v", groups)
	}
	if len(groups[0]) != 2 || groups[0][0].ID != "call_1" ||
		groups[0][1].ID != "call_2" {

		t.Fatalf("unexpected first parallel group: %#v", groups[0])
	}
	if len(groups[1]) != 1 || groups[1][0].ID != "call_3" {
		t.Fatalf("unexpected serial barrier group: %#v", groups[1])
	}
	if len(groups[2]) != 1 || groups[2][0].ID != "call_4" {
		t.Fatalf("unexpected trailing parallel group: %#v", groups[2])
	}
}

// TestToolExecutionGroupsSeparateTaskBatchesFromReads verifies subagent calls
// can overlap each other without mingling with parent read-only batches.
func TestToolExecutionGroupsSeparateTaskBatchesFromReads(t *testing.T) {
	registry := tool.DefaultRegistry()
	registry.Register(taskSafetyTool{})
	groups := toolExecutionGroups([]model.ToolCall{
		{ID: "call_1", Name: tool.NameRead},
		{ID: "call_2", Name: tool.NameTask},
		{ID: "call_3", Name: tool.NameTask},
		{ID: "call_4", Name: tool.NameGrep},
	}, registry)

	if len(groups) != 3 {
		t.Fatalf("expected three execution groups, got %#v", groups)
	}
	if len(groups[0]) != 1 || groups[0][0].ID != "call_1" {
		t.Fatalf("unexpected leading read group: %#v", groups[0])
	}
	if len(groups[1]) != 2 || groups[1][0].ID != "call_2" ||
		groups[1][1].ID != "call_3" {

		t.Fatalf("unexpected task group: %#v", groups[1])
	}
	if len(groups[2]) != 1 || groups[2][0].ID != "call_4" {
		t.Fatalf("unexpected trailing read group: %#v", groups[2])
	}
}

// TestFilterIncompleteToolCallEventsDropsDanglingAssistant verifies interrupted
// tool-call turns do not poison later provider replay.
func TestFilterIncompleteToolCallEventsDropsDanglingAssistant(t *testing.T) {
	events := []session.Event{
		testCoreEvent(
			"start", "", session.EventSessionStarted,
			session.StartedData{},
		),
		testCoreEvent(
			"user-1", "start", session.EventUserMessage,
			session.TextMessage(session.RoleUser, "first"),
		),
		testCoreEvent(
			"assistant-1", "user-1", session.EventAssistantMessage,
			session.TextMessage(session.RoleAssistant, "done"),
		),
		testCoreEvent(
			"response-1", "assistant-1", session.EventModelResponse,
			session.ResponseData{
				ProviderResponseID: "resp_complete",
			},
		),
		testCoreEvent(
			"user-2", "response-1", session.EventUserMessage,
			session.TextMessage(session.RoleUser, "delegate"),
		),
		testCoreEvent(
			"reasoning-2", "user-2", session.EventModelReasoning,
			session.ReasoningData{
				Reasoning: "planning tasks",
			},
		),
		testCoreEvent(
			"assistant-2", "reasoning-2",
			session.EventAssistantMessage,
			session.AssistantToolCallMessage(
				"",
				[]session.ToolCallData{
					{ID: "call_1", Name: tool.NameTask},
					{ID: "call_2", Name: tool.NameTask},
				},
			),
		),
		testCoreEvent(
			"usage-2", "assistant-2", session.EventModelUsage,
			session.UsageData{
				InputTokens: 1,
				TotalTokens: 1,
			},
		),
		testCoreEvent(
			"response-2", "usage-2", session.EventModelResponse,
			session.ResponseData{
				ProviderResponseID: "resp_dangling",
			},
		),
		testCoreEvent(
			"user-3", "response-2", session.EventUserMessage,
			session.TextMessage(session.RoleUser, "retry"),
		),
	}

	filtered, err := filterIncompleteToolCallEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	typesByID := make(map[string]string)
	for _, event := range filtered {
		typesByID[event.ID] = event.Type
	}
	for _, id := range []string{
		"reasoning-2", "assistant-2", "usage-2", "response-2",
	} {
		if _, ok := typesByID[id]; ok {
			t.Fatalf("dangling event %s was kept: %#v", id,
				filtered)
		}
	}
	if _, ok := typesByID["user-3"]; !ok {
		t.Fatalf("later user prompt was dropped: %#v", filtered)
	}

	messages, err := prompt.BuildHistoryMessages(prompt.HistoryRequest{
		Events: filtered,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if len(message.ToolCalls) > 0 {
			t.Fatalf("dangling tool calls reached replay: %#v",
				messages)
		}
	}
	continuation, err := initialResponseContinuation(filtered, "")
	if err != nil {
		t.Fatal(err)
	}
	if continuation.PreviousResponseID != "resp_complete" {
		t.Fatalf("unexpected continuation response: %#v", continuation)
	}
	if len(continuation.DeltaMessages) != 2 ||
		continuation.DeltaMessages[0].Content != "delegate" ||
		continuation.DeltaMessages[1].Content != "retry" {

		t.Fatalf("unexpected continuation delta: %#v",
			continuation.DeltaMessages)
	}
}

// TestRunTurnExecutesReadOnlyToolGroupConcurrently verifies model-requested
// read-only batches overlap in wall time while preserving ordered results.
func TestRunTurnExecutesReadOnlyToolGroupConcurrently(t *testing.T) {
	blocking := newBlockingReadTool(2)
	registry := tool.NewRegistry()
	registry.Register(blocking)
	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:        "call_1",
						Name:      tool.NameRead,
						Arguments: `{"path":"one"}`,
					},
				},
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:        "call_2",
						Name:      tool.NameRead,
						Arguments: `{"path":"two"}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "done",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	type turnResult struct {
		result *TurnResult
		err    error
	}
	done := make(chan turnResult, 1)
	go func() {
		result, err := RunTurn(ctx, TurnRequest{
			Prompt:     "read both",
			SessionDir: t.TempDir(),
			CWD:        "/work/project",
			Model:      client,
			Tools:      registry,
		})
		done <- turnResult{result: result, err: err}
	}()

	select {
	case <-blocking.allStarted:
		close(blocking.release)

	case <-ctx.Done():
		t.Fatal("read-only tool group did not start concurrently")
	}

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	events, err := session.ReadAll(got.result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var toolTexts []string
	for _, event := range events {
		if event.Type != session.EventToolMessage {
			continue
		}
		var message session.MessageData
		if err := json.Unmarshal(event.Data, &message); err != nil {
			t.Fatal(err)
		}
		toolTexts = append(toolTexts, summaryMessageText(message))
	}
	if len(toolTexts) != 2 || toolTexts[0] != "read {\"path\":\"one\"}" ||
		toolTexts[1] != "read {\"path\":\"two\"}" {

		t.Fatalf("tool results were not appended in call order: %#v",
			toolTexts)
	}
}

// TestRunTurnSerializesSharedParallelLane verifies stateful parallel-safe
// tools can opt into a per-resource lane without disabling the whole batch.
func TestRunTurnSerializesSharedParallelLane(t *testing.T) {
	laneTool := newLaneBlockingTool()
	registry := tool.NewRegistry()
	registry.Register(laneTool)
	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:        "call_1",
						Name:      "lane_read",
						Arguments: `{"path":"one"}`,
					},
				},
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:        "call_2",
						Name:      "lane_read",
						Arguments: `{"path":"two"}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "done",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := RunTurn(ctx, TurnRequest{
			Prompt:     "read both",
			SessionDir: t.TempDir(),
			CWD:        "/work/project",
			Model:      client,
			Tools:      registry,
		})
		done <- err
	}()

	<-laneTool.started
	select {
	case second := <-laneTool.started:
		t.Fatalf("shared-lane call started concurrently: %s", second)

	case <-time.After(100 * time.Millisecond):
	}
	close(laneTool.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := laneTool.StartedCount(); got != 2 {
		t.Fatalf("expected two serialized starts, got %d", got)
	}
}

// TestRunTurnProvidesToolExecutionContext verifies stateful tools can observe
// the durable parent session and tool-call metadata around their execution.
func TestRunTurnProvidesToolExecutionContext(t *testing.T) {
	recording := &contextRecordingTool{}
	registry := tool.NewRegistry()
	registry.Register(recording)
	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:        "call_context",
						Name:      "record_context",
						Arguments: `{}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "done",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}

	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "record context",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      client,
		Tools:      registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recording.meta.SessionID != result.SessionID ||
		recording.meta.SessionPath != result.SessionPath ||
		recording.meta.ToolCallID != "call_context" ||
		recording.meta.AssistantEventID == "" {

		t.Fatalf("unexpected execution context: %#v", recording.meta)
	}
}

// TestRunTurnInjectsSteeringAfterToolBatch verifies queued steering is admitted
// after tool results and before the next model request.
func TestRunTurnInjectsSteeringAfterToolBatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "")

	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:   "call_1",
						Name: tool.NameLS,
						Arguments: `{"path":` +
							quoteJSON(dir) +
							`}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "I included the steering.",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}
	drained := false

	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "list files",
		SessionDir: t.TempDir(),
		CWD:        dir,
		Model:      client,
		Tools:      tool.DefaultRegistry(),
		DrainSteering: func() []string {
			if drained {
				return nil
			}
			drained = true

			return []string{
				"also explain why",
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssistantText != "I included the steering." {
		t.Fatalf("unexpected assistant text: %q", result.AssistantText)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected two model requests, got %d",
			len(client.requests))
	}
	messages := client.requests[1].Messages
	last := messages[len(messages)-1]
	if last.Role != model.RoleUser ||
		last.Content != "also explain why" {

		t.Fatalf("missing steering message: %#v", messages)
	}
	previous := messages[len(messages)-2]
	if previous.Role != model.RoleTool {
		t.Fatalf("steering was not placed after tool result: %#v",
			messages)
	}
}

// TestRunTurnCompleteHookIncludesSteeringPrompts verifies turn completion
// payloads expose every prompt admitted during a steered turn.
func TestRunTurnCompleteHookIncludesSteeringPrompts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "")
	logPath := filepath.Join(dir, "hooks.log")
	hookScript := filepath.Join(dir, "record-hook.sh")
	writeFile(
		t, hookScript, "#!/bin/sh\npayload=$(cat)\nprintf '%s\n' "+
			"\"$payload\" >> \"$HOOK_LOG\"\nprintf '{}'\n",
	)
	if err := os.Chmod(hookScript, 0o755); err != nil {
		t.Fatalf("chmod hook: %v", err)
	}
	command := "HOOK_LOG=" + shellQuote(logPath) + " " +
		shellQuote(hookScript)
	runner, err := hooks.New([]config.HookConfig{
		{Event: hooks.EventTurnComplete, Command: command},
	}, dir)
	if err != nil {
		t.Fatalf("create hooks: %v", err)
	}
	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:   "call_1",
						Name: tool.NameLS,
						Arguments: `{"path":` +
							quoteJSON(dir) + `}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "done",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}
	drained := false

	_, err = RunTurn(context.Background(), TurnRequest{
		Prompt:     "list files",
		SessionDir: filepath.Join(dir, "sessions"),
		CWD:        dir,
		Model:      client,
		Tools:      tool.DefaultRegistry(),
		Hooks:      runner,
		DrainSteering: func() []string {
			if drained {
				return nil
			}
			drained = true

			return []string{
				"also explain why",
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read hook log: %v", err)
	}
	var envelope struct {
		Payload struct {
			Prompt      string   `json:"prompt"`
			UserPrompts []string `json:"userPrompts"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(
		[]byte(
			strings.TrimSpace(
				string(data),
			),
		),
		&envelope,
	); err != nil {

		t.Fatalf("decode hook envelope: %v", err)
	}
	if envelope.Payload.Prompt != "list files" {
		t.Fatalf("initial prompt = %q", envelope.Payload.Prompt)
	}
	want := []string{"list files", "also explain why"}
	if !equalStrings(envelope.Payload.UserPrompts, want) {
		t.Fatalf("user prompts mismatch:\nwant %#v\ngot  %#v", want,
			envelope.Payload.UserPrompts)
	}
}

// TestRunTurnAppliesPreToolUseHook verifies tool preflight hooks can transform
// arguments before persistence and execution.
func TestRunTurnAppliesPreToolUseHook(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	requested := filepath.Join(dir, "requested.txt")
	rewrite := filepath.Join(dir, "rewrite.txt")
	writeFile(t, requested, "requested")
	writeFile(t, rewrite, "rewritten")

	hookScript := filepath.Join(dir, "hook.sh")
	hookOutputBytes, err := json.Marshal(struct {
		Arguments string `json:"arguments"`
	}{
		Arguments: `{"path":` + quoteJSON(rewrite) + `}`,
	})
	if err != nil {
		t.Fatalf("marshal hook output: %v", err)
	}
	writeFile(
		t, hookScript, "#!/bin/sh\ncat >/dev/null\ncat <<'JSON'\n"+
			string(hookOutputBytes)+"\nJSON\n",
	)
	if err := os.Chmod(hookScript, 0o755); err != nil {
		t.Fatalf("chmod hook: %v", err)
	}
	runner, err := hooks.New([]config.HookConfig{{
		Event:   hooks.EventPreToolUse,
		Matcher: tool.NameRead,
		Command: hookScript,
	}}, dir)
	if err != nil {
		t.Fatalf("create hooks: %v", err)
	}

	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:   "call_1",
						Name: tool.NameRead,
						Arguments: `{"path":` +
							quoteJSON(
								requested,
							) +
							`}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "done",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}

	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "read",
		SessionDir: filepath.Join(dir, "sessions"),
		CWD:        dir,
		Model:      client,
		Tools:      tool.DefaultRegistry(),
		Hooks:      runner,
	})
	if err != nil {
		t.Fatal(err)
	}

	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var assistant session.MessageData
	if err := json.Unmarshal(events[2].Data, &assistant); err != nil {
		t.Fatal(err)
	}
	if assistant.ToolCalls[0].Arguments != `{"path":`+quoteJSON(rewrite)+`}` {
		t.Fatalf("tool call was not rewritten: %#v",
			assistant.ToolCalls[0])
	}
	last := client.requests[1].Messages[len(client.requests[1].Messages)-1]
	if !strings.Contains(last.Content, "rewritten") {
		t.Fatalf("rewritten file was not read: %#v", last)
	}
}

// TestRunTurnNotifiesObserver verifies that callers can render persisted
// assistant and tool events as the loop progresses.
func TestRunTurnNotifiesObserver(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "")
	observer := &recordingObserver{}

	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventReasoningDelta,
					Text: "checking files",
				},
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:   "call_1",
						Name: tool.NameLS,
						Arguments: `{"path":` +
							quoteJSON(dir) +
							`}`,
					},
				},
				{
					Type: model.EventMetrics,
					Metrics: model.Metrics{
						RequestBytes:  100,
						ResponseBytes: 40,
						TimeToHeaders: time.Millisecond,
						TimeToFirstEvent: 2 *
							time.Millisecond,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "done",
				},
				{
					Type: model.EventMetrics,
					Metrics: model.Metrics{
						RequestBytes:  120,
						ResponseBytes: 60,
						TimeToHeaders: 3 *
							time.Millisecond,
						TimeToFirstEvent: 4 *
							time.Millisecond,
					},
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}

	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "list files",
		SessionDir: t.TempDir(),
		CWD:        dir,
		Model:      client,
		Tools:      tool.DefaultRegistry(),
		Observer:   observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := observer.types()
	want := []string{
		session.EventUserMessage,
		session.EventModelReasoning,
		session.EventAssistantMessage,
		session.EventModelMetrics,
		session.EventToolMessage,
		session.EventAssistantMessage,
		session.EventModelMetrics,
	}
	if !equalStrings(got, want) {
		t.Fatalf("observer event mismatch:\nwant %#v\ngot  %#v", want,
			got)
	}
	if len(observer.toolCalls) != 1 {
		t.Fatalf("expected one live tool call, got %d",
			len(observer.toolCalls))
	}
	if observer.toolCalls[0].Name != tool.NameLS {
		t.Fatalf("unexpected live tool call: %#v",
			observer.toolCalls[0])
	}
	if len(observer.finishedToolCalls) != 1 {
		t.Fatalf("expected one live tool finish, got %d",
			len(observer.finishedToolCalls))
	}
	if observer.finishedToolCalls[0].Name != tool.NameLS {
		t.Fatalf("unexpected live tool finish: %#v",
			observer.finishedToolCalls[0])
	}
	if len(observer.reasoning) != 1 ||
		observer.reasoning[0] != "checking files" {

		t.Fatalf("unexpected reasoning summaries: %#v",
			observer.reasoning)
	}
	if len(observer.reasoningDeltas) != 1 ||
		observer.reasoningDeltas[0] != "checking files" {

		t.Fatalf("unexpected reasoning deltas: %#v",
			observer.reasoningDeltas)
	}
	if len(observer.textDeltas) != 1 || observer.textDeltas[0] != "done" {
		t.Fatalf("unexpected text deltas: %#v", observer.textDeltas)
	}
	if observer.timing.ModelDuration <= 0 {
		t.Fatalf("missing model timing: %#v", observer.timing)
	}
	if observer.timing.ModelCalls != 2 ||
		observer.timing.RequestBytes != 220 ||
		observer.timing.ResponseBytes != 100 ||
		observer.timing.TimeToHeaders != 4*time.Millisecond ||
		observer.timing.TimeToFirstEvent != 6*time.Millisecond {

		t.Fatalf("unexpected model metrics: %#v", observer.timing)
	}
	if observer.timing.ToolBatches != 1 ||
		observer.timing.LargestToolBatch != 1 {

		t.Fatalf("unexpected tool batch timing: %#v", observer.timing)
	}
	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 || events[2].Type != session.EventModelReasoning ||
		events[3].Type != session.EventAssistantMessage {

		t.Fatalf("reasoning was not persisted before assistant: %#v",
			events)
	}
}

// TestRunTurnAutoCompactsLargeContext verifies that automatic compaction
// appends a summary before the model call that answers the current turn.
func TestRunTurnAutoCompactsLargeContext(t *testing.T) {
	dir := t.TempDir()
	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventTextDelta,
					Text: "older turns summary",
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "final",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}
	observer := &recordingObserver{}

	first, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     strings.Repeat("alpha ", 60),
		SessionDir: filepath.Join(dir, "sessions"),
		CWD:        dir,
		Model:      model.EchoClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:                     strings.Repeat("beta ", 60),
		SessionPath:                first.SessionPath,
		CWD:                        dir,
		Model:                      client,
		ModelName:                  "test-model",
		AutoCompactThresholdTokens: 20,
		AutoCompactKeepMessages:    1,
		Observer:                   observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssistantText != "final" {
		t.Fatalf("unexpected assistant text: %q", result.AssistantText)
	}
	if len(observer.autoCompactions) != 1 {
		t.Fatalf("expected auto compaction, got %#v", observer)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected compaction and answer calls, got %d",
			len(client.requests))
	}

	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var summary session.SummaryData
	var summaryEvent session.Event
	for _, event := range events {
		if event.Type == session.EventContextSummary {
			summaryEvent = event
			if err := json.Unmarshal(
				event.Data, &summary,
			); err != nil {

				t.Fatal(err)
			}
		}
	}
	if summaryEvent.ID == "" {
		t.Fatal("missing context summary event")
	}
	if summary.Trigger != "auto" || summary.Model != "test-model" {
		t.Fatalf("unexpected summary metadata: %#v", summary)
	}
	if events[len(events)-1].ParentID != summaryEvent.ID {
		t.Fatalf("assistant should parent to summary: %#v", events)
	}
}

// TestAutoCompactHasUsefulReplayRejectsDominantSummary verifies auto
// compaction does not repeatedly summarize when the existing summary is the
// larger part of the projected context.
func TestAutoCompactHasUsefulReplayRejectsDominantSummary(t *testing.T) {
	stats := prompt.Stats{
		SummaryActive:   true,
		SummaryTokens:   100,
		RawReplayTokens: 50,
	}
	if autoCompactHasUsefulReplay(stats, 20) {
		t.Fatal("expected dominant summary to suppress auto compaction")
	}
}

// TestRunTurnFeedsToolErrorsBackToModel verifies that ordinary tool failures
// are persisted as tool results so the model can recover.
func TestRunTurnFeedsToolErrorsBackToModel(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "hello.md"), "hello\n")

	client := &scriptedToolClient{
		events: [][]model.Event{
			{
				{
					Type: model.EventToolCall,
					ToolCall: model.ToolCall{
						ID:   "call_1",
						Name: tool.NameEdit,
						Arguments: `{"path":"hello.md","edits":[` +
							`{"oldText":"","newText":` +
							`"hello world\n"}]}`,
					},
				},
				{
					Type: model.EventDone,
				},
			},
			{
				{
					Type: model.EventTextDelta,
					Text: "I need a non-empty oldText.",
				},
				{
					Type: model.EventDone,
				},
			},
		},
	}

	result, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "edit hello.md",
		SessionDir: t.TempDir(),
		CWD:        dir,
		Model:      client,
		Tools:      tool.DefaultRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssistantText != "I need a non-empty oldText." {
		t.Fatalf("unexpected assistant text: %q", result.AssistantText)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected two model requests, got %d",
			len(client.requests))
	}
	last := client.requests[1].Messages[len(client.requests[1].Messages)-1]
	if last.Role != model.RoleTool {
		t.Fatalf("expected tool error message, got %#v", last)
	}
	if last.Content != "tool error: edit 1 oldText must not be empty" {
		t.Fatalf("unexpected tool error content: %q", last.Content)
	}
}

// TestRunTurnRequiresFinalAssistantResponse verifies that exhausting tool
// rounds after tool calls does not return an empty successful turn.
func TestRunTurnRequiresFinalAssistantResponse(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "")

	// customLimit keeps the exhaustion path fast while proving the request
	// override is honored.
	const customLimit = 2

	scripts := make([][]model.Event, 0, customLimit)
	for i := 0; i < customLimit; i++ {
		scripts = append(scripts, []model.Event{
			{
				Type: model.EventToolCall,
				ToolCall: model.ToolCall{
					ID:   "call_1",
					Name: tool.NameLS,
					Arguments: `{"path":` +
						quoteJSON(dir) +
						`}`,
				},
			},
			{
				Type: model.EventDone,
			},
		})
	}

	_, err := RunTurn(context.Background(), TurnRequest{
		Prompt:        "keep using tools",
		SessionDir:    t.TempDir(),
		CWD:           dir,
		Model:         &scriptedToolClient{events: scripts},
		Tools:         tool.DefaultRegistry(),
		MaxToolRounds: customLimit,
	})
	if err == nil {
		t.Fatal("expected tool round limit error")
	}
}

// hasToolSpec reports whether a request advertised the named tool.
func hasToolSpec(specs []model.ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Name == name {
			return true
		}
	}

	return false
}

// recordingObserver stores appended event notifications for tests.
type recordingObserver struct {
	// events stores notifications in arrival order.
	events []session.Event

	// toolCalls stores live tool-start notifications in arrival order.
	toolCalls []model.ToolCall

	// finishedToolCalls stores live tool-finish notifications in arrival
	// order.
	finishedToolCalls []model.ToolCall

	// toolBatches stores live batch notifications in arrival order.
	toolBatches [][]model.ToolCall

	// textDeltas stores streamed assistant text deltas in arrival order.
	textDeltas []string

	// reasoningDeltas stores streamed reasoning deltas in arrival order.
	reasoningDeltas []string

	// reasoning stores model-provided reasoning summaries in arrival order.
	reasoning []string

	// autoCompactions stores automatic context maintenance notifications.
	autoCompactions []AutoCompactResult

	// timing stores the final turn timing notification when present.
	timing TurnTiming
}

// EventAppended records one persisted event.
func (o *recordingObserver) EventAppended(event session.Event) {
	o.events = append(o.events, event)
}

// ToolCallStarted records one local tool execution start notification.
func (o *recordingObserver) ToolCallStarted(call model.ToolCall) {
	o.toolCalls = append(o.toolCalls, call)
}

// ToolCallFinished records one local tool execution finish notification.
func (o *recordingObserver) ToolCallFinished(call model.ToolCall) {
	o.finishedToolCalls = append(o.finishedToolCalls, call)
}

// ToolBatchStarted records one model-requested batch notification.
func (o *recordingObserver) ToolBatchStarted(calls []model.ToolCall) {
	o.toolBatches = append(
		o.toolBatches,
		append(
			[]model.ToolCall{}, calls...,
		),
	)
}

// ModelTextDelta records one streamed assistant text delta.
func (o *recordingObserver) ModelTextDelta(text string) {
	o.textDeltas = append(o.textDeltas, text)
}

// ModelReasoningDelta records one streamed reasoning delta.
func (o *recordingObserver) ModelReasoningDelta(text string) {
	o.reasoningDeltas = append(o.reasoningDeltas, text)
}

// ReasoningCompleted records one model reasoning summary notification.
func (o *recordingObserver) ReasoningCompleted(text string) {
	o.reasoning = append(o.reasoning, text)
}

// AutoCompacted records one automatic compaction notification.
func (o *recordingObserver) AutoCompacted(result AutoCompactResult) {
	o.autoCompactions = append(o.autoCompactions, result)
}

// TurnTiming records coarse turn timing after successful completion.
func (o *recordingObserver) TurnTiming(timing TurnTiming) {
	o.timing = timing
}

// types returns recorded event types in arrival order.
func (o *recordingObserver) types() []string {
	types := make([]string, 0, len(o.events))
	for _, event := range o.events {
		types = append(types, event.Type)
	}

	return types
}

// equalStrings reports whether two string slices have identical contents.
func equalStrings(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// scriptedToolClient returns predetermined event streams and records requests.
type scriptedToolClient struct {
	// events stores one event script per model request.
	events [][]model.Event

	// requests stores the model requests received by the fake client.
	requests []model.Request
}

// contextRecordingTool captures the tool execution context supplied by core.
type contextRecordingTool struct {
	// meta stores the last context metadata observed by Execute.
	meta tool.ExecutionContext
}

// blockingReadTool waits until a configured number of calls have started.
type blockingReadTool struct {
	// expected is the number of concurrent starts required to unblock
	// tests.
	expected int

	// allStarted closes after expected calls have entered Execute.
	allStarted chan struct{}

	// release lets blocked calls return to the core turn loop.
	release chan struct{}

	// once closes allStarted exactly once.
	once sync.Once

	// mu protects started.
	mu sync.Mutex

	// started counts calls that have entered Execute.
	started int
}

// laneBlockingTool records starts while all calls share one parallel lane.
type laneBlockingTool struct {
	// started receives one value each time Execute begins.
	started chan string

	// release lets the first call return so the second lane call may start.
	release chan struct{}

	// mu protects count.
	mu sync.Mutex

	// count records how many calls entered Execute.
	count int
}

// callSafetyTool classifies each call's parallel safety from its arguments.
type callSafetyTool struct{}

// taskSafetyTool classifies task calls as parallel-safe for grouping tests.
type taskSafetyTool struct{}

// Spec returns the schema for the context-recording test tool.
func (t *contextRecordingTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "record_context",
		Description: "Record the execution context.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

// Execute stores the execution context attached by the core dispatcher.
func (t *contextRecordingTool) Execute(ctx context.Context, arguments string) (
	tool.Result, error) {

	meta, ok := tool.ExecutionContextFrom(ctx)
	if !ok {
		return tool.Result{}, fmt.Errorf("missing execution context")
	}
	t.meta = meta

	return tool.Result{Text: "recorded"}, nil
}

// newBlockingReadTool creates a read tool fixture for parallelism tests.
func newBlockingReadTool(expected int) *blockingReadTool {
	return &blockingReadTool{
		expected:   expected,
		allStarted: make(chan struct{}),
		release:    make(chan struct{}),
	}
}

// newLaneBlockingTool creates a fixture for shared-lane scheduling tests.
func newLaneBlockingTool() *laneBlockingTool {
	return &laneBlockingTool{
		started: make(chan string, 2),
		release: make(chan struct{}),
	}
}

// Spec returns the read tool schema used by the blocking fixture.
func (t *blockingReadTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        tool.NameRead,
		Description: "Block until sibling reads start.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

// Execute waits for sibling calls before returning its argument text.
func (t *blockingReadTool) Execute(ctx context.Context, arguments string) (
	tool.Result, error) {

	t.mu.Lock()
	t.started++
	if t.started >= t.expected {
		t.once.Do(func() {
			close(t.allStarted)
		})
	}
	t.mu.Unlock()

	select {
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()

	case <-t.release:
		return tool.Result{Text: "read " + arguments}, nil
	}
}

// StartedCount returns how many lane calls entered Execute.
func (t *laneBlockingTool) StartedCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.count
}

// Spec returns the schema for the shared-lane test tool.
func (t *laneBlockingTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "lane_read",
		Description: "Block while sharing one parallel lane.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

// Execute records entry and blocks the first call until release closes.
func (t *laneBlockingTool) Execute(ctx context.Context, arguments string) (
	tool.Result, error) {

	t.mu.Lock()
	t.count++
	count := t.count
	t.mu.Unlock()
	t.started <- arguments
	if count == 1 {
		select {
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()

		case <-t.release:
		}
	}

	return tool.Result{Text: arguments}, nil
}

// ParallelSafe reports every lane_read call as locally parallel-safe.
func (t *laneBlockingTool) ParallelSafe(call model.ToolCall) bool {
	return true
}

// ParallelLane returns the shared test lane used to serialize calls.
func (t *laneBlockingTool) ParallelLane(call model.ToolCall) string {
	return "lane"
}

// Spec returns the schema for the per-call safety test tool.
func (callSafetyTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "maybe_parallel",
		Description: "Test per-call parallel safety.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

// Execute returns a placeholder result for the per-call safety test tool.
func (callSafetyTool) Execute(ctx context.Context, arguments string) (
	tool.Result, error) {

	return tool.Result{Text: arguments}, nil
}

// ParallelSafe reports whether the test call carries safe:true.
func (callSafetyTool) ParallelSafe(call model.ToolCall) bool {
	return strings.Contains(call.Arguments, `"safe":true`)
}

// Spec returns the schema for the task grouping test tool.
func (taskSafetyTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        tool.NameTask,
		Description: "Test subagent task grouping.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

// Execute returns a placeholder result for the task grouping test tool.
func (taskSafetyTool) Execute(ctx context.Context, arguments string) (
	tool.Result, error) {

	return tool.Result{Text: arguments}, nil
}

// ParallelSafe reports that task calls can overlap other task calls.
func (taskSafetyTool) ParallelSafe(call model.ToolCall) bool {
	return true
}

// Stream returns the next scripted event stream.
func (c *scriptedToolClient) Stream(ctx context.Context, req model.Request) (
	<-chan model.Event, error) {

	c.requests = append(c.requests, req)
	events := make(chan model.Event, len(c.events[0]))
	for _, event := range c.events[0] {
		events <- event
	}
	close(events)
	c.events = c.events[1:]

	return events, nil
}

// quoteJSON returns a JSON string literal for test tool arguments.
func quoteJSON(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}

	return string(encoded)
}

// testCoreEvent creates one session event with JSON-encoded fixture data.
func testCoreEvent(id string, parentID string, eventType string,
	data any) session.Event {

	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}

	return session.Event{
		Type:     eventType,
		ID:       id,
		ParentID: parentID,
		Time:     time.Unix(0, 0),
		Data:     encoded,
	}
}

// shellQuote returns a single-quoted shell word for test hook commands.
func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "'\\''") + "'"
}

// writeFile creates a small file fixture for core tests.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
