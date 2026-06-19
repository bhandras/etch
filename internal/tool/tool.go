package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"harness/internal/model"
	fs "harness/internal/tools/fs"
	"harness/internal/tools/shell"
)

const (
	// NameLS is the model-facing name for the directory listing tool.
	NameLS = "ls"

	// NameRead is the model-facing name for the text file reading tool.
	NameRead = "read"

	// NameWrite is the model-facing name for the whole-file writing tool.
	NameWrite = "write"

	// NameEdit is the model-facing name for the exact replacement edit
	// tool.
	NameEdit = "edit"

	// NameBash is the model-facing name for bounded bash command execution.
	NameBash = "bash"
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
	registry.Register(editTool{})
	registry.Register(bashTool{})

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
			"Use this for new files, empty files, or full rewrites; use " +
			"edit for surgical changes to existing non-empty content.",
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

// editTool wraps the pure-Go exact replacement operation as a model tool.
type editTool struct{}

// Spec returns the model-facing schema for the edit tool.
func (editTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: NameEdit,
		Description: "Edit one existing text file using exact text " +
			"replacement. Each oldText must appear exactly once in the " +
			"original file. To add a line, replace a unique neighboring " +
			"block with the same block plus the new line. Use write for " +
			"empty files or full rewrites.",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{
					"type":"string",
					"description":"Existing file to edit. Relative paths resolve from the current working directory."
				},
				"edits":{
					"type":"array",
					"description":"Exact replacements matched against the original file before any replacement is applied.",
					"items":{
						"type":"object",
						"properties":{
							"oldText":{
								"type":"string",
								"minLength":1,
								"description":"Non-empty exact text to replace. Must include non-whitespace context and enough surrounding text to make it unique."
							},
							"newText":{
								"type":"string",
								"description":"Exact replacement text."
							}
						},
						"required":["oldText","newText"]
					}
				}
			},
			"required":["path","edits"]
		}`),
	}
}

// Execute decodes edit arguments and applies exact replacements.
func (editTool) Execute(ctx context.Context, arguments string) (Result, error) {
	var req fs.EditRequest
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &req); err != nil {
			return Result{}, fmt.Errorf("decode edit arguments: %w",
				err)
		}
	}

	text, err := fs.EditFile(ctx, req)
	if err != nil {
		return Result{}, err
	}

	return Result{Text: text}, nil
}

// bashTool wraps bounded local bash execution as a model tool.
type bashTool struct{}

// Spec returns the model-facing schema for the bash tool.
func (bashTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: NameBash,
		Description: "Run a local bash command in the current working " +
			"directory with a timeout and capped stdout/stderr. Use this " +
			"for verification commands such as tests and build checks.",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"command":{
					"type":"string",
					"description":"Bash command line to execute."
				},
				"timeoutSeconds":{
					"type":"integer",
					"description":"Optional timeout in seconds. Defaults to 30 and is capped at 120."
				}
			},
			"required":["command"]
		}`),
	}
}

// Execute decodes bash arguments and runs a bounded local command.
func (bashTool) Execute(ctx context.Context, arguments string) (Result, error) {
	var req shell.RunRequest
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &req); err != nil {
			return Result{}, fmt.Errorf("decode bash arguments: %w",
				err)
		}
	}

	text, err := shell.Run(ctx, req)
	if err != nil {
		return Result{}, err
	}

	return Result{Text: text}, nil
}
