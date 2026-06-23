package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"harness/internal/model"
)

// TestClientStreamsChatCompletions verifies request construction and SSE chunk
// conversion against a local OpenAI-compatible test server.
func TestClientStreamsChatCompletions(t *testing.T) {
	var gotAuth string
	var gotRequest chatRequest
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != chatCompletionsPath {
				t.Fatalf("unexpected path: %q", r.URL.Path)
			}
			gotAuth = r.Header.Get("Authorization")
			if err := json.NewDecoder(r.Body).Decode(
				&gotRequest,
			); err != nil {

				t.Fatal(err)
			}

			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(
				w, "data: "+
					"{\"choices\":[{\"delta\":{\"content\":\"he"+
					"l\"}}]}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"choices\":[{\"delta\":{\"content\":\"lo"+
					"\"}}]}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"choices\":[],\"usage\":{\"prompt_toke"+
					"ns\":10,\"completion_tokens\":3,\"total"+
					"_tokens\":13,\"prompt_tokens_details\""+
					":{\"cached_tokens\":6},\"completion_to"+
					"kens_details\":{\"reasoning_tokens\":1"+
					"}}}\n\n",
			)
			fmt.Fprint(w, "data: [DONE]\n\n")
		}),
	)
	defer server.Close()

	client := &Client{
		BaseURL: server.URL,
		APIKey:  "test-token",
		Model:   "test-model",
	}
	events, err := client.Stream(context.Background(), model.Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: "say hello",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := collectEvents(events)
	if gotAuth != "Bearer test-token" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
	}
	if gotRequest.Model != "test-model" {
		t.Fatalf("unexpected model: %q", gotRequest.Model)
	}
	if !gotRequest.Stream {
		t.Fatal("expected streaming request")
	}
	if !gotRequest.StreamOptions.IncludeUsage {
		t.Fatal("expected usage streaming option")
	}
	if len(gotRequest.Messages) != 1 ||
		gotRequest.Messages[0].Content != "say hello" {

		t.Fatalf("unexpected messages: %#v", gotRequest.Messages)
	}
	if len(got) != 5 {
		t.Fatalf("expected five events, got %#v", got)
	}
	if got[0].Text != "hel" || got[1].Text != "lo" {
		t.Fatalf("unexpected text events: %#v", got)
	}
	if got[2].Type != model.EventUsage ||
		got[2].Usage.InputTokens != 10 ||
		got[2].Usage.CachedInputTokens != 6 ||
		got[2].Usage.OutputTokens != 3 ||
		got[2].Usage.ReasoningOutputTokens != 1 ||
		got[2].Usage.TotalTokens != 13 {

		t.Fatalf("unexpected usage event: %#v", got[2])
	}
	if got[3].Type != model.EventMetrics ||
		got[3].Metrics.Requests != 1 ||
		got[3].Metrics.RequestBytes == 0 ||
		got[3].Metrics.ResponseBytes == 0 ||
		got[3].Metrics.InputMessages != 1 ||
		got[3].Metrics.TimeToHeaders == 0 ||
		got[3].Metrics.TimeToFirstEvent == 0 {

		t.Fatalf("unexpected metrics event: %#v", got[3])
	}
	if got[4].Type != model.EventDone {
		t.Fatalf("expected done event, got %#v", got[4])
	}
}

// TestSSEPayloadReaderHandlesFragmentedFrames verifies frame parsing survives
// arbitrary read boundaries and reports raw response bytes.
func TestSSEPayloadReaderHandlesFragmentedFrames(t *testing.T) {
	stream := "data: hello\n\n: ignored\n\ndata: [DONE]"
	metrics := streamMetrics{}
	reader := newSSEPayloadReader(&fragmentedReader{chunks: []string{
		"data: he",
		"llo\n",
		"\n: ign",
		"ored\n\n",
		"data: [DONE]",
	}}, &metrics)

	payload, ok, err := reader.Next()
	if err != nil || !ok || payload != "hello" {
		t.Fatalf("unexpected first payload: %q %v %v", payload, ok, err)
	}
	payload, ok, err = reader.Next()
	if err != nil || ok || payload != "" {
		t.Fatalf("unexpected comment frame: %q %v %v", payload, ok, err)
	}
	payload, ok, err = reader.Next()
	if err != nil || !ok || payload != "[DONE]" {
		t.Fatalf("unexpected final payload: %q %v %v", payload, ok, err)
	}
	_, _, err = reader.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
	if metrics.responseBytes != len(stream) {
		t.Fatalf("expected %d response bytes, got %d", len(stream),
			metrics.responseBytes)
	}
}

// TestSSEPayloadReaderJoinsMultilineData verifies spec-style multiline data
// frames are surfaced as one payload.
func TestSSEPayloadReaderJoinsMultilineData(t *testing.T) {
	metrics := streamMetrics{}
	reader := newSSEPayloadReader(
		strings.NewReader(
			"event: message\r\ndata: hello\r\ndata: world\r\n\r\n",
		),
		&metrics,
	)

	payload, ok, err := reader.Next()
	if err != nil || !ok {
		t.Fatalf("expected payload, got %q %v %v", payload, ok, err)
	}
	if payload != "hello\nworld" {
		t.Fatalf("unexpected payload: %q", payload)
	}
}

// TestSSEPayloadReaderRejectsOversizedFrame verifies malformed streams cannot
// grow the buffered frame without bound.
func TestSSEPayloadReaderRejectsOversizedFrame(t *testing.T) {
	reader := newSSEPayloadReader(
		strings.NewReader(
			"data: "+strings.Repeat("x", sseMaxFrameBytes),
		),
		nil,
	)

	_, _, err := reader.Next()
	if err == nil {
		t.Fatal("expected oversized frame error")
	}
	if !strings.Contains(err.Error(), "sse frame exceeded") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestStreamChatCompletionsClosesBodyOnCancel verifies context cancellation
// actively unblocks a pending response-body read.
func TestStreamChatCompletionsClosesBodyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := newBlockingReadCloser()
	events := make(chan model.Event)
	done := make(chan struct{})
	go func() {
		streamChatCompletions(ctx, body, events, streamMetrics{})
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-body.closed:
		<-done

	case <-time.After(2 * time.Second):
		t.Fatal("stream goroutine did not exit after cancellation")
	}
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("expected event channel to be closed")
		}

	default:
		t.Fatal("expected event channel to be closed")
	}
}

// TestClientStreamsFragmentedToolCall verifies that OpenAI tool-call deltas are
// accumulated into one complete neutral tool call.
func TestClientStreamsFragmentedToolCall(t *testing.T) {
	var gotRequest chatRequest
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(
				&gotRequest,
			); err != nil {

				t.Fatal(err)
			}

			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(
				w, "data: "+
					"{\"choices\":[{\"delta\":{\"tool_calls\":"+
					"[{\"index\":0,\"id\":\"call_1\",\"type\":\"f"+
					"unction\",\"function\":{\"name\":\"ls\",\"a"+
					"rguments\":\"{\\\"pa\"}}]}}]}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"choices\":[{\"delta\":{\"tool_calls\":"+
					"[{\"index\":0,\"function\":{\"arguments\""+
					":\"th\\\":\\\".\\\"}\"}}]}}]}\n\n",
			)
			fmt.Fprint(w, "data: [DONE]\n\n")
		}),
	)
	defer server.Close()

	client := &Client{
		BaseURL: server.URL,
		Model:   "test-model",
	}
	events, err := client.Stream(context.Background(), model.Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: "list files",
		}},
		Tools: []model.ToolSpec{{
			Name:        "ls",
			Description: "List a directory",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := collectEvents(events)
	if len(gotRequest.Tools) != 1 ||
		gotRequest.Tools[0].Function.Name != "ls" {

		t.Fatalf("request missing tool schema: %#v", gotRequest.Tools)
	}
	if len(got) != 3 {
		t.Fatalf("expected tool call, metrics, and done, got %#v", got)
	}
	if got[0].Type != model.EventToolCall {
		t.Fatalf("expected tool call event, got %#v", got[0])
	}
	if got[0].ToolCall.ID != "call_1" ||
		got[0].ToolCall.Name != "ls" ||
		got[0].ToolCall.Arguments != `{"path":"."}` {

		t.Fatalf("unexpected tool call: %#v", got[0].ToolCall)
	}
	if got[1].Type != model.EventMetrics ||
		got[1].Metrics.RequestBytes == 0 ||
		got[1].Metrics.ResponseBytes == 0 {

		t.Fatalf("unexpected metrics event: %#v", got[1])
	}
	if got[2].Type != model.EventDone {
		t.Fatalf("expected done event, got %#v", got[2])
	}
}

// TestClientStreamsChatReasoning verifies OpenAI-compatible chat deltas can
// surface displayable reasoning text when a provider sends it.
func TestClientStreamsChatReasoning(t *testing.T) {
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(
				w, "data: "+
					"{\"choices\":[{\"delta\":{\"reasoning_co"+
					"ntent\":\"checking\"}}]}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"choices\":[{\"delta\":{\"content\":\"hi"+
					"\"}}]}\n\n",
			)
			fmt.Fprint(w, "data: [DONE]\n\n")
		}),
	)
	defer server.Close()

	client := &Client{BaseURL: server.URL, Model: "test-model"}
	events, err := client.Stream(context.Background(), model.Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: "think",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := collectEvents(events)
	if len(got) != 4 {
		t.Fatalf("expected four events, got %#v", got)
	}
	if got[0].Type != model.EventReasoningDelta ||
		got[0].Text != "checking" {

		t.Fatalf("unexpected reasoning event: %#v", got[0])
	}
	if got[1].Type != model.EventTextDelta || got[1].Text != "hi" {
		t.Fatalf("unexpected text event: %#v", got[1])
	}
	if got[2].Type != model.EventMetrics {
		t.Fatalf("expected metrics event, got %#v", got[2])
	}
	if got[3].Type != model.EventDone {
		t.Fatalf("expected done event, got %#v", got[3])
	}
}

// TestClientStreamsResponsesAPI verifies Responses API reasoning summaries,
// text, and function calls are converted into neutral events.
func TestClientStreamsResponsesAPI(t *testing.T) {
	var gotPath string
	var gotBody string
	var gotRequest responseRequest
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			gotBody = string(body)
			if err := json.Unmarshal(
				body, &gotRequest,
			); err != nil {

				t.Fatal(err)
			}

			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.created\",\"respons"+
					"e\":{\"id\":\"resp_123\"}}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_item.added"+
					"\",\"item\":{\"type\":\"reasoning\",\"id\":\""+
					"rs_1\"}}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.reasoning_summary"+
					"_text.delta\",\"delta\":\"checking\"}"+
					"\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_item.done\""+
					",\"item\":{\"type\":\"reasoning\",\"id\":\"r"+
					"s_1\",\"encrypted_content\":\"opaque\",\""+
					"summary\":[{\"type\":\"summary_text\",\"t"+
					"ext\":\"checking\"}]}}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_item.added"+
					"\",\"item\":{\"type\":\"message\",\"id\":\"ms"+
					"g_1\",\"role\":\"assistant\"}}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_text.delta"+
					"\",\"delta\":\"hi\"}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_item.done\""+
					",\"item\":{\"type\":\"function_call\",\"ca"+
					"ll_id\":\"call_1\",\"name\":\"ls\",\"argume"+
					"nts\":\"{\\\"path\\\":\\\".\\\"}\"}}"+
					"\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.completed\",\"respo"+
					"nse\":{\"id\":\"resp_123\",\"usage\":{\"inp"+
					"ut_tokens\":20,\"output_tokens\":5,\"to"+
					"tal_tokens\":25,\"input_tokens_detail"+
					"s\":{\"cached_tokens\":12},\"output_tok"+
					"ens_details\":{\"reasoning_tokens\":2}"+
					"}}}\n\n",
			)
		}),
	)
	defer server.Close()

	client := &Client{
		BaseURL:          server.URL,
		Model:            "test-model",
		API:              APIResponses,
		ReasoningEffort:  "medium",
		ReasoningSummary: "auto",
	}
	events, err := client.Stream(context.Background(), model.Request{
		SessionID:          "session-responses",
		PreviousResponseID: "resp_previous",
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: "rules"},
			{Role: model.RoleUser, Content: "list files"},
		},
		DeltaMessages: []model.Message{
			{Role: model.RoleSystem, Content: "rules"},
			{Role: model.RoleUser, Content: "delta only"},
		},
		Tools: []model.ToolSpec{{
			Name:        "ls",
			Description: "List files",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := collectEvents(events)
	if gotPath != responsesPath {
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if gotRequest.Instructions != "rules" {
		t.Fatalf("unexpected instructions: %q", gotRequest.Instructions)
	}
	if gotRequest.Store {
		t.Fatal("expected response storage to be disabled")
	}
	if !strings.Contains(gotBody, `"store":false`) {
		t.Fatalf("request omitted explicit store false: %s", gotBody)
	}
	if gotRequest.PromptCacheKey != "session-responses" {
		t.Fatalf("unexpected prompt cache key: %q",
			gotRequest.PromptCacheKey)
	}
	if gotRequest.PreviousResponseID != "" {
		t.Fatalf("unexpected previous response id: %q",
			gotRequest.PreviousResponseID)
	}
	if len(gotRequest.Input) != 1 ||
		gotRequest.Input[0].Content != "list files" {

		t.Fatalf("request did not use full input: %#v",
			gotRequest.Input)
	}
	if gotRequest.Reasoning == nil ||
		gotRequest.Reasoning.Effort != "medium" ||
		gotRequest.Reasoning.Summary != "auto" {

		t.Fatalf("unexpected reasoning config: %#v",
			gotRequest.Reasoning)
	}
	if len(gotRequest.Include) != 1 ||
		gotRequest.Include[0] != "reasoning.encrypted_content" {

		t.Fatalf("unexpected include config: %#v", gotRequest.Include)
	}
	if len(got) != 8 {
		t.Fatalf("expected eight events, got %#v", got)
	}
	if got[0].Type != model.EventResponseInfo ||
		got[0].ResponseInfo.ProviderResponseID != "resp_123" {

		t.Fatalf("unexpected response info event: %#v", got[0])
	}
	if got[1].Type != model.EventReasoningDelta ||
		got[1].Text != "checking" {

		t.Fatalf("unexpected reasoning event: %#v", got[1])
	}
	if got[2].Type != model.EventProviderItem ||
		got[2].ProviderItem.Provider != "openai" ||
		got[2].ProviderItem.Type != "reasoning" ||
		got[2].ProviderItem.ID != "rs_1" ||
		got[2].ProviderItem.EncryptedContent != "opaque" ||
		got[2].ProviderItem.Summary != "checking" {

		t.Fatalf("unexpected provider item event: %#v", got[2])
	}
	if got[3].Type != model.EventTextDelta || got[3].Text != "hi" {
		t.Fatalf("unexpected text event: %#v", got[2])
	}
	if got[4].Type != model.EventToolCall ||
		got[4].ToolCall.ID != "call_1" ||
		got[4].ToolCall.Name != "ls" {

		t.Fatalf("unexpected tool call: %#v", got[4])
	}
	if got[5].Type != model.EventUsage ||
		got[5].Usage.InputTokens != 20 ||
		got[5].Usage.CachedInputTokens != 12 ||
		got[5].Usage.OutputTokens != 5 ||
		got[5].Usage.ReasoningOutputTokens != 2 ||
		got[5].Usage.TotalTokens != 25 {

		t.Fatalf("unexpected usage event: %#v", got[5])
	}
	if got[6].Type != model.EventMetrics ||
		got[6].Metrics.Requests != 1 ||
		got[6].Metrics.ContinuationRequests != 0 ||
		got[6].Metrics.RequestBytes == 0 ||
		got[6].Metrics.ResponseBytes == 0 ||
		got[6].Metrics.InputMessages != 2 ||
		got[6].Metrics.DeltaMessages != 0 ||
		got[6].Metrics.ToolCount != 1 ||
		got[6].Metrics.InstructionBytes != len("rules") ||
		got[6].Metrics.InputBytes == 0 ||
		got[6].Metrics.ToolBytes == 0 {

		t.Fatalf("unexpected metrics event: %#v", got[6])
	}
	if got[7].Type != model.EventDone {
		t.Fatalf("unexpected done event: %#v", got[7])
	}
}

// TestClientStreamsStoredResponsesContinuation verifies explicit stored
// Responses mode can send only the continuation delta.
func TestClientStreamsStoredResponsesContinuation(t *testing.T) {
	var gotRequest responseRequest
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(
				&gotRequest,
			); err != nil {

				t.Fatal(err)
			}

			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_item.added"+
					"\",\"item\":{\"type\":\"message\",\"role\":\""+
					"assistant\"}}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_text.delta"+
					"\",\"delta\":\"ok\"}\n\n",
			)
			fmt.Fprint(w, "data: [DONE]\n\n")
		}),
	)
	defer server.Close()

	client := &Client{
		BaseURL:        server.URL,
		Model:          "test-model",
		API:            APIResponses,
		StoreResponses: true,
	}
	events, err := client.Stream(context.Background(), model.Request{
		PreviousResponseID: "resp_previous",
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "full"},
			{Role: model.RoleUser, Content: "current"},
		},
		DeltaMessages: []model.Message{
			{Role: model.RoleUser, Content: "current"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := collectEvents(events)
	if gotRequest.PreviousResponseID != "resp_previous" ||
		len(gotRequest.Input) != 1 ||
		gotRequest.Input[0].Content != "current" ||
		!gotRequest.Store {

		t.Fatalf("unexpected stored continuation request: %#v",
			gotRequest)
	}
	if got[1].Type != model.EventMetrics ||
		got[1].Metrics.ContinuationRequests != 1 ||
		got[1].Metrics.DeltaMessages != 1 {

		t.Fatalf("unexpected continuation metrics: %#v", got[1])
	}
}

// TestPromptCacheKeyClampsRunes verifies cache affinity keys fit OpenAI's
// documented length limit without splitting multibyte characters.
func TestPromptCacheKeyClampsRunes(t *testing.T) {
	long := strings.Repeat("a", promptCacheKeyMaxRunes) + "éé"
	want := strings.Repeat("a", promptCacheKeyMaxRunes)
	if got := promptCacheKey(long); got != want {
		t.Fatalf("unexpected clamped key: %q", got)
	}
	if got := promptCacheKey("session"); got != "session" {
		t.Fatalf("unexpected short key: %q", got)
	}
	if got := promptCacheKey(""); got != "" {
		t.Fatalf("unexpected empty key: %q", got)
	}
}

// TestResponseInputReplaysOpenAIProviderItems verifies encrypted reasoning is
// replayed as a provider-native Responses item instead of plain text.
func TestResponseInputReplaysOpenAIProviderItems(t *testing.T) {
	input := responseInput([]model.Message{{
		ProviderItems: []model.ProviderItem{{
			Provider:         "openai",
			Type:             "reasoning",
			ID:               "rs_1",
			EncryptedContent: "opaque",
			Summary:          "checking",
		}},
	}})

	if len(input) != 1 ||
		input[0].Type != "reasoning" ||
		input[0].ID != "rs_1" ||
		input[0].EncryptedContent != "opaque" ||
		input[0].Summary == nil ||
		len(*input[0].Summary) != 1 ||
		(*input[0].Summary)[0].Text != "checking" {

		t.Fatalf("unexpected replay item: %#v", input)
	}
}

// TestResponseInputReplaysOldReasoningItems verifies old session logs still
// include the required Responses summary field during full-context replay.
func TestResponseInputReplaysOldReasoningItems(t *testing.T) {
	input := responseInput([]model.Message{{
		ProviderItems: []model.ProviderItem{{
			Provider:         "openai",
			Type:             "reasoning",
			ID:               "rs_1",
			EncryptedContent: "opaque",
		}},
	}})
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	if len(input) != 1 ||
		input[0].Type != "reasoning" ||
		input[0].EncryptedContent != "opaque" ||
		input[0].Summary == nil ||
		len(*input[0].Summary) != 0 {

		t.Fatalf("unexpected old replay item: %#v", input)
	}
	if !strings.Contains(string(body), `"summary":[]`) {
		t.Fatalf("old replay item omitted summary: %s", body)
	}
}

// TestResponseInputIgnoresForeignProviderItems verifies opaque state from
// unknown providers does not leak into OpenAI requests.
func TestResponseInputIgnoresForeignProviderItems(t *testing.T) {
	input := responseInput([]model.Message{{
		ProviderItems: []model.ProviderItem{{
			Provider:         "other",
			Type:             "reasoning",
			EncryptedContent: "opaque",
		}},
	}})

	if len(input) != 0 {
		t.Fatalf("foreign provider item leaked: %#v", input)
	}
}

// TestClientRetriesContinuationAsFullRequest verifies experimental
// previous_response_id failures fall back to a full Responses request.
func TestClientRetriesContinuationAsFullRequest(t *testing.T) {
	var requests []responseRequest
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var got responseRequest
			if err := json.NewDecoder(r.Body).Decode(
				&got,
			); err != nil {

				t.Fatal(err)
			}
			requests = append(requests, got)
			if len(requests) == 1 {
				http.Error(
					w, "bad previous response",
					http.StatusBadRequest,
				)

				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_item.added"+
					"\",\"item\":{\"type\":\"message\",\"role\":\""+
					"assistant\"}}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_text.delta"+
					"\",\"delta\":\"ok\"}\n\n",
			)
			fmt.Fprint(w, "data: [DONE]\n\n")
		}),
	)
	defer server.Close()

	client := &Client{
		BaseURL:        server.URL,
		Model:          "test-model",
		API:            APIResponses,
		StoreResponses: true,
	}
	events, err := client.Stream(context.Background(), model.Request{
		PreviousResponseID: "resp_previous",
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "full"},
			{Role: model.RoleUser, Content: "current"},
		},
		DeltaMessages: []model.Message{
			{Role: model.RoleUser, Content: "current"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := collectEvents(events)
	if len(got) != 3 || got[0].Text != "ok" {
		t.Fatalf("unexpected fallback stream: %#v", got)
	}
	if got[1].Type != model.EventMetrics ||
		got[1].Metrics.Requests != 2 ||
		got[1].Metrics.ContinuationRequests != 1 ||
		got[1].Metrics.ContinuationFallbacks != 1 ||
		got[1].Metrics.ContinuationFallbackStatus !=
			http.StatusBadRequest ||
		!strings.Contains(
			got[1].Metrics.ContinuationFallbackError,
			"bad previous response",
		) ||
		got[1].Metrics.RequestBytes == 0 {

		t.Fatalf("unexpected fallback metrics: %#v", got[1])
	}
	if len(requests) != 2 {
		t.Fatalf("expected retry, got %#v", requests)
	}
	if requests[0].PreviousResponseID != "resp_previous" ||
		len(requests[0].Input) != 1 ||
		requests[0].Input[0].Content != "current" {

		t.Fatalf("first request was not continuation: %#v", requests[0])
	}
	if requests[1].PreviousResponseID != "" ||
		len(requests[1].Input) != 2 ||
		requests[1].Input[0].Content != "full" ||
		requests[1].Input[1].Content != "current" {

		t.Fatalf("fallback was not full request: %#v", requests[1])
	}
}

// TestResponseStreamDecoderIgnoresOrphanTextDeltas verifies Responses text
// deltas are only forwarded while an assistant message item is active.
func TestResponseStreamDecoderIgnoresOrphanTextDeltas(t *testing.T) {
	decoder := responseStreamDecoder{}

	orphan := decoder.decode([]byte(
		`{"type":"response.output_text.delta","delta":"."}`,
	))
	if len(orphan) != 0 {
		t.Fatalf("orphan text delta should be ignored: %#v", orphan)
	}

	decoder.decode([]byte(
		`{"type":"response.output_item.added","item":{"type":"reasoning"}}`,
	))
	reasoningText := decoder.decode([]byte(
		`{"type":"response.output_text.delta","delta":"."}`,
	))
	if len(reasoningText) != 0 {
		t.Fatalf("reasoning item text delta should be ignored: %#v",
			reasoningText)
	}

	decoder.decode([]byte(
		`{"type":"response.output_item.added","item":{"type":"message","role":"assistant"}}`,
	))
	assistantText := decoder.decode([]byte(
		`{"type":"response.output_text.delta","delta":"hello"}`,
	))
	if len(assistantText) != 1 ||
		assistantText[0].Type != model.EventTextDelta ||
		assistantText[0].Text != "hello" {

		t.Fatalf("assistant text delta was not forwarded: %#v",
			assistantText)
	}
}

// TestClientReturnsHTTPError verifies that non-2xx responses fail before a
// stream is returned.
func TestClientReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad key", http.StatusUnauthorized)
		}),
	)
	defer server.Close()

	client := &Client{
		BaseURL: server.URL,
		Model:   "test-model",
	}
	_, err := client.Stream(
		context.Background(), model.Request{
			Messages: []model.Message{
				{Role: model.RoleUser, Content: "hello"},
			},
		},
	)
	if err == nil {
		t.Fatal("expected http error")
	}
}

// TestClientRejectsUnknownAPI verifies invalid API shape configuration fails
// before any HTTP request is attempted.
func TestClientRejectsUnknownAPI(t *testing.T) {
	client := &Client{
		Model: "test-model",
		API:   "mystery",
	}
	_, err := client.Stream(context.Background(), model.Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: "hello",
		}},
	})
	if err == nil {
		t.Fatal("expected unknown API error")
	}
	if !strings.Contains(err.Error(), `unknown openai api "mystery"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestClientReportsMalformedStream verifies that malformed SSE JSON is surfaced
// through the model stream as an error event.
func TestClientReportsMalformedStream(t *testing.T) {
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: not-json\n\n")
		}),
	)
	defer server.Close()

	client := &Client{
		BaseURL: server.URL,
		Model:   "test-model",
	}
	events, err := client.Stream(
		context.Background(), model.Request{
			Messages: []model.Message{
				{Role: model.RoleUser, Content: "hello"},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	got := collectEvents(events)
	if len(got) != 1 {
		t.Fatalf("expected one event, got %#v", got)
	}
	if got[0].Type != model.EventError || got[0].Err == "" {
		t.Fatalf("expected error event, got %#v", got[0])
	}
}

// collectEvents drains a model event stream for assertions.
func collectEvents(events <-chan model.Event) []model.Event {
	var got []model.Event
	for event := range events {
		got = append(got, event)
	}

	return got
}

// fragmentedReader returns fixed string chunks to exercise stream buffering.
type fragmentedReader struct {
	// chunks are returned in order as separate reads.
	chunks []string
}

// Read copies the next configured chunk into p.
func (r *fragmentedReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	if len(chunk) > len(p) {
		count := copy(p, chunk)
		r.chunks[0] = chunk[count:]

		return count, nil
	}
	r.chunks = r.chunks[1:]

	return copy(p, chunk), nil
}

// blockingReadCloser blocks reads until Close is called.
type blockingReadCloser struct {
	// closed is closed exactly once when Close is called.
	closed chan struct{}
}

// newBlockingReadCloser creates a response body that waits for cancellation.
func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{
		closed: make(chan struct{}),
	}
}

// Read blocks until Close is called.
func (b *blockingReadCloser) Read(_ []byte) (int, error) {
	<-b.closed

	return 0, io.ErrClosedPipe
}

// Close unblocks any pending Read call.
func (b *blockingReadCloser) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}

	return nil
}
