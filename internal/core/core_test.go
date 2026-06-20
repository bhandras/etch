package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if len(events) != 4 {
		t.Fatalf("expected four events, got %#v", events)
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
}

// TestRunTurnRejectsEmptyPrompt keeps invalid CLI input from creating empty
// session files.
func TestRunTurnRejectsEmptyPrompt(t *testing.T) {
	_, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "   ",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      model.EchoClient{},
	})
	if err == nil {
		t.Fatal("expected empty prompt error")
	}
}

// TestRunTurnContinuesExistingSession verifies that a follow-up turn appends
// to the same JSONL log and replays prior messages into the model request.
func TestRunTurnContinuesExistingSession(t *testing.T) {
	first, err := RunTurn(context.Background(), TurnRequest{
		Prompt:     "first",
		SessionDir: t.TempDir(),
		CWD:        "/work/project",
		Model:      model.EchoClient{},
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

	events, err := session.ReadAll(result.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("expected five session events, got %d", len(events))
	}
	if events[3].Type != session.EventToolMessage {
		t.Fatalf("expected tool message event, got %q", events[3].Type)
	}
}

// TestRunTurnAppliesPreToolUseHook verifies tool preflight hooks can transform
// arguments before persistence and execution.
func TestRunTurnAppliesPreToolUseHook(t *testing.T) {
	dir := t.TempDir()
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

	_, err := RunTurn(context.Background(), TurnRequest{
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
		session.EventAssistantMessage,
		session.EventToolMessage,
		session.EventAssistantMessage,
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
	if observer.timing.ToolBatches != 1 ||
		observer.timing.LargestToolBatch != 1 {

		t.Fatalf("unexpected tool batch timing: %#v", observer.timing)
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
