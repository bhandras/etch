package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"harness/internal/config"
	"harness/internal/model"
	"harness/internal/platform"
)

const (
	// EventSessionStart fires after a session log is opened for a turn.
	EventSessionStart = "SessionStart"

	// EventUserPromptSubmit fires before a user prompt is persisted.
	EventUserPromptSubmit = "UserPromptSubmit"

	// EventTurnStart fires after the user prompt is persisted and before
	// the first model call for that prompt.
	EventTurnStart = "TurnStart"

	// EventTurnComplete fires after the final assistant response for a
	// user turn is persisted.
	EventTurnComplete = "TurnComplete"

	// EventContextBuild fires before each provider-neutral model request.
	EventContextBuild = "ContextBuild"

	// EventPreToolUse fires before a model-requested tool executes.
	EventPreToolUse = "PreToolUse"

	// EventPostToolUse fires after a tool returns and before the result is
	// persisted or sent back to the model.
	EventPostToolUse = "PostToolUse"

	// EventPreCompact fires before manual session compaction summarizes
	// history.
	EventPreCompact = "PreCompact"

	// EventPostCompact fires after a compaction summary is persisted.
	EventPostCompact = "PostCompact"

	// schemaVersion identifies the hook JSON envelope shape.
	schemaVersion = 1

	// defaultTimeoutSeconds is used when a hook omits a timeout.
	defaultTimeoutSeconds = 30
)

// Runner executes configured external process hooks.
type Runner struct {
	cwd   string
	hooks []config.HookConfig
}

// SessionStartEvent describes a session log opened for turn execution.
type SessionStartEvent struct {
	// SessionPath is the JSONL session log path.
	SessionPath string `json:"sessionPath"`

	// SessionID is the stable session identifier.
	SessionID string `json:"sessionId"`

	// Reason explains whether the session was newly created or resumed.
	Reason string `json:"reason"`
}

// UserPromptSubmitEvent describes a prompt before it is admitted to history.
type UserPromptSubmitEvent struct {
	// Prompt is the user text that will be stored and sent to the model.
	Prompt string `json:"prompt"`

	// SessionPath is the session being continued, when any.
	SessionPath string `json:"sessionPath,omitempty"`
}

// TurnStartEvent describes a user turn after prompt persistence.
type TurnStartEvent struct {
	// SessionPath is the JSONL session log path.
	SessionPath string `json:"sessionPath"`

	// SessionID is the stable session identifier.
	SessionID string `json:"sessionId"`

	// UserEventID is the durable user message event ID.
	UserEventID string `json:"userEventId"`

	// Prompt is the prompt admitted to the session.
	Prompt string `json:"prompt"`
}

// TurnCompleteEvent describes a completed user turn.
type TurnCompleteEvent struct {
	// SessionPath is the JSONL session log path.
	SessionPath string `json:"sessionPath"`

	// SessionID is the stable session identifier.
	SessionID string `json:"sessionId"`

	// UserEventID is the durable user message event ID.
	UserEventID string `json:"userEventId"`

	// AssistantEventID is the durable final assistant message event ID.
	AssistantEventID string `json:"assistantEventId"`

	// Prompt is the initial prompt admitted to the session.
	Prompt string `json:"prompt"`

	// UserPrompts stores every user prompt admitted during the turn,
	// including steering prompts.
	UserPrompts []string `json:"userPrompts,omitempty"`

	// AssistantText is the final assistant response text.
	AssistantText string `json:"assistantText"`

	// ToolCalls is the number of local tool calls requested by the model.
	ToolCalls int `json:"toolCalls"`
}

// UserPromptSubmitResult stores hook mutations for a submitted prompt.
type UserPromptSubmitResult struct {
	// Prompt replaces the user prompt when non-nil.
	Prompt *string `json:"prompt,omitempty"`

	// Block prevents the prompt from entering the session.
	Block bool `json:"block,omitempty"`

	// Reason explains why the prompt was blocked.
	Reason string `json:"reason,omitempty"`
}

// ContextBuildEvent describes the model context before a provider call.
type ContextBuildEvent struct {
	// SessionPath is the session log that produced Messages, when known.
	SessionPath string `json:"sessionPath,omitempty"`

	// Round is the zero-based model/tool exchange round in the user turn.
	Round int `json:"round"`

	// Messages is the provider-neutral context for the next model call.
	Messages []Message `json:"messages"`
}

// ContextBuildResult stores hook mutations for provider-neutral context.
type ContextBuildResult struct {
	// Messages replaces the model context when non-empty.
	Messages []Message `json:"messages,omitempty"`
}

// PreToolUseEvent describes a tool call before local execution.
type PreToolUseEvent struct {
	// SessionPath is the active session log path.
	SessionPath string `json:"sessionPath,omitempty"`

	// Tool is the model-requested tool call about to execute.
	Tool ToolCall `json:"tool"`
}

// PreToolUseResult stores hook mutations for a pending tool call.
type PreToolUseResult struct {
	// Arguments replaces the raw JSON tool arguments when non-nil.
	Arguments *string `json:"arguments,omitempty"`

	// Block prevents tool execution and returns Reason as tool feedback.
	Block bool `json:"block,omitempty"`

	// Reason explains why the hook blocked the tool.
	Reason string `json:"reason,omitempty"`
}

// PostToolUseEvent describes a completed tool call.
type PostToolUseEvent struct {
	// SessionPath is the active session log path.
	SessionPath string `json:"sessionPath,omitempty"`

	// Tool is the executed tool call.
	Tool ToolCall `json:"tool"`

	// Output is the model-visible tool output.
	Output string `json:"output"`

	// Error reports whether the tool failed before post-hook mutation.
	Error bool `json:"error"`
}

// PostToolUseResult stores hook mutations for a completed tool call.
type PostToolUseResult struct {
	// Output replaces the model-visible tool output when non-nil.
	Output *string `json:"output,omitempty"`

	// Error replaces the error marker when non-nil.
	Error *bool `json:"error,omitempty"`
}

// PreCompactEvent describes an imminent compaction pass.
type PreCompactEvent struct {
	// SessionPath is the session log being compacted.
	SessionPath string `json:"sessionPath"`

	// Trigger describes why compaction started.
	Trigger string `json:"trigger"`

	// RangeStartID is the first event included in the compaction range.
	RangeStartID string `json:"rangeStartId"`

	// RangeEndID is the last event included in the compaction range.
	RangeEndID string `json:"rangeEndId"`

	// FirstKeptEventID is the first raw event retained after compaction.
	FirstKeptEventID string `json:"firstKeptEventId"`
}

// PreCompactResult stores hook decisions for compaction.
type PreCompactResult struct {
	// Block cancels compaction.
	Block bool `json:"block,omitempty"`

	// Reason explains why compaction was cancelled.
	Reason string `json:"reason,omitempty"`

	// Summary supplies a custom summary and skips model summarization.
	Summary *string `json:"summary,omitempty"`
}

// PostCompactEvent describes a persisted compaction summary.
type PostCompactEvent struct {
	// SessionPath is the compacted session log.
	SessionPath string `json:"sessionPath"`

	// Trigger describes why compaction started.
	Trigger string `json:"trigger"`

	// SummaryEventID is the durable context.summary event ID.
	SummaryEventID string `json:"summaryEventId"`

	// FirstKeptEventID is the first raw event retained after compaction.
	FirstKeptEventID string `json:"firstKeptEventId"`

	// Summary is the text persisted in the compaction event.
	Summary string `json:"summary"`
}

// Message is the hook JSON shape for provider-neutral model messages.
type Message struct {
	// Role identifies the message speaker.
	Role string `json:"role"`

	// Content stores plain text message content.
	Content string `json:"content,omitempty"`

	// ToolCalls stores assistant-requested tool calls.
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`

	// ToolCallID links a tool result to its assistant tool call.
	ToolCallID string `json:"toolCallId,omitempty"`

	// Name records the tool name for tool result messages.
	Name string `json:"name,omitempty"`
}

// ToolCall is the hook JSON shape for model-requested tool calls.
type ToolCall struct {
	// ID is the provider-assigned tool call ID.
	ID string `json:"id"`

	// Name is the model-facing tool name.
	Name string `json:"name"`

	// Arguments stores the raw JSON argument object.
	Arguments string `json:"arguments"`
}

// envelope is the stable JSON object passed to every hook process.
type envelope struct {
	// Version identifies the hook schema version.
	Version int `json:"version"`

	// Event is the lifecycle event name.
	Event string `json:"event"`

	// CWD is the command working directory.
	CWD string `json:"cwd"`

	// Payload stores the event-specific JSON payload.
	Payload any `json:"payload"`
}

// New creates a runner for configs, omitting disabled hooks.
func New(configs []config.HookConfig, cwd string) (*Runner, error) {
	var active []config.HookConfig
	for i, hook := range configs {
		if hook.Disabled {
			continue
		}
		if strings.TrimSpace(hook.Event) == "" {
			return nil, fmt.Errorf("hook %d event must not "+
				"be empty", i+1)
		}
		if strings.TrimSpace(hook.Command) == "" {
			return nil, fmt.Errorf("hook %d command must not "+
				"be empty", i+1)
		}
		if err := validateMatcher(hook.Matcher); err != nil {
			return nil, fmt.Errorf("hook %d matcher: %w", i+1, err)
		}
		active = append(active, hook)
	}
	if len(active) == 0 {
		return nil, nil
	}

	return &Runner{cwd: cwd, hooks: active}, nil
}

// HasEvent reports whether the runner has at least one active hook for event.
func (r *Runner) HasEvent(event string) bool {
	if r == nil {
		return false
	}
	for _, hook := range r.hooks {
		if hook.Event == event {
			return true
		}
	}

	return false
}

// SessionStart runs hooks that observe a newly opened session log.
func (r *Runner) SessionStart(ctx context.Context,
	event SessionStartEvent) error {

	for _, hook := range r.matching(EventSessionStart, event.Reason) {
		var ignored struct{}
		if err := r.run(
			ctx, hook, EventSessionStart, event, &ignored,
		); err != nil {
			return err
		}
	}

	return nil
}

// validateMatcher reports malformed regular expression matchers early.
func validateMatcher(matcher string) error {
	if matcher == "" || matcher == "*" {
		return nil
	}
	if _, err := regexp.Compile(matcher); err != nil {
		return err
	}

	return nil
}

// TurnStart runs hooks that observe the start of a user turn.
func (r *Runner) TurnStart(ctx context.Context, event TurnStartEvent) error {
	for _, hook := range r.matchingEvent(EventTurnStart) {
		var ignored struct{}
		if err := r.run(
			ctx, hook, EventTurnStart, event, &ignored,
		); err != nil {
			return err
		}
	}

	return nil
}

// TurnComplete runs hooks that observe the completion of a user turn.
func (r *Runner) TurnComplete(ctx context.Context,
	event TurnCompleteEvent) error {

	for _, hook := range r.matchingEvent(EventTurnComplete) {
		var ignored struct{}
		if err := r.run(
			ctx, hook, EventTurnComplete, event, &ignored,
		); err != nil {
			return err
		}
	}

	return nil
}

// UserPromptSubmit runs hooks that can transform or block a user prompt.
func (r *Runner) UserPromptSubmit(ctx context.Context,
	event UserPromptSubmitEvent) (UserPromptSubmitResult, error) {

	var result UserPromptSubmitResult
	current := event
	for _, hook := range r.matchingEvent(EventUserPromptSubmit) {
		var hookResult UserPromptSubmitResult
		if err := r.run(
			ctx, hook, EventUserPromptSubmit, current, &hookResult,
		); err != nil {
			return UserPromptSubmitResult{}, err
		}
		if hookResult.Prompt != nil {
			current.Prompt = *hookResult.Prompt
			result.Prompt = hookResult.Prompt
		}
		if hookResult.Block {
			result.Block = true
			result.Reason = hookResult.Reason

			return result, nil
		}
	}

	return result, nil
}

// ContextBuild runs hooks that can replace the provider-neutral model context.
func (r *Runner) ContextBuild(ctx context.Context, event ContextBuildEvent) (
	ContextBuildResult, error) {

	var result ContextBuildResult
	current := event
	for _, hook := range r.matchingEvent(EventContextBuild) {
		var hookResult ContextBuildResult
		if err := r.run(
			ctx, hook, EventContextBuild, current, &hookResult,
		); err != nil {
			return ContextBuildResult{}, err
		}
		if hookResult.Messages != nil {
			current.Messages = hookResult.Messages
			result.Messages = hookResult.Messages
		}
	}

	return result, nil
}

// PreToolUse runs hooks that can transform or block a tool call.
func (r *Runner) PreToolUse(ctx context.Context, event PreToolUseEvent) (
	PreToolUseResult, error) {

	var result PreToolUseResult
	current := event
	for _, hook := range r.matching(EventPreToolUse, event.Tool.Name) {
		var hookResult PreToolUseResult
		if err := r.run(
			ctx, hook, EventPreToolUse, current, &hookResult,
		); err != nil {
			return PreToolUseResult{}, err
		}
		if hookResult.Arguments != nil {
			current.Tool.Arguments = *hookResult.Arguments
			result.Arguments = hookResult.Arguments
		}
		if hookResult.Block {
			result.Block = true
			result.Reason = hookResult.Reason

			return result, nil
		}
	}

	return result, nil
}

// PostToolUse runs hooks that can transform a completed tool result.
func (r *Runner) PostToolUse(ctx context.Context, event PostToolUseEvent) (
	PostToolUseResult, error) {

	var result PostToolUseResult
	current := event
	for _, hook := range r.matching(EventPostToolUse, event.Tool.Name) {
		var hookResult PostToolUseResult
		if err := r.run(
			ctx, hook, EventPostToolUse, current, &hookResult,
		); err != nil {
			return PostToolUseResult{}, err
		}
		if hookResult.Output != nil {
			current.Output = *hookResult.Output
			result.Output = hookResult.Output
		}
		if hookResult.Error != nil {
			current.Error = *hookResult.Error
			result.Error = hookResult.Error
		}
	}

	return result, nil
}

// PreCompact runs hooks that can cancel compaction or provide a summary.
func (r *Runner) PreCompact(ctx context.Context, event PreCompactEvent) (
	PreCompactResult, error) {

	var result PreCompactResult
	current := event
	for _, hook := range r.matching(EventPreCompact, event.Trigger) {
		var hookResult PreCompactResult
		if err := r.run(
			ctx, hook, EventPreCompact, current, &hookResult,
		); err != nil {
			return PreCompactResult{}, err
		}
		if hookResult.Summary != nil {
			result.Summary = hookResult.Summary
		}
		if hookResult.Block {
			result.Block = true
			result.Reason = hookResult.Reason

			return result, nil
		}
	}

	return result, nil
}

// PostCompact runs hooks that observe a persisted compaction summary.
func (r *Runner) PostCompact(ctx context.Context,
	event PostCompactEvent) error {

	for _, hook := range r.matching(EventPostCompact, event.Trigger) {
		var ignored struct{}
		if err := r.run(
			ctx, hook, EventPostCompact, event, &ignored,
		); err != nil {
			return err
		}
	}

	return nil
}

// ModelMessages converts provider-neutral messages to the hook JSON shape.
func ModelMessages(messages []model.Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, Message{
			Role:       message.Role,
			Content:    message.Content,
			ToolCalls:  ModelToolCalls(message.ToolCalls),
			ToolCallID: message.ToolCallID,
			Name:       message.Name,
		})
	}

	return out
}

// NeutralMessages converts hook JSON messages back to provider-neutral values.
func NeutralMessages(messages []Message) []model.Message {
	out := make([]model.Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, model.Message{
			Role:       message.Role,
			Content:    message.Content,
			ToolCalls:  NeutralToolCalls(message.ToolCalls),
			ToolCallID: message.ToolCallID,
			Name:       message.Name,
		})
	}

	return out
}

// ModelToolCall converts a provider-neutral tool call to the hook JSON shape.
func ModelToolCall(call model.ToolCall) ToolCall {
	return ToolCall{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: call.Arguments,
	}
}

// ModelToolCalls converts provider-neutral tool calls to hook JSON values.
func ModelToolCalls(calls []model.ToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, ModelToolCall(call))
	}

	return out
}

// NeutralToolCalls converts hook JSON tool calls to provider-neutral values.
func NeutralToolCalls(calls []ToolCall) []model.ToolCall {
	out := make([]model.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, model.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
	}

	return out
}

// matching returns hooks configured for event whose matcher accepts value.
func (r *Runner) matching(event string, value string) []config.HookConfig {
	out := make([]config.HookConfig, 0, len(r.hooks))
	for _, hook := range r.hooks {
		if hook.Event != event {
			continue
		}
		if !matcherAccepts(hook.Matcher, value) {
			continue
		}
		out = append(out, hook)
	}

	return out
}

// matchingEvent returns hooks configured for event without applying matchers.
func (r *Runner) matchingEvent(event string) []config.HookConfig {
	out := make([]config.HookConfig, 0, len(r.hooks))
	for _, hook := range r.hooks {
		if hook.Event == event {
			out = append(out, hook)
		}
	}

	return out
}

// matcherAccepts reports whether matcher allows value.
func matcherAccepts(matcher string, value string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	matched, err := regexp.MatchString(matcher, value)
	if err != nil {
		return false
	}

	return matched
}

// run executes one hook command with a JSON event envelope.
func (r *Runner) run(ctx context.Context, hook config.HookConfig, event string,
	payload any, result any) error {

	input, err := json.Marshal(envelope{
		Version: schemaVersion,
		Event:   event,
		CWD:     r.cwd,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("marshal %s hook input: %w", event, err)
	}

	output, err := runCommand(ctx, r.cwd, hook, append(input, '\n'))
	if err != nil {
		return fmt.Errorf("%s hook failed: %w", event, err)
	}
	if strings.TrimSpace(string(output)) == "" {
		return nil
	}
	if err := json.Unmarshal(output, result); err != nil {
		return fmt.Errorf("decode %s hook output: %w", event, err)
	}

	return nil
}

// runCommand executes hook.Command through the platform shell.
func runCommand(ctx context.Context, cwd string, hook config.HookConfig,
	input []byte) ([]byte, error) {

	timeout := hook.TimeoutSeconds
	if timeout == 0 {
		timeout = defaultTimeoutSeconds
	}
	hookCtx, cancel := context.WithTimeout(
		ctx, time.Duration(timeout)*time.Second,
	)
	defer cancel()

	name, args := platform.ShellCommand(hook.Command)
	cmd := exec.CommandContext(hookCtx, name, args...)
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if hookCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("command timed out after %ds", timeout)
	}
	if err != nil {
		text := strings.TrimSpace(stderr.String())
		if text == "" {
			text = err.Error()
		}

		return nil, fmt.Errorf("%s", text)
	}

	return stdout.Bytes(), nil
}
