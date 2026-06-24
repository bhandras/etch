package prompt

import (
	"fmt"
	"strings"
	"time"

	"etch/internal/model"
	"etch/internal/session"
)

// DumpRequest contains the logical model context to render for debugging.
type DumpRequest struct {
	// CreatedAt records when the dump was generated.
	CreatedAt time.Time

	// CWD is the working directory associated with the chat session.
	CWD string

	// SessionPath is the JSONL session log projected into context.
	SessionPath string

	// ModelName is the provider model selected by the CLI.
	ModelName string

	// Events stores durable session events in replay order.
	Events []session.Event

	// Project stores pinned project context loaded for the chat.
	Project ProjectContext

	// Tools stores model-facing tool schemas available to the turn.
	Tools []model.ToolSpec
}

// DumpText renders a plain-text, layered view of logical model context.
func DumpText(req DumpRequest) (string, error) {
	created := req.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	stats, err := BuildStats(req.Events, req.Project.SystemText)
	if err != nil {
		return "", err
	}
	summary, start, err := latestSummary(req.Events)
	if err != nil {
		return "", err
	}
	replay, err := replayMessagesForDump(req.Events, start)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	writeDumpHeader(&out, req, created, stats)
	writeDumpSection(&out, "Base Prompt", BaseSystemPrompt)
	writeConfigPromptDumpSection(&out, req.Project)
	writeInstructionDumpSections(
		&out, "Project System Prompt", req.Project.SystemFiles,
	)
	writeInstructionDumpSections(
		&out, "Project Instructions", req.Project.InstructionFiles,
	)
	if catalog := skillCatalogText(req.Project.Skills); catalog != "" {
		writeDumpSection(
			&out, "Skill Catalog", strings.TrimSpace(catalog),
		)
	}
	if summary != nil {
		writeDumpSection(&out, "Conversation Summary", summary.Summary)
	}
	writeMessageDumpSection(&out, replay)
	writeToolDumpSection(&out, req.Tools)

	return strings.TrimRight(out.String(), "\n") + "\n", nil
}

// writeDumpHeader renders metadata and approximate context counters.
func writeDumpHeader(out *strings.Builder, req DumpRequest, created time.Time,
	stats Stats) {

	fmt.Fprintln(out, "etch Context Dump")
	fmt.Fprintf(out, "created: %s\n", created.Format(time.RFC3339))
	fmt.Fprintf(out, "cwd: %s\n", displayDumpValue(req.CWD))
	fmt.Fprintf(out, "model: %s\n", displayDumpValue(req.ModelName))
	fmt.Fprintf(out, "session: %s\n", displayDumpValue(req.SessionPath))
	fmt.Fprintf(out, "events: %d\n", stats.EventCount)
	fmt.Fprintf(
		out, "approx context: %d bytes, ~%d tokens\n",
		stats.ApproxContextBytes, stats.ApproxContextTokens,
	)
	fmt.Fprintln(
		out, "request shape: logical full context before provider "+
			"transport selection and context-build hooks",
	)
}

// displayDumpValue renders unset metadata consistently.
func displayDumpValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(unset)"
	}

	return value
}

// writeConfigPromptDumpSection renders config prompt text when present.
func writeConfigPromptDumpSection(out *strings.Builder,
	project ProjectContext) {

	if strings.TrimSpace(project.ConfigPrompt) == "" {
		return
	}
	title := "Config System Prompt"
	if project.ConfigPromptPath != "" {
		title += " (" + project.ConfigPromptPath + ")"
	}
	writeDumpSection(out, title, project.ConfigPrompt)
}

// writeInstructionDumpSections renders loaded instruction file sections.
func writeInstructionDumpSections(out *strings.Builder, title string,
	files []InstructionFile) {

	for _, file := range files {
		writeDumpSection(out, title+" ("+file.Path+")", file.Text)
	}
}

// writeMessageDumpSection renders replayed model-visible messages.
func writeMessageDumpSection(out *strings.Builder, messages []model.Message) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "===== Conversation Replay =====")
	if len(messages) == 0 {
		fmt.Fprintln(out, "(empty)")

		return
	}
	for i, message := range messages {
		fmt.Fprintf(
			out, "\n----- Message %d: %s -----\n", i+1,
			messageDumpLabel(message),
		)
		writeMessageDump(out, message)
	}
}

// messageDumpLabel returns the most useful human label for one message.
func messageDumpLabel(message model.Message) string {
	if len(message.ProviderItems) > 0 {
		return "provider item"
	}
	if message.Role == model.RoleTool && message.Name != "" {
		if message.ToolCallID != "" {
			return "tool " + message.Name + " (" + message.ToolCallID +
				")"
		}

		return "tool " + message.Name
	}
	if message.Role == "" {
		return "(no role)"
	}

	return message.Role
}

// writeMessageDump renders one projected model message.
func writeMessageDump(out *strings.Builder, message model.Message) {
	if len(message.ProviderItems) > 0 {
		for _, item := range message.ProviderItems {
			writeProviderItemDump(out, item)
		}

		return
	}
	if strings.TrimSpace(message.Content) != "" {
		fmt.Fprintln(out, message.Content)
	}
	if len(message.ToolCalls) == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Tool calls:")
	for _, call := range message.ToolCalls {
		fmt.Fprintf(out, "- %s %s\n", call.ID, call.Name)
		if strings.TrimSpace(call.Arguments) != "" {
			fmt.Fprintln(out, call.Arguments)
		}
	}
}

// writeProviderItemDump renders replay metadata for opaque provider items.
func writeProviderItemDump(out *strings.Builder, item model.ProviderItem) {
	fmt.Fprintf(out, "provider: %s\n", displayDumpValue(item.Provider))
	fmt.Fprintf(out, "type: %s\n", displayDumpValue(item.Type))
	if item.ID != "" {
		fmt.Fprintf(out, "id: %s\n", item.ID)
	}
	if item.Summary != "" {
		fmt.Fprintf(out, "summary:\n%s\n", item.Summary)
	}
	if item.EncryptedContent != "" {
		fmt.Fprintf(
			out, "encrypted content: %d bytes\n",
			len(item.EncryptedContent),
		)
	}
}

// writeToolDumpSection renders model-facing tool schemas.
func writeToolDumpSection(out *strings.Builder, tools []model.ToolSpec) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "===== Tool Schemas =====")
	if len(tools) == 0 {
		fmt.Fprintln(out, "(none)")

		return
	}
	for _, spec := range tools {
		fmt.Fprintf(out, "\n----- Tool: %s -----\n", spec.Name)
		fmt.Fprintln(out, "Description:")
		fmt.Fprintln(out, spec.Description)
		if len(spec.Parameters) == 0 {
			continue
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Parameters:")
		fmt.Fprintln(out, string(spec.Parameters))
	}
}

// writeDumpSection renders one named text block.
func writeDumpSection(out *strings.Builder, title string, text string) {
	fmt.Fprintln(out)
	fmt.Fprintf(out, "===== %s =====\n", title)
	if strings.TrimSpace(text) == "" {
		fmt.Fprintln(out, "(empty)")

		return
	}
	fmt.Fprintln(out, strings.TrimRight(text, "\n"))
}

// replayMessagesForDump returns raw replay messages after the active summary.
func replayMessagesForDump(events []session.Event,
	start int) ([]model.Message, error) {

	var messages []model.Message
	var pendingReasoning string
	for _, event := range events[start:] {
		if event.Type == session.EventContextSummary {
			continue
		}
		if event.Type == session.EventModelReasoning {
			reasoning, err := reasoningFromEvent(event)
			if err != nil {
				return nil, err
			}
			pendingReasoning = reasoning

			continue
		}
		message, ok, err := messageFromEvent(event, pendingReasoning)
		if err != nil {
			return nil, err
		}
		if ok {
			messages = append(messages, message)
			pendingReasoning = ""
		}
	}

	return messages, nil
}
