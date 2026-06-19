package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"harness/internal/model"
	"harness/internal/session"
	"harness/internal/tool"
)

const (
	// maxToolRounds caps model/tool exchange loops within one turn.
	maxToolRounds = 8
)

// TurnRequest contains everything needed to run one non-interactive turn.
type TurnRequest struct {
	// Prompt is the user text admitted into the session.
	Prompt string

	// SessionDir is the directory where the new JSONL session should be
	// stored.
	SessionDir string

	// CWD records the working directory associated with the session.
	CWD string

	// Model is the provider-neutral client used to stream the assistant
	// reply.
	Model model.Client

	// Tools contains builtin tools the model may call during the turn.
	Tools *tool.Registry
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

// RunTurn executes one prompt against a model client and persists the exchange.
func RunTurn(ctx context.Context, req TurnRequest) (*TurnResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt must not be empty")
	}
	if req.SessionDir == "" {
		return nil, fmt.Errorf("session dir must not be empty")
	}
	if req.Model == nil {
		return nil, fmt.Errorf("model client must not be nil")
	}

	store, started, err := session.Create(
		req.SessionDir, req.CWD, req.Prompt,
	)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	user, err := store.Append(
		session.EventUserMessage, started.ID,
		session.TextMessage(session.RoleUser, req.Prompt),
	)
	if err != nil {
		return nil, err
	}

	messages := []model.Message{{
		Role:    model.RoleUser,
		Content: req.Prompt,
	}}
	parentID := user.ID

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
		messages = append(messages, model.Message{
			Role:      model.RoleAssistant,
			Content:   response.Text,
			ToolCalls: response.ToolCalls,
		})

		parentID = assistant.ID
		for _, call := range response.ToolCalls {
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
		return nil, fmt.Errorf("tool call limit exceeded")
	}
	if !finalReceived {
		return nil, fmt.Errorf("tool call limit exceeded before " +
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

// modelResponse is one complete model pass through text and tool-call events.
type modelResponse struct {
	// Text is the complete assistant text assembled from streamed deltas.
	Text string

	// ToolCalls stores complete tool calls requested by the model.
	ToolCalls []model.ToolCall
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

// collectStream consumes a model stream and joins text and tool-call events.
func collectStream(ctx context.Context,
	stream <-chan model.Event) (modelResponse, error) {

	var text strings.Builder
	var calls []model.ToolCall
	for {
		select {
		case <-ctx.Done():
			return modelResponse{}, ctx.Err()

		case event, ok := <-stream:
			if !ok {
				return modelResponse{
					Text:      text.String(),
					ToolCalls: calls,
				}, nil
			}
			switch event.Type {
			case model.EventTextDelta:
				text.WriteString(event.Text)

			case model.EventToolCall:
				calls = append(calls, event.ToolCall)

			case model.EventDone:
				return modelResponse{
					Text:      text.String(),
					ToolCalls: calls,
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
