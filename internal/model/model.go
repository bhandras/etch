package model

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	// RoleSystem identifies system instructions in model requests.
	RoleSystem = "system"

	// RoleUser identifies user messages in model requests.
	RoleUser = "user"

	// RoleAssistant identifies assistant messages in model requests.
	RoleAssistant = "assistant"

	// RoleTool identifies tool-result messages in model requests.
	RoleTool = "tool"

	// EventTextDelta reports streamed assistant text.
	EventTextDelta = "text_delta"

	// EventReasoningDelta reports streamed displayable reasoning summary
	// text. It must not contain private raw chain-of-thought unless a
	// provider explicitly exposes that as safe user-visible content.
	EventReasoningDelta = "reasoning_delta"

	// EventToolCall reports a complete tool call requested by the model.
	EventToolCall = "tool_call"

	// EventUsage reports provider-counted token usage for one model call.
	EventUsage = "usage"

	// EventMetrics reports transport-level measurements for one model call.
	EventMetrics = "metrics"

	// EventDone reports that a stream completed normally.
	EventDone = "done"

	// EventError reports a provider stream error after a stream has
	// started.
	EventError = "error"
)

// Message is one provider-neutral chat message.
type Message struct {
	// Role identifies the speaker that produced Content.
	Role string

	// Content stores the plain text for this first executable slice.
	Content string

	// ToolCalls stores assistant-requested calls attached to this message.
	ToolCalls []ToolCall

	// ToolCallID links a tool-result message to the assistant tool call.
	ToolCallID string

	// Name records the tool name for tool-result messages.
	Name string
}

// Request is the provider-neutral input passed to a model client.
type Request struct {
	// Messages contains the ordered model context for the turn.
	Messages []Message

	// Tools contains model-callable tools available for the turn.
	Tools []ToolSpec
}

// Event is one streamed model event emitted by a client.
type Event struct {
	// Type identifies the stream event kind.
	Type string

	// Text stores assistant text for EventTextDelta events.
	Text string

	// ToolCall stores a complete call for EventToolCall events.
	ToolCall ToolCall

	// Usage stores token counters for EventUsage events.
	Usage Usage

	// Metrics stores transport counters for EventMetrics events.
	Metrics Metrics

	// Err stores a provider error message for EventError events.
	Err string
}

// Usage stores provider-reported token counters for one model call.
type Usage struct {
	// InputTokens is the number of prompt or input tokens.
	InputTokens int

	// CachedInputTokens is the subset of input tokens served from cache.
	CachedInputTokens int

	// OutputTokens is the number of completion or output tokens.
	OutputTokens int

	// ReasoningOutputTokens is the subset of output tokens used for hidden
	// reasoning when the provider reports it.
	ReasoningOutputTokens int

	// TotalTokens is the provider-reported total token count.
	TotalTokens int
}

// Add returns the element-wise sum of two usage counters.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:       u.InputTokens + other.InputTokens,
		CachedInputTokens: u.CachedInputTokens + other.CachedInputTokens,
		OutputTokens:      u.OutputTokens + other.OutputTokens,
		ReasoningOutputTokens: u.ReasoningOutputTokens +
			other.ReasoningOutputTokens,
		TotalTokens: u.TotalTokens + other.TotalTokens,
	}
}

// Empty reports whether the usage value contains no provider counters.
func (u Usage) Empty() bool {
	return u.InputTokens == 0 && u.CachedInputTokens == 0 &&
		u.OutputTokens == 0 && u.ReasoningOutputTokens == 0 &&
		u.TotalTokens == 0
}

// Metrics stores transport-level measurements for one model call.
type Metrics struct {
	// RequestBytes is the JSON request body size sent to the provider.
	RequestBytes int

	// ResponseBytes is the approximate streamed response bytes read.
	ResponseBytes int

	// TimeToHeaders is the duration from starting the request until the
	// provider returned response headers.
	TimeToHeaders time.Duration

	// TimeToFirstEvent is the duration from starting the request until the
	// first meaningful stream event payload was read.
	TimeToFirstEvent time.Duration
}

// Add returns the element-wise sum of two transport metric values.
func (m Metrics) Add(other Metrics) Metrics {
	return Metrics{
		RequestBytes:     m.RequestBytes + other.RequestBytes,
		ResponseBytes:    m.ResponseBytes + other.ResponseBytes,
		TimeToHeaders:    m.TimeToHeaders + other.TimeToHeaders,
		TimeToFirstEvent: m.TimeToFirstEvent + other.TimeToFirstEvent,
	}
}

// Empty reports whether metrics contains no provider measurements.
func (m Metrics) Empty() bool {
	return m.RequestBytes == 0 && m.ResponseBytes == 0 &&
		m.TimeToHeaders == 0 && m.TimeToFirstEvent == 0
}

// ToolSpec describes one model-callable tool.
type ToolSpec struct {
	// Name is the stable tool identifier used in model tool calls.
	Name string

	// Description explains when and how the model should call the tool.
	Description string

	// Parameters is a JSON Schema object describing tool arguments.
	Parameters json.RawMessage
}

// ToolCall is one complete model-requested tool invocation.
type ToolCall struct {
	// ID is the provider-assigned call identifier.
	ID string

	// Name is the tool name to execute.
	Name string

	// Arguments stores the raw JSON argument object.
	Arguments string
}

// Client streams model events for one request.
type Client interface {
	// Stream starts a model response for req and closes the returned
	// channel when no more events are available.
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

// EchoClient is a deterministic model client that repeats the latest user
// message.
type EchoClient struct{}

// Stream emits the latest user message as a single text delta followed by done.
func (EchoClient) Stream(ctx context.Context, req Request) (<-chan Event,
	error) {

	text, err := latestUserMessage(req.Messages)
	if err != nil {
		return nil, err
	}

	events := make(chan Event, 2)
	go func() {
		defer close(events)
		select {
		case <-ctx.Done():
			return

		case events <- Event{Type: EventTextDelta, Text: text}:
		}

		select {
		case <-ctx.Done():
			return

		case events <- Event{Type: EventDone}:
		}
	}()

	return events, nil
}

// latestUserMessage returns the last user-authored message from a request.
func latestUserMessage(messages []Message) (string, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser {
			return messages[i].Content, nil
		}
	}

	return "", fmt.Errorf("model request has no user message")
}
