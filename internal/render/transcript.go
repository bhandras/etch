package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"harness/internal/session"
)

// MessageLines returns human-readable transcript lines for one session message.
func MessageLines(message session.MessageData) []string {
	text := MessageText(message)
	switch message.Role {
	case session.RoleAssistant:
		if text != "" {
			return []string{"assistant: " + text}
		}

		var lines []string
		for _, call := range message.ToolCalls {
			lines = append(lines, ToolCallLines(call)...)
		}

		return lines

	case session.RoleTool:
		return ToolResultLines(message.Name, text)

	default:
		return []string{message.Role + ": " + text}
	}
}

// MessageText joins text content parts for human transcript rendering.
func MessageText(message session.MessageData) string {
	var parts []string
	for _, part := range message.Content {
		if part.Type == session.ContentText {
			parts = append(parts, part.Text)
		}
	}

	return strings.Join(parts, "")
}

// ToolCallLines returns a compact human rendering for one tool invocation.
func ToolCallLines(call session.ToolCallData) []string {
	switch call.Name {
	case "ls":
		return []string{
			"-> ls " + stringArg(call.Arguments, "path", "."),
		}

	case "read":
		return []string{"-> read " + readTarget(call.Arguments)}

	case "write":
		return []string{
			"-> write " + stringArg(call.Arguments, "path", ""),
		}

	case "edit":
		return []string{fmt.Sprintf("-> edit %s%s",
			stringArg(call.Arguments, "path", ""),
			editCountSuffix(call.Arguments))}

	case "bash":
		return []string{
			"-> bash " + stringArg(call.Arguments, "command", ""),
		}

	default:
		return []string{fmt.Sprintf("-> %s %s", call.Name,
			strings.TrimSpace(call.Arguments))}
	}
}

// ToolResultLines returns a human rendering for one tool result.
func ToolResultLines(name string, text string) []string {
	switch name {
	case "edit":
		return prefixedBlockLines("   ", text)

	case "write":
		return prefixedBlockLines("   ", text)

	case "bash":
		return prefixedBlockLines("   ", text)

	case "read":
		return summarizeRead(text)

	default:
		return prefixedBlockLines("   ", text)
	}
}

// stringArg reads a string field from a raw JSON argument object.
func stringArg(raw string, name string, fallback string) string {
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return fallback
	}
	value, ok := args[name]
	if !ok {
		return fallback
	}

	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return fallback
	}
	if strings.TrimSpace(text) == "" {
		return fallback
	}

	return text
}

// intArg reads an integer field from a raw JSON argument object.
func intArg(raw string, name string) (int, bool) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return 0, false
	}
	value, ok := args[name]
	if !ok {
		return 0, false
	}

	var number int
	if err := json.Unmarshal(value, &number); err != nil {
		return 0, false
	}

	return number, true
}

// readTarget renders a read call target with optional line range hints.
func readTarget(raw string) string {
	path := stringArg(raw, "path", "")
	if path == "" {
		path = "<missing path>"
	}

	offset, hasOffset := intArg(raw, "offset")
	limit, hasLimit := intArg(raw, "limit")
	if !hasOffset && !hasLimit {
		return path
	}
	if offset <= 0 {
		offset = 1
	}
	if hasLimit && limit > 0 {
		return fmt.Sprintf("%s lines %d-%d", path, offset,
			offset+limit-1)
	}

	return fmt.Sprintf("%s from line %d", path, offset)
}

// editCountSuffix renders the number of replacement blocks when available.
func editCountSuffix(raw string) string {
	var args struct {
		Edits []json.RawMessage `json:"edits"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return ""
	}
	if len(args.Edits) == 0 {
		return ""
	}
	if len(args.Edits) == 1 {
		return " (1 replacement)"
	}

	return fmt.Sprintf(" (%d replacements)", len(args.Edits))
}

// prefixedBlockLines indents each line of text with prefix.
func prefixedBlockLines(prefix string, text string) []string {
	if text == "" {
		return []string{prefix + "(empty result)"}
	}

	rawLines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if line == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, prefix+line)
	}

	return lines
}

// summarizeRead returns a compact human summary for read results.
func summarizeRead(text string) []string {
	lineCount := 0
	if text != "" {
		lineCount = len(
			strings.Split(
				strings.TrimRight(text, "\n"),
				"\n",
			),
		)
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("   read %d lines", lineCount))
	if continuation := readContinuation(text); continuation != "" {
		lines = append(lines, "   "+continuation)
	}

	return lines
}

// readContinuation extracts the continuation hint emitted by read results.
func readContinuation(text string) string {
	start := strings.LastIndex(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end <= start {
		return ""
	}
	hint := text[start : end+1]
	if !strings.Contains(hint, "offset=") &&
		!strings.Contains(hint, "more") {
		return ""
	}

	return hint
}
