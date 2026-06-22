package openai

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6455 requires SHA-1 for accept keys.
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"harness/internal/model"
)

const (
	// websocketGUID is the RFC 6455 accept-key suffix.
	websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	// websocketVersion is the only protocol version standardized by RFC
	// 6455.
	websocketVersion = "13"

	// websocketBetaHeader enables OpenAI's Responses WebSocket endpoint.
	websocketBetaHeader = "responses_websockets=2026-02-06"

	// websocketMaxMessageBytes bounds one reassembled server text message.
	websocketMaxMessageBytes = 8 * 1024 * 1024

	// websocketCacheTTL keeps idle session connections briefly reusable.
	websocketCacheTTL = 5 * time.Minute
)

const (
	// websocketOpcodeContinuation identifies a continuation frame.
	websocketOpcodeContinuation = 0x0

	// websocketOpcodeText identifies a text frame.
	websocketOpcodeText = 0x1

	// websocketOpcodeClose identifies a close control frame.
	websocketOpcodeClose = 0x8

	// websocketOpcodePing identifies a ping control frame.
	websocketOpcodePing = 0x9

	// websocketOpcodePong identifies a pong control frame.
	websocketOpcodePong = 0xA
)

// websocketCache stores reusable Responses WebSocket connections by session.
var websocketCache = struct {
	sync.Mutex
	entries map[string]*websocketCacheEntry
}{entries: map[string]*websocketCacheEntry{}}

// websocketCacheEntry is one idle connection that may carry continuation
// state.
type websocketCacheEntry struct {
	// conn is the live WebSocket connection.
	conn *websocketConn

	// timer closes the connection after an idle period.
	timer *time.Timer

	// busy reports whether a model call currently owns conn.
	busy bool
}

// responseCreateRequest is the JSON message sent over the Responses WebSocket.
type responseCreateRequest struct {
	// Type identifies the WebSocket command.
	Type string `json:"type"`

	responseRequest
}

// streamResponsesWebSocket starts a Responses WebSocket request.
func (c *Client) streamResponsesWebSocket(ctx context.Context,
	req model.Request) (<-chan model.Event, error) {

	lease, reused, err := c.acquireResponsesWebSocket(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}

	body, requestMetrics, err := c.responsesRequestBody(req, reused)
	if err != nil {
		lease.release(false)

		return nil, err
	}
	body, err = websocketRequestBody(body)
	if err != nil {
		lease.release(false)

		return nil, err
	}
	requestMetrics.RequestBytes = len(body)
	if err := lease.conn.WriteText(ctx, body); err != nil {
		lease.release(false)

		return nil, fmt.Errorf("write openai websocket request: %w",
			err)
	}

	events := make(chan model.Event)
	metrics := streamMetrics{
		startedAt:      time.Now(),
		requestMetrics: requestMetrics,
	}
	go streamResponsesWebSocketEvents(ctx, lease, events, metrics)

	return events, nil
}

// websocketRequestBody adds the response.create command type to a Responses
// request body.
func websocketRequestBody(body []byte) ([]byte, error) {
	var request responseRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("decode websocket request body: %w", err)
	}

	return json.Marshal(responseCreateRequest{
		Type:            "response.create",
		responseRequest: request,
	})
}

// streamResponsesWebSocketEvents decodes WebSocket JSON messages as Responses
// stream events.
func streamResponsesWebSocketEvents(ctx context.Context, lease websocketLease,
	events chan<- model.Event, metrics streamMetrics) {

	defer close(events)

	decoder := responseStreamDecoder{}
	keep := true
	defer func() {
		lease.release(keep)
	}()

	for {
		payload, err := lease.conn.ReadText(ctx)
		if err != nil {
			keep = false
			sendEvent(ctx, events, model.Event{
				Type: model.EventError,
				Err: fmt.Sprintf(
					"read openai websocket: %v",
					err,
				),
			})

			return
		}
		metrics.addBytes(len(payload))
		metrics.markEvent(string(payload))
		for _, event := range decoder.decode(payload) {
			if event.Type == model.EventDone {
				sendMetricsAndDone(ctx, events, metrics)

				return
			}
			if !sendEvent(ctx, events, event) {
				keep = false

				return
			}
		}
	}
}

// websocketLease owns one cached or temporary WebSocket for a model call.
type websocketLease struct {
	// key is the cache key that owns entry.
	key string

	// entry is the cached connection state when the socket is reusable.
	entry *websocketCacheEntry

	// conn is the leased connection.
	conn *websocketConn

	// cached reports whether conn came from websocketCache.
	cached bool
}

// release returns the leased connection to the cache or closes it.
func (l websocketLease) release(keep bool) {
	if !l.cached {
		l.conn.Close()

		return
	}

	websocketCache.Lock()
	defer websocketCache.Unlock()

	if websocketCache.entries[l.key] != l.entry {
		l.conn.Close()

		return
	}
	if !keep || l.conn.Closed() {
		delete(websocketCache.entries, l.key)
		l.conn.Close()

		return
	}
	l.entry.busy = false
	l.entry.timer = time.AfterFunc(websocketCacheTTL, func() {
		expireWebSocketCacheEntry(l.key, l.entry)
	})
}

// expireWebSocketCacheEntry closes an idle cached connection if it is still
// current.
func expireWebSocketCacheEntry(key string, entry *websocketCacheEntry) {
	websocketCache.Lock()
	defer websocketCache.Unlock()

	if websocketCache.entries[key] != entry || entry.busy {
		return
	}
	delete(websocketCache.entries, key)
	entry.conn.Close()
}

// acquireResponsesWebSocket returns a reusable connection for sessionID when
// possible.
func (c *Client) acquireResponsesWebSocket(ctx context.Context,
	sessionID string) (websocketLease, bool, error) {

	key := c.websocketCacheKey(sessionID)
	if key != "" {
		websocketCache.Lock()
		entry := websocketCache.entries[key]
		if entry != nil && !entry.busy && !entry.conn.Closed() {
			if entry.timer != nil {
				entry.timer.Stop()
				entry.timer = nil
			}
			entry.busy = true
			websocketCache.Unlock()

			return websocketLease{
				key:    key,
				entry:  entry,
				conn:   entry.conn,
				cached: true,
			}, true, nil
		}
		if entry != nil && entry.conn.Closed() {
			delete(websocketCache.entries, key)
		}
		websocketCache.Unlock()
	}

	conn, err := c.connectResponsesWebSocket(ctx)
	if err != nil {
		return websocketLease{}, false, err
	}
	if key == "" {
		return websocketLease{conn: conn}, false, nil
	}

	entry := &websocketCacheEntry{conn: conn, busy: true}
	websocketCache.Lock()
	websocketCache.entries[key] = entry
	websocketCache.Unlock()

	return websocketLease{
		key:    key,
		entry:  entry,
		conn:   conn,
		cached: true,
	}, false, nil
}

// websocketCacheKey returns the cache key for a session-scoped connection.
func (c *Client) websocketCacheKey(sessionID string) string {
	if sessionID == "" {
		return ""
	}

	return c.endpoint(responsesPath) + "\x00" + c.Model + "\x00" + sessionID
}

// connectResponsesWebSocket opens and validates the WebSocket handshake.
func (c *Client) connectResponsesWebSocket(ctx context.Context) (*websocketConn,
	error) {

	endpoint, err := websocketEndpoint(c.endpoint(responsesPath))
	if err != nil {
		return nil, err
	}
	headers := c.websocketHeaders()
	conn, err := dialWebSocket(ctx, endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("connect openai websocket: %w", err)
	}

	return conn, nil
}

// websocketHeaders returns provider headers for the Responses WebSocket.
func (c *Client) websocketHeaders() http.Header {
	headers := http.Header{}
	if c.UserAgent != "" {
		headers.Set("User-Agent", c.UserAgent)
	}
	if c.APIKey != "" {
		headers.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.AccountID != "" {
		headers.Set("chatgpt-account-id", c.AccountID)
	}
	headers.Set("OpenAI-Beta", websocketBetaHeader)

	return headers
}

// websocketEndpoint converts an HTTP endpoint URL to a WebSocket endpoint URL.
func websocketEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse websocket endpoint: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"

	case "http":
		u.Scheme = "ws"

	case "wss", "ws":
	default:
		return nil, fmt.Errorf("unsupported websocket scheme %q",
			u.Scheme)
	}

	return u, nil
}

// websocketConn is a minimal RFC 6455 client connection.
type websocketConn struct {
	// conn is the underlying network connection.
	conn net.Conn

	// reader buffers handshake leftovers and frame reads.
	reader *bufio.Reader

	// writeMu serializes frame writes.
	writeMu sync.Mutex

	// closeOnce closes conn once.
	closeOnce sync.Once

	// closed reports whether Close has run.
	closed bool

	// closedMu guards closed.
	closedMu sync.Mutex
}

// Closed reports whether the connection has been closed locally.
func (c *websocketConn) Closed() bool {
	c.closedMu.Lock()
	defer c.closedMu.Unlock()

	return c.closed
}

// Close closes the underlying network connection.
func (c *websocketConn) Close() {
	c.closeOnce.Do(func() {
		c.closedMu.Lock()
		c.closed = true
		c.closedMu.Unlock()
		c.conn.Close()
	})
}

// WriteText writes one masked client text message.
func (c *websocketConn) WriteText(ctx context.Context, payload []byte) error {
	if deadline, ok := ctx.Deadline(); ok {
		c.conn.SetWriteDeadline(deadline)
		defer c.conn.SetWriteDeadline(time.Time{})
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	return c.writeFrame(websocketOpcodeText, payload)
}

// ReadText reads one complete server text message.
func (c *websocketConn) ReadText(ctx context.Context) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		c.conn.SetReadDeadline(deadline)
		defer c.conn.SetReadDeadline(time.Time{})
	}

	var message []byte
	for {
		frame, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch frame.opcode {
		case websocketOpcodeText, websocketOpcodeContinuation:
			message = append(message, frame.payload...)
			if len(message) > websocketMaxMessageBytes {
				return nil, fmt.Errorf("websocket message "+
					"exceeds %d bytes",
					websocketMaxMessageBytes)
			}
			if frame.final {
				return message, nil
			}

		case websocketOpcodePing:
			if err := c.writeFrame(
				websocketOpcodePong, frame.payload,
			); err != nil {
				return nil, err
			}

		case websocketOpcodePong:
			continue

		case websocketOpcodeClose:
			c.Close()

			return nil, io.EOF

		default:
			return nil, fmt.Errorf("unsupported websocket "+
				"opcode %d", frame.opcode)
		}
	}
}

// websocketFrame is one decoded server frame.
type websocketFrame struct {
	// final reports whether this is the last fragment.
	final bool

	// opcode identifies the frame type.
	opcode byte

	// payload is the unmasked frame payload.
	payload []byte
}

// readFrame reads one server frame.
func (c *websocketConn) readFrame() (websocketFrame, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.reader, header); err != nil {
		return websocketFrame{}, err
	}
	final := header[0]&0x80 != 0
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return websocketFrame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))

	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return websocketFrame{}, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	if length > websocketMaxMessageBytes {
		return websocketFrame{}, fmt.Errorf("websocket frame exceeds "+
			"%d bytes", websocketMaxMessageBytes)
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.reader, mask[:]); err != nil {
			return websocketFrame{}, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return websocketFrame{}, err
	}
	if masked {
		applyWebSocketMask(payload, mask)
	}

	return websocketFrame{
		final:   final,
		opcode:  opcode,
		payload: payload,
	}, nil
}

// writeFrame writes one masked client frame.
func (c *websocketConn) writeFrame(opcode byte, payload []byte) error {
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("websocket mask: %w", err)
	}
	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, 0x80|byte(length))

	case length <= 0xffff:
		shortLength := uint16(length)
		var extended [2]byte
		binary.BigEndian.PutUint16(extended[:], shortLength)
		header = append(header, 0x80|126)
		header = append(header, extended[:]...)

	default:
		header = append(header, 0x80|127)
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(length))
		header = append(header, extended[:]...)
	}
	header = append(header, mask[:]...)
	masked := append([]byte(nil), payload...)
	applyWebSocketMask(masked, mask)
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)

	return err
}

// applyWebSocketMask applies an RFC 6455 masking key in place.
func applyWebSocketMask(payload []byte, mask [4]byte) {
	for i := range payload {
		payload[i] ^= mask[i%len(mask)]
	}
}

// dialWebSocket opens the network connection and performs the HTTP upgrade.
func dialWebSocket(ctx context.Context, endpoint *url.URL,
	headers http.Header) (*websocketConn, error) {

	rawConn, err := dialWebSocketNetwork(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	conn := &websocketConn{
		conn:   rawConn,
		reader: bufio.NewReader(rawConn),
	}
	if err := websocketHandshake(ctx, conn, endpoint, headers); err != nil {
		conn.Close()

		return nil, err
	}

	return conn, nil
}

// dialWebSocketNetwork opens a TCP or TLS connection for endpoint.
func dialWebSocketNetwork(ctx context.Context,
	endpoint *url.URL) (net.Conn, error) {

	address := websocketAddress(endpoint)
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if endpoint.Scheme != "wss" {
		return conn, nil
	}
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: endpoint.Hostname(),
		MinVersion: tls.VersionTLS12,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()

		return nil, err
	}

	return tlsConn, nil
}

// websocketAddress returns the host:port address for endpoint.
func websocketAddress(endpoint *url.URL) string {
	if endpoint.Port() != "" {
		return endpoint.Host
	}
	if endpoint.Scheme == "wss" {
		return net.JoinHostPort(endpoint.Hostname(), "443")
	}

	return net.JoinHostPort(endpoint.Hostname(), "80")
}

// websocketHandshake writes and validates the HTTP upgrade request.
func websocketHandshake(ctx context.Context, conn *websocketConn,
	endpoint *url.URL, headers http.Header) error {

	key, err := websocketKey()
	if err != nil {
		return err
	}
	req := &http.Request{
		Method: http.MethodGet,
		URL:    endpoint,
		Host:   endpoint.Host,
		Header: headers.Clone(),
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", websocketVersion)
	if deadline, ok := ctx.Deadline(); ok {
		conn.conn.SetDeadline(deadline)
		defer conn.conn.SetDeadline(time.Time{})
	}
	if err := req.Write(conn.conn); err != nil {
		return err
	}
	resp, err := http.ReadResponse(conn.reader, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

		return fmt.Errorf("websocket status %s: %s", resp.Status,
			strings.TrimSpace(string(body)))
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		return fmt.Errorf("websocket upgrade header missing")
	}
	if !headerContainsToken(resp.Header.Get("Connection"), "upgrade") {
		return fmt.Errorf("websocket connection header missing upgrade")
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"),
		websocketAccept(key); got != want {
		return fmt.Errorf("websocket accept mismatch")
	}

	return nil
}

// websocketKey returns a random base64 nonce for the handshake.
func websocketKey() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("websocket key: %w", err)
	}

	return base64.StdEncoding.EncodeToString(nonce[:]), nil
}

// websocketAccept returns the RFC 6455 expected accept header value.
func websocketAccept(key string) string {
	// #nosec G401 -- RFC 6455 defines this SHA-1 based handshake value.
	sum := sha1.Sum([]byte(key + websocketGUID))

	return base64.StdEncoding.EncodeToString(sum[:])
}

// headerContainsToken reports whether a comma-separated HTTP header has token.
func headerContainsToken(header string, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}

	return false
}
