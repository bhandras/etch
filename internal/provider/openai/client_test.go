package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		got[3].Metrics.RequestBytes == 0 ||
		got[3].Metrics.ResponseBytes == 0 ||
		got[3].Metrics.TimeToHeaders == 0 ||
		got[3].Metrics.TimeToFirstEvent == 0 {

		t.Fatalf("unexpected metrics event: %#v", got[3])
	}
	if got[4].Type != model.EventDone {
		t.Fatalf("expected done event, got %#v", got[4])
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
					"nse\":{\"usage\":{\"input_tokens\":20,\"o"+
					"utput_tokens\":5,\"total_tokens\":25,\""+
					"input_tokens_details\":{\"cached_toke"+
					"ns\":12},\"output_tokens_details\":{\"r"+
					"easoning_tokens\":2}}}}\n\n",
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
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: "rules"},
			{Role: model.RoleUser, Content: "list files"},
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
	if gotRequest.Reasoning == nil ||
		gotRequest.Reasoning.Effort != "medium" ||
		gotRequest.Reasoning.Summary != "auto" {

		t.Fatalf("unexpected reasoning config: %#v",
			gotRequest.Reasoning)
	}
	if len(got) != 6 {
		t.Fatalf("expected six events, got %#v", got)
	}
	if got[0].Type != model.EventReasoningDelta ||
		got[0].Text != "checking" {

		t.Fatalf("unexpected reasoning event: %#v", got[0])
	}
	if got[1].Type != model.EventTextDelta || got[1].Text != "hi" {
		t.Fatalf("unexpected text event: %#v", got[1])
	}
	if got[2].Type != model.EventToolCall ||
		got[2].ToolCall.ID != "call_1" ||
		got[2].ToolCall.Name != "ls" {

		t.Fatalf("unexpected tool call: %#v", got[2])
	}
	if got[3].Type != model.EventUsage ||
		got[3].Usage.InputTokens != 20 ||
		got[3].Usage.CachedInputTokens != 12 ||
		got[3].Usage.OutputTokens != 5 ||
		got[3].Usage.ReasoningOutputTokens != 2 ||
		got[3].Usage.TotalTokens != 25 {

		t.Fatalf("unexpected usage event: %#v", got[3])
	}
	if got[4].Type != model.EventMetrics ||
		got[4].Metrics.RequestBytes == 0 ||
		got[4].Metrics.ResponseBytes == 0 {

		t.Fatalf("unexpected metrics event: %#v", got[4])
	}
	if got[5].Type != model.EventDone {
		t.Fatalf("unexpected done event: %#v", got[5])
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
