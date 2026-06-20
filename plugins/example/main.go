package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// protocolVersion is the Harness plugin protocol version this example
	// understands.
	protocolVersion = "0.1.0"

	// methodInitialize is the startup method Harness calls to discover
	// plugin metadata and tool schemas.
	methodInitialize = "initialize"

	// methodToolExecute is the request method Harness calls for one tool
	// execution.
	methodToolExecute = "tool.execute"

	// toolPluginEcho echoes text and basic text statistics.
	toolPluginEcho = "plugin_echo"

	// toolProjectFiles summarizes files under a local directory.
	toolProjectFiles = "project_files"

	// defaultProjectFileLimit bounds project_files when no limit is given.
	defaultProjectFileLimit = 500

	// maxProjectFileLimit prevents accidental giant directory walks.
	maxProjectFileLimit = 5000
)

// request is one JSONL request received from Harness.
type request struct {
	// ID correlates the request with the plugin response.
	ID string `json:"id"`

	// Method names the requested plugin operation.
	Method string `json:"method"`

	// Params stores the method-specific JSON object.
	Params json.RawMessage `json:"params,omitempty"`
}

// response is one JSONL response written back to Harness.
type response struct {
	// ID correlates this response with a Harness request.
	ID string `json:"id"`

	// Result stores the method-specific success object.
	Result any `json:"result,omitempty"`

	// Error stores a protocol or tool failure.
	Error *responseError `json:"error,omitempty"`
}

// responseError describes one plugin failure.
type responseError struct {
	// Message explains the failure in human-readable text.
	Message string `json:"message"`
}

// initializeParams stores the protocol version advertised by Harness.
type initializeParams struct {
	// ProtocolVersion is the highest protocol version supported by Harness.
	ProtocolVersion string `json:"protocolVersion"`
}

// initializeResult describes this plugin's available tools.
type initializeResult struct {
	// Name is the display name Harness can use in diagnostics.
	Name string `json:"name"`

	// Tools are model-callable tool schemas exposed by this plugin.
	Tools []toolSpec `json:"tools"`
}

// toolSpec describes one model-callable plugin tool.
type toolSpec struct {
	// Name is the model-facing tool identifier.
	Name string `json:"name"`

	// Description tells the model when to call the tool.
	Description string `json:"description"`

	// Parameters is a JSON Schema object for the tool arguments.
	Parameters any `json:"parameters"`
}

// toolExecuteParams stores one tool invocation from Harness.
type toolExecuteParams struct {
	// CallID is the provider-assigned tool call identifier.
	CallID string `json:"callID"`

	// Name is the model-facing tool name to execute.
	Name string `json:"name"`

	// Arguments stores the raw tool argument object.
	Arguments json.RawMessage `json:"arguments"`
}

// toolExecuteResult stores model-visible content after a tool call.
type toolExecuteResult struct {
	// Content is the ordered list of model-visible output parts.
	Content []contentPart `json:"content"`
}

// contentPart is one output part returned to Harness.
type contentPart struct {
	// Type identifies how Text should be interpreted.
	Type string `json:"type"`

	// Text stores plain text output.
	Text string `json:"text"`
}

// echoArgs stores plugin_echo arguments.
type echoArgs struct {
	// Text is the text returned by plugin_echo.
	Text string `json:"text"`
}

// projectFilesArgs stores project_files arguments.
type projectFilesArgs struct {
	// Path is the directory to inspect. Empty means the current directory.
	Path string `json:"path"`

	// Limit bounds how many filesystem entries are visited.
	Limit int `json:"limit"`
}

// projectStats accumulates the filesystem summary returned by project_files.
type projectStats struct {
	root       string
	files      int
	dirs       int
	bytes      int64
	visited    int
	truncated  bool
	extensions map[string]int
	samples    []string
}

// extensionCount stores one extension frequency row for sorting.
type extensionCount struct {
	name  string
	count int
}

// main serves plugin requests from stdin until EOF.
func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		handleLine(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "read request: %v\n", err)
	}
}

// handleLine decodes and dispatches one JSONL protocol request.
func handleLine(line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(
			response{
				ID: "",
				Error: &responseError{
					Message: "decode request: " +
						err.Error(),
				},
			},
		)

		return
	}

	switch req.Method {
	case methodInitialize:
		handleInitialize(req)

	case methodToolExecute:
		handleToolExecute(req)

	default:
		writeResponse(response{
			ID: req.ID,
			Error: &responseError{
				Message: "unknown method " + req.Method,
			},
		})
	}
}

// handleInitialize validates the protocol version and returns tool schemas.
func handleInitialize(req request) {
	var params initializeParams
	if len(req.Params) != 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeError(
				req.ID,
				"decode initialize params: "+err.Error(),
			)

			return
		}
	}
	if params.ProtocolVersion != "" &&
		params.ProtocolVersion != protocolVersion {

		writeError(
			req.ID, "unsupported protocol version "+
				params.ProtocolVersion,
		)

		return
	}

	writeResponse(response{
		ID: req.ID,
		Result: initializeResult{
			Name: "example",
			Tools: []toolSpec{
				pluginEchoSpec(),
				projectFilesSpec(),
			},
		},
	})
}

// handleToolExecute decodes one tool execution request and runs the tool.
func handleToolExecute(req request) {
	var params toolExecuteParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(req.ID, "decode tool params: "+err.Error())

		return
	}

	var text string
	var err error
	switch params.Name {
	case toolPluginEcho:
		text, err = runPluginEcho(params.Arguments)

	case toolProjectFiles:
		text, err = runProjectFiles(params.Arguments)

	default:
		err = fmt.Errorf("unknown tool %s", params.Name)
	}
	if err != nil {
		writeError(req.ID, err.Error())

		return
	}

	writeResponse(response{
		ID: req.ID,
		Result: toolExecuteResult{
			Content: []contentPart{{
				Type: "text",
				Text: text,
			}},
		},
	})
}

// pluginEchoSpec returns the schema for the plugin_echo tool.
func pluginEchoSpec() toolSpec {
	return toolSpec{
		Name: toolPluginEcho,
		Description: "Echo text through the example plugin and report " +
			"small text statistics. Use this for plugin smoke tests.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Text to echo.",
				},
			},
			"required": []string{
				"text",
			},
		},
	}
}

// projectFilesSpec returns the schema for the project_files tool.
func projectFilesSpec() toolSpec {
	return toolSpec{
		Name: toolProjectFiles,
		Description: "Summarize file counts, byte size, common " +
			"extensions, and sample paths under a local directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type": "string",
					"description": "Directory to inspect. " +
						"Defaults to the current directory.",
				},
				"limit": map[string]any{
					"type": "integer",
					"description": "Maximum entries to visit. " +
						"Defaults to 500 and caps at 5000.",
				},
			},
		},
	}
}

// runPluginEcho echoes text and includes dependency-free text statistics.
func runPluginEcho(raw json.RawMessage) (string, error) {
	var args echoArgs
	if err := decodeArguments(raw, &args); err != nil {
		return "", err
	}

	return fmt.Sprintf("%s\n\nbytes: %d\nrunes: %d\nwords: %d",
		args.Text, len(args.Text),
		utf8.RuneCountInString(args.Text), countWords(args.Text)), nil
}

// runProjectFiles walks a directory and returns a compact project summary.
func runProjectFiles(raw json.RawMessage) (string, error) {
	var args projectFilesArgs
	if err := decodeArguments(raw, &args); err != nil {
		return "", err
	}
	root := strings.TrimSpace(args.Path)
	if root == "" {
		root = "."
	}
	limit := normalizeLimit(args.Limit)

	stats := projectStats{
		root:       root,
		extensions: make(map[string]int),
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry,
		walkErr error) error {

		if walkErr != nil {
			return walkErr
		}
		if shouldSkip(path, entry) {
			return filepath.SkipDir
		}
		if stats.visited >= limit {
			stats.truncated = true

			return filepath.SkipAll
		}
		stats.visited++
		rel := relativePath(root, path)
		if entry.IsDir() {
			stats.dirs++
		} else {
			stats.files++
			info, err := entry.Info()
			if err != nil {
				return err
			}
			stats.bytes += info.Size()
			stats.extensions[fileExtension(path)]++
		}
		if rel != "." && len(stats.samples) < 12 {
			stats.samples = append(stats.samples, rel)
		}

		return nil
	})
	if err != nil {
		return "", err
	}

	return formatProjectStats(stats), nil
}

// decodeArguments unmarshals a possibly empty JSON object into out.
func decodeArguments(raw json.RawMessage, out any) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}

	return nil
}

// normalizeLimit returns the bounded filesystem walk limit.
func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultProjectFileLimit
	}
	if limit > maxProjectFileLimit {
		return maxProjectFileLimit
	}

	return limit
}

// shouldSkip reports whether a directory should be omitted from summaries.
func shouldSkip(path string, entry fs.DirEntry) bool {
	if !entry.IsDir() {
		return false
	}
	name := entry.Name()
	if path == "." || name == "." {
		return false
	}
	switch name {
	case ".git", ".harness", "bin", "vendor", "node_modules":
		return true

	default:
		return false
	}
}

// relativePath returns a stable slash-separated path for output.
func relativePath(root string, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}

	return filepath.ToSlash(rel)
}

// fileExtension returns the lowercase extension bucket for a file path.
func fileExtension(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "[none]"
	}

	return ext
}

// formatProjectStats renders the project_files output.
func formatProjectStats(stats projectStats) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "path: %s\n", stats.root)
	fmt.Fprintf(&builder, "visited: %d", stats.visited)
	if stats.truncated {
		fmt.Fprintf(&builder, " (truncated)")
	}
	fmt.Fprintln(&builder)
	fmt.Fprintf(&builder, "directories: %d\n", stats.dirs)
	fmt.Fprintf(&builder, "files: %d\n", stats.files)
	fmt.Fprintf(&builder, "bytes: %d\n", stats.bytes)

	extensions := sortedExtensions(stats.extensions)
	if len(extensions) > 0 {
		fmt.Fprintln(&builder, "\nextensions:")
		for _, ext := range extensions {
			fmt.Fprintf(&builder, "- %s: %d\n", ext.name, ext.count)
		}
	}
	if len(stats.samples) > 0 {
		fmt.Fprintln(&builder, "\nsamples:")
		for _, sample := range stats.samples {
			fmt.Fprintf(&builder, "- %s\n", sample)
		}
	}
	fmt.Fprintf(
		&builder, "\ngenerated: %s", time.Now().UTC().Format(
			time.RFC3339,
		),
	)

	return builder.String()
}

// sortedExtensions returns extension buckets sorted by count then name.
func sortedExtensions(counts map[string]int) []extensionCount {
	extensions := make([]extensionCount, 0, len(counts))
	for name, count := range counts {
		extensions = append(extensions, extensionCount{
			name:  name,
			count: count,
		})
	}
	sort.Slice(extensions, func(i, j int) bool {
		if extensions[i].count == extensions[j].count {
			return extensions[i].name < extensions[j].name
		}

		return extensions[i].count > extensions[j].count
	})
	if len(extensions) > 8 {
		extensions = extensions[:8]
	}

	return extensions
}

// countWords returns a Unicode-aware word count for text.
func countWords(text string) int {
	inWord := false
	words := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			inWord = false
			continue
		}
		if !inWord {
			words++
			inWord = true
		}
	}

	return words
}

// writeError writes one failed protocol response.
func writeError(id string, message string) {
	writeResponse(response{
		ID:    id,
		Error: &responseError{Message: message},
	})
}

// writeResponse writes one JSONL protocol response to stdout.
func writeResponse(resp response) {
	encoded, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode response: %v\n", err)

		return
	}
	fmt.Fprintln(os.Stdout, string(encoded))
}
