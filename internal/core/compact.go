package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"harness/internal/hooks"
	"harness/internal/model"
	"harness/internal/prompt"
	"harness/internal/session"
	"harness/internal/textutil"
)

// errNotEnoughHistory reports that a compaction request has no useful prefix.
var errNotEnoughHistory = errors.New("not enough history to compact")

const (
	// DefaultCompactKeepMessages is the number of latest message events
	// kept raw when token-budget compaction is disabled.
	DefaultCompactKeepMessages = 12

	// DefaultCompactKeepRecentTokens is the approximate recent context
	// budget retained raw after Pi-style compaction.
	DefaultCompactKeepRecentTokens = 20000

	// compactToolResultLimit caps serialized tool results in summary
	// prompts.
	compactToolResultLimit = 2048
)

const (
	// initialSummaryPrompt is the structured checkpoint format used for
	// the first summary in a session.
	initialSummaryPrompt = `The messages above are a conversation to summarize.
Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:
## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by the user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, exact file paths, function names, error messages, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

	// updateSummaryPrompt updates an existing checkpoint with newly
	// summarized conversation history.
	updateSummaryPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.
Update the existing structured summary with new information.

RULES:
- PRESERVE all existing information from the previous summary.
- ADD new progress, decisions, and context from the new messages.
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed.
- UPDATE "Next Steps" based on what was accomplished.
- PRESERVE exact file paths, function names, and error messages.
- If something is no longer relevant, you may remove it.

Use this EXACT format:
## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing constraints and preferences, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work, updated based on progress]

### Blocked
- [Current blockers, remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve previous decisions, add new ones)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new context if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

	// summarizationSystemPrompt prevents the model from continuing the
	// conversation being summarized.
	summarizationSystemPrompt = `You are a context summarization assistant.
Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.
Do NOT continue the conversation.
Do NOT respond to any questions in the conversation.
ONLY output the structured summary.`
)

// CompactRequest contains everything needed to append a session summary.
type CompactRequest struct {
	// SessionPath is the JSONL session log to compact.
	SessionPath string

	// Model is the provider-neutral client used to summarize older history.
	Model model.Client

	// KeepMessages is the number of latest message events to keep raw.
	KeepMessages int

	// KeepRecentTokens is the approximate recent context budget retained
	// raw. Values less than one fall back to KeepMessages.
	KeepRecentTokens int

	// ModelName records the summarization model name in the summary event.
	ModelName string

	// Trigger records why compaction started. Empty means manual.
	Trigger string

	// Instructions are optional user-provided focus instructions for this
	// compaction pass.
	Instructions string

	// Hooks runs external lifecycle transformers around compaction. Nil
	// means no hooks are configured.
	Hooks *hooks.Runner
}

// CompactResult reports the summary event appended by compaction.
type CompactResult struct {
	// SessionPath is the compacted JSONL session log.
	SessionPath string

	// SummaryEventID is the appended context.summary event identifier.
	SummaryEventID string

	// FirstKeptEventID is the first raw event retained after the summary.
	FirstKeptEventID string

	// Summary is the model-written checkpoint.
	Summary string
}

// CompactSession summarizes older session history and appends a summary event.
func CompactSession(ctx context.Context,
	req CompactRequest) (*CompactResult, error) {

	if strings.TrimSpace(req.SessionPath) == "" {
		return nil, fmt.Errorf("session path must not be empty")
	}
	if req.Model == nil {
		return nil, fmt.Errorf("model client must not be nil")
	}

	store, events, err := session.Open(req.SessionPath)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	result, _, err := compactStore(ctx, req, store, events)

	return result, err
}

// compactStore summarizes older history through an already-open session store.
func compactStore(ctx context.Context, req CompactRequest, store *session.Store,
	events []session.Event) (*CompactResult, *session.Event, error) {

	plan, err := planCompaction(events, req)
	if err != nil {
		return nil, nil, err
	}
	if plan.CutIndex == 0 {
		return nil, nil, errNotEnoughHistory
	}

	summary, err := compactSummary(ctx, req, plan)
	if err != nil {
		return nil, nil, err
	}
	summary = appendFileOperations(
		summary, plan.ReadFiles, plan.ModifiedFiles,
	)
	trigger := compactTrigger(req.Trigger)
	event, err := store.Append(
		session.EventContextSummary, store.LastID(),
		session.SummaryData{
			Summary:          summary,
			RangeStartID:     events[plan.RangeStartIndex].ID,
			RangeEndID:       events[plan.CutIndex-1].ID,
			FirstKeptEventID: plan.FirstKeptEventID,
			Model:            req.ModelName,
			Trigger:          trigger,
			TokensBefore:     plan.TokensBefore,
			ReadFiles:        plan.ReadFiles,
			ModifiedFiles:    plan.ModifiedFiles,
		},
	)
	if err != nil {
		return nil, nil, err
	}
	if req.Hooks != nil {
		if err := req.Hooks.PostCompact(ctx, hooks.PostCompactEvent{
			SessionPath:      req.SessionPath,
			Trigger:          trigger,
			SummaryEventID:   event.ID,
			FirstKeptEventID: plan.FirstKeptEventID,
			Summary:          summary,
		}); err != nil {
			return nil, nil, err
		}
	}

	return &CompactResult{
		SessionPath:      req.SessionPath,
		SummaryEventID:   event.ID,
		FirstKeptEventID: plan.FirstKeptEventID,
		Summary:          summary,
	}, event, nil
}

// compactTrigger returns the durable trigger label for a compaction pass.
func compactTrigger(trigger string) string {
	if strings.TrimSpace(trigger) == "" {
		return "manual"
	}

	return trigger
}

// compactSummary returns either a hook-provided or model-written summary.
func compactSummary(ctx context.Context, req CompactRequest,
	plan compactionPlan) (string, error) {

	if req.Hooks != nil {
		trigger := compactTrigger(req.Trigger)
		result, err := req.Hooks.PreCompact(ctx, hooks.PreCompactEvent{
			SessionPath:      req.SessionPath,
			Trigger:          trigger,
			RangeStartID:     plan.Events[plan.RangeStartIndex].ID,
			RangeEndID:       plan.Events[plan.CutIndex-1].ID,
			FirstKeptEventID: plan.FirstKeptEventID,
		})
		if err != nil {
			return "", err
		}
		if result.Block {
			return "", fmt.Errorf("compaction blocked by hook: %s",
				nonEmptyReason(result.Reason))
		}
		if result.Summary != nil {
			summary := strings.TrimSpace(*result.Summary)
			if summary == "" {
				return "", fmt.Errorf("hook compaction " +
					"summary was empty")
			}

			return summary, nil
		}
	}

	return summarizeEvents(ctx, req.Model, plan, req.Instructions)
}

// compactionPlan stores all derived inputs for one compaction pass.
type compactionPlan struct {
	// Events stores the complete session event list being compacted.
	Events []session.Event

	// RangeStartIndex is the first event included in the summary range.
	RangeStartIndex int

	// CutIndex is the first event retained raw after compaction.
	CutIndex int

	// FirstKeptEventID is the event ID where future raw replay resumes.
	FirstKeptEventID string

	// PreviousSummary is the latest summary that should be updated.
	PreviousSummary *session.SummaryData

	// TokensBefore is the approximate projected context before compaction.
	TokensBefore int

	// ReadFiles stores read-only file paths discovered across summaries.
	ReadFiles []string

	// ModifiedFiles stores mutated file paths discovered across summaries.
	ModifiedFiles []string
}

// planCompaction chooses the summary range, retained boundary, and metadata.
func planCompaction(events []session.Event,
	req CompactRequest) (compactionPlan, error) {

	if len(events) == 0 {
		return compactionPlan{}, errNotEnoughHistory
	}
	previous, start, err := previousCompaction(events)
	if err != nil {
		return compactionPlan{}, err
	}

	cut, firstKeptID, err := compactionCutByTokens(
		events, start, req.KeepRecentTokens,
	)
	if err != nil {
		return compactionPlan{}, err
	}
	if cut == 0 {
		keep := req.KeepMessages
		if keep <= 0 {
			keep = DefaultCompactKeepMessages
		}
		cut, firstKeptID, err = compactionCutByMessages(
			events, start, keep,
		)
		if err != nil {
			return compactionPlan{}, err
		}
	}
	if cut <= start {
		return compactionPlan{}, errNotEnoughHistory
	}

	readFiles, modifiedFiles := compactionFileLists(
		events[start:cut], previous,
	)

	return compactionPlan{
		Events:           events,
		RangeStartIndex:  start,
		CutIndex:         cut,
		FirstKeptEventID: firstKeptID,
		PreviousSummary:  previous,
		TokensBefore:     sessionApproxTokens(events),
		ReadFiles:        readFiles,
		ModifiedFiles:    modifiedFiles,
	}, nil
}

// previousCompaction returns the newest summary and the next summary start.
func previousCompaction(events []session.Event) (*session.SummaryData, int,
	error) {

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != session.EventContextSummary {
			continue
		}

		var summary session.SummaryData
		if err := json.Unmarshal(events[i].Data, &summary); err != nil {
			return nil, 0, fmt.Errorf("decode summary %s: %w",
				events[i].ID, err)
		}
		start := eventIndex(events, summary.FirstKeptEventID)
		if start < 0 {
			start = i + 1
		}

		return &summary, start, nil
	}

	return nil, 0, nil
}

// eventIndex returns the index of id or -1 when it is absent.
func eventIndex(events []session.Event, id string) int {
	if id == "" {
		return -1
	}
	for i, event := range events {
		if event.ID == id {
			return i
		}
	}

	return -1
}

// compactionCutByTokens keeps roughly keepRecentTokens of recent raw context.
func compactionCutByTokens(events []session.Event, start int,
	keepRecentTokens int) (int, string, error) {

	if keepRecentTokens <= 0 {
		return 0, "", nil
	}
	valid := validCompactionCutPoints(events, start, len(events))
	if len(valid) == 0 {
		return 0, "", nil
	}

	accumulated := 0
	cut := valid[0]
	for i := len(events) - 1; i >= start; i-- {
		if !compactMessageEvent(events[i].Type) {
			continue
		}
		accumulated += eventApproxTokens(events[i])
		if accumulated >= keepRecentTokens {
			cut = closestCutAtOrAfter(valid, i)
			break
		}
	}
	if cut <= start {
		return 0, "", nil
	}

	return cut, events[cut].ID, nil
}

// validCompactionCutPoints returns event indexes where raw replay may resume.
func validCompactionCutPoints(events []session.Event, start int,
	end int) []int {

	points := make([]int, 0, end-start)
	for i := start; i < end; i++ {
		message, ok := compactMessageData(events[i])
		if !ok {
			continue
		}
		if message.Role == session.RoleUser ||
			message.Role == session.RoleAssistant {

			points = append(points, i)
		}
	}

	return points
}

// closestCutAtOrAfter returns the first valid cut point at or after index.
func closestCutAtOrAfter(points []int, index int) int {
	for _, point := range points {
		if point >= index {
			return point
		}
	}

	return points[len(points)-1]
}

// compactionCutByMessages keeps the latest keepMessages message events raw.
func compactionCutByMessages(events []session.Event, start int,
	keepMessages int) (int, string, error) {

	messageIndexes := make([]int, 0, len(events))
	for i := start; i < len(events); i++ {
		event := events[i]
		if compactMessageEvent(event.Type) {
			messageIndexes = append(messageIndexes, i)
		}
	}
	if len(messageIndexes) <= keepMessages {
		return 0, "", nil
	}

	keepStartMessage := messageIndexes[len(messageIndexes)-keepMessages]
	if keepStartMessage <= start {
		return 0, "", nil
	}

	return keepStartMessage, events[keepStartMessage].ID, nil
}

// compactMessageEvent reports whether an event should count toward raw
// recency retention.
func compactMessageEvent(eventType string) bool {
	return eventType == session.EventUserMessage ||
		eventType == session.EventAssistantMessage ||
		eventType == session.EventToolMessage
}

// summarizeEvents asks the model to summarize serialized session events.
func summarizeEvents(ctx context.Context, client model.Client,
	plan compactionPlan, instructions string) (string, error) {

	stream, err := client.Stream(ctx, model.Request{
		Messages: []model.Message{
			{
				Role:    model.RoleSystem,
				Content: summarizationSystemPrompt,
			},
			{
				Role:    model.RoleUser,
				Content: summarizationPrompt(plan, instructions),
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("start compaction model stream: %w", err)
	}

	response, err := collectStream(ctx, stream, nil)
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(response.Text)
	if summary == "" {
		return "", fmt.Errorf("compaction summary was empty")
	}

	return summary, nil
}

// summarizationPrompt builds the model-visible compaction request.
func summarizationPrompt(plan compactionPlan, instructions string) string {
	var out strings.Builder
	fmt.Fprintf(
		&out, "<conversation>\n%s</conversation>\n\n",
		serializeEventsForSummary(
			plan.Events[plan.RangeStartIndex:plan.CutIndex],
		),
	)
	if plan.PreviousSummary != nil {
		fmt.Fprintf(
			&out, "<previous-summary>\n%s\n</previous-summary>\n\n",
			plan.PreviousSummary.Summary,
		)
	}
	if strings.TrimSpace(instructions) != "" {
		fmt.Fprintf(
			&out, "Additional focus: %s\n\n",
			strings.TrimSpace(instructions),
		)
	}
	if plan.PreviousSummary != nil {
		out.WriteString(updateSummaryPrompt)
	} else {
		out.WriteString(initialSummaryPrompt)
	}

	return out.String()
}

// serializeEventsForSummary converts session events into a compact transcript.
func serializeEventsForSummary(events []session.Event) string {
	var out strings.Builder
	for _, event := range events {
		if !compactMessageEvent(event.Type) {
			continue
		}

		var message session.MessageData
		if err := json.Unmarshal(event.Data, &message); err != nil {
			continue
		}
		switch message.Role {
		case session.RoleUser:
			writeSummaryLine(
				&out, "User", summaryMessageText(message),
			)

		case session.RoleAssistant:
			if len(message.ToolCalls) > 0 {
				for _, call := range message.ToolCalls {
					writeSummaryLine(
						&out, "Assistant tool call",
						call.Name+" "+call.Arguments,
					)
				}
			} else {
				writeSummaryLine(
					&out, "Assistant",
					summaryMessageText(message),
				)
			}

		case session.RoleTool:
			writeSummaryLine(
				&out, "Tool "+message.Name,
				limitSummaryText(
					summaryMessageText(message),
				),
			)
		}
	}

	return out.String()
}

// compactMessageData decodes a model-visible message event.
func compactMessageData(event session.Event) (session.MessageData, bool) {
	if !compactMessageEvent(event.Type) {
		return session.MessageData{}, false
	}
	var message session.MessageData
	if err := json.Unmarshal(event.Data, &message); err != nil {
		return session.MessageData{}, false
	}

	return message, true
}

// sessionApproxTokens estimates model-visible message tokens in events.
func sessionApproxTokens(events []session.Event) int {
	tokens := 0
	for _, event := range events {
		tokens += eventApproxTokens(event)
	}

	return tokens
}

// eventApproxTokens estimates model-visible token count for one event.
func eventApproxTokens(event session.Event) int {
	message, ok := compactMessageData(event)
	if !ok {
		if event.Type == session.EventContextSummary {
			var summary session.SummaryData
			if err := json.Unmarshal(
				event.Data, &summary,
			); err == nil {
				return prompt.ApproxTokens(summary.Summary)
			}
		}

		return 0
	}
	if message.Role == session.RoleAssistant && len(message.ToolCalls) > 0 {
		text := summaryMessageText(message)
		for _, call := range message.ToolCalls {
			text += "\n" + call.Name + " " + call.Arguments
		}

		return prompt.ApproxTokens(text)
	}

	return prompt.ApproxTokens(summaryMessageText(message))
}

// compactionFileLists returns read-only and modified files for the summary.
func compactionFileLists(events []session.Event,
	previous *session.SummaryData) ([]string, []string) {

	read := map[string]bool{}
	modified := map[string]bool{}
	if previous != nil {
		for _, path := range previous.ReadFiles {
			read[path] = true
		}
		for _, path := range previous.ModifiedFiles {
			modified[path] = true
		}
	}
	for _, event := range events {
		message, ok := compactMessageData(event)
		if !ok || message.Role != session.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			path := toolCallPath(call.Arguments)
			if path == "" {
				continue
			}
			switch call.Name {
			case "read":
				read[path] = true

			case "write", "edit":
				modified[path] = true
			}
		}
	}
	for path := range modified {
		delete(read, path)
	}

	return sortedKeys(read), sortedKeys(modified)
}

// toolCallPath extracts a string path argument from raw tool-call JSON.
func toolCallPath(arguments string) string {
	var data struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(arguments), &data); err != nil {
		return ""
	}

	return strings.TrimSpace(data.Path)
}

// sortedKeys returns map keys in deterministic order.
func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	return keys
}

// appendFileOperations appends deterministic file operation metadata.
func appendFileOperations(summary string, readFiles []string,
	modifiedFiles []string) string {

	if len(readFiles) == 0 && len(modifiedFiles) == 0 {
		return summary
	}
	var out strings.Builder
	out.WriteString(strings.TrimSpace(summary))
	out.WriteString("\n\n## Files\n")
	if len(readFiles) > 0 {
		out.WriteString("### Read\n")
		for _, path := range readFiles {
			fmt.Fprintf(&out, "- %s\n", path)
		}
	}
	if len(modifiedFiles) > 0 {
		out.WriteString("### Modified\n")
		for _, path := range modifiedFiles {
			fmt.Fprintf(&out, "- %s\n", path)
		}
	}

	return strings.TrimSpace(out.String())
}

// summaryMessageText joins text parts from a session message.
func summaryMessageText(message session.MessageData) string {
	var text string
	for _, part := range message.Content {
		if part.Type == session.ContentText {
			text += part.Text
		}
	}

	return text
}

// writeSummaryLine appends one labelled transcript line.
func writeSummaryLine(out *strings.Builder, label string, text string) {
	fmt.Fprintf(out, "[%s]: %s\n", label, strings.TrimSpace(text))
}

// limitSummaryText caps large tool results before summarization.
func limitSummaryText(text string) string {
	limited, truncated := textutil.TruncateUTF8Bytes(
		text, compactToolResultLimit,
	)
	if truncated {
		return limited + "\n[truncated]"
	}

	return limited
}
