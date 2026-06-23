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

	// EventResponseInfo reports provider response identity for one model
	// call. It is observational state for diagnostics and future
	// continuation support.
	EventResponseInfo = "response_info"

	// EventProviderItem reports an opaque provider-native item that should
	// be durably replayed only by compatible provider clients.
	EventProviderItem = "provider_item"

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

	// ProviderItems stores opaque provider-native history items that are
	// not ordinary model-visible text.
	ProviderItems []ProviderItem
}

// Request is the provider-neutral input passed to a model client.
type Request struct {
	// SessionID identifies the durable local session for provider-side
	// cache affinity. Empty means the request is not tied to a session.
	SessionID string

	// PreviousResponseID identifies a provider response that this request
	// should continue. Empty means the request contains a full context.
	PreviousResponseID string

	// Messages contains the ordered model context for the turn.
	Messages []Message

	// DeltaMessages contains only new input since PreviousResponseID when
	// a continuation request is safe. Providers that cannot continue can
	// ignore it and send Messages instead.
	DeltaMessages []Message

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

	// ResponseInfo stores provider identity for EventResponseInfo events.
	ResponseInfo ResponseInfo

	// ProviderItem stores opaque provider-native data for EventProviderItem
	// events.
	ProviderItem ProviderItem

	// Metrics stores transport counters for EventMetrics events.
	Metrics Metrics

	// Err stores a provider error message for EventError events.
	Err string
}

// ResponseInfo stores provider response identity for continuation-aware APIs.
type ResponseInfo struct {
	// ProviderResponseID is the provider's stable response identifier when
	// it exposes one.
	ProviderResponseID string
}

// Empty reports whether response info contains no provider identity.
func (r ResponseInfo) Empty() bool {
	return r.ProviderResponseID == ""
}

// ProviderItem stores opaque provider-native history that a compatible client
// can replay without exposing it as plain text to unrelated providers.
type ProviderItem struct {
	// Provider identifies the model backend that produced the item.
	Provider string

	// Type is the provider-native item type, such as reasoning.
	Type string

	// ID is the provider item identifier when supplied by the backend.
	ID string

	// EncryptedContent stores opaque provider ciphertext for replay.
	EncryptedContent string

	// Summary stores displayable provider reasoning summary text needed to
	// replay opaque reasoning items with their original shape.
	Summary string
}

// Empty reports whether the provider item has no replayable content.
func (p ProviderItem) Empty() bool {
	return p.Provider == "" && p.Type == "" && p.ID == "" &&
		p.EncryptedContent == "" && p.Summary == ""
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
	// Transport is the provider transport used for this request, such as
	// http or websocket.
	Transport string

	// Requests is the number of provider HTTP requests represented by this
	// metric value. Individual stream events normally report one request.
	Requests int

	// WebSocketConnections is the number of new WebSocket connections
	// opened for this metric value.
	WebSocketConnections int

	// WebSocketReuses is the number of requests that reused an existing
	// WebSocket connection.
	WebSocketReuses int

	// ContinuationRequests is the number of requests that continued from a
	// provider response ID instead of sending a full model context.
	ContinuationRequests int

	// ContinuationFallbacks is the number of continuation attempts that
	// were retried as full-context requests after provider rejection.
	ContinuationFallbacks int

	// ContinuationFallbackStatus is the HTTP status code that caused the
	// most recent continuation fallback.
	ContinuationFallbackStatus int

	// ContinuationFallbackError is bounded provider error text captured
	// from the most recent continuation fallback.
	ContinuationFallbackError string

	// RequestBytes is the JSON request body size sent to the provider.
	RequestBytes int

	// ResponseBytes is the approximate streamed response bytes read.
	ResponseBytes int

	// InputMessages is the number of neutral model messages selected as
	// provider input.
	InputMessages int

	// DeltaMessages is the number of neutral model messages selected from
	// Request.DeltaMessages for continuation-aware calls.
	DeltaMessages int

	// ToolCount is the number of tool schemas sent with the request.
	ToolCount int

	// InstructionBytes is the byte length of provider instruction text sent
	// outside the ordinary input list when the API has such a field.
	InstructionBytes int

	// InputBytes is the serialized byte length of the provider input
	// message payload when it is measured separately from the full body.
	InputBytes int

	// ToolBytes is the serialized byte length of the provider tool schema
	// payload when it is measured separately from the full body.
	ToolBytes int

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
		Transport: mergeMetricString(m.Transport, other.Transport),
		Requests:  m.Requests + other.Requests,
		WebSocketConnections: m.WebSocketConnections +
			other.WebSocketConnections,
		WebSocketReuses: m.WebSocketReuses + other.WebSocketReuses,
		ContinuationRequests: m.ContinuationRequests +
			other.ContinuationRequests,
		ContinuationFallbacks: m.ContinuationFallbacks +
			other.ContinuationFallbacks,
		ContinuationFallbackStatus: mergeMetricInt(
			m.ContinuationFallbackStatus,
			other.ContinuationFallbackStatus,
		),
		ContinuationFallbackError: mergeMetricString(
			m.ContinuationFallbackError,
			other.ContinuationFallbackError,
		),
		RequestBytes:     m.RequestBytes + other.RequestBytes,
		ResponseBytes:    m.ResponseBytes + other.ResponseBytes,
		InputMessages:    m.InputMessages + other.InputMessages,
		DeltaMessages:    m.DeltaMessages + other.DeltaMessages,
		ToolCount:        m.ToolCount + other.ToolCount,
		InstructionBytes: m.InstructionBytes + other.InstructionBytes,
		InputBytes:       m.InputBytes + other.InputBytes,
		ToolBytes:        m.ToolBytes + other.ToolBytes,
		TimeToHeaders:    m.TimeToHeaders + other.TimeToHeaders,
		TimeToFirstEvent: m.TimeToFirstEvent + other.TimeToFirstEvent,
	}
}

// Empty reports whether metrics contains no provider measurements.
func (m Metrics) Empty() bool {
	return m.Transport == "" &&
		m.Requests == 0 &&
		m.WebSocketConnections == 0 &&
		m.WebSocketReuses == 0 &&
		m.ContinuationRequests == 0 &&
		m.ContinuationFallbacks == 0 &&
		m.ContinuationFallbackStatus == 0 &&
		m.ContinuationFallbackError == "" &&
		m.RequestBytes == 0 && m.ResponseBytes == 0 &&
		m.InputMessages == 0 && m.DeltaMessages == 0 &&
		m.ToolCount == 0 && m.InstructionBytes == 0 &&
		m.InputBytes == 0 && m.ToolBytes == 0 &&
		m.TimeToHeaders == 0 && m.TimeToFirstEvent == 0
}

// mergeMetricInt returns the newer non-zero diagnostic integer when present.
func mergeMetricInt(current int, newer int) int {
	if newer != 0 {
		return newer
	}

	return current
}

// mergeMetricString returns the newer non-empty diagnostic string when
// present.
func mergeMetricString(current string, newer string) string {
	if newer != "" {
		return newer
	}

	return current
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
