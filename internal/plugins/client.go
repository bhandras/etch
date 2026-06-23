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
	"runtime"
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

	// responseLineLimitBytes caps one plugin JSONL response frame.
	responseLineLimitBytes = 1024 * 1024

	// resultTextLimitBytes caps model-visible text returned by one plugin
	// call.
	resultTextLimitBytes = 128 * 1024

	// shutdownGrace gives cooperative plugins time to exit after stdin
	// closes or a termination signal is sent.
	shutdownGrace = 500 * time.Millisecond
)

// Client owns one configured plugin process and its JSONL protocol state.
type Client struct {
	name           string
	cfg            config.PluginConfig
	cwd            string
	timeout        time.Duration
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout         *bufio.Reader
	stderr         *limitedBuffer
	mu             sync.Mutex
	stateMu        sync.Mutex
	close          sync.Once
	closeErr       error
	nextID         int
	tools          []toolSpec
	unusable       bool
	unusableReason string
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
		clients = append(clients, client)
	}
	if err := validateClientTools(clients, registry); err != nil {
		closeClients(clients)

		return nil, err
	}
	for _, client := range clients {
		for _, spec := range client.tools {
			registry.Register(remoteTool{
				client: client,
				spec:   spec,
			})
		}
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
	cmd.Env = pluginEnvironment(cfg.Env)
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
		cfg:     cfg,
		cwd:     cwd,
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

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- c.cmd.Wait()
	}()

	select {
	case err := <-waitDone:
		return c.waitError(err, false)

	case <-time.After(shutdownGrace):
	}

	terminated := false
	if c.cmd.Process != nil {
		processTerminated, err := terminatePluginProcess(c.cmd)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("terminate plugin %s: %w", c.name,
				err)
		}
		terminated = processTerminated
	}

	select {
	case err := <-waitDone:
		return c.waitError(err, terminated)

	case <-time.After(shutdownGrace):
	}

	killed := false
	if c.cmd.Process != nil {
		processKilled, err := killPluginProcess(c.cmd)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("kill plugin %s: %w", c.name, err)
		}
		killed = processKilled
	}
	err := <-waitDone

	return c.waitError(err, killed)
}

// waitError turns a process wait result into a user-facing plugin diagnostic.
func (c *Client) waitError(err error, intentionallyStopped bool) error {
	if err != nil && strings.Contains(err.Error(), "waitid: no child") {
		return nil
	}
	if err != nil && intentionallyStopped {
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
		if err := tool.ValidateName(spec.Name); err != nil {
			return initializeResult{}, fmt.Errorf("plugin %s "+
				"returned invalid tool name: %w", c.name, err)
		}
		if _, ok := seen[spec.Name]; ok {
			return initializeResult{}, fmt.Errorf("plugin %s "+
				"returned duplicate tool %q", c.name, spec.Name)
		}
		seen[spec.Name] = struct{}{}
		if err := validateToolParameters(c.name, spec); err != nil {
			return initializeResult{}, err
		}
		if !tool.ParallelSafetyAllowed(spec.ParallelSafety) {
			return initializeResult{}, fmt.Errorf("plugin %s tool "+
				"%s returned unsupported parallel safety %q",
				c.name, spec.Name, spec.ParallelSafety)
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
			c.markUnusable(
				fmt.Sprintf("timed out after %s", c.timeout),
			)

			return tool.Result{}, fmt.Errorf("plugin tool %s "+
				"timed out after %s", call.Name, c.timeout)
		}

		return tool.Result{}, err
	}

	text, err := contentText(result.Content)
	if err != nil {
		c.markUnusable(err.Error())
		_ = c.Close()

		return tool.Result{}, fmt.Errorf("plugin tool %s: %w",
			call.Name, err)
	}

	return tool.Result{Text: text}, nil
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
			done <- responseResult{err: pluginFatalError{err: fmt.Errorf(
				"write plugin %s request: %w", c.name, err)}}

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
		c.markUnusable(ctx.Err().Error())
		_ = c.Close()

		return ctx.Err()

	case got := <-done:
		if got.err != nil {
			if fatalPluginError(got.err) {
				c.markUnusable(got.err.Error())
				_ = c.Close()
			}

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

			c.markUnusable(err.Error())
			_ = c.Close()

			return fmt.Errorf("decode plugin %s %s result: %w",
				c.name, method, err)
		}

		return nil
	}
}

// readResponse reads lines until the response for id arrives.
func (c *Client) readResponse(id string) (response, error) {
	for {
		line, err := c.readResponseLine()
		if err != nil {
			return response{}, c.readError(err)
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var got response
		if err := json.Unmarshal(line, &got); err != nil {
			return response{}, pluginFatalError{err: fmt.Errorf(
				"decode plugin %s response: %w", c.name, err)}
		}
		if got.ID == "" {
			return response{}, pluginFatalError{err: fmt.Errorf(
				"plugin %s returned response without id", c.name)}
		}
		if got.ID != id {
			return response{}, pluginFatalError{err: fmt.Errorf(
				"plugin %s returned response id %q while waiting "+
					"for %q", c.name, got.ID, id)}
		}

		return got, nil
	}
}

// readResponseLine reads one bounded JSONL response line.
func (c *Client) readResponseLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := c.stdout.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > responseLineLimitBytes {
			return nil, fmt.Errorf("response line exceeds %d bytes",
				responseLineLimitBytes)
		}
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		return nil, err
	}
}

// readError returns a diagnostic read failure with retained stderr when useful.
func (c *Client) readError(err error) error {
	text := strings.TrimSpace(c.stderr.String())
	if text == "" {
		return pluginFatalError{err: fmt.Errorf(
			"read plugin %s response: %w", c.name, err)}
	}

	return pluginFatalError{err: fmt.Errorf(
		"read plugin %s response: %w: %s", c.name, err, text)}
}

// nextRequestID returns a new request ID while c.mu is held.
func (c *Client) nextRequestID() string {
	c.nextID++

	return fmt.Sprintf("%d", c.nextID)
}

// Available reports whether this plugin client should still be advertised.
func (c *Client) Available() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	return !c.unusable
}

// UnavailableReason returns the diagnostic reason recorded for a hidden
// plugin client.
func (c *Client) UnavailableReason() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	return c.unusableReason
}

// markUnusable hides tools backed by this client from future model requests.
func (c *Client) markUnusable(reason string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	c.unusable = true
	c.unusableReason = strings.TrimSpace(reason)
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

// Available reports whether the owning plugin process is still healthy enough
// to advertise this tool to future model calls.
func (t remoteTool) Available() bool {
	return t.client.Available()
}

// ParallelSafe reports whether the plugin explicitly declared this tool safe
// for parallel execution.
func (t remoteTool) ParallelSafe(call model.ToolCall) bool {
	return tool.ParallelSafetyIsSafe(t.spec.ParallelSafety)
}

// ParallelLane serializes calls backed by this single plugin process while
// leaving other parallel-safe tools free to overlap.
func (t remoteTool) ParallelLane(call model.ToolCall) string {
	return "plugin:" + t.client.name
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

	if !t.client.Available() {
		reason := t.client.UnavailableReason()
		if reason != "" {
			reason = ": " + reason
		}

		return tool.Result{}, fmt.Errorf("plugin %s tool %s is "+
			"unavailable%s", t.client.name, t.spec.Name, reason)
	}

	return t.client.execute(ctx, call)
}

// contentText joins text content parts into the model-visible tool result.
func contentText(parts []contentPart) (string, error) {
	var out strings.Builder
	for _, part := range parts {
		if part.Type != contentTypeText {
			return "", fmt.Errorf("unsupported content type %q",
				part.Type)
		}
		if out.Len()+len(part.Text) > resultTextLimitBytes {
			return "", fmt.Errorf("text result exceeds %d bytes",
				resultTextLimitBytes)
		}
		out.WriteString(part.Text)
	}

	return out.String(), nil
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

// validateClientTools checks all initialized plugin tools before registration
// so startup is transactional from the registry's point of view.
func validateClientTools(clients []*Client, registry *tool.Registry) error {
	seen := map[string]string{}
	for _, client := range clients {
		for _, spec := range client.tools {
			if registry.Has(spec.Name) {
				return fmt.Errorf("plugin %s tool %q "+
					"conflicts with an existing tool",
					client.name, spec.Name)
			}
			if previous := seen[spec.Name]; previous != "" {
				return fmt.Errorf("plugin %s tool %q "+
					"conflicts with plugin %s", client.name,
					spec.Name, previous)
			}
			seen[spec.Name] = client.name
		}
	}

	return nil
}

// pluginEnvironment returns a sanitized child environment plus explicit
// user-requested forwarded variables.
func pluginEnvironment(extra []string) []string {
	allowed := basePluginEnvironment()
	for _, name := range extra {
		name = strings.TrimSpace(name)
		if name != "" {
			if runtime.GOOS == "windows" {
				name = strings.ToUpper(name)
			}
			allowed[name] = true
		}
	}

	env := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if pluginEnvironmentAllowed(allowed, name) {
			env = append(env, entry)
		}
	}

	return env
}

// basePluginEnvironment returns portable process variables safe to forward.
func basePluginEnvironment() map[string]bool {
	return map[string]bool{
		"APPDATA":      true,
		"COMSPEC":      true,
		"HOME":         true,
		"LANG":         true,
		"LOCALAPPDATA": true,
		"PATH":         true,
		"PATHEXT":      true,
		"PROGRAMDATA":  true,
		"SYSTEMROOT":   true,
		"TEMP":         true,
		"TMP":          true,
		"TMPDIR":       true,
		"USERPROFILE":  true,
		"WINDIR":       true,
	}
}

// pluginEnvironmentAllowed reports whether a parent environment name should
// be forwarded, preserving Unix case sensitivity for explicit allowlists.
func pluginEnvironmentAllowed(allowed map[string]bool, name string) bool {
	if runtime.GOOS == "windows" {
		upper := strings.ToUpper(name)

		return allowed[upper] || strings.HasPrefix(upper, "LC_")
	}

	return allowed[name] || strings.HasPrefix(name, "LC_")
}

// pluginFatalError wraps protocol or transport failures that desynchronize a
// plugin process and should hide its tools from later model requests.
type pluginFatalError struct {
	// err is the underlying user-facing failure.
	err error
}

// Error returns the underlying fatal plugin diagnostic.
func (e pluginFatalError) Error() string {
	return e.err.Error()
}

// Unwrap returns the underlying error for errors.Is and errors.As callers.
func (e pluginFatalError) Unwrap() error {
	return e.err
}

// fatalPluginError reports whether err should make a plugin unavailable.
func fatalPluginError(err error) bool {
	var fatal pluginFatalError

	return errors.As(err, &fatal)
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
