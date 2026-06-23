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
