package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	if len(gotRequest.Messages) != 1 ||
		gotRequest.Messages[0].Content != "say hello" {

		t.Fatalf("unexpected messages: %#v", gotRequest.Messages)
	}
	if len(got) != 3 {
		t.Fatalf("expected three events, got %#v", got)
	}
	if got[0].Text != "hel" || got[1].Text != "lo" {
		t.Fatalf("unexpected text events: %#v", got)
	}
	if got[2].Type != model.EventDone {
		t.Fatalf("expected done event, got %#v", got[2])
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
	if len(got) != 2 {
		t.Fatalf("expected tool call and done, got %#v", got)
	}
	if got[0].Type != model.EventToolCall {
		t.Fatalf("expected tool call event, got %#v", got[0])
	}
	if got[0].ToolCall.ID != "call_1" ||
		got[0].ToolCall.Name != "ls" ||
		got[0].ToolCall.Arguments != `{"path":"."}` {

		t.Fatalf("unexpected tool call: %#v", got[0].ToolCall)
	}
	if got[1].Type != model.EventDone {
		t.Fatalf("expected done event, got %#v", got[1])
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
