package core

import (
	"context"
	"fmt"
	"strings"

	"harness/internal/model"
	"harness/internal/session"
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

	stream, err := req.Model.Stream(ctx, model.Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: req.Prompt,
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("start model stream: %w", err)
	}

	text, err := collectAssistantText(ctx, stream)
	if err != nil {
		return nil, err
	}
	assistant, err := store.Append(
		session.EventAssistantMessage, user.ID,
		session.TextMessage(session.RoleAssistant, text),
	)
	if err != nil {
		return nil, err
	}

	return &TurnResult{
		SessionPath:      store.Path(),
		SessionID:        store.ID(),
		UserEventID:      user.ID,
		AssistantEventID: assistant.ID,
		AssistantText:    text,
	}, nil
}

// collectAssistantText consumes a model stream and joins text deltas into one
// assistant message.
func collectAssistantText(ctx context.Context,
	stream <-chan model.Event) (string, error) {

	var text strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()

		case event, ok := <-stream:
			if !ok {
				return text.String(), nil
			}
			switch event.Type {
			case model.EventTextDelta:
				text.WriteString(event.Text)

			case model.EventDone:
				return text.String(), nil

			case model.EventError:
				return "", fmt.Errorf("model stream error: %s",
					event.Err)

			default:
				return "", fmt.Errorf("unknown model "+
					"event type %q", event.Type)
			}
		}
	}
}
