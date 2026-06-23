package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"harness/internal/model"
	"harness/internal/textutil"
)

const (
	// DefaultBaseURL is the OpenAI-compatible API root used when no
	// override is provided.
	DefaultBaseURL = "https://api.openai.com/v1"

	// ProviderName identifies the OpenAI-compatible provider in CLI flags.
	ProviderName = "openai"

	// openaiProviderItemName labels opaque provider items emitted by this
	// package in model-neutral streams.
	openaiProviderItemName = "openai"

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

	// TransportHTTP selects plain HTTP/SSE for Responses API calls.
	TransportHTTP = "http"

	// TransportWebSocket selects the Responses WebSocket transport.
	TransportWebSocket = "websocket"

	// TransportAuto tries WebSocket first and falls back to HTTP/SSE before
	// any stream events are emitted.
	TransportAuto = "auto"

	// promptCacheKeyMaxRunes is OpenAI's maximum prompt cache key length.
	promptCacheKeyMaxRunes = 64

	// codexResponsesBetaHeader enables the ChatGPT/Codex Responses HTTP
	// endpoint used by OAuth-backed subscription requests.
	codexResponsesBetaHeader = "responses=experimental"

	// sseReadBufferSize keeps stream reads large enough to amortize
	// syscalls without retaining oversized buffers for normal model deltas.
	sseReadBufferSize = 32 * 1024

	// sseMaxFrameBytes bounds one server-sent-event frame before a
	// delimiter must arrive.
	sseMaxFrameBytes = 4 * 1024 * 1024

	// continuationFallbackErrorBytes bounds provider error text retained
	// when a stored Responses continuation falls back to full context.
	continuationFallbackErrorBytes = 2048
)

// Client streams responses from an OpenAI-compatible Chat Completions endpoint.
type Client struct {
	// BaseURL is the API root, such as https://api.openai.com/v1.
	BaseURL string

	// APIKey is sent as a Bearer token when non-empty.
	APIKey string

	// AccountID is sent to ChatGPT/Codex backends when OAuth credentials
	// identify a workspace account.
	AccountID string

	// Model is the provider model name passed in each request.
	Model string

	// API selects the OpenAI API shape. Empty means APIChatCompletions.
	API string

	// Transport selects the Responses transport: http, websocket, or auto.
	// Empty means http.
	Transport string

	// ReasoningEffort asks reasoning-capable models to adjust effort.
	ReasoningEffort string

	// ReasoningSummary asks reasoning-capable models for a summary.
	ReasoningSummary string

	// HTTPClient performs requests; http.DefaultClient is used when nil.
	HTTPClient *http.Client

	// UserAgent identifies harness to provider backends when non-empty.
	UserAgent string

	// StoreResponses enables provider-side Responses API storage. The
	// default is false because ChatGPT/Codex OAuth backends reject stored
	// responses, and plain HTTP previous_response_id continuation is only
	// valid when storage is enabled.
	StoreResponses bool
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
	if err := c.validateTransport(); err != nil {
		return nil, err
	}

	if c.apiMode() == APIResponses && c.transportMode() != TransportHTTP {
		if c.transportMode() == TransportAuto &&
			websocketHTTPFallbackActive(req.SessionID) {
			return c.streamHTTP(
				ctx, requestWithoutContinuation(req),
			)
		}

		events, err := c.streamResponsesWebSocket(ctx, req)
		if err == nil && c.transportMode() == TransportAuto {
			return c.streamResponsesAutoFallback(ctx, req, events), nil
		}
		if err == nil || c.transportMode() == TransportWebSocket {
			return events, err
		}
		req = requestWithoutContinuation(req)
	}

	return c.streamHTTP(ctx, req)
}

// streamHTTP starts the configured OpenAI API over HTTP/SSE.
func (c *Client) streamHTTP(ctx context.Context, req model.Request) (
	<-chan model.Event, error) {

	httpReq, requestMetrics, err := c.newRequest(ctx, req)
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
	if shouldRetryWithoutContinuation(resp, requestMetrics) {
		statusCode, fallbackError := readContinuationFallbackError(resp)
		fallbackReq, fallbackMetrics, err := c.newRequest(
			ctx, requestWithoutContinuation(req),
		)
		if err != nil {
			return nil, err
		}
		requestMetrics.ContinuationFallbacks++
		requestMetrics.ContinuationFallbackStatus = statusCode
		requestMetrics.ContinuationFallbackError = fallbackError
		fallbackMetrics = requestMetrics.Add(fallbackMetrics)
		httpReq = fallbackReq
		requestMetrics = fallbackMetrics
		startedAt = time.Now()
		resp, err = httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("openai request: %w", err)
		}
		timeToHeaders = time.Since(startedAt)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()

		return nil, readErrorResponse(resp)
	}

	events := make(chan model.Event)
	metrics := streamMetrics{
		startedAt:      startedAt,
		timeToHeaders:  timeToHeaders,
		requestMetrics: requestMetrics,
	}
	if c.apiMode() == APIResponses {
		go streamResponsesAPI(ctx, resp.Body, events, metrics)
	} else {
		go streamChatCompletions(ctx, resp.Body, events, metrics)
	}

	return events, nil
}

// streamResponsesAutoFallback forwards WebSocket events and falls back to
// HTTP/SSE when auto transport gets a pre-payload WebSocket error.
func (c *Client) streamResponsesAutoFallback(ctx context.Context,
	req model.Request,
	websocketEvents <-chan model.Event) <-chan model.Event {

	events := make(chan model.Event)
	go func() {
		defer close(events)

		seenEvent := false
		for event := range websocketEvents {
			if event.Type == model.EventError && !seenEvent {
				recordWebSocketHTTPFallback(req.SessionID)
				c.forwardHTTPFallback(
					ctx, requestWithoutContinuation(req),
					events,
				)

				return
			}
			if !sendEvent(ctx, events, event) {
				return
			}
			seenEvent = true
		}
	}()

	return events
}

// forwardHTTPFallback streams a full-context HTTP retry into events.
func (c *Client) forwardHTTPFallback(ctx context.Context, req model.Request,
	events chan<- model.Event) {

	httpEvents, err := c.streamHTTP(ctx, req)
	if err != nil {
		sendEvent(ctx, events, model.Event{
			Type: model.EventError,
			Err:  fmt.Sprintf("fallback openai http: %v", err),
		})

		return
	}
	for event := range httpEvents {
		if !sendEvent(ctx, events, event) {
			return
		}
	}
}

// shouldRetryWithoutContinuation reports whether a failed continuation should
// be retried as a full-context request.
func shouldRetryWithoutContinuation(resp *http.Response,
	metrics model.Metrics) bool {

	return metrics.ContinuationRequests > 0 &&
		resp.StatusCode == http.StatusBadRequest
}

// readContinuationFallbackError closes resp and returns bounded diagnostic
// text for a rejected continuation request.
func readContinuationFallbackError(resp *http.Response) (int, string) {
	defer resp.Body.Close()

	body, err := io.ReadAll(
		io.LimitReader(
			resp.Body, continuationFallbackErrorBytes+1,
		),
	)
	if err != nil {
		return resp.StatusCode, fmt.Sprintf("%s: read error body: %v",
			resp.Status, err)
	}
	text := strings.TrimSpace(string(body))
	if len(body) > continuationFallbackErrorBytes {
		text, _ = textutil.TruncateUTF8Bytes(
			text, continuationFallbackErrorBytes,
		)
		text += "..."
	}
	if text == "" {
		return resp.StatusCode, resp.Status
	}

	return resp.StatusCode, resp.Status + ": " + text
}

// requestWithoutContinuation returns a full-context retry request.
func requestWithoutContinuation(req model.Request) model.Request {
	req.PreviousResponseID = ""
	req.DeltaMessages = nil

	return req
}

// newRequest builds the HTTP request for a streaming chat completion.
func (c *Client) newRequest(ctx context.Context, req model.Request) (
	*http.Request, model.Metrics, error) {

	if c.apiMode() == APIResponses {
		return c.newResponsesRequest(ctx, req)
	}
	messages := chatMessages(req.Messages)
	tools := chatTools(req.Tools)

	body, err := json.Marshal(chatRequest{
		Model:         c.Model,
		Stream:        true,
		StreamOptions: chatStreamOptions{IncludeUsage: true},
		Messages:      messages,
		Tools:         tools,
	})
	if err != nil {
		return nil, model.Metrics{},
			fmt.Errorf("marshal openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.endpoint(chatCompletionsPath),
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, model.Metrics{},
			fmt.Errorf("create openai request: %w", err)
	}
	c.addCommonHeaders(httpReq)

	return httpReq, model.Metrics{
		Transport:     TransportHTTP,
		Requests:      1,
		RequestBytes:  requestContentLength(httpReq),
		InputMessages: len(req.Messages),
		ToolCount:     len(req.Tools),
		InputBytes:    jsonPayloadSize(messages),
		ToolBytes:     jsonPayloadSize(tools),
	}, nil
}

// newResponsesRequest builds the HTTP request for a streaming response.
func (c *Client) newResponsesRequest(ctx context.Context, req model.Request) (
	*http.Request, model.Metrics, error) {

	body, metrics, err := c.responsesRequestBody(req, c.StoreResponses)
	if err != nil {
		return nil, model.Metrics{}, err
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.endpoint(responsesPath),
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, model.Metrics{},
			fmt.Errorf("create openai response request: %w", err)
	}
	c.addCommonHeaders(httpReq)
	c.addCodexResponsesHeaders(httpReq, req.SessionID)
	metrics.RequestBytes = requestContentLength(httpReq)

	return httpReq, metrics, nil
}

// responsesRequestBody builds the serialized Responses body and request
// metrics.
func (c *Client) responsesRequestBody(req model.Request,
	allowContinuation bool) ([]byte, model.Metrics, error) {

	inputMessages := req.Messages
	previousResponseID := ""
	continued := allowContinuation && req.PreviousResponseID != "" &&
		len(req.DeltaMessages) > 0
	if continued {
		inputMessages = req.DeltaMessages
		previousResponseID = req.PreviousResponseID
	}
	instructions := responseInstructions(req.Messages)
	input := responseInput(inputMessages)
	tools := responseTools(req.Tools)
	body, err := json.Marshal(responseRequest{
		Model:              c.Model,
		Stream:             true,
		Store:              c.StoreResponses,
		PromptCacheKey:     promptCacheKey(req.SessionID),
		PreviousResponseID: previousResponseID,
		Instructions:       instructions,
		Input:              input,
		Tools:              tools,
		Reasoning: responseReasoningConfig(
			c.ReasoningEffort, c.ReasoningSummary,
		),
		Include: responseIncludeConfig(
			c.ReasoningEffort, c.ReasoningSummary,
		),
	})
	if err != nil {
		return nil, model.Metrics{},
			fmt.Errorf("marshal openai response request: %w", err)
	}

	metrics := model.Metrics{
		Transport:        TransportHTTP,
		Requests:         1,
		RequestBytes:     len(body),
		InputMessages:    len(inputMessages),
		ToolCount:        len(req.Tools),
		InstructionBytes: len(instructions),
		InputBytes:       jsonPayloadSize(input),
		ToolBytes:        jsonPayloadSize(tools),
	}
	if continued {
		metrics.ContinuationRequests = 1
		metrics.DeltaMessages = len(req.DeltaMessages)
	}

	return body, metrics, nil
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

// validateTransport rejects unknown transport modes before making a request.
func (c *Client) validateTransport() error {
	switch c.transportMode() {
	case TransportHTTP, TransportWebSocket, TransportAuto:
		return nil

	default:
		return fmt.Errorf("unknown openai transport %q", c.Transport)
	}
}

// transportMode returns the configured provider transport.
func (c *Client) transportMode() string {
	switch c.Transport {
	case "", TransportHTTP:
		return TransportHTTP

	case TransportWebSocket:
		return TransportWebSocket

	case TransportAuto:
		return TransportAuto

	default:
		return c.Transport
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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.AccountID != "" {
		req.Header.Set("chatgpt-account-id", c.AccountID)
	}
}

// addCodexResponsesHeaders applies ChatGPT/Codex-specific Responses HTTP
// headers without affecting generic OpenAI-compatible providers.
func (c *Client) addCodexResponsesHeaders(req *http.Request, sessionID string) {
	if !c.codexBackend() {
		return
	}

	req.Header.Set("OpenAI-Beta", codexResponsesBetaHeader)
	req.Header.Set("originator", "harness")
	if sessionID == "" {
		return
	}
	req.Header.Set("session-id", sessionID)
	req.Header.Set("x-client-request-id", sessionID)
}

// codexBackend reports whether BaseURL points at the ChatGPT/Codex backend.
func (c *Client) codexBackend() bool {
	baseURL := strings.TrimRight(c.BaseURL, "/")

	return strings.Contains(baseURL, "chatgpt.com/backend-api/codex")
}

// promptCacheKey returns a provider-safe cache affinity key for a session.
func promptCacheKey(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	runes := []rune(sessionID)
	if len(runes) <= promptCacheKeyMaxRunes {
		return sessionID
	}

	return string(runes[:promptCacheKeyMaxRunes])
}

// requestContentLength returns the JSON body length when the request knows it.
func requestContentLength(req *http.Request) int {
	if req.ContentLength <= 0 {
		return 0
	}

	return int(req.ContentLength)
}

// jsonPayloadSize returns the serialized byte length of a request fragment.
func jsonPayloadSize(value any) int {
	body, err := json.Marshal(value)
	if err != nil {
		return 0
	}

	return len(body)
}

// streamMetrics tracks one HTTP/SSE model call while it is decoded.
type streamMetrics struct {
	// startedAt is the wall-clock time before the HTTP request began.
	startedAt time.Time

	// timeToHeaders is how long the provider took to return headers.
	timeToHeaders time.Duration

	// requestMetrics stores the serialized request shape measured before
	// the HTTP call began.
	requestMetrics model.Metrics

	// responseBytes is the raw SSE body byte count read so far.
	responseBytes int

	// timeToFirstEvent is set after the first meaningful SSE payload.
	timeToFirstEvent time.Duration
}

// addBytes records raw stream bytes as they are read from the response body.
func (m *streamMetrics) addBytes(count int) {
	m.responseBytes += count
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
		Metrics: m.requestMetrics.Add(model.Metrics{
			ResponseBytes:    m.responseBytes,
			TimeToHeaders:    m.timeToHeaders,
			TimeToFirstEvent: m.timeToFirstEvent,
		}),
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

	accumulator := newToolAccumulator()
	streamSSEPayloads(
		ctx, body, events, &metrics, "read openai stream",
		func(payload string) bool {
			if payload == "[DONE]" {
				for _, call := range accumulator.calls() {
					if !sendEvent(ctx, events, model.Event{
						Type:     model.EventToolCall,
						ToolCall: call,
					}) {
						return false
					}
				}
				sendMetricsAndDone(ctx, events, metrics)

				return false
			}

			chunk, err := decodeChunk([]byte(payload))
			if err != nil {
				sendEvent(ctx, events, model.Event{
					Type: model.EventError,
					Err:  err.Error(),
				})

				return false
			}
			for _, event := range chunk.Events {
				if !sendEvent(ctx, events, event) {
					return false
				}
			}
			for _, delta := range chunk.ToolDeltas {
				accumulator.add(delta)
			}

			return true
		},
	)
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

// streamSSEPayloads reads meaningful SSE payloads and dispatches them to
// handle until the stream ends, the context is cancelled, or handle stops.
func streamSSEPayloads(ctx context.Context, body io.ReadCloser,
	events chan<- model.Event, metrics *streamMetrics,
	readErrorPrefix string, handle func(payload string) bool) {

	defer body.Close()
	stopCloseOnCancel := closeOnCancel(ctx, body)
	defer stopCloseOnCancel()

	reader := newSSEPayloadReader(body, metrics)
	for {
		payload, ok, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			sendEvent(ctx, events, model.Event{
				Type: model.EventError,
				Err: fmt.
					Errorf("%s: %w", readErrorPrefix, err).
					Error(),
			})

			return
		}
		select {
		case <-ctx.Done():
			return

		default:
		}
		if !ok {
			continue
		}
		metrics.markEvent(payload)
		if !handle(payload) {
			return
		}
	}
}

// closeOnCancel closes body when ctx is cancelled to unblock a pending read.
func closeOnCancel(ctx context.Context, body io.Closer) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = body.Close()

		case <-done:
		}
	}()

	return func() {
		close(done)
	}
}

// ssePayloadReader reads Server-Sent Event frames without bufio.Scanner size
// limits and reports raw transport bytes to stream metrics.
type ssePayloadReader struct {
	// reader is the underlying HTTP response body.
	reader io.Reader

	// metrics receives byte counts as data is read from reader.
	metrics *streamMetrics

	// buffer stores bytes that have been read but not yet framed.
	buffer []byte

	// scratch is reused for response body reads.
	scratch []byte

	// pendingErr is returned after any already-read buffered frames.
	pendingErr error

	// reachedEOF reports that the reader has no more bytes to provide.
	reachedEOF bool
}

// newSSEPayloadReader creates a frame-oriented SSE reader for a stream body.
func newSSEPayloadReader(reader io.Reader,
	metrics *streamMetrics) *ssePayloadReader {

	return &ssePayloadReader{
		reader:  reader,
		metrics: metrics,
		scratch: make([]byte, sseReadBufferSize),
	}
}

// Next returns the next SSE data payload, skipping comments and empty frames.
func (r *ssePayloadReader) Next() (string, bool, error) {
	for {
		frame, ok, err := r.popFrame()
		if err != nil {
			return "", false, err
		}
		if ok {
			payload, hasPayload := sseFramePayload(frame)

			return payload, hasPayload, nil
		}
		if r.reachedEOF {
			return "", false, io.EOF
		}
		if r.pendingErr != nil {
			err := r.pendingErr
			r.pendingErr = nil
			if errors.Is(err, io.EOF) {
				r.reachedEOF = true

				continue
			}

			return "", false, err
		}

		count, err := r.reader.Read(r.scratch)
		if count > 0 {
			if r.metrics != nil {
				r.metrics.addBytes(count)
			}
			r.buffer = append(r.buffer, r.scratch[:count]...)
			if len(r.buffer) > sseMaxFrameBytes {
				if index, _ := sseFrameBoundary(
					r.buffer,
				); index < 0 {
					return "", false, fmt.Errorf("sse "+
						"frame exceeded %d bytes",
						sseMaxFrameBytes)
				}
			}
			if err != nil {
				r.pendingErr = err
			}

			continue
		}
		if errors.Is(err, io.EOF) {
			r.reachedEOF = true

			continue
		}
		if err != nil {
			return "", false, err
		}
	}
}

// popFrame removes one complete SSE frame or the final unterminated frame.
func (r *ssePayloadReader) popFrame() ([]byte, bool, error) {
	if index, width := sseFrameBoundary(r.buffer); index >= 0 {
		if index > sseMaxFrameBytes {
			return nil, false, fmt.Errorf("sse frame exceeded "+
				"%d bytes", sseMaxFrameBytes)
		}
		frame := r.buffer[:index]
		r.buffer = r.buffer[index+width:]

		return frame, true, nil
	}
	if r.reachedEOF && len(bytes.TrimSpace(r.buffer)) > 0 {
		if len(r.buffer) > sseMaxFrameBytes {
			return nil, false, fmt.Errorf("sse frame exceeded "+
				"%d bytes", sseMaxFrameBytes)
		}
		frame := r.buffer
		r.buffer = nil

		return frame, true, nil
	}

	return nil, false, nil
}

// sseFrameBoundary returns the earliest blank-line boundary in an SSE buffer.
func sseFrameBoundary(buffer []byte) (int, int) {
	lineFeed := bytes.Index(buffer, []byte("\n\n"))
	crlf := bytes.Index(buffer, []byte("\r\n\r\n"))
	switch {
	case lineFeed < 0:
		return crlf, 4

	case crlf < 0:
		return lineFeed, 2

	case lineFeed < crlf:
		return lineFeed, 2

	default:
		return crlf, 4
	}
}

// sseFramePayload extracts and joins data fields from one SSE frame.
func sseFramePayload(frame []byte) (string, bool) {
	lines := strings.Split(
		strings.ReplaceAll(
			string(frame),
			"\r\n", "\n",
		),
		"\n",
	)
	payloadLines := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ":") {
			continue
		}
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payloadLines = append(
			payloadLines,
			strings.TrimSpace(
				strings.TrimPrefix(trimmed, "data:"),
			),
		)
	}
	if len(payloadLines) == 0 {
		return "", false
	}

	return strings.TrimSpace(strings.Join(payloadLines, "\n")), true
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

	decoder := responseStreamDecoder{}
	streamSSEPayloads(
		ctx, body, events, &metrics, "read openai response stream",
		func(payload string) bool {
			if payload == "[DONE]" {
				sendMetricsAndDone(ctx, events, metrics)

				return false
			}

			for _, event := range decoder.decode([]byte(payload)) {
				if event.Type == model.EventDone {
					sendMetricsAndDone(ctx, events, metrics)

					return false
				}
				if !sendEvent(ctx, events, event) {
					return false
				}
			}

			return true
		},
	)
}

// responseStreamDecoder converts Responses API SSE with item lifecycle state.
type responseStreamDecoder struct {
	// activeItemType is the current Responses output item receiving deltas.
	activeItemType string

	// activeItemRole is the role for active message items when present.
	activeItemRole string

	// activeItemID is the identifier for the current Responses item.
	activeItemID string

	// activeReasoningSummary accumulates streamed summary text for the
	// active reasoning item so its replay item can be reconstructed.
	activeReasoningSummary strings.Builder

	// responseID is the last provider response identifier emitted.
	responseID string
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
	case "response.created":
		return d.responseInfoEvents(event.Response.ID)

	case "response.output_item.added":
		d.activeItemType = event.Item.Type
		d.activeItemRole = event.Item.Role
		d.activeItemID = event.Item.ID

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
		d.activeReasoningSummary.WriteString(event.Delta)

		return []model.Event{{Type: model.EventReasoningDelta,
			Text: event.Delta}}

	case "response.output_item.done":
		defer d.clearActiveItem()
		if event.Item.Type == "reasoning" &&
			event.Item.EncryptedContent != "" {
			return []model.Event{{
				Type: model.EventProviderItem,
				ProviderItem: model.ProviderItem{
					Provider: openaiProviderItemName,
					Type:     "reasoning",
					ID:       event.Item.ID,
					EncryptedContent: event.Item.
						EncryptedContent,
					Summary: d.reasoningSummary(event.Item),
				},
			}}
		}
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
		events := d.responseInfoEvents(event.Response.ID)
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

// responseInfoEvents returns a response identity event when it has changed.
func (d *responseStreamDecoder) responseInfoEvents(id string) []model.Event {
	if id == "" || id == d.responseID {
		return nil
	}
	d.responseID = id

	return []model.Event{{
		Type: model.EventResponseInfo,
		ResponseInfo: model.ResponseInfo{
			ProviderResponseID: id,
		},
	}}
}

// clearActiveItem forgets the Responses output item receiving deltas.
func (d *responseStreamDecoder) clearActiveItem() {
	d.activeItemType = ""
	d.activeItemRole = ""
	d.activeItemID = ""
	d.activeReasoningSummary.Reset()
}

// reasoningSummary returns the completed summary for a reasoning item.
func (d *responseStreamDecoder) reasoningSummary(
	item responseOutputItem) string {

	if summary := item.summaryText(); summary != "" {
		return summary
	}

	return d.activeReasoningSummary.String()
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
		for _, item := range message.ProviderItems {
			if item.Provider != openaiProviderItemName ||
				item.Type != "reasoning" ||
				item.EncryptedContent == "" {

				continue
			}
			out = append(out, responseInputItem{
				Type:             "reasoning",
				ID:               item.ID,
				EncryptedContent: item.EncryptedContent,
				Summary: responseReasoningSummaryItems(
					item.Summary,
				),
			})
		}
		if len(message.ProviderItems) > 0 &&
			message.Content == "" &&
			len(message.ToolCalls) == 0 {

			continue
		}
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

// responseIncludeConfig requests opaque provider items needed for safe replay.
func responseIncludeConfig(effort string, summary string) []string {
	if responseReasoningConfig(effort, summary) == nil {
		return nil
	}

	return []string{"reasoning.encrypted_content"}
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

	// PromptCacheKey asks OpenAI-compatible backends to reuse prompt cache
	// state for requests from the same local session.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`

	// PreviousResponseID asks Responses to continue from a prior response.
	PreviousResponseID string `json:"previous_response_id,omitempty"`

	// Instructions stores system-level guidance for the response.
	Instructions string `json:"instructions,omitempty"`

	// Input contains message and function-call history items.
	Input []responseInputItem `json:"input"`

	// Tools contains function tools available to the model.
	Tools []responseTool `json:"tools,omitempty"`

	// Reasoning configures reasoning effort and summary output.
	Reasoning *responseReasoning `json:"reasoning,omitempty"`

	// Include asks Responses to include opaque output fields needed for
	// replay.
	Include []string `json:"include,omitempty"`
}

// responseInputItem is one Responses API input item.
type responseInputItem struct {
	// Type identifies function call items. Empty means message input.
	Type string `json:"type,omitempty"`

	// ID is the provider item identifier when replaying opaque items.
	ID string `json:"id,omitempty"`

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

	// EncryptedContent stores opaque provider ciphertext for reasoning
	// replay.
	EncryptedContent string `json:"encrypted_content,omitempty"`

	// Summary stores provider reasoning summary blocks. Reasoning items
	// require this field during manual Responses API replay, even when it
	// is empty.
	Summary *[]responseReasoningSummaryItem `json:"summary,omitempty"`
}

// responseReasoning configures reasoning-capable model behavior.
type responseReasoning struct {
	// Effort constrains how much the model reasons.
	Effort string `json:"effort,omitempty"`

	// Summary requests a displayable reasoning summary.
	Summary string `json:"summary,omitempty"`
}

// responseReasoningSummaryItem is one Responses reasoning summary block.
type responseReasoningSummaryItem struct {
	// Type identifies the summary block kind.
	Type string `json:"type"`

	// Text stores displayable reasoning summary text.
	Text string `json:"text"`
}

// responseReasoningSummaryItems converts summary text into Responses blocks.
func responseReasoningSummaryItems(
	summary string) *[]responseReasoningSummaryItem {

	if strings.TrimSpace(summary) == "" {
		items := []responseReasoningSummaryItem{}

		return &items
	}

	items := []responseReasoningSummaryItem{{
		Type: "summary_text",
		Text: summary,
	}}

	return &items
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
	// ID is the provider output item identifier.
	ID string `json:"id"`

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

	// EncryptedContent stores opaque provider ciphertext for reasoning
	// replay.
	EncryptedContent string `json:"encrypted_content"`

	// Summary stores provider reasoning summary blocks.
	Summary []responseReasoningSummaryItem `json:"summary"`
}

// summaryText joins displayable reasoning summary blocks.
func (i responseOutputItem) summaryText() string {
	var out strings.Builder
	for _, item := range i.Summary {
		if item.Type == "summary_text" {
			out.WriteString(item.Text)
		}
	}

	return out.String()
}

// responseEventResponse stores failed or incomplete response state.
type responseEventResponse struct {
	// ID is the provider response identifier.
	ID string `json:"id"`

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
