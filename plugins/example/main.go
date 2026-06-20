package main

import (
	"context"
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

	"harness/sdk"
)

const (
	// toolPluginEcho echoes text and basic text statistics.
	toolPluginEcho = "plugin_echo"

	// toolProjectFiles summarizes files under a local directory.
	toolProjectFiles = "project_files"

	// defaultProjectFileLimit bounds project_files when no limit is given.
	defaultProjectFileLimit = 500

	// maxProjectFileLimit prevents accidental giant directory walks.
	maxProjectFileLimit = 5000
)

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

// main serves the example plugin protocol until stdin closes.
func main() {
	if err := sdk.ServePlugin(sdk.Plugin{
		Name: "example",
		Tools: []sdk.Tool{
			pluginEchoSpec(),
			projectFilesSpec(),
		},
	}); err != nil {

		fmt.Fprintln(os.Stderr, err)
	}
}

// pluginEchoSpec returns the schema for the plugin_echo tool.
func pluginEchoSpec() sdk.Tool {
	return sdk.Tool{
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
		Handler: handlePluginEcho,
	}
}

// projectFilesSpec returns the schema for the project_files tool.
func projectFilesSpec() sdk.Tool {
	return sdk.Tool{
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
		Handler: handleProjectFiles,
	}
}

// handlePluginEcho executes plugin_echo through the SDK handler interface.
func handlePluginEcho(ctx context.Context,
	call sdk.ToolCall) (sdk.ToolResult, error) {

	if err := ctx.Err(); err != nil {
		return sdk.ToolResult{}, err
	}
	text, err := runPluginEcho(call.Arguments)
	if err != nil {
		return sdk.ToolResult{}, err
	}

	return sdk.TextResult(text), nil
}

// handleProjectFiles executes project_files through the SDK handler interface.
func handleProjectFiles(ctx context.Context,
	call sdk.ToolCall) (sdk.ToolResult, error) {

	if err := ctx.Err(); err != nil {
		return sdk.ToolResult{}, err
	}
	text, err := runProjectFiles(call.Arguments)
	if err != nil {
		return sdk.ToolResult{}, err
	}

	return sdk.TextResult(text), nil
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
