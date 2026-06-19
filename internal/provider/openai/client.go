package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
			sendEvent(
				ctx, events, model.Event{
					Type: model.EventDone,
				},
			)

			return
		}

		text, err := deltaText([]byte(payload))
		if err != nil {
			sendEvent(ctx, events, model.Event{
				Type: model.EventError,
				Err:  err.Error(),
			})

			return
		}
		if text == "" {
			continue
		}
		if !sendEvent(ctx, events, model.Event{
			Type: model.EventTextDelta,
			Text: text,
		}) {
			return
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

// deltaText extracts the streamed content delta from a chat completion chunk.
func deltaText(payload []byte) (string, error) {
	var chunk chatChunk
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return "", fmt.Errorf("decode openai stream chunk: %w", err)
	}
	if len(chunk.Choices) == 0 {
		return "", nil
	}

	return chunk.Choices[0].Delta.Content, nil
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
			Role:    message.Role,
			Content: message.Content,
		})
	}

	return out
}

// chatRequest is the JSON request shape for Chat Completions.
type chatRequest struct {
	// Model is the provider model identifier.
	Model string `json:"model"`

	// Stream enables server-sent event output.
	Stream bool `json:"stream"`

	// Messages contains the ordered chat history.
	Messages []chatMessage `json:"messages"`
}

// chatMessage is one OpenAI-compatible chat message.
type chatMessage struct {
	// Role identifies the chat speaker.
	Role string `json:"role"`

	// Content stores the text message body.
	Content string `json:"content"`
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
}
