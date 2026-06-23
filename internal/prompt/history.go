package prompt

import (
	"encoding/json"
	"fmt"

	"harness/internal/model"
	"harness/internal/session"
)

const (
	// SummaryContextPrefix marks compacted history when projected into the
	// model-visible system context.
	SummaryContextPrefix = "Conversation summary for earlier history:\n"
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

	summary, startIndex, err := latestSummary(req.Events)
	if err != nil {
		return nil, err
	}
	if summary != nil {
		messages = append(messages, model.Message{
			Role:    model.RoleSystem,
			Content: SummaryContextPrefix + summary.Summary,
		})
	}

	var pendingReasoning string
	for _, event := range req.Events[startIndex:] {
		if event.Type == session.EventModelReasoning {
			pendingReasoning, err = reasoningFromEvent(event)
			if err != nil {
				return nil, err
			}

			continue
		}

		message, ok, err := messageFromEvent(event, pendingReasoning)
		if err != nil {
			return nil, err
		}
		if ok {
			messages = append(messages, message)
			pendingReasoning = ""
		}
	}

	return messages, nil
}

// latestSummary returns the newest compaction summary and replay start index.
func latestSummary(events []session.Event) (*session.SummaryData, int, error) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != session.EventContextSummary {
			continue
		}

		var summary session.SummaryData
		if err := json.Unmarshal(events[i].Data, &summary); err != nil {
			return nil, 0, fmt.Errorf("decode summary %s: %w",
				events[i].ID, err)
		}

		start := indexAfterSummary(events, summary.FirstKeptEventID)

		return &summary, start, nil
	}

	return nil, 0, nil
}

// indexAfterSummary finds where raw replay should resume.
func indexAfterSummary(events []session.Event, firstKeptID string) int {
	if firstKeptID == "" {
		return 0
	}
	for i, event := range events {
		if event.ID == firstKeptID {
			return i
		}
	}

	return 0
}

// messageFromEvent converts one durable message event into a model message.
func messageFromEvent(event session.Event, reasoningFallback string) (
	model.Message, bool, error) {

	if event.Type == session.EventModelProviderItem {
		item, err := providerItemFromEvent(event)
		if err != nil {
			return model.Message{}, false, err
		}
		if item.Summary == "" {
			item.Summary = reasoningFallback
		}

		return model.Message{
			ProviderItems: []model.ProviderItem{
				item,
			},
		}, true, nil
	}
	if !session.IsMessageEvent(event.Type) {
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

// reasoningFromEvent decodes displayable reasoning text for replay fallback.
func reasoningFromEvent(event session.Event) (string, error) {
	var data session.ReasoningData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return "", fmt.Errorf("decode reasoning %s: %w", event.ID, err)
	}

	return data.Reasoning, nil
}

// providerItemFromEvent converts one durable provider item into model history.
func providerItemFromEvent(event session.Event) (model.ProviderItem, error) {
	var data session.ProviderItemData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return model.ProviderItem{}, fmt.Errorf("decode provider item "+
			"%s: %w", event.ID, err)
	}

	return model.ProviderItem{
		Provider:         data.Provider,
		Type:             data.Type,
		ID:               data.ID,
		EncryptedContent: data.EncryptedContent,
		Summary:          data.Summary,
	}, nil
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
