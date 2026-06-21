package plugins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"harness/internal/config"
	"harness/internal/model"
	"harness/internal/platform"
	"harness/internal/tool"
)

const (
	// defaultTimeoutSeconds bounds plugin startup and tool calls when
	// config leaves timeout_seconds unset.
	defaultTimeoutSeconds = 30

	// stderrLimitBytes caps plugin diagnostic text retained for errors.
	stderrLimitBytes = 4096
)

// Client owns one configured plugin process and its JSONL protocol state.
type Client struct {
	name     string
	timeout  time.Duration
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	stderr   *limitedBuffer
	mu       sync.Mutex
	close    sync.Once
	closeErr error
	nextID   int
	tools    []toolSpec
}

// StartConfigured starts all enabled configured plugins and registers their
// tools. Already-started plugins are returned so callers can close them later.
func StartConfigured(ctx context.Context, configs []config.PluginConfig,
	cwd string, registry *tool.Registry) ([]*Client, error) {

	var clients []*Client
	for _, cfg := range configs {
		if cfg.Disabled {
			continue
		}
		client, err := Start(ctx, cfg, cwd)
		if err != nil {
			closeClients(clients)

			return nil, err
		}
		for _, spec := range client.tools {
			if registry.Has(spec.Name) {
				_ = client.Close()
				closeClients(clients)

				return nil, fmt.Errorf("plugin %s tool %q "+
					"conflicts with an existing tool",
					client.name, spec.Name)
			}
		}
		for _, spec := range client.tools {
			registry.Register(remoteTool{
				client: client,
				spec:   spec,
			})
		}
		clients = append(clients, client)
	}

	return clients, nil
}

// Start launches one plugin process and completes the initialize handshake.
func Start(ctx context.Context, cfg config.PluginConfig,
	cwd string) (*Client, error) {

	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("plugin command must not be empty")
	}
	timeout := pluginTimeout(cfg.TimeoutSeconds)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	name, args := platform.ShellCommand(cfg.Command)
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	preparePluginCommand(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open plugin stdin: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open plugin stdout: %w", err)
	}
	stderr := &limitedBuffer{limit: stderrLimitBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start plugin %s: %w", pluginName(cfg),
			err)
	}

	client := &Client{
		name:    pluginName(cfg),
		timeout: timeout,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdoutPipe),
		stderr:  stderr,
	}
	pluginCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	initialized, err := client.initialize(pluginCtx)
	if err != nil {
		client.Close()
		if pluginCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("initialize plugin %s timed "+
				"out after %s", client.name, timeout)
		}

		return nil, err
	}
	if strings.TrimSpace(initialized.Name) != "" {
		client.name = initialized.Name
	}
	client.tools = append([]toolSpec{}, initialized.Tools...)

	return client, nil
}

// Close terminates the plugin process and releases its pipes.
func (c *Client) Close() error {
	if c == nil || c.cmd == nil {
		return nil
	}
	c.close.Do(func() {
		c.closeErr = c.closeProcess()
	})

	return c.closeErr
}

// closeProcess performs the one-time process shutdown for Close.
func (c *Client) closeProcess() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	killed := false
	if c.cmd.Process != nil {
		processKilled, err := killPluginProcess(c.cmd)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("kill plugin %s: %w", c.name, err)
		}
		killed = processKilled
	}
	err := c.cmd.Wait()
	if err != nil && strings.Contains(err.Error(), "waitid: no child") {
		return nil
	}
	if err != nil && killed {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == -1 {
			return nil
		}
	}
	if err != nil {
		text := strings.TrimSpace(c.stderr.String())
		if text != "" {
			return fmt.Errorf("wait plugin %s: %w: %s", c.name, err,
				text)
		}

		return fmt.Errorf("wait plugin %s: %w", c.name, err)
	}

	return nil
}

// initialize performs the plugin startup handshake.
func (c *Client) initialize(ctx context.Context) (initializeResult, error) {
	var result initializeResult
	err := c.call(ctx, methodInitialize, initializeParams{
		ProtocolVersion: ProtocolVersion,
	}, &result)
	if err != nil {
		return initializeResult{}, err
	}
	seen := make(map[string]struct{})
	for _, spec := range result.Tools {
		if strings.TrimSpace(spec.Name) == "" {
			return initializeResult{}, fmt.Errorf("plugin %s "+
				"returned a tool without a name", c.name)
		}
		if _, ok := seen[spec.Name]; ok {
			return initializeResult{}, fmt.Errorf("plugin %s "+
				"returned duplicate tool %q", c.name, spec.Name)
		}
		seen[spec.Name] = struct{}{}
		if err := validateToolParameters(c.name, spec); err != nil {
			return initializeResult{}, err
		}
	}

	return result, nil
}

// validateToolParameters enforces the object-schema shape model providers
// expect for tool arguments.
func validateToolParameters(pluginName string, spec toolSpec) error {
	if len(spec.Parameters) == 0 || !json.Valid(spec.Parameters) {
		return fmt.Errorf("plugin %s tool %s returned invalid "+
			"parameters", pluginName, spec.Name)
	}

	var schema map[string]json.RawMessage
	if err := json.Unmarshal(spec.Parameters, &schema); err != nil ||
		schema == nil {
		return fmt.Errorf("plugin %s tool %s parameters must be a "+
			"JSON object", pluginName, spec.Name)
	}

	rawType, ok := schema["type"]
	if !ok {
		return fmt.Errorf("plugin %s tool %s parameters must set "+
			`"type":"object"`,
			pluginName, spec.Name)
	}

	var schemaType string
	if err := json.Unmarshal(rawType, &schemaType); err != nil ||
		schemaType != "object" {
		return fmt.Errorf("plugin %s tool %s parameters must set "+
			`"type":"object"`,
			pluginName, spec.Name)
	}

	return nil
}

// execute runs one remote plugin tool call.
func (c *Client) execute(ctx context.Context, call model.ToolCall) (tool.Result,
	error) {

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	arguments := json.RawMessage([]byte(strings.TrimSpace(call.Arguments)))
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(arguments) {
		return tool.Result{}, fmt.Errorf("plugin tool %s received "+
			"invalid JSON arguments", call.Name)
	}

	var result toolExecuteResult
	err := c.call(callCtx, methodToolExecute, toolExecuteParams{
		CallID:    call.ID,
		Name:      call.Name,
		Arguments: arguments,
	}, &result)
	if err != nil {
		if callCtx.Err() == context.DeadlineExceeded {
			return tool.Result{}, fmt.Errorf("plugin tool %s "+
				"timed out after %s", call.Name, c.timeout)
		}

		return tool.Result{}, err
	}

	return tool.Result{Text: contentText(result.Content)}, nil
}

// call sends one request and waits for the matching response.
func (c *Client) call(ctx context.Context, method string, params any,
	result any) error {

	done := make(chan responseResult, 1)
	go func() {
		c.mu.Lock()
		defer c.mu.Unlock()

		id := c.nextRequestID()
		encoded, err := json.Marshal(request{
			ID:     id,
			Method: method,
			Params: params,
		})
		if err != nil {
			done <- responseResult{err: fmt.Errorf(
				"marshal plugin request: %w", err)}

			return
		}
		if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
			done <- responseResult{err: fmt.Errorf(
				"write plugin %s request: %w", c.name, err)}

			return
		}

		response, err := c.readResponse(id)
		if err != nil {
			done <- responseResult{err: err}

			return
		}
		done <- responseResult{response: response}
	}()

	select {
	case <-ctx.Done():
		_ = c.Close()

		return ctx.Err()

	case got := <-done:
		if got.err != nil {
			return got.err
		}
		if got.response.Error != nil {
			return fmt.Errorf("plugin %s %s failed: %s", c.name,
				method, got.response.Error.Message)
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(
			got.response.Result, result,
		); err != nil {
			return fmt.Errorf("decode plugin %s %s result: %w",
				c.name, method, err)
		}

		return nil
	}
}

// readResponse reads lines until the response for id arrives.
func (c *Client) readResponse(id string) (response, error) {
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			return response{}, c.readError(err)
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var got response
		if err := json.Unmarshal(line, &got); err != nil {
			return response{}, fmt.Errorf("decode plugin %s "+
				"response: %w", c.name, err)
		}
		if got.ID == "" {
			continue
		}
		if got.ID != id {
			return response{}, fmt.Errorf("plugin %s returned "+
				"response id %q while waiting for %q", c.name,
				got.ID, id)
		}

		return got, nil
	}
}

// readError returns a diagnostic read failure with retained stderr when useful.
func (c *Client) readError(err error) error {
	text := strings.TrimSpace(c.stderr.String())
	if text == "" {
		return fmt.Errorf("read plugin %s response: %w", c.name, err)
	}

	return fmt.Errorf("read plugin %s response: %w: %s", c.name, err, text)
}

// nextRequestID returns a new request ID while c.mu is held.
func (c *Client) nextRequestID() string {
	c.nextID++

	return fmt.Sprintf("%d", c.nextID)
}

// responseResult carries a response or error across a cancellation channel.
type responseResult struct {
	// response is the decoded plugin response.
	response response

	// err is the protocol or transport failure.
	err error
}

// remoteTool adapts one plugin tool schema to the core tool interface.
type remoteTool struct {
	// client owns the plugin process that executes the tool.
	client *Client

	// spec is the plugin-provided model-facing schema.
	spec toolSpec
}

// Spec returns the plugin-provided model-facing schema.
func (t remoteTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        t.spec.Name,
		Description: t.spec.Description,
		Parameters:  append(json.RawMessage{}, t.spec.Parameters...),
	}
}

// Execute sends a direct tool invocation to the owning plugin process.
func (t remoteTool) Execute(ctx context.Context, arguments string) (tool.Result,
	error) {

	return t.ExecuteCall(ctx, model.ToolCall{
		ID:        "manual",
		Name:      t.spec.Name,
		Arguments: arguments,
	})
}

// ExecuteCall sends the complete model tool call to the owning plugin process.
func (t remoteTool) ExecuteCall(ctx context.Context, call model.ToolCall) (
	tool.Result, error) {

	return t.client.execute(ctx, call)
}

// contentText joins text content parts into the model-visible tool result.
func contentText(parts []contentPart) string {
	var out strings.Builder
	for _, part := range parts {
		if part.Type != contentTypeText {
			continue
		}
		out.WriteString(part.Text)
	}

	return out.String()
}

// pluginTimeout returns the configured plugin timeout duration.
func pluginTimeout(seconds int) time.Duration {
	if seconds < 1 {
		seconds = defaultTimeoutSeconds
	}

	return time.Duration(seconds) * time.Second
}

// pluginName returns the configured name or a fallback for diagnostics.
func pluginName(cfg config.PluginConfig) string {
	if strings.TrimSpace(cfg.Name) != "" {
		return cfg.Name
	}

	return cfg.Command
}

// closeClients closes partially-started plugins after startup failure.
func closeClients(clients []*Client) {
	for _, client := range clients {
		_ = client.Close()
	}
}

// limitedBuffer is a concurrency-safe capped diagnostic buffer.
type limitedBuffer struct {
	// mu guards buf because process stderr can be written while errors read
	// it.
	mu sync.Mutex

	// limit is the maximum number of bytes retained.
	limit int

	// buf stores the retained suffix of stderr output.
	buf []byte
}

// Write appends p while retaining at most b.limit latest bytes.
func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf = append(b.buf, p...)
	if b.limit > 0 && len(b.buf) > b.limit {
		b.buf = append([]byte{}, b.buf[len(b.buf)-b.limit:]...)
	}

	return len(p), nil
}

// String returns the retained diagnostic text.
func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return string(b.buf)
}
