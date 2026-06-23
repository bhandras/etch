package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const (
	// ProtocolVersion is the Harness JSONL plugin protocol version served
	// by this SDK package.
	ProtocolVersion = "0.1.0"

	// ContentTypeText identifies plain text tool output content.
	ContentTypeText = "text"

	// methodInitialize is the protocol method used to discover plugin
	// metadata and tools.
	methodInitialize = "initialize"

	// methodToolExecute is the protocol method used to execute one tool.
	methodToolExecute = "tool.execute"

	// ParallelSafetySerial marks tools that must run as serial barriers.
	ParallelSafetySerial = "serial"

	// ParallelSafetyReadOnly marks tools that read state without mutating
	// it and may overlap other safe reads.
	ParallelSafetyReadOnly = "read_only"

	// ParallelSafetyParallel marks tools that are independently safe to
	// overlap with other safe calls.
	ParallelSafetyParallel = "parallel_safe"
)

// toolNamePattern is the provider-compatible subset accepted by Harness.
var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Plugin describes one standalone Harness plugin process.
type Plugin struct {
	// Name is the plugin display name returned during initialization.
	Name string

	// Tools are the model-callable tools exposed by the plugin.
	Tools []Tool
}

// Tool describes one model-callable plugin tool and its handler.
type Tool struct {
	// Name is the model-facing tool identifier.
	Name string

	// Description explains when and how the model should call the tool.
	Description string

	// Parameters is a JSON Schema object describing tool arguments.
	Parameters any

	// ParallelSafety optionally declares whether this tool may overlap
	// other parallel-safe calls. Empty means serial.
	ParallelSafety string

	// Handler executes this tool for one model-requested call.
	Handler ToolHandler
}

// ToolHandler executes one plugin tool call.
type ToolHandler func(context.Context, ToolCall) (ToolResult, error)

// ToolCall contains one model-requested plugin tool invocation.
type ToolCall struct {
	// CallID is the model provider's tool-call identifier.
	CallID string

	// Name is the model-facing tool name to execute.
	Name string

	// Arguments stores the raw JSON argument object from the model.
	Arguments json.RawMessage
}

// ToolResult contains the model-visible result of one plugin tool call.
type ToolResult struct {
	// Content stores ordered model-visible output parts.
	Content []ContentPart `json:"content,omitempty"`
}

// ContentPart is one typed plugin output part.
type ContentPart struct {
	// Type identifies how Text should be interpreted.
	Type string `json:"type"`

	// Text stores plain text for ContentTypeText parts.
	Text string `json:"text"`
}

// InitializeParams is sent by Harness during plugin startup.
type InitializeParams struct {
	// ProtocolVersion is the highest protocol version Harness supports.
	ProtocolVersion string `json:"protocolVersion"`
}

// InitializeResult describes plugin capabilities after startup.
type InitializeResult struct {
	// Name is the plugin's preferred display name.
	Name string `json:"name"`

	// Tools are model-callable tool schemas exposed by the plugin.
	Tools []ToolSpec `json:"tools,omitempty"`
}

// ToolSpec is the wire representation of one model-callable tool schema.
type ToolSpec struct {
	// Name is the model-facing tool identifier.
	Name string `json:"name"`

	// Description explains when and how the model should call the tool.
	Description string `json:"description"`

	// Parameters is a JSON Schema object describing tool arguments.
	Parameters any `json:"parameters"`

	// ParallelSafety optionally declares whether this tool may overlap
	// other safe calls. Empty means serial.
	ParallelSafety string `json:"parallelSafety,omitempty"`
}

// ToolExecuteParams is sent when Harness asks a plugin to execute a tool.
type ToolExecuteParams struct {
	// CallID is the model provider's tool-call identifier.
	CallID string `json:"callID"`

	// Name is the model-facing tool name to execute.
	Name string `json:"name"`

	// Arguments stores the raw JSON argument object from the model.
	Arguments json.RawMessage `json:"arguments"`
}

// TextResult returns a ToolResult containing one plain text output part.
func TextResult(text string) ToolResult {
	return ToolResult{
		Content: []ContentPart{{
			Type: ContentTypeText,
			Text: text,
		}},
	}
}

// ServePlugin serves plugin over process stdin and stdout until stdin closes.
func ServePlugin(plugin Plugin) error {
	return ServePluginIO(
		context.Background(), plugin, os.Stdin, os.Stdout, os.Stderr,
	)
}

// ServePluginIO serves plugin over caller-provided streams for tests and
// embedders.
func ServePluginIO(ctx context.Context, plugin Plugin, stdin io.Reader,
	stdout io.Writer, stderr io.Writer) error {

	server, err := newPluginServer(plugin, stdout, stderr)
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		server.handleLine(ctx, scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read plugin request: %w", err)
	}

	return ctx.Err()
}

// pluginServer stores dispatch state for one plugin process.
type pluginServer struct {
	plugin  Plugin
	tools   map[string]Tool
	stdout  io.Writer
	stderr  io.Writer
	encoder *json.Encoder
}

// newPluginServer validates plugin and prepares deterministic tool dispatch.
func newPluginServer(plugin Plugin, stdout io.Writer,
	stderr io.Writer) (*pluginServer, error) {

	if plugin.Name == "" {
		return nil, fmt.Errorf("plugin name must not be empty")
	}
	tools := make(map[string]Tool)
	for _, tool := range plugin.Tools {
		if err := validateToolName(tool.Name); err != nil {
			return nil, err
		}
		if tool.Handler == nil {
			return nil, fmt.Errorf("plugin tool %s handler must "+
				"not be nil", tool.Name)
		}
		if err := validateToolParameters(tool); err != nil {
			return nil, err
		}
		if !validParallelSafety(tool.ParallelSafety) {
			return nil, fmt.Errorf("plugin tool %s parallel "+
				"safety %q is not supported", tool.Name,
				tool.ParallelSafety)
		}
		if _, ok := tools[tool.Name]; ok {
			return nil, fmt.Errorf("plugin tool %s is registered "+
				"more than once", tool.Name)
		}
		tools[tool.Name] = tool
	}

	return &pluginServer{
		plugin:  plugin,
		tools:   tools,
		stdout:  stdout,
		stderr:  stderr,
		encoder: json.NewEncoder(stdout),
	}, nil
}

// handleLine decodes and dispatches one JSONL protocol request.
func (s *pluginServer) handleLine(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeResponse(response{
			Error: &responseError{
				Message: "decode request: " + err.Error(),
			},
		})

		return
	}

	switch req.Method {
	case methodInitialize:
		s.handleInitialize(req)

	case methodToolExecute:
		s.handleToolExecute(ctx, req)

	default:
		s.writeError(req.ID, "unknown method "+req.Method)
	}
}

// handleInitialize validates the requested protocol version and returns tool
// metadata.
func (s *pluginServer) handleInitialize(req request) {
	var params InitializeParams
	if len(req.Params) != 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.writeError(
				req.ID, "decode initialize params: "+
					err.Error(),
			)

			return
		}
	}
	if params.ProtocolVersion != "" &&
		params.ProtocolVersion != ProtocolVersion {

		s.writeError(
			req.ID, "unsupported protocol version "+
				params.ProtocolVersion,
		)

		return
	}

	specs := make([]ToolSpec, 0, len(s.plugin.Tools))
	for _, tool := range s.plugin.Tools {
		specs = append(specs, ToolSpec{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
			ParallelSafety: strings.TrimSpace(
				tool.ParallelSafety,
			),
		})
	}

	s.writeResponse(response{
		ID: req.ID,
		Result: InitializeResult{
			Name:  s.plugin.Name,
			Tools: specs,
		},
	})
}

// validateToolName reports whether name is accepted by supported providers.
func validateToolName(name string) error {
	if name == strings.TrimSpace(name) &&
		toolNamePattern.MatchString(name) {
		return nil
	}

	return fmt.Errorf("plugin tool name %q must match %s", name,
		toolNamePattern.String())
}

// validateToolParameters verifies SDK tools expose an object argument schema.
func validateToolParameters(tool Tool) error {
	encoded, err := json.Marshal(tool.Parameters)
	if err != nil || !json.Valid(encoded) {
		return fmt.Errorf("plugin tool %s parameters must be "+
			"valid JSON", tool.Name)
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &schema); err != nil ||
		schema == nil {
		return fmt.Errorf("plugin tool %s parameters must be a "+
			"JSON object", tool.Name)
	}
	rawType, ok := schema["type"]
	if !ok {
		return fmt.Errorf("plugin tool %s parameters must set "+
			`"type":"object"`,
			tool.Name)
	}
	var schemaType string
	if err := json.Unmarshal(rawType, &schemaType); err != nil ||
		schemaType != "object" {
		return fmt.Errorf("plugin tool %s parameters must set "+
			`"type":"object"`,
			tool.Name)
	}

	return nil
}

// validParallelSafety reports whether safety is supported by the protocol.
func validParallelSafety(safety string) bool {
	switch strings.TrimSpace(safety) {
	case "", ParallelSafetySerial, ParallelSafetyReadOnly,
		ParallelSafetyParallel:
		return true

	default:
		return false
	}
}

// handleToolExecute decodes and runs one plugin tool request.
func (s *pluginServer) handleToolExecute(ctx context.Context, req request) {
	var params ToolExecuteParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(req.ID, "decode tool params: "+err.Error())

		return
	}

	tool, ok := s.tools[params.Name]
	if !ok {
		s.writeError(req.ID, "unknown tool "+params.Name)

		return
	}

	result, err := tool.Handler(ctx, ToolCall(params))
	if err != nil {
		s.writeError(req.ID, err.Error())

		return
	}

	s.writeResponse(response{
		ID:     req.ID,
		Result: result,
	})
}

// writeError writes one failed protocol response.
func (s *pluginServer) writeError(id string, message string) {
	s.writeResponse(response{
		ID:    id,
		Error: &responseError{Message: message},
	})
}

// writeResponse writes one JSONL response to plugin stdout.
func (s *pluginServer) writeResponse(resp response) {
	if err := s.encoder.Encode(resp); err != nil {
		fmt.Fprintf(s.stderr, "encode response: %v\n", err)
	}
}

// request is one JSONL-RPC request sent from Harness to a plugin.
type request struct {
	// ID correlates the request with one plugin response.
	ID string `json:"id"`

	// Method names the requested plugin operation.
	Method string `json:"method"`

	// Params stores the method-specific request object.
	Params json.RawMessage `json:"params,omitempty"`
}

// response is one JSONL-RPC response sent from a plugin to Harness.
type response struct {
	// ID correlates the response with one Harness request.
	ID string `json:"id,omitempty"`

	// Result stores the method-specific success object.
	Result any `json:"result,omitempty"`

	// Error stores the method failure, when any.
	Error *responseError `json:"error,omitempty"`
}

// responseError describes one plugin protocol failure.
type responseError struct {
	// Message is the human-readable error text.
	Message string `json:"message"`
}
