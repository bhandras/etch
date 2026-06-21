package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"harness/internal/hooks"
	"harness/internal/model"
	"harness/internal/prompt"
	"harness/internal/session"
	"harness/internal/tool"
)

const (
	// DefaultMaxToolRounds is the normal safety limit for model/tool
	// exchange loops within one user turn.
	DefaultMaxToolRounds = 32

	// DefaultAutoCompactThresholdTokens is the approximate context size
	// that triggers auto compaction when the feature is enabled.
	DefaultAutoCompactThresholdTokens = 120000
)

// TurnRequest contains everything needed to run one non-interactive turn.
type TurnRequest struct {
	// Prompt is the user text admitted into the session.
	Prompt string

	// SessionDir is the directory where the new JSONL session should be
	// stored.
	SessionDir string

	// SessionPath is an existing JSONL session log to continue. Empty means
	// a new session should be created in SessionDir.
	SessionPath string

	// CWD records the working directory associated with the session.
	CWD string

	// SystemText stores optional system instructions for the model context.
	SystemText string

	// Model is the provider-neutral client used to stream the assistant
	// reply.
	Model model.Client

	// ModelName records the provider-specific model identifier in
	// auto-compaction summary events.
	ModelName string

	// Tools contains builtin tools the model may call during the turn.
	Tools *tool.Registry

	// MaxToolRounds caps model/tool exchange loops within one turn. Values
	// less than one use DefaultMaxToolRounds.
	MaxToolRounds int

	// AutoCompactThresholdTokens enables automatic compaction when the
	// projected prompt context reaches this approximate token count.
	AutoCompactThresholdTokens int

	// AutoCompactKeepMessages is the number of latest message events kept
	// raw by automatic compaction. Values less than one use the default.
	AutoCompactKeepMessages int

	// AutoCompactKeepRecentTokens is the approximate recent context budget
	// retained raw by automatic compaction.
	AutoCompactKeepRecentTokens int

	// DrainSteering returns user prompts submitted while the turn is
	// running. The core admits them after a tool batch, before the next
	// model call, because tool-call protocol order must stay contiguous.
	DrainSteering func() []string

	// Observer receives durable events as they are appended during the
	// turn.
	Observer Observer

	// Hooks runs external lifecycle transformers around the turn. Nil means
	// no hooks are configured.
	Hooks *hooks.Runner
}

// TurnResult reports the durable and user-visible output from one turn.
type TurnResult struct {
	// SessionPath is the JSONL file written for the turn.
	SessionPath string `json:"sessionPath"`

	// SessionID is the stable session file and index identifier.
	SessionID string `json:"sessionId"`

	// UserEventID is the durable ID of the user message event.
	UserEventID string `json:"userEventId"`

	// AssistantEventID is the durable ID of the assistant message event.
	AssistantEventID string `json:"assistantEventId"`

	// AssistantText is the complete assistant text assembled from the
	// stream.
	AssistantText string `json:"assistantText"`

	// Timing stores coarse model, transport, and tool timing collected
	// while producing the turn.
	Timing TurnTiming `json:"timing"`
}

// AutoCompactResult describes one automatic compaction pass.
type AutoCompactResult struct {
	// SummaryEventID is the durable context.summary event appended by the
	// automatic compaction pass.
	SummaryEventID string

	// BeforeTokens is the approximate projected context size before
	// compaction.
	BeforeTokens int

	// AfterTokens is the approximate projected context size after
	// compaction.
	AfterTokens int

	// ThresholdTokens is the configured approximate trigger threshold.
	ThresholdTokens int
}

// TurnTiming records coarse wall-clock timing for one completed turn.
type TurnTiming struct {
	// ModelCalls is the number of provider requests made during the turn.
	ModelCalls int

	// ModelDuration is the cumulative time spent waiting for model streams.
	ModelDuration time.Duration

	// RequestBytes is the cumulative provider request body size.
	RequestBytes int

	// ResponseBytes is the approximate cumulative provider stream bytes.
	ResponseBytes int

	// TimeToHeaders is the cumulative time until provider response headers.
	TimeToHeaders time.Duration

	// TimeToFirstEvent is the cumulative time until first stream events.
	TimeToFirstEvent time.Duration

	// ToolDuration is the cumulative time spent executing tools and
	// post-tool hooks.
	ToolDuration time.Duration

	// ToolBatches is the number of model passes that requested tools.
	ToolBatches int

	// LargestToolBatch is the largest number of tools requested by one
	// model pass.
	LargestToolBatch int
}

// Observer receives turn events as soon as they are persisted.
type Observer interface {
	// EventAppended receives one durable event after it has been written to
	// the session log.
	EventAppended(event session.Event)
}

// ToolCallObserver receives live progress before local tool execution.
type ToolCallObserver interface {
	// ToolCallStarted receives one model-requested tool call immediately
	// before the core executes it locally.
	ToolCallStarted(call model.ToolCall)
}

// ToolBatchObserver receives one model-requested batch before execution.
type ToolBatchObserver interface {
	// ToolBatchStarted receives all tool calls requested by one model pass
	// after pre-tool hooks have transformed or blocked them.
	ToolBatchStarted(calls []model.ToolCall)
}

// StreamObserver receives live model stream deltas before persistence.
type StreamObserver interface {
	// ModelTextDelta receives one assistant text delta as soon as the
	// model emits it.
	ModelTextDelta(text string)

	// ModelReasoningDelta receives one displayable reasoning delta as soon
	// as the model emits it.
	ModelReasoningDelta(text string)
}

// ReasoningObserver receives displayable model reasoning summaries.
type ReasoningObserver interface {
	// ReasoningCompleted receives the complete reasoning summary emitted
	// by one model pass. Raw hidden chain-of-thought should not be sent
	// through this hook.
	ReasoningCompleted(text string)
}

// AutoCompactObserver receives automatic compaction progress.
type AutoCompactObserver interface {
	// AutoCompacted receives a compact report after a summary event is
	// persisted for the current turn.
	AutoCompacted(result AutoCompactResult)
}

// TimingObserver receives coarse timing data for one successful turn.
type TimingObserver interface {
	// TurnTiming receives model/tool timing after the turn completes.
	TurnTiming(timing TurnTiming)
}

// RunTurn executes one prompt against a model client and persists the exchange.
func RunTurn(ctx context.Context, req TurnRequest) (*TurnResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt must not be empty")
	}
	if req.SessionDir == "" && req.SessionPath == "" {
		return nil, fmt.Errorf("session dir must not be empty")
	}
	if req.Model == nil {
		return nil, fmt.Errorf("model client must not be nil")
	}
	sessionStartReason := "new"
	if req.SessionPath != "" {
		sessionStartReason = "resume"
	}
	promptText, err := runUserPromptHooks(ctx, req, req.Prompt)
	if err != nil {
		return nil, err
	}
	req.Prompt = promptText

	store, history, err := openTurnStore(req)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	req.SessionPath = store.Path()
	if err := runSessionStartHooks(
		ctx, req, store, sessionStartReason,
	); err != nil {
		return nil, err
	}

	user, err := store.Append(
		session.EventUserMessage, store.LastID(),
		session.TextMessage(session.RoleUser, req.Prompt),
	)
	if err != nil {
		return nil, err
	}
	history = append(history, *user)
	notifyEvent(req.Observer, user)
	if err := runTurnStartHooks(ctx, req, store, user); err != nil {
		return nil, err
	}

	autoCompact, history, err := maybeAutoCompact(ctx, req, store, history)
	if err != nil {
		return nil, err
	}
	if autoCompact != nil {
		notifyAutoCompacted(req.Observer, *autoCompact)
	}

	messages, err := prompt.BuildHistoryMessages(prompt.HistoryRequest{
		Events:     history,
		SystemText: req.SystemText,
	})
	if err != nil {
		return nil, err
	}
	continuation, err := initialResponseContinuation(
		history, req.SystemText,
	)
	if err != nil {
		return nil, err
	}
	if req.Hooks != nil {
		continuation = responseContinuation{}
	}
	parentID := store.LastID()

	maxToolRounds := toolRoundLimit(req.MaxToolRounds)
	var assistant *session.Event
	finalReceived := false
	var text string
	toolCallCount := 0
	userPrompts := []string{req.Prompt}
	var timing TurnTiming
	for round := 0; round < maxToolRounds; round++ {
		callMessages, err := runContextBuildHooks(
			ctx, req, messages, round,
		)
		if err != nil {
			return nil, err
		}
		modelStarted := time.Now()
		response, err := collectModelResponse(
			ctx, req.Model, store.ID(), continuation, callMessages,
			req.Tools, req.Observer,
		)
		timing.ModelDuration += time.Since(modelStarted)
		timing.ModelCalls++
		if err != nil {
			return nil, err
		}
		timing.RequestBytes += response.Metrics.RequestBytes
		timing.ResponseBytes += response.Metrics.ResponseBytes
		timing.TimeToHeaders += response.Metrics.TimeToHeaders
		timing.TimeToFirstEvent += response.Metrics.TimeToFirstEvent
		notifyReasoningCompleted(req.Observer, response.Reasoning)
		if len(response.ToolCalls) == 0 {
			assistant, err = store.Append(
				session.EventAssistantMessage, parentID,
				session.TextMessage(
					session.RoleAssistant, response.Text,
				),
			)
			if err != nil {
				return nil, err
			}
			notifyEvent(req.Observer, assistant)
			metadataParentID := assistant.ID
			if usageEvent, err := appendModelUsage(
				store, metadataParentID, response.Usage,
			); err != nil {
				return nil, err
			} else if usageEvent != nil {
				notifyEvent(req.Observer, usageEvent)
				metadataParentID = usageEvent.ID
			}
			if responseEvent, err := appendModelResponse(
				store, metadataParentID, response.ResponseInfo,
			); err != nil {
				return nil, err
			} else if responseEvent != nil {
				notifyEvent(req.Observer, responseEvent)
			}
			text = response.Text
			finalReceived = true

			break
		}

		toolCalls, blockedCalls, err := runPreToolUseHooks(
			ctx, req, response.ToolCalls,
		)
		if err != nil {
			return nil, err
		}
		if len(toolCalls) > 0 {
			timing.ToolBatches++
			if len(toolCalls) > timing.LargestToolBatch {
				timing.LargestToolBatch = len(toolCalls)
			}
			notifyToolBatchStarted(req.Observer, toolCalls)
		}
		assistant, err = store.Append(
			session.EventAssistantMessage, parentID,
			session.AssistantToolCallMessage(
				response.Text, sessionToolCalls(toolCalls),
			),
		)
		if err != nil {
			return nil, err
		}
		notifyEvent(req.Observer, assistant)
		metadataParentID := assistant.ID
		usageEvent, err := appendModelUsage(
			store, metadataParentID, response.Usage,
		)
		if err != nil {
			return nil, err
		}
		if usageEvent != nil {
			notifyEvent(req.Observer, usageEvent)
			metadataParentID = usageEvent.ID
		}
		responseEvent, err := appendModelResponse(
			store, metadataParentID, response.ResponseInfo,
		)
		if err != nil {
			return nil, err
		}
		if responseEvent != nil {
			notifyEvent(req.Observer, responseEvent)
		}
		messages = append(messages, model.Message{
			Role:      model.RoleAssistant,
			Content:   response.Text,
			ToolCalls: toolCalls,
		})
		continuation = responseContinuation{}
		deltaStart := len(messages)
		if req.Hooks == nil &&
			response.ResponseInfo.ProviderResponseID != "" {

			continuation.PreviousResponseID =
				response.ResponseInfo.ProviderResponseID
		}

		parentID = assistant.ID
		if usageEvent != nil {
			parentID = usageEvent.ID
		}
		if responseEvent != nil {
			parentID = responseEvent.ID
		}
		for _, call := range toolCalls {
			toolCallCount++
			notifyToolCallStarted(req.Observer, call)
			toolStarted := time.Now()
			result := tool.Result{}
			toolFailed := false
			if reason, ok := blockedCalls[call.ID]; ok {
				toolFailed = true
				result = tool.Result{
					Text: blockedToolText(reason),
				}
			} else {
				result, err = executeTool(ctx, req.Tools, call)
				if err != nil {
					if errors.Is(err, context.Canceled) ||
						errors.Is(
							err,
							context.DeadlineExceeded,
						) {
						return nil, err
					}
					toolFailed = true
					result = tool.Result{
						Text: toolErrorText(err),
					}
				}
			}
			result, err = runPostToolUseHooks(
				ctx, req, call, result, toolFailed,
			)
			timing.ToolDuration += time.Since(toolStarted)
			if err != nil {
				return nil, err
			}
			toolEvent, err := store.Append(
				session.EventToolMessage, parentID,
				session.ToolMessage(
					call.ID, call.Name, result.Text,
				),
			)
			if err != nil {
				return nil, err
			}
			notifyEvent(req.Observer, toolEvent)
			messages = append(messages, model.Message{
				Role:       model.RoleTool,
				Content:    result.Text,
				ToolCallID: call.ID,
				Name:       call.Name,
			})
			parentID = toolEvent.ID
		}
		steeredParentID, err := applySteeringPrompts(
			ctx, req, store, parentID, &messages, &userPrompts,
		)
		if err != nil {
			return nil, err
		}
		parentID = steeredParentID
		if continuation.PreviousResponseID != "" {
			continuation.DeltaMessages = continuationMessages(
				messages, deltaStart,
			)
			if !hasNonSystemMessage(continuation.DeltaMessages) {
				continuation = responseContinuation{}
			}
		}
	}
	if assistant == nil {
		return nil, fmt.Errorf("tool round limit exceeded")
	}
	if !finalReceived {
		return nil, fmt.Errorf("tool round limit exceeded before " +
			"final assistant response")
	}
	if err := runTurnCompleteHooks(
		ctx, req, store, user, assistant, text, toolCallCount,
		userPrompts,
	); err != nil {
		return nil, err
	}
	notifyTurnTiming(req.Observer, timing)

	return &TurnResult{
		SessionPath:      store.Path(),
		SessionID:        store.ID(),
		UserEventID:      user.ID,
		AssistantEventID: assistant.ID,
		AssistantText:    text,
		Timing:           timing,
	}, nil
}

// toolRoundLimit returns the effective model/tool loop limit for a turn.
func toolRoundLimit(requested int) int {
	if requested > 0 {
		return requested
	}

	return DefaultMaxToolRounds
}

// runSessionStartHooks applies configured session lifecycle hooks.
func runSessionStartHooks(ctx context.Context, req TurnRequest,
	store *session.Store, reason string) error {

	if req.Hooks == nil {
		return nil
	}

	return req.Hooks.SessionStart(ctx, hooks.SessionStartEvent{
		SessionPath: store.Path(),
		SessionID:   store.ID(),
		Reason:      reason,
	})
}

// runUserPromptHooks applies configured prompt submission hooks.
func runUserPromptHooks(ctx context.Context, req TurnRequest,
	prompt string) (string, error) {

	if req.Hooks == nil {
		return prompt, nil
	}

	result, err := req.Hooks.UserPromptSubmit(
		ctx,
		hooks.UserPromptSubmitEvent{
			Prompt:      prompt,
			SessionPath: req.SessionPath,
		},
	)
	if err != nil {
		return "", err
	}
	if result.Block {
		return "", fmt.Errorf("prompt blocked by hook: %s",
			nonEmptyReason(result.Reason))
	}
	if result.Prompt != nil {
		return *result.Prompt, nil
	}

	return prompt, nil
}

// applySteeringPrompts admits queued user steering before the next model call.
func applySteeringPrompts(ctx context.Context, req TurnRequest,
	store *session.Store, parentID string, messages *[]model.Message,
	userPrompts *[]string) (string, error) {

	if req.DrainSteering == nil {
		return parentID, nil
	}
	for _, promptText := range req.DrainSteering() {
		if strings.TrimSpace(promptText) == "" {
			continue
		}
		hookedPrompt, err := runUserPromptHooks(ctx, req, promptText)
		if err != nil {
			return parentID, err
		}
		if strings.TrimSpace(hookedPrompt) == "" {
			continue
		}
		user, err := store.Append(
			session.EventUserMessage, parentID,
			session.TextMessage(session.RoleUser, hookedPrompt),
		)
		if err != nil {
			return parentID, err
		}
		notifyEvent(req.Observer, user)
		*messages = append(*messages, model.Message{
			Role:    model.RoleUser,
			Content: hookedPrompt,
		})
		*userPrompts = append(*userPrompts, hookedPrompt)
		parentID = user.ID
	}

	return parentID, nil
}

// runTurnStartHooks applies configured turn start lifecycle hooks.
func runTurnStartHooks(ctx context.Context, req TurnRequest,
	store *session.Store, user *session.Event) error {

	if req.Hooks == nil {
		return nil
	}

	return req.Hooks.TurnStart(ctx, hooks.TurnStartEvent{
		SessionPath: store.Path(),
		SessionID:   store.ID(),
		UserEventID: user.ID,
		Prompt:      req.Prompt,
	})
}

// runTurnCompleteHooks applies configured turn completion lifecycle hooks.
func runTurnCompleteHooks(ctx context.Context, req TurnRequest,
	store *session.Store, user *session.Event, assistant *session.Event,
	text string, toolCalls int, userPrompts []string) error {

	if req.Hooks == nil {
		return nil
	}

	return req.Hooks.TurnComplete(ctx, hooks.TurnCompleteEvent{
		SessionPath:      store.Path(),
		SessionID:        store.ID(),
		UserEventID:      user.ID,
		AssistantEventID: assistant.ID,
		Prompt:           req.Prompt,
		UserPrompts:      append([]string(nil), userPrompts...),
		AssistantText:    text,
		ToolCalls:        toolCalls,
	})
}

// runContextBuildHooks applies configured model-context hooks.
func runContextBuildHooks(ctx context.Context, req TurnRequest,
	messages []model.Message, round int) ([]model.Message, error) {

	if req.Hooks == nil {
		return messages, nil
	}

	result, err := req.Hooks.ContextBuild(ctx, hooks.ContextBuildEvent{
		SessionPath: req.SessionPath,
		Round:       round,
		Messages:    hooks.ModelMessages(messages),
	})
	if err != nil {
		return nil, err
	}
	if result.Messages != nil {
		return hooks.NeutralMessages(result.Messages), nil
	}

	return messages, nil
}

// runPreToolUseHooks applies configured tool preflight hooks in source order.
func runPreToolUseHooks(ctx context.Context, req TurnRequest,
	calls []model.ToolCall) ([]model.ToolCall, map[string]string, error) {

	prepared := append([]model.ToolCall{}, calls...)
	blocked := make(map[string]string)
	if req.Hooks == nil {
		return prepared, blocked, nil
	}

	for i, call := range prepared {
		result, err := req.Hooks.PreToolUse(ctx, hooks.PreToolUseEvent{
			SessionPath: req.SessionPath,
			Tool:        hooks.ModelToolCall(call),
		})
		if err != nil {
			return nil, nil, err
		}
		if result.Arguments != nil {
			prepared[i].Arguments = *result.Arguments
		}
		if result.Block {
			blocked[call.ID] = nonEmptyReason(result.Reason)
		}
	}

	return prepared, blocked, nil
}

// runPostToolUseHooks applies configured result hooks before persistence.
func runPostToolUseHooks(ctx context.Context, req TurnRequest,
	call model.ToolCall, result tool.Result,
	failed bool) (tool.Result, error) {

	if req.Hooks == nil {
		return result, nil
	}

	hookResult, err := req.Hooks.PostToolUse(ctx, hooks.PostToolUseEvent{
		SessionPath: req.SessionPath,
		Tool:        hooks.ModelToolCall(call),
		Output:      result.Text,
		Error:       failed,
	})
	if err != nil {
		return tool.Result{}, err
	}
	if hookResult.Output != nil {
		result.Text = *hookResult.Output
	}

	return result, nil
}

// notifyEvent sends an appended event to the optional turn observer.
func notifyEvent(observer Observer, event *session.Event) {
	if observer != nil && event != nil {
		observer.EventAppended(*event)
	}
}

// notifyToolCallStarted sends live progress to observers that support it.
func notifyToolCallStarted(observer Observer, call model.ToolCall) {
	if observer == nil {
		return
	}
	toolObserver, ok := observer.(ToolCallObserver)
	if ok {
		toolObserver.ToolCallStarted(call)
	}
}

// notifyToolBatchStarted sends live progress for one model-requested batch.
func notifyToolBatchStarted(observer Observer, calls []model.ToolCall) {
	if observer == nil || len(calls) == 0 {
		return
	}
	batchObserver, ok := observer.(ToolBatchObserver)
	if ok {
		batchObserver.ToolBatchStarted(calls)
	}
}

// notifyAutoCompacted sends automatic compaction reports to interested
// observers.
func notifyAutoCompacted(observer Observer, result AutoCompactResult) {
	if observer == nil {
		return
	}
	autoObserver, ok := observer.(AutoCompactObserver)
	if ok {
		autoObserver.AutoCompacted(result)
	}
}

// notifyTurnTiming sends coarse turn timing to interested observers.
func notifyTurnTiming(observer Observer, timing TurnTiming) {
	if observer == nil {
		return
	}
	timingObserver, ok := observer.(TimingObserver)
	if ok {
		timingObserver.TurnTiming(timing)
	}
}

// maybeAutoCompact summarizes older history when projected context is large.
func maybeAutoCompact(ctx context.Context, req TurnRequest,
	store *session.Store, history []session.Event) (*AutoCompactResult,
	[]session.Event, error) {

	threshold := req.AutoCompactThresholdTokens
	if threshold <= 0 {
		return nil, history, nil
	}

	before, err := prompt.BuildStats(history, req.SystemText)
	if err != nil {
		return nil, nil, err
	}
	if before.ApproxContextTokens < threshold ||
		!autoCompactHasUsefulReplay(before, threshold) {
		return nil, history, nil
	}

	result, event, err := compactStore(ctx, CompactRequest{
		SessionPath:      store.Path(),
		Model:            req.Model,
		KeepMessages:     req.AutoCompactKeepMessages,
		KeepRecentTokens: req.AutoCompactKeepRecentTokens,
		ModelName:        req.ModelName,
		Trigger:          "auto",
		Hooks:            req.Hooks,
	}, store, history)
	if err != nil {
		if errors.Is(err, errNotEnoughHistory) {
			return nil, history, nil
		}

		return nil, nil, err
	}
	history = append(history, *event)
	notifyEvent(req.Observer, event)
	after, err := prompt.BuildStats(history, req.SystemText)
	if err != nil {
		return nil, nil, err
	}

	return &AutoCompactResult{
		SummaryEventID:  result.SummaryEventID,
		BeforeTokens:    before.ApproxContextTokens,
		AfterTokens:     after.ApproxContextTokens,
		ThresholdTokens: threshold,
	}, history, nil
}

// autoCompactHasUsefulReplay prevents repeated summaries when the active
// summary or pinned context, rather than raw replay, dominates the request.
func autoCompactHasUsefulReplay(stats prompt.Stats, threshold int) bool {
	if !stats.SummaryActive {
		return true
	}
	if stats.RawReplayTokens < stats.SummaryTokens {
		return false
	}
	minimumReplay := threshold / 4
	if minimumReplay < 1 {
		minimumReplay = 1
	}

	return stats.RawReplayTokens >= minimumReplay
}

// openTurnStore creates or opens the session store for one turn.
func openTurnStore(req TurnRequest) (*session.Store, []session.Event, error) {
	if req.SessionPath != "" {
		store, events, err := session.Open(req.SessionPath)
		if err != nil {
			return nil, nil, err
		}

		return store, events, nil
	}

	store, started, err := session.Create(
		req.SessionDir, req.CWD, req.Prompt,
	)
	if err != nil {
		return nil, nil, err
	}

	return store, []session.Event{*started}, nil
}

// responseContinuation stores a safe provider continuation request slice.
type responseContinuation struct {
	// PreviousResponseID identifies the provider response to continue.
	PreviousResponseID string

	// DeltaMessages contains only messages added after PreviousResponseID.
	DeltaMessages []model.Message
}

// initialResponseContinuation derives a continuation from prior session
// history after the latest durable provider response.
func initialResponseContinuation(events []session.Event,
	systemText string) (responseContinuation, error) {

	index, response, ok, err := latestModelResponse(events)
	if err != nil || !ok || response.ProviderResponseID == "" {
		return responseContinuation{}, err
	}
	deltaEvents := events[index+1:]
	if containsSummaryEvent(deltaEvents) {
		return responseContinuation{}, nil
	}

	messages, err := prompt.BuildHistoryMessages(prompt.HistoryRequest{
		Events:     deltaEvents,
		SystemText: systemText,
	})
	if err != nil {
		return responseContinuation{}, err
	}
	if !hasNonSystemMessage(messages) {
		return responseContinuation{}, nil
	}

	return responseContinuation{
		PreviousResponseID: response.ProviderResponseID,
		DeltaMessages:      messages,
	}, nil
}

// latestModelResponse returns the newest durable provider response identity.
func latestModelResponse(events []session.Event) (int, session.ResponseData,
	bool, error) {

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != session.EventModelResponse {
			continue
		}
		var response session.ResponseData
		if err := json.Unmarshal(
			events[i].Data, &response,
		); err != nil {
			return 0, session.ResponseData{}, false,
				fmt.Errorf("decode model response %s: %w",
					events[i].ID, err)
		}

		return i, response, true, nil
	}

	return 0, session.ResponseData{}, false, nil
}

// containsSummaryEvent reports whether delta events crossed compaction.
func containsSummaryEvent(events []session.Event) bool {
	for _, event := range events {
		if event.Type == session.EventContextSummary {
			return true
		}
	}

	return false
}

// continuationMessages prepends leading system messages to a context suffix.
func continuationMessages(messages []model.Message, start int) []model.Message {
	if start < 0 {
		start = 0
	}
	if start > len(messages) {
		start = len(messages)
	}
	out := make([]model.Message, 0, start+len(messages)-start)
	for _, message := range messages {
		if message.Role != model.RoleSystem {
			break
		}
		out = append(out, message)
	}
	out = append(out, messages[start:]...)

	return out
}

// hasNonSystemMessage reports whether messages contain provider input.
func hasNonSystemMessage(messages []model.Message) bool {
	for _, message := range messages {
		if message.Role != model.RoleSystem {
			return true
		}
	}

	return false
}

// modelResponse is one complete model pass through text and tool-call events.
type modelResponse struct {
	// Text is the complete assistant text assembled from streamed deltas.
	Text string

	// Reasoning is the complete displayable reasoning summary assembled
	// from streamed deltas.
	Reasoning string

	// ToolCalls stores complete tool calls requested by the model.
	ToolCalls []model.ToolCall

	// Usage stores provider-reported token counters for this model pass.
	Usage model.Usage

	// ResponseInfo stores provider response identity for this model pass.
	ResponseInfo model.ResponseInfo

	// Metrics stores provider-reported transport counters for this pass.
	Metrics model.Metrics
}

// collectModelResponse starts a model stream and collects one assistant pass.
func collectModelResponse(ctx context.Context, client model.Client,
	sessionID string, continuation responseContinuation,
	messages []model.Message, registry *tool.Registry,
	observer Observer) (modelResponse, error) {

	var specs []model.ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}

	stream, err := client.Stream(ctx, model.Request{
		SessionID:          sessionID,
		PreviousResponseID: continuation.PreviousResponseID,
		Messages:           messages,
		DeltaMessages:      continuation.DeltaMessages,
		Tools:              specs,
	})
	if err != nil {
		return modelResponse{}, fmt.Errorf("start model stream: %w",
			err)
	}

	return collectStream(ctx, stream, observer)
}

// collectStream consumes a model stream and joins reasoning, text, and tool
// call events.
func collectStream(ctx context.Context, stream <-chan model.Event,
	observer Observer) (modelResponse, error) {

	var text strings.Builder
	var reasoning strings.Builder
	var calls []model.ToolCall
	var usage model.Usage
	var responseInfo model.ResponseInfo
	var metrics model.Metrics
	for {
		select {
		case <-ctx.Done():
			return modelResponse{}, ctx.Err()

		case event, ok := <-stream:
			if !ok {
				return modelResponse{
					Text:         text.String(),
					Reasoning:    reasoning.String(),
					ToolCalls:    calls,
					Usage:        usage,
					ResponseInfo: responseInfo,
					Metrics:      metrics,
				}, nil
			}
			switch event.Type {
			case model.EventTextDelta:
				text.WriteString(event.Text)
				notifyModelTextDelta(observer, event.Text)

			case model.EventReasoningDelta:
				reasoning.WriteString(event.Text)
				notifyModelReasoningDelta(observer, event.Text)

			case model.EventToolCall:
				calls = append(calls, event.ToolCall)

			case model.EventUsage:
				usage = usage.Add(event.Usage)

			case model.EventResponseInfo:
				responseInfo = event.ResponseInfo

			case model.EventMetrics:
				metrics = metrics.Add(event.Metrics)

			case model.EventDone:
				return modelResponse{
					Text:         text.String(),
					Reasoning:    reasoning.String(),
					ToolCalls:    calls,
					Usage:        usage,
					ResponseInfo: responseInfo,
					Metrics:      metrics,
				}, nil

			case model.EventError:
				return modelResponse{}, fmt.Errorf("model "+
					"stream error: %s", event.Err)

			default:
				return modelResponse{}, fmt.Errorf("unknown "+
					"model event type %q", event.Type)
			}
		}
	}
}

// appendModelUsage persists provider usage when a model call reports it.
func appendModelUsage(store *session.Store, parentID string,
	usage model.Usage) (*session.Event, error) {

	if usage.Empty() {
		return nil, nil
	}

	event, err := store.Append(session.EventModelUsage, parentID,
		session.UsageData{
			InputTokens:           usage.InputTokens,
			CachedInputTokens:     usage.CachedInputTokens,
			OutputTokens:          usage.OutputTokens,
			ReasoningOutputTokens: usage.ReasoningOutputTokens,
			TotalTokens:           usage.TotalTokens,
		})
	if err != nil {
		return nil, fmt.Errorf("append model usage: %w", err)
	}

	return event, nil
}

// appendModelResponse persists provider response identity when available.
func appendModelResponse(store *session.Store, parentID string,
	response model.ResponseInfo) (*session.Event, error) {

	if response.Empty() {
		return nil, nil
	}

	event, err := store.Append(session.EventModelResponse, parentID,
		session.ResponseData{
			ProviderResponseID: response.ProviderResponseID,
		})
	if err != nil {
		return nil, fmt.Errorf("append model response: %w", err)
	}

	return event, nil
}

// notifyModelTextDelta sends streamed assistant text to live observers.
func notifyModelTextDelta(observer Observer, text string) {
	if observer == nil || text == "" {
		return
	}
	streamObserver, ok := observer.(StreamObserver)
	if ok {
		streamObserver.ModelTextDelta(text)
	}
}

// notifyModelReasoningDelta sends streamed reasoning text to live observers.
func notifyModelReasoningDelta(observer Observer, text string) {
	if observer == nil || text == "" {
		return
	}
	streamObserver, ok := observer.(StreamObserver)
	if ok {
		streamObserver.ModelReasoningDelta(text)
	}
}

// notifyReasoningCompleted sends reasoning summaries to interested observers.
func notifyReasoningCompleted(observer Observer, text string) {
	if observer == nil || strings.TrimSpace(text) == "" {
		return
	}
	reasoningObserver, ok := observer.(ReasoningObserver)
	if ok {
		reasoningObserver.ReasoningCompleted(text)
	}
}

// executeTool dispatches one model-requested tool call through the registry.
func executeTool(ctx context.Context, registry *tool.Registry,
	call model.ToolCall) (tool.Result, error) {

	if registry == nil {
		return tool.Result{}, fmt.Errorf("model requested tool %q but "+
			"no tools are registered", call.Name)
	}

	return registry.Execute(ctx, call)
}

// toolErrorText formats a tool failure as model-visible feedback.
func toolErrorText(err error) string {
	return "tool error: " + err.Error()
}

// blockedToolText formats hook policy denial as model-visible feedback.
func blockedToolText(reason string) string {
	return "tool blocked by hook: " + nonEmptyReason(reason)
}

// nonEmptyReason returns a fallback reason for hook decisions.
func nonEmptyReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "no reason provided"
	}

	return reason
}

// sessionToolCalls converts model tool calls into durable session payloads.
func sessionToolCalls(calls []model.ToolCall) []session.ToolCallData {
	out := make([]session.ToolCallData, 0, len(calls))
	for _, call := range calls {
		out = append(out, session.ToolCallData{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
	}

	return out
}
