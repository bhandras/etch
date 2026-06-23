package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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

	// NameFind is the model-facing name for recursive path discovery.
	NameFind = "find"

	// NameGrep is the model-facing name for literal text search.
	NameGrep = "grep"

	// NameWrite is the model-facing name for the whole-file writing tool.
	NameWrite = "write"

	// NameEdit is the model-facing name for the exact replacement edit
	// tool.
	NameEdit = "edit"

	// NameBash is the model-facing name for bounded bash command execution.
	NameBash = "bash"

	// NameTask is the model-facing name for configured subagent delegation.
	NameTask = "task"
)

const (
	// ParallelSafetySerial marks tools that must run as serial barriers.
	ParallelSafetySerial = "serial"

	// ParallelSafetyReadOnly marks tools that read local state without
	// mutating it and may overlap other safe reads.
	ParallelSafetyReadOnly = "read_only"

	// ParallelSafetyParallel marks tools that are independently safe to
	// overlap with other safe calls.
	ParallelSafetyParallel = "parallel_safe"
)

// toolNamePattern is the provider-compatible model tool name subset Harness
// accepts for built-ins and plugins.
var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Result is the text returned by a builtin tool execution.
type Result struct {
	// Text is the model-visible tool output.
	Text string `json:"text"`
}

// ExecutionContext describes the session and parent call around a tool run.
type ExecutionContext struct {
	// SessionID is the durable session currently executing the tool.
	SessionID string

	// SessionPath is the JSONL log path for the current session.
	SessionPath string

	// AssistantEventID is the assistant event that requested this tool.
	AssistantEventID string

	// ToolCallID is the provider-assigned tool call identifier.
	ToolCallID string

	// Progress receives ephemeral status updates for this tool call.
	Progress ProgressSink
}

// executionContextKey stores ExecutionContext values in context.Context.
type executionContextKey struct{}

// ProgressEvent is one ephemeral tool progress update for live UIs.
type ProgressEvent struct {
	// ToolCallID links progress to the parent model tool call.
	ToolCallID string

	// Message is the compact human-readable activity label.
	Message string

	// Usage carries optional provider token counters reported by a
	// long-running tool, such as a child agent.
	Usage model.Usage

	// Metrics carries optional provider transport counters reported by a
	// long-running tool, such as a child agent.
	Metrics model.Metrics
}

// ProgressSink receives live progress updates for one running tool.
type ProgressSink func(ProgressEvent)

// Tool executes one model-callable builtin operation.
type Tool interface {
	// Spec returns the model-facing tool schema.
	Spec() model.ToolSpec

	// Execute runs the tool with raw JSON arguments.
	Execute(ctx context.Context, arguments string) (Result, error)
}

// WithExecutionContext returns ctx annotated with tool execution metadata.
func WithExecutionContext(ctx context.Context,
	meta ExecutionContext) context.Context {

	return context.WithValue(ctx, executionContextKey{}, meta)
}

// ExecutionContextFrom returns tool execution metadata from ctx when present.
func ExecutionContextFrom(ctx context.Context) (ExecutionContext, bool) {
	meta, ok := ctx.Value(executionContextKey{}).(ExecutionContext)

	return meta, ok
}

// ReportProgress sends one ephemeral progress update when ctx carries a sink.
func ReportProgress(ctx context.Context, message string) {
	meta, ok := ExecutionContextFrom(ctx)
	if !ok || meta.Progress == nil || strings.TrimSpace(message) == "" {
		return
	}
	meta.Progress(ProgressEvent{
		ToolCallID: meta.ToolCallID,
		Message:    message,
	})
}

// CallExecutor executes a tool with access to the complete model call.
type CallExecutor interface {
	// ExecuteCall runs the tool with the provider-assigned call metadata.
	ExecuteCall(ctx context.Context, call model.ToolCall) (Result, error)
}

// ParallelSafetyChecker lets stateful tools decide whether one concrete call
// may run inside a parallel execution group.
type ParallelSafetyChecker interface {
	// ParallelSafe reports whether call may overlap other parallel-safe
	// calls under the tool's own concurrency and isolation rules.
	ParallelSafe(call model.ToolCall) bool
}

// ParallelLaneChecker lets tools serialize calls that share a stateful local
// resource while still overlapping unrelated parallel-safe tools.
type ParallelLaneChecker interface {
	// ParallelLane returns a non-empty lane key for calls that must not
	// overlap each other inside a parallel execution group.
	ParallelLane(call model.ToolCall) string
}

// AvailabilityChecker lets stateful tools hide themselves from future model
// requests after a fatal local failure.
type AvailabilityChecker interface {
	// Available reports whether this tool should still be advertised.
	Available() bool
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
	registry.Register(findTool{})
	registry.Register(grepTool{})
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

// RegisterStrict adds one tool and reports an error instead of replacing an
// existing tool with the same model-facing name.
func (r *Registry) RegisterStrict(tool Tool) error {
	name := tool.Spec().Name
	if r.Has(name) {
		return fmt.Errorf("tool %q is already registered", name)
	}
	r.Register(tool)

	return nil
}

// Has reports whether a model-facing tool name is already registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]

	return ok
}

// Subset returns a registry containing only the requested existing tool names.
func (r *Registry) Subset(names []string) (*Registry, []string) {
	subset := NewRegistry()
	var missing []string
	for _, name := range names {
		registered, ok := r.tools[name]
		if !ok {
			missing = append(missing, name)

			continue
		}
		subset.Register(registered)
	}

	return subset, missing
}

// Names returns deterministic model-facing tool names in the registry.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// Specs returns deterministic model-facing tool schemas.
func (r *Registry) Specs() []model.ToolSpec {
	specs := make([]model.ToolSpec, 0, len(r.tools))
	for _, tool := range r.tools {
		if checker, ok := tool.(AvailabilityChecker); ok &&
			!checker.Available() {

			continue
		}
		specs = append(specs, tool.Spec())
	}
	sort.Slice(specs, func(i, j int) bool {
		return specs[i].Name < specs[j].Name
	})

	return specs
}

// ParallelSafe reports whether call may run in a concurrent execution group.
func (r *Registry) ParallelSafe(call model.ToolCall) bool {
	registered, ok := r.tools[call.Name]
	if !ok {
		return false
	}
	if checker, ok := registered.(ParallelSafetyChecker); ok {
		return checker.ParallelSafe(call)
	}

	return ReadOnlyToolName(call.Name)
}

// ParallelLane returns a stateful serialization key for a parallel-safe call.
func (r *Registry) ParallelLane(call model.ToolCall) string {
	registered, ok := r.tools[call.Name]
	if !ok {
		return ""
	}
	if checker, ok := registered.(ParallelLaneChecker); ok {
		return checker.ParallelLane(call)
	}

	return ""
}

// Execute dispatches a complete model tool call to its registered tool.
func (r *Registry) Execute(ctx context.Context, call model.ToolCall) (Result,
	error) {

	tool, ok := r.tools[call.Name]
	if !ok {
		return Result{}, fmt.Errorf("unknown tool %q", call.Name)
	}
	if checker, ok := tool.(AvailabilityChecker); ok &&
		!checker.Available() {
		return Result{}, fmt.Errorf("tool %q is unavailable", call.Name)
	}

	if executor, ok := tool.(CallExecutor); ok {
		return executor.ExecuteCall(ctx, call)
	}

	return tool.Execute(ctx, call.Arguments)
}

// ReadOnlyToolName reports whether a model-facing tool name is read-only by
// convention when no stateful tool provides stricter per-call safety.
func ReadOnlyToolName(name string) bool {
	switch name {
	case NameLS, NameRead, NameFind, NameGrep:
		return true

	default:
		return strings.HasPrefix(name, "go_")
	}
}

// ValidName reports whether name is accepted by supported model providers.
func ValidName(name string) bool {
	return name == strings.TrimSpace(name) &&
		toolNamePattern.MatchString(name)
}

// ValidateName returns a user-facing error when name cannot be registered as a
// model tool.
func ValidateName(name string) error {
	if ValidName(name) {
		return nil
	}

	return fmt.Errorf("tool name %q must match %s", name,
		toolNamePattern.String())
}

// ParallelSafetyAllowed reports whether safety is a supported plugin tool
// declaration.
func ParallelSafetyAllowed(safety string) bool {
	switch strings.TrimSpace(safety) {
	case "", ParallelSafetySerial, ParallelSafetyReadOnly,
		ParallelSafetyParallel:
		return true

	default:
		return false
	}
}

// ParallelSafetyIsSafe reports whether a declared safety class can run in a
// parallel-safe execution group.
func ParallelSafetyIsSafe(safety string) bool {
	switch strings.TrimSpace(safety) {
	case ParallelSafetyReadOnly, ParallelSafetyParallel:
		return true

	default:
		return false
	}
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
		Description: "Read one local text file or several independent " +
			"file ranges. Output is bounded by lines and bytes; use " +
			"offset and limit for large files. When you already know " +
			"multiple relevant files or ranges, pass files=[...] in " +
			"one call instead of making repeated read calls. Prefer " +
			"grep/find/go symbol tools to locate relevant ranges " +
			"before reading broad files, and do not reread the same " +
			"range.",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{
					"type":"string",
					"description":"Single file to read. Relative paths resolve from the current working directory. If files is non-empty, files wins and this top-level path is ignored."
				},
				"offset":{
					"type":"integer",
					"description":"1-indexed line number to start reading from for a single-file read."
				},
				"limit":{
					"type":"integer",
					"description":"Maximum lines to return for a single-file read before the default truncation limit is considered."
				},
				"lineNumbers":{
					"type":"boolean",
					"description":"Whether to prefix each returned line with its source line number. Defaults to true."
				},
				"files":{
					"type":"array",
					"description":"Independent file ranges to read in one call. Use this for multiple known files or offsets instead of repeated read calls. At most 8 entries are accepted. When files is non-empty, top-level path/offset/limit are ignored.",
					"items":{
						"type":"object",
						"properties":{
							"path":{
								"type":"string",
								"description":"File to read."
							},
							"offset":{
								"type":"integer",
								"description":"1-indexed line number to start reading from."
							},
							"limit":{
								"type":"integer",
								"description":"Maximum lines to return for this file."
							},
							"lineNumbers":{
								"type":"boolean",
								"description":"Whether to prefix lines from this file with source line numbers. Defaults to the top-level value, then true."
							}
						},
						"required":["path"]
					}
				}
			}
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

// findTool wraps the pure-Go recursive path search operation as a model tool.
type findTool struct{}

// Spec returns the model-facing schema for the find tool.
func (findTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: NameFind,
		Description: "Find files and directories recursively by " +
			"case-insensitive path substring. Use this to discover " +
			"project files without relying on external fd/find binaries.",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{
					"type":"string",
					"description":"Directory or file where searching starts. Defaults to the current directory."
				},
				"query":{
					"type":"string",
					"description":"Case-insensitive substring matched against relative paths. Empty returns all non-internal paths."
				},
				"glob":{
					"type":"string",
					"description":"Optional slash-separated glob filter such as *.go, **/*_test.go, or cmd/**/*.go."
				},
				"limit":{
					"type":"integer",
					"description":"Maximum matches to return. Defaults to 500."
				}
			}
		}`),
	}
}

// Execute decodes find arguments and returns matching paths.
func (findTool) Execute(ctx context.Context, arguments string) (Result, error) {
	var req fs.FindRequest
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &req); err != nil {
			return Result{}, fmt.Errorf("decode find arguments: %w",
				err)
		}
	}

	text, err := fs.Find(ctx, req)
	if err != nil {
		return Result{}, err
	}

	return Result{Text: text}, nil
}

// grepTool wraps the pure-Go literal text search operation as a model tool.
type grepTool struct{}

// Spec returns the model-facing schema for the grep tool.
func (grepTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: NameGrep,
		Description: "Search files recursively for literal text and " +
			"return path:line:text matches. Use regex=true for Go " +
			"RE2 patterns. Prefer speculative grep searches over " +
			"sequential find+read scans when locating symbols, " +
			"errors, TODOs, config keys, or cross-file strings; " +
			"then use read with files=[...] for the relevant ranges. " +
			"This tool respects .gitignore and does not require " +
			"external rg/grep.",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{
					"type":"string",
					"description":"File or directory where searching starts. Defaults to the current directory. For multiple roots, use paths instead of space-separated text."
				},
				"paths":{
					"type":"array",
					"description":"Files or directories to search in one call. Use this for multiple known roots such as [\"cmd/harness\",\"internal/config\"]. When non-empty, paths wins over path. At most 8 entries are accepted.",
					"items":{
						"type":"string"
					}
				},
				"pattern":{
					"type":"string",
					"description":"Non-empty literal text to search for unless regex is true."
				},
				"regex":{
					"type":"boolean",
					"description":"Treat pattern as Go RE2 regular expression syntax. Literal search is the default."
				},
				"glob":{
					"type":"string",
					"description":"Optional slash-separated file glob such as *.go, **/*_test.go, or cmd/**/*.go."
				},
				"context":{
					"type":"integer",
					"description":"Context lines before and after each match. Values above 5 are clamped."
				},
				"limit":{
					"type":"integer",
					"description":"Maximum total matches to return. Defaults to 100."
				},
				"ignoreCase":{
					"type":"boolean",
					"description":"Whether to match case-insensitively."
				}
			},
			"required":["pattern"]
		}`),
	}
}

// Execute decodes grep arguments and returns literal text matches.
func (grepTool) Execute(ctx context.Context, arguments string) (Result, error) {
	var req fs.GrepRequest
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &req); err != nil {
			return Result{}, fmt.Errorf("decode grep arguments: %w",
				err)
		}
	}

	text, err := fs.Grep(ctx, req)
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
				},
				"dryRun":{
					"type":"boolean",
					"description":"When true, validate the replacements and return the diff without modifying the file."
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
