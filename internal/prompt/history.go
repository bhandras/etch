package prompt

import (
	"encoding/json"
	"fmt"

	"harness/internal/model"
	"harness/internal/session"
)

// HistoryRequest contains durable events to project into model messages.
type HistoryRequest struct {
	// Events stores session events in model-visible order.
	Events []session.Event

	// SystemText stores optional system instructions to prepend.
	SystemText string
}

// BuildHistoryMessages projects session message events into model messages.
func BuildHistoryMessages(req HistoryRequest) ([]model.Message, error) {
	messages := make([]model.Message, 0, len(req.Events)+1)
	if req.SystemText != "" {
		messages = append(messages, model.Message{
			Role:    model.RoleSystem,
			Content: req.SystemText,
		})
	}

	for _, event := range req.Events {
		message, ok, err := messageFromEvent(event)
		if err != nil {
			return nil, err
		}
		if ok {
			messages = append(messages, message)
		}
	}

	return messages, nil
}

// messageFromEvent converts one durable message event into a model message.
func messageFromEvent(event session.Event) (model.Message, bool, error) {
	if !isMessageEvent(event.Type) {
		return model.Message{}, false, nil
	}

	var data session.MessageData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return model.Message{}, false, fmt.Errorf("decode session "+
			"message %s: %w", event.ID, err)
	}

	return model.Message{
		Role:       data.Role,
		Content:    messageText(data),
		ToolCalls:  modelToolCalls(data.ToolCalls),
		ToolCallID: data.ToolCallID,
		Name:       data.Name,
	}, true, nil
}

// isMessageEvent reports whether an event carries model-visible message data.
func isMessageEvent(eventType string) bool {
	return eventType == session.EventUserMessage ||
		eventType == session.EventAssistantMessage ||
		eventType == session.EventToolMessage
}

// messageText joins text content parts from a durable message.
func messageText(message session.MessageData) string {
	var text string
	for _, part := range message.Content {
		if part.Type == session.ContentText {
			text += part.Text
		}
	}

	return text
}

// modelToolCalls converts durable tool calls to model request tool calls.
func modelToolCalls(calls []session.ToolCallData) []model.ToolCall {
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
