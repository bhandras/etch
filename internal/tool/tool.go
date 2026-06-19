package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"harness/internal/model"
	fs "harness/internal/tools/fs"
)

const (
	// NameLS is the model-facing name for the directory listing tool.
	NameLS = "ls"

	// NameRead is the model-facing name for the text file reading tool.
	NameRead = "read"

	// NameWrite is the model-facing name for the whole-file writing tool.
	NameWrite = "write"
)

// Result is the text returned by a builtin tool execution.
type Result struct {
	// Text is the model-visible tool output.
	Text string `json:"text"`
}

// Tool executes one model-callable builtin operation.
type Tool interface {
	// Spec returns the model-facing tool schema.
	Spec() model.ToolSpec

	// Execute runs the tool with raw JSON arguments.
	Execute(ctx context.Context, arguments string) (Result, error)
}

// Registry stores builtin tools by name and dispatches model tool calls.
type Registry struct {
	tools map[string]Tool
}

// DefaultRegistry returns the builtin tools available to the agent.
func DefaultRegistry() *Registry {
	registry := NewRegistry()
	registry.Register(lsTool{})
	registry.Register(readTool{})
	registry.Register(writeTool{})

	return registry
}

// NewRegistry creates an empty builtin tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds or replaces one tool by its model-facing name.
func (r *Registry) Register(tool Tool) {
	r.tools[tool.Spec().Name] = tool
}

// Specs returns deterministic model-facing tool schemas.
func (r *Registry) Specs() []model.ToolSpec {
	specs := make([]model.ToolSpec, 0, len(r.tools))
	for _, tool := range r.tools {
		specs = append(specs, tool.Spec())
	}
	sort.Slice(specs, func(i, j int) bool {
		return specs[i].Name < specs[j].Name
	})

	return specs
}

// Execute dispatches a complete model tool call to its registered tool.
func (r *Registry) Execute(ctx context.Context, call model.ToolCall) (Result,
	error) {

	tool, ok := r.tools[call.Name]
	if !ok {
		return Result{}, fmt.Errorf("unknown tool %q", call.Name)
	}

	return tool.Execute(ctx, call.Arguments)
}

// lsTool wraps the pure-Go filesystem listing operation as a model tool.
type lsTool struct{}

// Spec returns the model-facing schema for the ls tool.
func (lsTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: NameLS,
		Description: "List one local directory. Use this to inspect project " +
			"files before answering questions about the filesystem.",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{
					"type":"string",
					"description":"Directory to list. Defaults to the current directory."
				},
				"limit":{
					"type":"integer",
					"description":"Maximum entries to return. Defaults to 500."
				}
			}
		}`),
	}
}

// Execute decodes ls arguments and returns the directory listing.
func (lsTool) Execute(ctx context.Context, arguments string) (Result, error) {
	var req fs.ListRequest
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &req); err != nil {
			return Result{}, fmt.Errorf("decode ls arguments: %w",
				err)
		}
	}

	text, err := fs.List(ctx, req)
	if err != nil {
		return Result{}, err
	}

	return Result{Text: text}, nil
}

// readTool wraps the pure-Go filesystem read operation as a model tool.
type readTool struct{}

// Spec returns the model-facing schema for the read tool.
func (readTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: NameRead,
		Description: "Read a local text file. Output is bounded by lines " +
			"and bytes; use offset and limit to continue through large " +
			"files.",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{
					"type":"string",
					"description":"File to read. Relative paths resolve from the current working directory."
				},
				"offset":{
					"type":"integer",
					"description":"1-indexed line number to start reading from."
				},
				"limit":{
					"type":"integer",
					"description":"Maximum lines to return before the default truncation limit is considered."
				}
			},
			"required":["path"]
		}`),
	}
}

// Execute decodes read arguments and returns bounded file content.
func (readTool) Execute(ctx context.Context, arguments string) (Result, error) {
	var req fs.ReadRequest
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &req); err != nil {
			return Result{}, fmt.Errorf("decode read arguments: %w",
				err)
		}
	}

	text, err := fs.Read(ctx, req)
	if err != nil {
		return Result{}, err
	}

	return Result{Text: text}, nil
}

// writeTool wraps the pure-Go filesystem write operation as a model tool.
type writeTool struct{}

// Spec returns the model-facing schema for the write tool.
func (writeTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: NameWrite,
		Description: "Create or completely overwrite a local text file. " +
			"Use this for new files or full rewrites; use edit for " +
			"surgical changes once that tool is available.",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{
					"type":"string",
					"description":"File to create or completely overwrite. Relative paths resolve from the current working directory."
				},
				"content":{
					"type":"string",
					"description":"The complete desired file content."
				}
			},
			"required":["path","content"]
		}`),
	}
}

// Execute decodes write arguments and performs a whole-file replacement.
func (writeTool) Execute(ctx context.Context, arguments string) (Result,
	error) {

	var req fs.WriteRequest
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &req); err != nil {
			return Result{}, fmt.Errorf("decode write "+
				"arguments: %w", err)
		}
	}

	text, err := fs.Write(ctx, req)
	if err != nil {
		return Result{}, err
	}

	return Result{Text: text}, nil
}
