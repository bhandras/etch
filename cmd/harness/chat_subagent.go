package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"harness/internal/model"
	"harness/internal/render"
	"harness/internal/session"
	"harness/internal/tool"
)

// subagentCallDisplay stores the terminal-facing fields from a task tool call.
type subagentCallDisplay struct {
	// Profile is the configured child-agent profile name.
	Profile string

	// Task is the delegated child-agent instruction.
	Task string

	// Context is optional focused background passed to the child.
	Context string
}

// subagentResultDisplay stores the terminal-facing fields from a task result.
type subagentResultDisplay struct {
	// Profile is the configured child-agent profile name.
	Profile string

	// SessionID is the durable child session identifier.
	SessionID string

	// SessionPath is the durable child JSONL path.
	SessionPath string

	// Duration is the child run duration formatted by the task tool.
	Duration string

	// ModelCalls is the number of provider calls made by the child.
	ModelCalls string

	// ToolCalls is the number of tool calls made by the child.
	ToolCalls string

	// Result is the final assistant text produced by the child.
	Result string

	// Inspect is the shell command that shows the child transcript.
	Inspect string

	// Resume is the shell command that resumes the child transcript.
	Resume string
}

// liveToolCallLabel returns the concise line shown when a tool starts.
func liveToolCallLabel(call model.ToolCall) string {
	if call.Name == tool.NameTask {
		if display, ok := parseSubagentCall(call.Arguments); ok {
			return subagentCallLabel(display)
		}
	}

	return "Ran " + render.ToolCallText(session.ToolCallData{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: call.Arguments,
	})
}

// parseSubagentCall extracts the useful fields from task tool JSON arguments.
func parseSubagentCall(raw string) (subagentCallDisplay, bool) {
	var args struct {
		Profile string `json:"profile"`
		Task    string `json:"task"`
		Context string `json:"context"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return subagentCallDisplay{}, false
	}
	args.Profile = strings.TrimSpace(args.Profile)
	args.Task = strings.TrimSpace(args.Task)
	args.Context = strings.TrimSpace(args.Context)
	if args.Profile == "" && args.Task == "" {
		return subagentCallDisplay{}, false
	}

	return subagentCallDisplay{
		Profile: args.Profile,
		Task:    args.Task,
		Context: args.Context,
	}, true
}

// subagentCallLabel formats one task invocation as child-agent activity.
func subagentCallLabel(display subagentCallDisplay) string {
	profile := display.Profile
	if profile == "" {
		profile = "subagent"
	}
	task := compactOneLine(display.Task)
	if task == "" {
		return "Started subagent " + profile
	}

	return fmt.Sprintf("Started subagent %s: %s", profile,
		truncateRunes(task, 100))
}

// renderSubagentToolCall renders the full delegated task prompt.
func (r *liveChatRenderer) renderSubagentToolCall(call model.ToolCall) bool {
	if call.Name != tool.NameTask {
		return false
	}
	display, ok := parseSubagentCall(call.Arguments)
	if !ok {
		return false
	}

	r.renderSubagentCallDisplay(display)

	return true
}

// renderSubagentCallDisplay writes one subagent start block.
func (r *liveChatRenderer) renderSubagentCallDisplay(
	display subagentCallDisplay) {

	header := subagentStartHeader(display)
	fmt.Fprintln(r.stdout, "• "+header)
	if strings.TrimSpace(display.Task) != "" {
		fmt.Fprintln(r.stdout, r.style.muted("  Task:"))
		for _, line := range markdownLines(display.Task, r.style) {
			fmt.Fprintln(r.stdout, "  "+line)
		}
	}
	if strings.TrimSpace(display.Context) != "" {
		fmt.Fprintln(r.stdout, r.style.muted("  Context:"))
		for _, line := range markdownLines(display.Context, r.style) {
			fmt.Fprintln(r.stdout, "  "+line)
		}
	}
	r.redrawStatusLocked()
}

// subagentStartHeader returns the first visible task-call line.
func subagentStartHeader(display subagentCallDisplay) string {
	profile := display.Profile
	if profile == "" {
		profile = "subagent"
	}

	return "Started subagent " + profile
}

// subagentLiveLabel returns a compact row label for live child status.
func subagentLiveLabel(display subagentCallDisplay) string {
	profile := display.Profile
	if profile == "" {
		profile = "subagent"
	}
	task := compactOneLine(display.Task)
	if task == "" {
		return profile
	}

	return profile + " " + truncateRunes(task, 48)
}

// subagentCalls extracts display data for task calls in call order.
func subagentCalls(calls []model.ToolCall) []subagentCallDisplay {
	var displays []subagentCallDisplay
	for _, call := range calls {
		if call.Name != tool.NameTask {
			continue
		}
		display, ok := parseSubagentCall(call.Arguments)
		if ok {
			displays = append(displays, display)
		}
	}

	return displays
}

// nonSubagentToolCalls returns calls that should use generic tool rendering.
func nonSubagentToolCalls(calls []model.ToolCall) []model.ToolCall {
	var out []model.ToolCall
	for _, call := range calls {
		if call.Name == tool.NameTask {
			continue
		}
		out = append(out, call)
	}

	return out
}

// renderSubagentToolResult renders task output as a compact child-agent card.
func (r *liveChatRenderer) renderSubagentToolResult(
	message session.MessageData) bool {

	if message.Name != tool.NameTask {
		return false
	}
	display, ok := parseSubagentResult(render.MessageText(message))
	if !ok {
		return false
	}

	header := subagentResultHeader(display)
	fmt.Fprintln(r.stdout, "• "+header)
	for _, line := range subagentMetadataLines(display) {
		fmt.Fprintln(r.stdout, r.style.muted("  "+line))
	}
	if strings.TrimSpace(display.Result) != "" {
		fmt.Fprintln(r.stdout, r.style.muted("  Result:"))
		lines := markdownLines(display.Result, r.style)
		if len(lines) > liveSubagentResultLimit {
			remaining := len(lines) - liveSubagentResultLimit
			lines = append(
				append(
					[]string{},
					lines[:liveSubagentResultLimit]...,
				),
				fmt.Sprintf("... %d more lines", remaining),
			)
		}
		for _, line := range lines {
			fmt.Fprintln(r.stdout, "  "+line)
		}
	}
	r.redrawStatusLocked()

	return true
}

// parseSubagentResult extracts fields from the task tool's compact result.
func parseSubagentResult(text string) (subagentResultDisplay, bool) {
	var display subagentResultDisplay
	lines := strings.Split(text, "\n")
	resultStart := -1
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "Profile:"):
			display.Profile = trimField(line, "Profile:")

		case strings.HasPrefix(line, "Session:"):
			display.SessionID = trimField(line, "Session:")

		case strings.HasPrefix(line, "Session path:"):
			display.SessionPath = trimField(line, "Session path:")

		case strings.HasPrefix(line, "Duration:"):
			display.Duration = trimField(line, "Duration:")

		case strings.HasPrefix(line, "Model calls:"):
			display.ModelCalls = trimField(line, "Model calls:")

		case strings.HasPrefix(line, "Tool calls:"):
			display.ToolCalls = trimField(line, "Tool calls:")

		case strings.HasPrefix(line, "Inspect:"):
			display.Inspect = trimField(line, "Inspect:")

		case strings.HasPrefix(line, "Resume:"):
			display.Resume = trimField(line, "Resume:")

		case strings.TrimSpace(line) == "Result:":
			resultStart = i + 1
		}
	}
	if resultStart >= 0 {
		display.Result = collectSubagentResult(lines[resultStart:])
	}
	if display.Profile == "" && display.SessionID == "" &&
		display.Result == "" {
		return subagentResultDisplay{}, false
	}

	return display, true
}

// collectSubagentResult returns the result body before inspect/resume metadata.
func collectSubagentResult(lines []string) string {
	end := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(line, "Inspect:") ||
			strings.HasPrefix(line, "Resume:") {

			end = i

			break
		}
	}

	return strings.TrimSpace(strings.Join(lines[:end], "\n"))
}

// subagentResultHeader returns the first visible task-result line.
func subagentResultHeader(display subagentResultDisplay) string {
	profile := display.Profile
	if profile == "" {
		profile = "subagent"
	}
	parts := []string{"Subagent " + profile + " completed"}
	if display.Duration != "" {
		parts = append(parts, display.Duration)
	}
	if display.ModelCalls != "" {
		parts = append(
			parts, pluralCounter(
				display.ModelCalls, "model call", "model calls",
			),
		)
	}
	if display.ToolCalls != "" {
		parts = append(
			parts, pluralCounter(display.ToolCalls,
				"tool", "tools"),
		)
	}

	return strings.Join(parts, " · ")
}

// pluralCounter formats a string counter with a singular or plural noun.
func pluralCounter(count string, singular string, plural string) string {
	if count == "1" {
		return count + " " + singular
	}

	return count + " " + plural
}

// subagentMetadataLines returns muted child-session follow-up commands.
func subagentMetadataLines(display subagentResultDisplay) []string {
	var lines []string
	if display.SessionID != "" {
		lines = append(lines, "Session: "+display.SessionID)
	}
	if display.Inspect != "" {
		lines = append(lines, "Inspect: "+display.Inspect)
	}
	if display.Resume != "" {
		lines = append(lines, "Resume: "+display.Resume)
	}

	return lines
}

// trimField removes a fixed field label and surrounding whitespace.
func trimField(line string, prefix string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, prefix))
}

// compactOneLine collapses whitespace for terminal labels.
func compactOneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// truncateRunes returns text capped to limit runes with a trailing ellipsis.
func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit == 1 {
		return "…"
	}

	return string(runes[:limit-1]) + "…"
}
