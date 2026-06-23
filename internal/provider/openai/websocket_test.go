package openai

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"harness/internal/model"
)

// TestClientStreamsResponsesWebSocketReusesConnection verifies cached
// WebSocket sessions can send delta continuation requests.
func TestClientStreamsResponsesWebSocketReusesConnection(t *testing.T) {
	var mu sync.Mutex
	var got []responseCreateRequest
	connections := 0
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("OpenAI-Beta") != websocketBetaHeader {
				t.Errorf("missing websocket beta header: %q",
					r.Header.Get("OpenAI-Beta"))
			}
			conn, rw, err := testAcceptWebSocket(w, r)
			if err != nil {
				t.Error(err)

				return
			}
			defer conn.Close()
			mu.Lock()
			connections++
			mu.Unlock()
			for index := 0; index < 2; index++ {
				payload, err := testReadClientText(rw.Reader)
				if err != nil {
					t.Error(err)

					return
				}
				var request responseCreateRequest
				if err := json.Unmarshal(
					payload, &request,
				); err != nil {

					t.Error(err)

					return
				}
				mu.Lock()
				got = append(got, request)
				mu.Unlock()
				responseID := fmt.Sprintf("resp_%d", index+1)
				testWriteServerText(t, conn,
					`{"type":"response.created","response":{"id":"`+
						responseID+`"}}`)
				testWriteServerText(t, conn,
					`{"type":"response.output_item.added","item":`+
						`{"type":"message","role":"assistant"}}`)
				testWriteServerText(
					t, conn,
					`{"type":"response.output_text.delta","delta":"ok"}`,
				)
				testWriteServerText(t, conn,
					`{"type":"response.completed","response":{"id":"`+
						responseID+`"}}`)
			}
		},
	))
	defer server.Close()

	client := &Client{
		BaseURL:   server.URL,
		APIKey:    "token",
		Model:     "test-model",
		API:       APIResponses,
		Transport: TransportWebSocket,
	}
	first, err := client.Stream(contextBackground(), model.Request{
		SessionID: "session-1",
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: "full",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstEvents := collectEvents(first)
	if len(firstEvents) == 0 {
		t.Fatalf("first stream returned no events")
	}
	second, err := client.Stream(contextBackground(), model.Request{
		SessionID:          "session-1",
		PreviousResponseID: "resp_1",
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "full"},
			{Role: model.RoleUser, Content: "next"},
		},
		DeltaMessages: []model.Message{{
			Role:    model.RoleUser,
			Content: "next",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondEvents := collectEvents(second)
	if len(secondEvents) == 0 {
		t.Fatalf("second stream returned no events")
	}
	if firstEvents[len(firstEvents)-2].Type != model.EventMetrics ||
		firstEvents[len(firstEvents)-2].Metrics.Transport !=
			TransportWebSocket ||
		firstEvents[len(firstEvents)-2].Metrics.WebSocketConnections !=
			1 ||
		firstEvents[len(firstEvents)-2].Metrics.WebSocketReuses != 0 {

		t.Fatalf("unexpected first websocket metrics: %#v", firstEvents)
	}
	if secondEvents[len(secondEvents)-2].Type != model.EventMetrics ||
		secondEvents[len(secondEvents)-2].Metrics.Transport !=
			TransportWebSocket ||
		secondEvents[len(secondEvents)-
			2].Metrics.WebSocketConnections !=
			0 ||
		secondEvents[len(secondEvents)-2].Metrics.WebSocketReuses !=
			1 ||
		secondEvents[len(secondEvents)-
			2].Metrics.ContinuationRequests !=
			1 {

		t.Fatalf("unexpected second websocket metrics: %#v",
			secondEvents)
	}

	mu.Lock()
	defer mu.Unlock()
	if connections != 1 {
		t.Fatalf("expected one reused websocket, got %d", connections)
	}
	if len(got) != 2 {
		t.Fatalf("expected two requests, got %#v", got)
	}
	if got[0].PreviousResponseID != "" || len(got[0].Input) != 1 ||
		got[0].Input[0].Content != "full" {

		t.Fatalf("unexpected first request: %#v", got[0])
	}
	if got[1].PreviousResponseID != "resp_1" ||
		len(got[1].Input) != 1 ||
		got[1].Input[0].Content != "next" {

		t.Fatalf("unexpected delta request: %#v", got[1])
	}
}

// TestClientStreamsResponsesWebSocketRetriesStaleReuse verifies an idle cached
// socket that closes before any response event is retried on a fresh socket.
func TestClientStreamsResponsesWebSocketRetriesStaleReuse(t *testing.T) {
	var mu sync.Mutex
	var got []responseCreateRequest
	connections := 0
	server := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				conn, rw, err := testAcceptWebSocket(w, r)
				if err != nil {
					t.Error(err)

					return
				}
				defer conn.Close()
				mu.Lock()
				connections++
				connIndex := connections
				mu.Unlock()

				switch connIndex {
				case 1:
					payload, err := testReadClientText(
						rw.Reader,
					)
					if err != nil {
						t.Error(err)

						return
					}
					testAppendResponseCreateRequest(
						t, &mu, &got, payload,
					)
					testWriteResponsesWebSocketText(
						t, conn, "resp_1", "ok",
					)

					payload, err = testReadClientText(
						rw.Reader,
					)
					if err != nil {
						t.Error(err)

						return
					}
					testAppendResponseCreateRequest(
						t, &mu, &got, payload,
					)

					return

				case 2:
					payload, err := testReadClientText(
						rw.Reader,
					)
					if err != nil {
						t.Error(err)

						return
					}
					testAppendResponseCreateRequest(
						t, &mu, &got, payload,
					)
					testWriteResponsesWebSocketText(
						t, conn, "resp_2", "retry",
					)

				default:
					t.Errorf("unexpected websocket "+
						"connection %d", connIndex)
				}
			},
		),
	)
	defer server.Close()

	client := &Client{
		BaseURL:   server.URL,
		APIKey:    "token",
		Model:     "test-model",
		API:       APIResponses,
		Transport: TransportWebSocket,
	}
	first, err := client.Stream(contextBackground(), model.Request{
		SessionID: "stale-session",
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: "full",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstEvents := collectEvents(first)
	if len(firstEvents) == 0 {
		t.Fatalf("first stream returned no events")
	}
	second, err := client.Stream(contextBackground(), model.Request{
		SessionID:          "stale-session",
		PreviousResponseID: "resp_1",
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "full"},
			{Role: model.RoleUser, Content: "next"},
		},
		DeltaMessages: []model.Message{{
			Role:    model.RoleUser,
			Content: "next",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondEvents := collectEvents(second)
	var text string
	var metrics model.Metrics
	for _, event := range secondEvents {
		if event.Type == model.EventError {
			t.Fatalf("unexpected stream error: %s", event.Err)
		}
		if event.Type == model.EventTextDelta {
			text += event.Text
		}
		if event.Type == model.EventMetrics {
			metrics = event.Metrics
		}
	}
	if text != "retry" {
		t.Fatalf("unexpected retried text: %q", text)
	}
	if metrics.Requests != 2 || metrics.WebSocketReuses != 1 ||
		metrics.WebSocketConnections != 1 ||
		metrics.ContinuationRequests != 1 {

		t.Fatalf("unexpected retry metrics: %#v", metrics)
	}

	mu.Lock()
	defer mu.Unlock()
	if connections != 2 {
		t.Fatalf("expected two websocket connections, got %d",
			connections)
	}
	if len(got) != 3 {
		t.Fatalf("expected three websocket requests, got %#v", got)
	}
	if got[1].PreviousResponseID != "resp_1" ||
		len(got[1].Input) != 1 ||
		got[1].Input[0].Content != "next" {

		t.Fatalf("unexpected stale reuse request: %#v", got[1])
	}
	if got[2].PreviousResponseID != "" ||
		len(got[2].Input) != 2 {

		t.Fatalf("unexpected retry request: %#v", got[2])
	}
}

// TestClientStreamsResponsesAutoFallsBackAfterWebSocketEOF verifies auto
// transport retries over HTTP when a fresh WebSocket closes before any event.
func TestClientStreamsResponsesAutoFallsBackAfterWebSocketEOF(t *testing.T) {
	var mu sync.Mutex
	var websocketRequests int
	var httpRequests []responseRequest
	server := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				if strings.EqualFold(
					r.Header.Get("Upgrade"),
					"websocket",
				) {

					conn, rw, err := testAcceptWebSocket(
						w, r,
					)
					if err != nil {
						t.Error(err)

						return
					}
					defer conn.Close()
					if _, err := testReadClientText(
						rw.Reader,
					); err != nil {

						t.Error(err)

						return
					}
					mu.Lock()
					websocketRequests++
					mu.Unlock()

					return
				}

				var request responseRequest
				if err := json.NewDecoder(r.Body).Decode(
					&request,
				); err != nil {

					t.Error(err)

					return
				}
				mu.Lock()
				httpRequests = append(httpRequests, request)
				mu.Unlock()
				w.Header().Set(
					"Content-Type", "text/event-stream",
				)
				fmt.Fprint(
					w, "data: "+
						"{\"type\":\"response.output_it"+
						"em.added\",\"item\":{\"type\":\"m"+
						"essage\",\"role\":\"assistant\"}"+
						"}\n\n",
				)
				fmt.Fprint(
					w, "data: "+
						"{\"type\":\"response.output_te"+
						"xt.delta\",\"delta\":\"http\"}"+
						"\n\n",
				)
				fmt.Fprint(w, "data: [DONE]\n\n")
			},
		),
	)
	defer server.Close()

	client := &Client{
		BaseURL:   server.URL,
		APIKey:    "token",
		Model:     "test-model",
		API:       APIResponses,
		Transport: TransportAuto,
	}
	events, err := client.Stream(contextBackground(), model.Request{
		SessionID: "auto-fallback-session",
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "full"},
			{Role: model.RoleUser, Content: "next"},
		},
		PreviousResponseID: "resp_previous",
		DeltaMessages: []model.Message{
			{Role: model.RoleUser, Content: "next"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := collectEvents(events)
	var text string
	for _, event := range got {
		if event.Type == model.EventError {
			t.Fatalf("unexpected fallback error: %s", event.Err)
		}
		if event.Type == model.EventTextDelta {
			text += event.Text
		}
	}
	if text != "http" {
		t.Fatalf("unexpected text: %q events=%#v", text, got)
	}

	mu.Lock()
	if websocketRequests != 1 {
		mu.Unlock()
		t.Fatalf("expected one websocket request, got %d",
			websocketRequests)
	}
	if len(httpRequests) != 1 {
		mu.Unlock()
		t.Fatalf("expected one http fallback request, got %#v",
			httpRequests)
	}
	if httpRequests[0].PreviousResponseID != "" ||
		len(httpRequests[0].Input) != 2 {

		mu.Unlock()
		t.Fatalf("fallback should send full context: %#v",
			httpRequests[0])
	}
	mu.Unlock()

	secondEvents, err := client.Stream(contextBackground(), model.Request{
		SessionID: "auto-fallback-session",
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "again"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got = collectEvents(secondEvents)
	text = ""
	for _, event := range got {
		if event.Type == model.EventError {
			t.Fatalf("unexpected second fallback error: %s",
				event.Err)
		}
		if event.Type == model.EventTextDelta {
			text += event.Text
		}
	}
	if text != "http" {
		t.Fatalf("unexpected second text: %q events=%#v", text, got)
	}
	mu.Lock()
	defer mu.Unlock()
	if websocketRequests != 1 {
		t.Fatalf("expected fallback session to skip websocket, got %d",
			websocketRequests)
	}
	if len(httpRequests) != 2 {
		t.Fatalf("expected two http requests, got %#v", httpRequests)
	}
}

// testAppendResponseCreateRequest records one decoded WebSocket create
// request under the shared test mutex.
func testAppendResponseCreateRequest(t *testing.T, mu *sync.Mutex,
	got *[]responseCreateRequest, payload []byte) {

	t.Helper()
	var request responseCreateRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		t.Error(err)

		return
	}
	mu.Lock()
	defer mu.Unlock()
	*got = append(*got, request)
}

// testWriteResponsesWebSocketText writes a minimal successful Responses stream.
func testWriteResponsesWebSocketText(t *testing.T, conn net.Conn,
	responseID string, text string) {

	t.Helper()
	testWriteServerText(t, conn,
		`{"type":"response.created","response":{"id":"`+
			responseID+`"}}`)
	testWriteServerText(t, conn,
		`{"type":"response.output_item.added","item":`+
			`{"type":"message","role":"assistant"}}`)
	testWriteServerText(
		t, conn,
		`{"type":"response.output_text.delta","delta":"`+text+`"}`,
	)
	testWriteServerText(t, conn,
		`{"type":"response.completed","response":{"id":"`+
			responseID+`"}}`)
}

// contextBackground returns the base context for WebSocket tests.
func contextBackground() context.Context {
	return context.Background()
}

// testAcceptWebSocket upgrades one httptest request to a raw WebSocket.
func testAcceptWebSocket(w http.ResponseWriter, r *http.Request) (net.Conn,
	*bufio.ReadWriter, error) {

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer cannot hijack")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, nil, fmt.Errorf("missing websocket key")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\n")
	fmt.Fprintf(rw, "Upgrade: websocket\r\n")
	fmt.Fprintf(rw, "Connection: Upgrade\r\n")
	fmt.Fprintf(
		rw, "Sec-WebSocket-Accept: %s\r\n\r\n", websocketAccept(key),
	)
	if err := rw.Flush(); err != nil {
		conn.Close()

		return nil, nil, err
	}

	return conn, rw, nil
}

// testReadClientText reads one masked client text message.
func testReadClientText(reader *bufio.Reader) ([]byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	if header[0]&0x0f != websocketOpcodeText {
		return nil, fmt.Errorf("unexpected opcode %d", header[0]&0x0f)
	}
	if header[1]&0x80 == 0 {
		return nil, fmt.Errorf("client frame was not masked")
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))

	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	var mask [4]byte
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return nil, err
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	applyWebSocketMask(payload, mask)

	return payload, nil
}

// testWriteServerText writes one unmasked server text frame.
func testWriteServerText(t *testing.T, conn net.Conn, text string) {
	t.Helper()
	payload := []byte(strings.TrimSpace(text))
	header := []byte{0x80 | websocketOpcodeText}
	switch length := len(payload); {
	case length < 126:
		header = append(header, byte(length))

	case length <= 0xffff:
		header = append(header, 126, byte(length>>8), byte(length))

	default:
		t.Fatalf("test payload too large: %d", length)
	}
	if _, err := conn.Write(header); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
}
