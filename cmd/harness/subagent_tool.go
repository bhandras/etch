package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	harnessconfig "harness/internal/config"
	"harness/internal/core"
	"harness/internal/hooks"
	"harness/internal/model"
	promptctx "harness/internal/prompt"
	"harness/internal/session"
	"harness/internal/tool"
)

const (
	// defaultSubagentMaxPerTurn caps task delegations when config omits a
	// global limit.
	defaultSubagentMaxPerTurn = 4

	// defaultSubagentMaxConcurrent caps child runs when config omits a
	// global limit.
	defaultSubagentMaxConcurrent = 2

	// maxTrackedSubagentTurns bounds per-turn delegation bookkeeping kept
	// by the long-lived task tool.
	maxTrackedSubagentTurns = 1024
)

// taskTool delegates isolated work to configured child-agent profiles.
type taskTool struct {
	// cfg stores the parent CLI configuration used for inherited settings.
	cfg cliConfig

	// cwd is the project working directory shared with child tools.
	cwd string

	// registry is the parent registry used to build child allowlist
	// subsets.
	registry *tool.Registry

	// sem limits concurrent child runs for future parallel tool batches.
	sem chan struct{}

	// mu protects turnCounts.
	mu sync.Mutex

	// turnCounts tracks task calls accepted for each parent assistant
	// event.
	turnCounts map[string]subagentTurnCount
}

// subagentTurnCount stores bounded delegation bookkeeping for one parent turn.
type subagentTurnCount struct {
	// Count is the number of accepted task calls for the parent turn.
	Count int

	// UpdatedAt records the last reserve update for pruning old turns.
	UpdatedAt time.Time
}

// taskArguments is the model-facing input accepted by the task tool.
type taskArguments struct {
	// Profile names the configured subagent profile to run.
	Profile string `json:"profile"`

	// Task is the concrete work delegated to the child agent.
	Task string `json:"task"`

	// Context gives the child optional focused constraints or background.
	Context string `json:"context,omitempty"`
}

// newTaskTool creates a stateful delegation tool for configured profiles.
func newTaskTool(cfg cliConfig, cwd string, registry *tool.Registry) *taskTool {
	maxConcurrent := positiveConfigOrDefault(
		cfg.subagents.MaxConcurrent, defaultSubagentMaxConcurrent,
	)

	return &taskTool{
		cfg:        cfg,
		cwd:        cwd,
		registry:   registry,
		sem:        make(chan struct{}, maxConcurrent),
		turnCounts: make(map[string]subagentTurnCount),
	}
}

// Spec returns a dynamic schema advertising enabled configured profiles.
func (t *taskTool) Spec() model.ToolSpec {
	profiles := activeSubagentProfiles(t.cfg.subagents)
	names := make([]string, 0, len(profiles))
	var description strings.Builder
	fmt.Fprintln(
		&description, "Delegate an isolated task to a configured "+
			"child-agent profile.",
	)
	fmt.Fprintln(&description)
	fmt.Fprintln(&description, "Available profiles:")
	for _, profile := range profiles {
		names = append(names, profile.Name)
		fmt.Fprintf(
			&description, "- %s: %s\n", profile.Name,
			profile.Description,
		)
	}

	parameters := taskParametersSchema(names)

	return model.ToolSpec{
		Name:        tool.NameTask,
		Description: strings.TrimSpace(description.String()),
		Parameters:  parameters,
	}
}

// Execute runs the task tool without provider call metadata.
func (t *taskTool) Execute(ctx context.Context, arguments string) (tool.Result,
	error) {

	return t.ExecuteCall(ctx, model.ToolCall{
		ID:        "manual",
		Name:      tool.NameTask,
		Arguments: arguments,
	})
}

// ExecuteCall runs one configured child agent and returns its compact result.
func (t *taskTool) ExecuteCall(ctx context.Context, call model.ToolCall) (
	tool.Result, error) {

	var args taskArguments
	if strings.TrimSpace(call.Arguments) != "" {
		if err := json.Unmarshal(
			[]byte(call.Arguments), &args,
		); err != nil {
			return tool.Result{}, fmt.Errorf("decode task "+
				"arguments: %w", err)
		}
	}
	profile, err := findSubagentProfile(t.cfg.subagents, args.Profile)
	if err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(args.Task) == "" {
		return tool.Result{}, fmt.Errorf("task must not be empty")
	}
	meta, _ := tool.ExecutionContextFrom(ctx)
	if err := t.reserveTurnSlot(meta); err != nil {
		return tool.Result{}, err
	}

	select {
	case t.sem <- struct{}{}:
		defer func() {
			<-t.sem
		}()

	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}

	started := time.Now()
	result, err := t.runChild(ctx, call.ID, meta, profile, args)
	if err != nil {
		return tool.Result{}, err
	}
	result.Duration = time.Since(started)

	return tool.Result{Text: formatTaskResult(result)}, nil
}

// ParallelSafe reports whether the requested child profile may run
// concurrently.
func (t *taskTool) ParallelSafe(call model.ToolCall) bool {
	var args taskArguments
	if strings.TrimSpace(call.Arguments) != "" {
		if err := json.Unmarshal(
			[]byte(call.Arguments), &args,
		); err != nil {
			return false
		}
	}
	if _, err := findSubagentProfile(
		t.cfg.subagents, args.Profile,
	); err != nil {
		return false
	}

	return true
}

// subagentRunResult stores the compact outcome returned to the parent.
type subagentRunResult struct {
	// Profile is the configured child-agent profile name.
	Profile string

	// SessionID is the child session identifier.
	SessionID string

	// SessionPath is the child JSONL transcript path.
	SessionPath string

	// AssistantText is the final child answer returned to the parent.
	AssistantText string

	// Duration is the wall-clock child runtime.
	Duration time.Duration

	// Status stores durable child activity counters.
	Status session.Status
}

// runChild builds inherited runtime settings and executes one child turn.
func (t *taskTool) runChild(ctx context.Context, callID string,
	meta tool.ExecutionContext, profile harnessconfig.SubagentProfileConfig,
	args taskArguments) (subagentRunResult, error) {

	childCfg := t.childConfig(profile)
	client, err := modelClient(childCfg)
	if err != nil {
		return subagentRunResult{}, err
	}
	childRegistry, err := t.childToolRegistry(profile)
	if err != nil {
		return subagentRunResult{}, err
	}
	systemText, err := t.childSystemText(profile)
	if err != nil {
		return subagentRunResult{}, err
	}
	hookRunner, err := hooks.New(childCfg.hooks, t.cwd)
	if err != nil {
		return subagentRunResult{}, err
	}
	progress := newSubagentProgressObserver(
		meta.Progress, nonEmptyString(meta.ToolCallID, callID),
	)

	turn, err := core.RunTurn(ctx, core.TurnRequest{
		Prompt:                      childPrompt(args),
		SessionDir:                  childCfg.sessionDir,
		CWD:                         t.cwd,
		SystemText:                  systemText,
		Model:                       client,
		ModelName:                   childCfg.model,
		Tools:                       childRegistry,
		MaxToolRounds:               childCfg.maxToolRounds,
		AutoCompactThresholdTokens:  autoCompactThreshold(childCfg),
		AutoCompactKeepMessages:     childCfg.keepMessages,
		AutoCompactKeepRecentTokens: childCfg.keepRecentTokens,
		ParentSessionID:             meta.SessionID,
		ParentToolCallID: nonEmptyString(
			meta.ToolCallID, callID,
		),
		SubagentProfile: profile.Name,
		Hooks:           hookRunner,
		Observer:        progress,
	})
	if err != nil {
		return subagentRunResult{}, err
	}
	status, err := readSessionStatus(turn.SessionPath)
	if err != nil {
		return subagentRunResult{}, err
	}

	return subagentRunResult{
		Profile:       profile.Name,
		SessionID:     turn.SessionID,
		SessionPath:   turn.SessionPath,
		AssistantText: turn.AssistantText,
		Status:        status,
	}, nil
}

// childConfig returns parent configuration with profile overrides applied.
func (t *taskTool) childConfig(
	profile harnessconfig.SubagentProfileConfig) cliConfig {

	child := t.cfg
	if profile.Provider != "" {
		child.provider = profile.Provider
		child.providerExplicit = true
	}
	if profile.Model != "" {
		child.model = profile.Model
	}
	if profile.BaseURL != "" {
		child.baseURL = profile.BaseURL
		child.baseURLExplicit = true
	}
	if profile.OpenAIAPI != "" {
		child.openaiAPI = profile.OpenAIAPI
		child.openaiAPIExplicit = true
	}
	if profile.ReasoningEffort != "" {
		child.reasoningEffort = profile.ReasoningEffort
	}
	if profile.ReasoningSummary != "" {
		child.reasoningSummary = profile.ReasoningSummary
	}
	if profile.MaxToolRounds > 0 {
		child.maxToolRounds = profile.MaxToolRounds
	}
	if profile.AutoCompactThresholdTokens > 0 {
		child.autoCompactLimit = profile.AutoCompactThresholdTokens
	}
	if profile.KeepMessages > 0 {
		child.keepMessages = profile.KeepMessages
	}
	if profile.KeepRecentTokens > 0 {
		child.keepRecentTokens = profile.KeepRecentTokens
	}
	if profile.AutoCompact && child.autoCompactLimit == 0 {
		child.autoCompactLimit = core.DefaultAutoCompactThresholdTokens
	}
	if profile.AutoCompact {
		child.autoCompact = true
	}

	return child
}

// childSystemText builds the child system prompt from project and profile text.
func (t *taskTool) childSystemText(
	profile harnessconfig.SubagentProfileConfig) (string, error) {

	projectContext, err := promptctx.LoadProjectContext(t.cwd)
	if err != nil {
		return "", err
	}
	profileText, err := t.profileSystemPrompt(profile)
	if err != nil {
		return "", err
	}
	parts := []string{
		projectContext.SystemText,
		childAgentSystemPrompt(),
		profileText,
	}

	return strings.TrimSpace(strings.Join(nonEmptyStrings(parts), "\n\n")), nil
}

// profileSystemPrompt loads profile-specific instructions.
func (t *taskTool) profileSystemPrompt(
	profile harnessconfig.SubagentProfileConfig) (string, error) {

	if strings.TrimSpace(profile.SystemPromptFile) == "" {
		return profile.SystemPrompt, nil
	}
	path, err := resolveProfilePromptPath(
		t.cfg.configPath, t.cwd, profile.SystemPromptFile,
	)
	if err != nil {
		return "", err
	}
	// #nosec G304 -- subagent prompt files are explicit user config.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read subagent prompt %s: %w", path, err)
	}

	return string(data), nil
}

// reserveTurnSlot enforces the configured per-parent-turn delegation limit.
func (t *taskTool) reserveTurnSlot(meta tool.ExecutionContext) error {
	maxPerTurn := positiveConfigOrDefault(
		t.cfg.subagents.MaxPerTurn, defaultSubagentMaxPerTurn,
	)
	key := meta.SessionID + ":" + meta.AssistantEventID
	if key == ":" {
		key = "manual"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.turnCounts[key]
	if entry.Count >= maxPerTurn {
		return fmt.Errorf("subagent task limit reached for this turn")
	}
	entry.Count++
	entry.UpdatedAt = time.Now()
	t.turnCounts[key] = entry
	t.pruneTurnCountsLocked()

	return nil
}

// pruneTurnCountsLocked bounds stale per-turn task counters.
func (t *taskTool) pruneTurnCountsLocked() {
	for len(t.turnCounts) > maxTrackedSubagentTurns {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range t.turnCounts {
			if oldestKey == "" || entry.UpdatedAt.Before(oldest) {
				oldestKey = key
				oldest = entry.UpdatedAt
			}
		}
		delete(t.turnCounts, oldestKey)
	}
}

// childToolRegistry returns an allowlisted registry for one child profile.
func (t *taskTool) childToolRegistry(
	profile harnessconfig.SubagentProfileConfig) (*tool.Registry, error) {

	allowed, allowTask := filteredChildToolNames(profile.AllowedTools)
	if len(allowed) == 0 {
		return nil, fmt.Errorf("subagent profile %q has no "+
			"allowed tools", profile.Name)
	}
	child, missing := t.registry.Subset(allowed)
	if len(missing) > 0 {
		return nil, fmt.Errorf("subagent profile %q allows unknown "+
			"tools: %s", profile.Name, strings.Join(missing, ", "))
	}
	if allowTask {
		child.Register(newTaskTool(t.cfg, t.cwd, child))
	}

	return child, nil
}

// filteredChildToolNames separates direct child tools from nested delegation.
func filteredChildToolNames(names []string) ([]string, bool) {
	filtered := make([]string, 0, len(names))
	allowTask := false
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if name == tool.NameTask {
			allowTask = true

			continue
		}
		filtered = append(filtered, name)
	}

	return filtered, allowTask
}

// childPrompt formats the user prompt admitted into the child session.
func childPrompt(args taskArguments) string {
	task := strings.TrimSpace(args.Task)
	contextText := strings.TrimSpace(args.Context)
	if contextText == "" {
		return task
	}

	return "Task:\n" + task + "\n\nContext:\n" + contextText
}

// childAgentSystemPrompt returns the built-in child-agent wrapper prompt.
func childAgentSystemPrompt() string {
	return strings.TrimSpace(`
You are a configured child agent delegated by a parent agent.
Work independently in this child session and return a concise result for the parent.
Do not assume the parent saw your tool outputs.
Mention important files, symbols, and verification steps when useful.
Only spawn further subagents if the task tool is available and delegation would
materially improve the result.
`)
}

// activeSubagentProfiles returns enabled profiles advertised to the parent.
func activeSubagentProfiles(
	config harnessconfig.SubagentConfig) []harnessconfig.SubagentProfileConfig {

	if !config.Enabled {
		return nil
	}
	profiles := make(
		[]harnessconfig.SubagentProfileConfig, 0, len(config.Profiles),
	)
	for _, profile := range config.Profiles {
		if profile.Disabled {
			continue
		}
		profiles = append(profiles, profile)
	}

	return profiles
}

// findSubagentProfile resolves one enabled profile by name.
func findSubagentProfile(config harnessconfig.SubagentConfig,
	name string) (harnessconfig.SubagentProfileConfig, error) {

	for _, profile := range activeSubagentProfiles(config) {
		if profile.Name == name {
			return profile, nil
		}
	}

	return harnessconfig.SubagentProfileConfig{},
		fmt.Errorf("unknown subagent profile %q", name)
}

// taskParametersSchema builds the JSON schema for configured profile names.
func taskParametersSchema(profileNames []string) json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"profile": map[string]any{
				"type":        "string",
				"enum":        profileNames,
				"description": "Configured subagent profile to run.",
			},
			"task": map[string]any{
				"type": "string",
				"description": "Concrete task for the child agent. " +
					"Be specific about expected output.",
			},
			"context": map[string]any{
				"type": "string",
				"description": "Optional focused context, files, " +
					"constraints, or assumptions.",
			},
		},
		"required": []string{
			"profile",
			"task",
		},
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}

	return encoded
}

// formatTaskResult renders a compact child result for the parent model.
func formatTaskResult(result subagentRunResult) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Task %s completed.\n\n", result.Profile)
	fmt.Fprintf(&out, "Profile: %s\n", result.Profile)
	fmt.Fprintf(&out, "Session: %s\n", result.SessionID)
	fmt.Fprintf(&out, "Session path: %s\n", result.SessionPath)
	fmt.Fprintf(&out, "Duration: %s\n", formatElapsed(result.Duration))
	fmt.Fprintf(&out, "Model calls: %d\n", result.Status.ModelCalls)
	fmt.Fprintf(&out, "Tool calls: %d\n\n", result.Status.ToolCalls)
	fmt.Fprintln(&out, "Result:")
	fmt.Fprintln(&out, strings.TrimSpace(result.AssistantText))
	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "Inspect: harness show %s\n", result.SessionID)
	fmt.Fprintf(&out, "Resume: harness resume %s", result.SessionID)

	return out.String()
}

// readSessionStatus computes durable counters for a child session.
func readSessionStatus(path string) (session.Status, error) {
	events, err := session.ReadAll(path)
	if err != nil {
		return session.Status{}, err
	}
	status, err := session.BuildStatus(events, time.Now())
	if err != nil {
		return session.Status{}, err
	}

	return status, nil
}

// resolveProfilePromptPath resolves subagent prompt files from config location.
func resolveProfilePromptPath(configPath string, cwd string,
	path string) (string, error) {

	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	base := cwd
	if configPath != "" {
		base = filepath.Dir(configPath)
	}

	return filepath.Join(base, path), nil
}

// nonEmptyStrings returns strings that contain non-whitespace text.
func nonEmptyStrings(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		filtered = append(filtered, strings.TrimSpace(value))
	}

	return filtered
}

// nonEmptyString returns fallback when value is blank.
func nonEmptyString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}

	return value
}
