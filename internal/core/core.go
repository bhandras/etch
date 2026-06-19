package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"harness/internal/model"
	"harness/internal/prompt"
	"harness/internal/session"
	"harness/internal/tool"
)

const (
	// DefaultMaxToolRounds is the normal safety limit for model/tool
	// exchange loops within one user turn.
	DefaultMaxToolRounds = 32
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

	// Tools contains builtin tools the model may call during the turn.
	Tools *tool.Registry

	// MaxToolRounds caps model/tool exchange loops within one turn. Values
	// less than one use DefaultMaxToolRounds.
	MaxToolRounds int

	// Observer receives durable events as they are appended during the
	// turn.
	Observer Observer
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

// ReasoningObserver receives displayable model reasoning summaries.
type ReasoningObserver interface {
	// ReasoningCompleted receives the complete reasoning summary emitted
	// by one model pass. Raw hidden chain-of-thought should not be sent
	// through this hook.
	ReasoningCompleted(text string)
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

	store, history, err := openTurnStore(req)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	user, err := store.Append(
		session.EventUserMessage, store.LastID(),
		session.TextMessage(session.RoleUser, req.Prompt),
	)
	if err != nil {
		return nil, err
	}
	history = append(history, *user)
	notifyEvent(req.Observer, user)

	messages, err := prompt.BuildHistoryMessages(prompt.HistoryRequest{
		Events:     history,
		SystemText: req.SystemText,
	})
	if err != nil {
		return nil, err
	}
	parentID := user.ID

	maxToolRounds := toolRoundLimit(req.MaxToolRounds)
	var assistant *session.Event
	finalReceived := false
	var text string
	for round := 0; round < maxToolRounds; round++ {
		response, err := collectModelResponse(
			ctx, req.Model, messages, req.Tools,
		)
		if err != nil {
			return nil, err
		}
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
			if usageEvent, err := appendModelUsage(
				store, assistant.ID, response.Usage,
			); err != nil {
				return nil, err
			} else if usageEvent != nil {
				notifyEvent(req.Observer, usageEvent)
			}
			text = response.Text
			finalReceived = true

			break
		}

		assistant, err = store.Append(
			session.EventAssistantMessage, parentID,
			session.AssistantToolCallMessage(
				response.Text,
				sessionToolCalls(response.ToolCalls),
			),
		)
		if err != nil {
			return nil, err
		}
		notifyEvent(req.Observer, assistant)
		usageEvent, err := appendModelUsage(
			store, assistant.ID, response.Usage,
		)
		if err != nil {
			return nil, err
		}
		if usageEvent != nil {
			notifyEvent(req.Observer, usageEvent)
		}
		messages = append(messages, model.Message{
			Role:      model.RoleAssistant,
			Content:   response.Text,
			ToolCalls: response.ToolCalls,
		})

		parentID = assistant.ID
		if usageEvent != nil {
			parentID = usageEvent.ID
		}
		for _, call := range response.ToolCalls {
			notifyToolCallStarted(req.Observer, call)
			result, err := executeTool(ctx, req.Tools, call)
			if err != nil {
				if errors.Is(err, context.Canceled) ||
					errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				result = tool.Result{Text: toolErrorText(err)}
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
	}
	if assistant == nil {
		return nil, fmt.Errorf("tool round limit exceeded")
	}
	if !finalReceived {
		return nil, fmt.Errorf("tool round limit exceeded before " +
			"final assistant response")
	}

	return &TurnResult{
		SessionPath:      store.Path(),
		SessionID:        store.ID(),
		UserEventID:      user.ID,
		AssistantEventID: assistant.ID,
		AssistantText:    text,
	}, nil
}

// toolRoundLimit returns the effective model/tool loop limit for a turn.
func toolRoundLimit(requested int) int {
	if requested > 0 {
		return requested
	}

	return DefaultMaxToolRounds
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
}

// collectModelResponse starts a model stream and collects one assistant pass.
func collectModelResponse(ctx context.Context, client model.Client,
	messages []model.Message,
	registry *tool.Registry) (modelResponse, error) {

	var specs []model.ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}

	stream, err := client.Stream(ctx, model.Request{
		Messages: messages,
		Tools:    specs,
	})
	if err != nil {
		return modelResponse{}, fmt.Errorf("start model stream: %w",
			err)
	}

	return collectStream(ctx, stream)
}

// collectStream consumes a model stream and joins reasoning, text, and tool
// call events.
func collectStream(ctx context.Context,
	stream <-chan model.Event) (modelResponse, error) {

	var text strings.Builder
	var reasoning strings.Builder
	var calls []model.ToolCall
	var usage model.Usage
	for {
		select {
		case <-ctx.Done():
			return modelResponse{}, ctx.Err()

		case event, ok := <-stream:
			if !ok {
				return modelResponse{
					Text:      text.String(),
					Reasoning: reasoning.String(),
					ToolCalls: calls,
					Usage:     usage,
				}, nil
			}
			switch event.Type {
			case model.EventTextDelta:
				text.WriteString(event.Text)

			case model.EventReasoningDelta:
				reasoning.WriteString(event.Text)

			case model.EventToolCall:
				calls = append(calls, event.ToolCall)

			case model.EventUsage:
				usage = usage.Add(event.Usage)

			case model.EventDone:
				return modelResponse{
					Text:      text.String(),
					Reasoning: reasoning.String(),
					ToolCalls: calls,
					Usage:     usage,
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
