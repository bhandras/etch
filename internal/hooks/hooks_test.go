package hooks

import (
	"context"
	"testing"

	"harness/internal/config"
	"harness/internal/model"
)

// TestRunnerHasEventReportsActiveHooks verifies callers can distinguish
// context-rewriting hooks from unrelated lifecycle hooks.
func TestRunnerHasEventReportsActiveHooks(t *testing.T) {
	empty, err := New(nil, t.TempDir())
	if err != nil {
		t.Fatalf("create empty runner: %v", err)
	}
	if empty != nil {
		t.Fatalf("empty hook config should not create a runner: %#v",
			empty)
	}
	runner, err := New([]config.HookConfig{{
		Event:   EventTurnStart,
		Command: "printf '{}'",
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	if !runner.HasEvent(EventTurnStart) {
		t.Fatal("runner did not report configured event")
	}
	if runner.HasEvent(EventContextBuild) {
		t.Fatal("runner reported an unconfigured context hook")
	}
}

// TestPreToolUseTransformsArguments verifies command hook output can rewrite a
// pending tool call.
func TestPreToolUseTransformsArguments(t *testing.T) {
	runner, err := New([]config.HookConfig{{
		Event:   EventPreToolUse,
		Matcher: "bash",
		Command: "printf '{\"arguments\":\"{\\\\\"command\\\\\":\\\\\"pwd\\\\\"}\"}'",
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.PreToolUse(context.Background(), PreToolUseEvent{
		Tool: ToolCall{
			ID:        "call_1",
			Name:      "bash",
			Arguments: `{"command":"ls"}`,
		},
	})
	if err != nil {
		t.Fatalf("run hook: %v", err)
	}
	if result.Arguments == nil || *result.Arguments != `{"command":"pwd"}` {
		t.Fatalf("unexpected hook result: %#v", result)
	}
}

// TestUserPromptSubmitBlocksPrompt verifies prompt hooks can stop a turn before
// session mutation.
func TestUserPromptSubmitBlocksPrompt(t *testing.T) {
	runner, err := New([]config.HookConfig{{
		Event:   EventUserPromptSubmit,
		Command: "printf '{\"block\":true,\"reason\":\"nope\"}'",
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.UserPromptSubmit(
		context.Background(), UserPromptSubmitEvent{
			Prompt: "secret",
		},
	)
	if err != nil {
		t.Fatalf("run hook: %v", err)
	}
	if !result.Block || result.Reason != "nope" {
		t.Fatalf("unexpected block result: %#v", result)
	}
}

// TestUserPromptSubmitIgnoresMatcher verifies prompt hooks run even if a
// matcher is present because this event has no matcher target.
func TestUserPromptSubmitIgnoresMatcher(t *testing.T) {
	runner, err := New([]config.HookConfig{{
		Event:   EventUserPromptSubmit,
		Matcher: "^no-target$",
		Command: "printf '{\"prompt\":\"changed\"}'",
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result, err := runner.UserPromptSubmit(
		context.Background(), UserPromptSubmitEvent{
			Prompt: "original",
		},
	)
	if err != nil {
		t.Fatalf("run hook: %v", err)
	}
	if result.Prompt == nil || *result.Prompt != "changed" {
		t.Fatalf("unexpected prompt result: %#v", result)
	}
}

// TestContextBuildRoundTripsMessages verifies context hooks use explicit JSON
// field names rather than Go struct field names.
func TestContextBuildRoundTripsMessages(t *testing.T) {
	messages := []model.Message{{
		Role:    model.RoleUser,
		Content: "hello",
		ToolCalls: []model.ToolCall{{
			ID:        "call_1",
			Name:      "ls",
			Arguments: `{}`,
		}},
	}}
	converted := NeutralMessages(ModelMessages(messages))
	if len(converted) != 1 || converted[0].ToolCalls[0].Name != "ls" {
		t.Fatalf("unexpected round trip: %#v", converted)
	}
}
