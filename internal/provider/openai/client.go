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
	"time"

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

	// responsesPath is the endpoint used for streaming Responses API
	// events.
	responsesPath = "/responses"

	// APIChatCompletions selects the Chat Completions request shape.
	APIChatCompletions = "chat"

	// APIResponses selects the Responses API request shape.
	APIResponses = "responses"
)

// Client streams responses from an OpenAI-compatible Chat Completions endpoint.
type Client struct {
	// BaseURL is the API root, such as https://api.openai.com/v1.
	BaseURL string

	// APIKey is sent as a Bearer token when non-empty.
	APIKey string

	// Model is the provider model name passed in each request.
	Model string

	// API selects the OpenAI API shape. Empty means APIChatCompletions.
	API string

	// ReasoningEffort asks reasoning-capable models to adjust effort.
	ReasoningEffort string

	// ReasoningSummary asks reasoning-capable models for a summary.
	ReasoningSummary string

	// HTTPClient performs requests; http.DefaultClient is used when nil.
	HTTPClient *http.Client

	// UserAgent identifies harness to provider backends when non-empty.
	UserAgent string
}

// Stream starts a streaming chat completion request and returns model events.
func (c *Client) Stream(ctx context.Context, req model.Request) (
	<-chan model.Event, error) {

	if c.Model == "" {
		return nil, fmt.Errorf("openai model must not be empty")
	}
	if err := c.validateAPI(); err != nil {
		return nil, err
	}

	httpReq, err := c.newRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	startedAt := time.Now()
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	timeToHeaders := time.Since(startedAt)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()

		return nil, readErrorResponse(resp)
	}

	events := make(chan model.Event)
	metrics := streamMetrics{
		startedAt:     startedAt,
		timeToHeaders: timeToHeaders,
		requestBytes:  requestContentLength(httpReq),
	}
	if c.apiMode() == APIResponses {
		go streamResponsesAPI(ctx, resp.Body, events, metrics)
	} else {
		go streamChatCompletions(ctx, resp.Body, events, metrics)
	}

	return events, nil
}

// newRequest builds the HTTP request for a streaming chat completion.
func (c *Client) newRequest(ctx context.Context, req model.Request) (
	*http.Request, error) {

	if c.apiMode() == APIResponses {
		return c.newResponsesRequest(ctx, req)
	}

	body, err := json.Marshal(chatRequest{
		Model:         c.Model,
		Stream:        true,
		StreamOptions: chatStreamOptions{IncludeUsage: true},
		Messages:      chatMessages(req.Messages),
		Tools:         chatTools(req.Tools),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.endpoint(chatCompletionsPath),
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("create openai request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	c.addCommonHeaders(httpReq)
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	return httpReq, nil
}

// newResponsesRequest builds the HTTP request for a streaming response.
func (c *Client) newResponsesRequest(ctx context.Context, req model.Request) (
	*http.Request, error) {

	body, err := json.Marshal(responseRequest{
		Model:        c.Model,
		Stream:       true,
		Store:        false,
		Instructions: responseInstructions(req.Messages),
		Input:        responseInput(req.Messages),
		Tools:        responseTools(req.Tools),
		Reasoning: responseReasoningConfig(
			c.ReasoningEffort, c.ReasoningSummary,
		),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal openai response request: %w",
			err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.endpoint(responsesPath),
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("create openai response request: %w",
			err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	c.addCommonHeaders(httpReq)
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	return httpReq, nil
}

// apiMode returns the configured OpenAI API shape.
func (c *Client) apiMode() string {
	switch c.API {
	case "", APIChatCompletions:
		return APIChatCompletions

	case APIResponses:
		return APIResponses

	default:
		return c.API
	}
}

// validateAPI rejects unknown OpenAI API modes before making a request.
func (c *Client) validateAPI() error {
	switch c.apiMode() {
	case APIChatCompletions, APIResponses:
		return nil

	default:
		return fmt.Errorf("unknown openai api %q", c.API)
	}
}

// endpoint returns the complete endpoint URL for an API path.
func (c *Client) endpoint(path string) string {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	return baseURL + path
}

// addCommonHeaders applies optional headers shared by OpenAI request shapes.
func (c *Client) addCommonHeaders(req *http.Request) {
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
}

// requestContentLength returns the JSON body length when the request knows it.
func requestContentLength(req *http.Request) int {
	if req.ContentLength <= 0 {
		return 0
	}

	return int(req.ContentLength)
}

// streamMetrics tracks one HTTP/SSE model call while it is decoded.
type streamMetrics struct {
	// startedAt is the wall-clock time before the HTTP request began.
	startedAt time.Time

	// timeToHeaders is how long the provider took to return headers.
	timeToHeaders time.Duration

	// requestBytes is the serialized JSON request body size.
	requestBytes int

	// responseBytes is the approximate SSE line bytes read so far.
	responseBytes int

	// timeToFirstEvent is set after the first meaningful SSE payload.
	timeToFirstEvent time.Duration
}

// addLine records one raw scanner line in the approximate stream byte total.
func (m *streamMetrics) addLine(line []byte) {
	m.responseBytes += len(line)
}

// markEvent records the first meaningful event payload arrival time.
func (m *streamMetrics) markEvent(payload string) {
	if m.timeToFirstEvent != 0 || payload == "" || payload == "[DONE]" {
		return
	}
	m.timeToFirstEvent = time.Since(m.startedAt)
}

// event returns a neutral model metric event for the completed stream.
func (m streamMetrics) event() model.Event {
	return model.Event{
		Type: model.EventMetrics,
		Metrics: model.Metrics{
			RequestBytes:     m.requestBytes,
			ResponseBytes:    m.responseBytes,
			TimeToHeaders:    m.timeToHeaders,
			TimeToFirstEvent: m.timeToFirstEvent,
		},
	}
}

// sendMetricsAndDone reports transport metrics before the stream completion.
func sendMetricsAndDone(ctx context.Context, events chan<- model.Event,
	metrics streamMetrics) {

	if !sendEvent(ctx, events, metrics.event()) {
		return
	}
	sendEvent(
		ctx, events, model.Event{
			Type: model.EventDone,
		},
	)
}

// streamChatCompletions decodes Chat Completions SSE into neutral events.
func streamChatCompletions(ctx context.Context, body io.ReadCloser,
	events chan<- model.Event, metrics streamMetrics) {

	defer close(events)
	defer body.Close()

	accumulator := newToolAccumulator()
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		metrics.addLine(scanner.Bytes())
		select {
		case <-ctx.Done():
			return

		default:
		}

		payload, ok := eventPayload(scanner.Text())
		if !ok {
			continue
		}
		metrics.markEvent(payload)
		if payload == "[DONE]" {
			for _, call := range accumulator.calls() {
				if !sendEvent(ctx, events, model.Event{
					Type:     model.EventToolCall,
					ToolCall: call,
				}) {
					return
				}
			}
			sendMetricsAndDone(ctx, events, metrics)

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
	var decoded decodedChunk
	if usage := chunk.Usage.neutral(); !usage.Empty() {
		decoded.Events = append(decoded.Events, model.Event{
			Type:  model.EventUsage,
			Usage: usage,
		})
	}
	if len(chunk.Choices) == 0 {
		return decoded, nil
	}

	for _, choice := range chunk.Choices {
		if choice.Delta.reasoningText() != "" {
			decoded.Events = append(decoded.Events, model.Event{
				Type: model.EventReasoningDelta,
				Text: choice.Delta.reasoningText(),
			})
		}
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

// streamResponsesAPI decodes Responses API SSE into neutral model events.
func streamResponsesAPI(ctx context.Context, body io.ReadCloser,
	events chan<- model.Event, metrics streamMetrics) {

	defer close(events)
	defer body.Close()

	decoder := responseStreamDecoder{}
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		metrics.addLine(scanner.Bytes())
		select {
		case <-ctx.Done():
			return

		default:
		}

		payload, ok := eventPayload(scanner.Text())
		if !ok {
			continue
		}
		metrics.markEvent(payload)
		if payload == "[DONE]" {
			sendMetricsAndDone(ctx, events, metrics)

			return
		}

		for _, event := range decoder.decode([]byte(payload)) {
			if event.Type == model.EventDone {
				sendMetricsAndDone(ctx, events, metrics)

				return
			}
			if !sendEvent(ctx, events, event) {
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		sendEvent(ctx, events, model.Event{
			Type: model.EventError,
			Err: fmt.
				Errorf("read openai response stream: %w", err).
				Error(),
		})
	}
}

// responseStreamDecoder converts Responses API SSE with item lifecycle state.
type responseStreamDecoder struct {
	// activeItemType is the current Responses output item receiving deltas.
	activeItemType string

	// activeItemRole is the role for active message items when present.
	activeItemRole string
}

// decode converts one Responses API stream event.
func (d *responseStreamDecoder) decode(payload []byte) []model.Event {
	var event responseStreamEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return []model.Event{{
			Type: model.EventError,
			Err: fmt.Errorf("decode openai response stream "+
				"event: %w", err).Error(),
		}}
	}

	switch event.Type {
	case "response.output_item.added":
		d.activeItemType = event.Item.Type
		d.activeItemRole = event.Item.Role

		return nil

	case "response.output_text.delta":
		if d.activeItemType != "message" ||
			(d.activeItemRole != "" &&
				d.activeItemRole != model.RoleAssistant) {
			return nil
		}

		return []model.Event{
			{
				Type: model.EventTextDelta,
				Text: event.Delta,
			},
		}

	case "response.reasoning_summary_text.delta":
		if d.activeItemType != "reasoning" {
			return nil
		}

		return []model.Event{{Type: model.EventReasoningDelta,
			Text: event.Delta}}

	case "response.output_item.done":
		defer d.clearActiveItem()
		if event.Item.Type != "function_call" {
			return nil
		}

		return []model.Event{{
			Type: model.EventToolCall,
			ToolCall: model.ToolCall{
				ID:        event.Item.CallID,
				Name:      event.Item.Name,
				Arguments: event.Item.Arguments,
			},
		}}

	case "response.completed":
		var events []model.Event
		if usage := event.Response.Usage.neutral(); !usage.Empty() {
			events = append(events, model.Event{
				Type:  model.EventUsage,
				Usage: usage,
			})
		}
		events = append(events, model.Event{Type: model.EventDone})

		return events

	case "response.failed", "response.incomplete":
		return []model.Event{{Type: model.EventError,
			Err: responseErrorText(event)}}

	default:
		return nil
	}
}

// clearActiveItem forgets the Responses output item receiving deltas.
func (d *responseStreamDecoder) clearActiveItem() {
	d.activeItemType = ""
	d.activeItemRole = ""
}

// responseErrorText returns a concise message from a Responses error event.
func responseErrorText(event responseStreamEvent) string {
	if event.Response.Error.Message != "" {
		return event.Response.Error.Message
	}
	if event.Type != "" {
		return event.Type
	}

	return "openai response failed"
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

// responseInstructions joins system messages for the Responses instructions.
func responseInstructions(messages []model.Message) string {
	var parts []string
	for _, message := range messages {
		if message.Role == model.RoleSystem && message.Content != "" {
			parts = append(parts, message.Content)
		}
	}

	return strings.Join(parts, "\n\n")
}

// responseInput converts neutral history into Responses input items.
func responseInput(messages []model.Message) []responseInputItem {
	var out []responseInputItem
	for _, message := range messages {
		if message.Role == model.RoleSystem {
			continue
		}
		if message.Role == model.RoleTool {
			out = append(out, responseInputItem{
				Type:   "function_call_output",
				CallID: message.ToolCallID,
				Output: message.Content,
			})

			continue
		}
		if message.Content != "" {
			out = append(out, responseInputItem{
				Role:    message.Role,
				Content: message.Content,
			})
		}
		for _, call := range message.ToolCalls {
			out = append(out, responseInputItem{
				Type:      "function_call",
				CallID:    call.ID,
				Name:      call.Name,
				Arguments: call.Arguments,
			})
		}
	}

	return out
}

// responseTools converts neutral tool specs into Responses function tools.
func responseTools(specs []model.ToolSpec) []responseTool {
	out := make([]responseTool, 0, len(specs))
	for _, spec := range specs {
		out = append(out, responseTool{
			Type:        "function",
			Name:        spec.Name,
			Description: spec.Description,
			Parameters:  spec.Parameters,
		})
	}

	return out
}

// responseReasoningConfig returns a reasoning object when configured.
func responseReasoningConfig(effort string, summary string) *responseReasoning {
	if effort == "" && summary == "" {
		return nil
	}

	return &responseReasoning{
		Effort:  effort,
		Summary: summary,
	}
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

	// StreamOptions configures extra streaming metadata.
	StreamOptions chatStreamOptions `json:"stream_options,omitempty"`

	// Messages contains the ordered chat history.
	Messages []chatMessage `json:"messages"`

	// Tools contains function tools available to the model.
	Tools []chatTool `json:"tools,omitempty"`
}

// chatStreamOptions configures Chat Completions streaming behavior.
type chatStreamOptions struct {
	// IncludeUsage asks OpenAI to send a final usage chunk.
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// responseRequest is the JSON request shape for the Responses API.
type responseRequest struct {
	// Model is the provider model identifier.
	Model string `json:"model"`

	// Stream enables server-sent event output.
	Stream bool `json:"stream"`

	// Store disables provider-side response storage when false.
	Store bool `json:"store"`

	// Instructions stores system-level guidance for the response.
	Instructions string `json:"instructions,omitempty"`

	// Input contains message and function-call history items.
	Input []responseInputItem `json:"input"`

	// Tools contains function tools available to the model.
	Tools []responseTool `json:"tools,omitempty"`

	// Reasoning configures reasoning effort and summary output.
	Reasoning *responseReasoning `json:"reasoning,omitempty"`
}

// responseInputItem is one Responses API input item.
type responseInputItem struct {
	// Type identifies function call items. Empty means message input.
	Type string `json:"type,omitempty"`

	// Role identifies message speakers for ordinary input messages.
	Role string `json:"role,omitempty"`

	// Content stores message text or function-call output.
	Content string `json:"content,omitempty"`

	// CallID links function-call outputs to prior function calls.
	CallID string `json:"call_id,omitempty"`

	// Name is the function name for function-call history.
	Name string `json:"name,omitempty"`

	// Arguments stores raw JSON function-call arguments.
	Arguments string `json:"arguments,omitempty"`

	// Output stores function-call result text.
	Output string `json:"output,omitempty"`
}

// responseReasoning configures reasoning-capable model behavior.
type responseReasoning struct {
	// Effort constrains how much the model reasons.
	Effort string `json:"effort,omitempty"`

	// Summary requests a displayable reasoning summary.
	Summary string `json:"summary,omitempty"`
}

// responseTool is one Responses API function tool declaration.
type responseTool struct {
	// Type identifies the tool kind.
	Type string `json:"type"`

	// Name is the model-facing function name.
	Name string `json:"name"`

	// Description explains when the model should call the function.
	Description string `json:"description"`

	// Parameters is the function argument JSON Schema.
	Parameters json.RawMessage `json:"parameters"`
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

	// Usage stores final token counts when stream usage is enabled.
	Usage openAIUsage `json:"usage"`
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

	// Reasoning stores displayable reasoning text emitted by compatible
	// providers that use a generic reasoning field.
	Reasoning string `json:"reasoning"`

	// ReasoningContent stores displayable reasoning text emitted by
	// OpenAI-compatible providers such as some local model runtimes.
	ReasoningContent string `json:"reasoning_content"`

	// ToolCalls stores streamed tool calls emitted by this chunk.
	ToolCalls []chatToolCall `json:"tool_calls"`
}

// reasoningText returns the first populated chat reasoning delta field.
func (d chatDelta) reasoningText() string {
	if d.ReasoningContent != "" {
		return d.ReasoningContent
	}

	return d.Reasoning
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

// responseStreamEvent is a partially decoded Responses API stream event.
type responseStreamEvent struct {
	// Type identifies the streamed event.
	Type string `json:"type"`

	// Delta stores text deltas for output text and reasoning summaries.
	Delta string `json:"delta"`

	// Item stores completed output item details.
	Item responseOutputItem `json:"item"`

	// Response stores failed or incomplete response details.
	Response responseEventResponse `json:"response"`
}

// responseOutputItem stores one completed Responses output item.
type responseOutputItem struct {
	// Type identifies the output item kind.
	Type string `json:"type"`

	// Role identifies message authors when Type is message.
	Role string `json:"role"`

	// CallID is the call identifier for function calls.
	CallID string `json:"call_id"`

	// Name is the function name.
	Name string `json:"name"`

	// Arguments stores raw JSON function arguments.
	Arguments string `json:"arguments"`
}

// responseEventResponse stores failed or incomplete response state.
type responseEventResponse struct {
	// Error stores provider error details when present.
	Error responseEventError `json:"error"`

	// Usage stores token usage on completed response events.
	Usage openAIUsage `json:"usage"`
}

// responseEventError stores provider error text.
type responseEventError struct {
	// Message is the human-readable error description.
	Message string `json:"message"`
}

// openAIUsage stores both Chat Completions and Responses usage shapes.
type openAIUsage struct {
	// PromptTokens is the Chat Completions input token count.
	PromptTokens int `json:"prompt_tokens"`

	// CompletionTokens is the Chat Completions output token count.
	CompletionTokens int `json:"completion_tokens"`

	// InputTokens is the Responses input token count.
	InputTokens int `json:"input_tokens"`

	// OutputTokens is the Responses output token count.
	OutputTokens int `json:"output_tokens"`

	// TotalTokens is the provider-reported total token count.
	TotalTokens int `json:"total_tokens"`

	// PromptDetails stores Chat Completions prompt token details.
	PromptDetails openAIInputTokenDetails `json:"prompt_tokens_details"`

	// CompletionDetails stores Chat Completions completion token details.
	CompletionDetails openAIOutputTokenDetails `json:"completion_tokens_details"`

	// InputDetails stores Responses input token details.
	InputDetails openAIInputTokenDetails `json:"input_tokens_details"`

	// OutputDetails stores Responses output token details.
	OutputDetails openAIOutputTokenDetails `json:"output_tokens_details"`
}

// neutral converts OpenAI usage fields into provider-neutral counters.
func (u openAIUsage) neutral() model.Usage {
	usage := model.Usage{
		InputTokens:           u.InputTokens,
		CachedInputTokens:     u.InputDetails.CachedTokens,
		OutputTokens:          u.OutputTokens,
		ReasoningOutputTokens: u.OutputDetails.ReasoningTokens,
		TotalTokens:           u.TotalTokens,
	}
	if usage.InputTokens == 0 {
		usage.InputTokens = u.PromptTokens
	}
	if usage.CachedInputTokens == 0 {
		usage.CachedInputTokens = u.PromptDetails.CachedTokens
	}
	if usage.OutputTokens == 0 {
		usage.OutputTokens = u.CompletionTokens
	}
	if usage.ReasoningOutputTokens == 0 {
		usage.ReasoningOutputTokens = u.
			CompletionDetails.
			ReasoningTokens
	}
	if usage.TotalTokens == 0 &&
		(usage.InputTokens != 0 || usage.OutputTokens != 0) {

		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}

	return usage
}

// openAIInputTokenDetails stores cache details for input tokens.
type openAIInputTokenDetails struct {
	// CachedTokens is the subset of input tokens served from prompt cache.
	CachedTokens int `json:"cached_tokens"`
}

// openAIOutputTokenDetails stores output-token subcategories.
type openAIOutputTokenDetails struct {
	// ReasoningTokens is the hidden reasoning token count.
	ReasoningTokens int `json:"reasoning_tokens"`
}
