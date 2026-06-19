package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"harness/internal/model"
)

const (
	// DefaultBaseURL is the OpenAI-compatible API root used when no
	// override is provided.
	DefaultBaseURL = "https://api.openai.com/v1"

	// ProviderName identifies the OpenAI-compatible provider in CLI flags.
	ProviderName = "openai"

	// chatCompletionsPath is the endpoint used for streaming chat
	// responses.
	chatCompletionsPath = "/chat/completions"
)

// Client streams responses from an OpenAI-compatible Chat Completions endpoint.
type Client struct {
	// BaseURL is the API root, such as https://api.openai.com/v1.
	BaseURL string

	// APIKey is sent as a Bearer token when non-empty.
	APIKey string

	// Model is the provider model name passed in each request.
	Model string

	// HTTPClient performs requests; http.DefaultClient is used when nil.
	HTTPClient *http.Client
}

// Stream starts a streaming chat completion request and returns model events.
func (c *Client) Stream(ctx context.Context, req model.Request) (
	<-chan model.Event, error) {

	if c.Model == "" {
		return nil, fmt.Errorf("openai model must not be empty")
	}

	httpReq, err := c.newRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()

		return nil, readErrorResponse(resp)
	}

	events := make(chan model.Event)
	go streamResponse(ctx, resp.Body, events)

	return events, nil
}

// newRequest builds the HTTP request for a streaming chat completion.
func (c *Client) newRequest(ctx context.Context, req model.Request) (
	*http.Request, error) {

	body, err := json.Marshal(chatRequest{
		Model:    c.Model,
		Stream:   true,
		Messages: chatMessages(req.Messages),
		Tools:    chatTools(req.Tools),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("create openai request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	return httpReq, nil
}

// endpoint returns the complete chat completions endpoint URL.
func (c *Client) endpoint() string {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	return baseURL + chatCompletionsPath
}

// streamResponse decodes server-sent events into provider-neutral model events.
func streamResponse(ctx context.Context, body io.ReadCloser,
	events chan<- model.Event) {

	defer close(events)
	defer body.Close()

	accumulator := newToolAccumulator()
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return

		default:
		}

		payload, ok := eventPayload(scanner.Text())
		if !ok {
			continue
		}
		if payload == "[DONE]" {
			for _, call := range accumulator.calls() {
				if !sendEvent(ctx, events, model.Event{
					Type:     model.EventToolCall,
					ToolCall: call,
				}) {
					return
				}
			}
			sendEvent(
				ctx, events, model.Event{
					Type: model.EventDone,
				},
			)

			return
		}

		chunk, err := decodeChunk([]byte(payload))
		if err != nil {
			sendEvent(ctx, events, model.Event{
				Type: model.EventError,
				Err:  err.Error(),
			})

			return
		}
		for _, event := range chunk.Events {
			if !sendEvent(ctx, events, event) {
				return
			}
		}
		for _, delta := range chunk.ToolDeltas {
			accumulator.add(delta)
		}
	}
	if err := scanner.Err(); err != nil {
		sendEvent(ctx, events, model.Event{
			Type: model.EventError,
			Err:  fmt.Errorf("read openai stream: %w", err).Error(),
		})
	}
}

// sendEvent sends one stream event unless the context has been cancelled.
func sendEvent(ctx context.Context, events chan<- model.Event,
	event model.Event) bool {

	select {
	case <-ctx.Done():
		return false

	case events <- event:
		return true
	}
}

// eventPayload extracts the payload from one SSE data line.
func eventPayload(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, ":") {
		return "", false
	}
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}

	return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
}

// decodeChunk converts one JSON stream payload into neutral model events.
func decodeChunk(payload []byte) (decodedChunk, error) {
	var chunk chatChunk
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return decodedChunk{}, fmt.Errorf("decode openai stream "+
			"chunk: %w", err)
	}
	if len(chunk.Choices) == 0 {
		return decodedChunk{}, nil
	}

	var decoded decodedChunk
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			decoded.Events = append(decoded.Events, model.Event{
				Type: model.EventTextDelta,
				Text: choice.Delta.Content,
			})
		}
		decoded.ToolDeltas = append(
			decoded.ToolDeltas, choice.Delta.ToolCalls...,
		)
	}

	return decoded, nil
}

// readErrorResponse converts a non-2xx response into a concise error.
func readErrorResponse(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("openai status %s: read error body: %w",
			resp.Status, err)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Errorf("openai status %s", resp.Status)
	}

	return fmt.Errorf("openai status %s: %s", resp.Status, text)
}

// chatMessages converts provider-neutral messages into OpenAI-compatible
// message objects.
func chatMessages(messages []model.Message) []chatMessage {
	out := make([]chatMessage, 0, len(messages))
	for _, message := range messages {
		out = append(out, chatMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
			ToolCalls:  chatMessageToolCalls(message.ToolCalls),
		})
	}

	return out
}

// chatMessageToolCalls converts neutral assistant tool calls into OpenAI
// history entries.
func chatMessageToolCalls(calls []model.ToolCall) []chatToolCall {
	out := make([]chatToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, chatToolCall{
			ID:   call.ID,
			Type: "function",
			Function: chatToolFunction{
				Name:      call.Name,
				Arguments: call.Arguments,
			},
		})
	}

	return out
}

// chatTools converts neutral tool specs into OpenAI function tool schemas.
func chatTools(specs []model.ToolSpec) []chatTool {
	out := make([]chatTool, 0, len(specs))
	for _, spec := range specs {
		out = append(out, chatTool{
			Type: "function",
			Function: chatToolSpec{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  spec.Parameters,
			},
		})
	}

	return out
}

// decodedChunk stores neutral events extracted from one OpenAI stream chunk.
type decodedChunk struct {
	// Events contains text events extracted from the chunk.
	Events []model.Event

	// ToolDeltas contains streamed OpenAI tool-call fragments.
	ToolDeltas []chatToolCall
}

// toolAccumulator rebuilds complete tool calls from streamed deltas.
type toolAccumulator struct {
	// callsByIndex stores partially assembled calls by OpenAI stream index.
	callsByIndex map[int]*model.ToolCall
}

// newToolAccumulator creates an empty streamed tool-call accumulator.
func newToolAccumulator() *toolAccumulator {
	return &toolAccumulator{
		callsByIndex: make(map[int]*model.ToolCall),
	}
}

// add merges one OpenAI tool-call delta into the accumulated call.
func (a *toolAccumulator) add(delta chatToolCall) {
	call := a.callsByIndex[delta.Index]
	if call == nil {
		call = &model.ToolCall{}
		a.callsByIndex[delta.Index] = call
	}
	if delta.ID != "" {
		call.ID = delta.ID
	}
	if delta.Function.Name != "" {
		call.Name = delta.Function.Name
	}
	if delta.Function.Arguments != "" {
		call.Arguments += delta.Function.Arguments
	}
}

// calls returns complete accumulated calls sorted by stream index.
func (a *toolAccumulator) calls() []model.ToolCall {
	indexes := make([]int, 0, len(a.callsByIndex))
	for index := range a.callsByIndex {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	calls := make([]model.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		call := *a.callsByIndex[index]
		if call.ID == "" && call.Name == "" && call.Arguments == "" {
			continue
		}
		calls = append(calls, call)
	}

	return calls
}

// chatRequest is the JSON request shape for Chat Completions.
type chatRequest struct {
	// Model is the provider model identifier.
	Model string `json:"model"`

	// Stream enables server-sent event output.
	Stream bool `json:"stream"`

	// Messages contains the ordered chat history.
	Messages []chatMessage `json:"messages"`

	// Tools contains function tools available to the model.
	Tools []chatTool `json:"tools,omitempty"`
}

// chatMessage is one OpenAI-compatible chat message.
type chatMessage struct {
	// Role identifies the chat speaker.
	Role string `json:"role"`

	// Content stores the text message body.
	Content string `json:"content"`

	// ToolCallID links tool result messages to assistant tool calls.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// ToolCalls stores assistant tool calls in conversation history.
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
}

// chatChunk is one streamed Chat Completions response chunk.
type chatChunk struct {
	// Choices contains streamed candidate deltas.
	Choices []chatChoice `json:"choices"`
}

// chatChoice contains one streamed candidate delta.
type chatChoice struct {
	// Delta stores the incremental assistant message content.
	Delta chatDelta `json:"delta"`
}

// chatDelta contains incremental assistant text for one chunk.
type chatDelta struct {
	// Content stores text emitted by this chunk.
	Content string `json:"content"`

	// ToolCalls stores streamed tool calls emitted by this chunk.
	ToolCalls []chatToolCall `json:"tool_calls"`
}

// chatTool is one OpenAI function tool declaration.
type chatTool struct {
	// Type identifies the OpenAI tool kind.
	Type string `json:"type"`

	// Function stores the function schema.
	Function chatToolSpec `json:"function"`
}

// chatToolSpec is the OpenAI function schema payload.
type chatToolSpec struct {
	// Name is the model-facing function name.
	Name string `json:"name"`

	// Description explains when the model should call the function.
	Description string `json:"description"`

	// Parameters is the function argument JSON Schema.
	Parameters json.RawMessage `json:"parameters"`
}

// chatToolCall is one OpenAI function tool call.
type chatToolCall struct {
	// Index identifies the call position in streamed deltas.
	Index int `json:"index,omitempty"`

	// ID is the provider-assigned tool call identifier.
	ID string `json:"id,omitempty"`

	// Type identifies the tool call kind.
	Type string `json:"type,omitempty"`

	// Function stores the function name and raw arguments.
	Function chatToolFunction `json:"function"`
}

// chatToolFunction stores OpenAI function call details.
type chatToolFunction struct {
	// Name is the function name to execute.
	Name string `json:"name,omitempty"`

	// Arguments stores the raw JSON argument object.
	Arguments string `json:"arguments,omitempty"`
}
