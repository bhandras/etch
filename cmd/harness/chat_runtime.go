package main

import (
	"context"
	"fmt"
	"os"

	"harness/internal/hooks"
	"harness/internal/model"
	promptctx "harness/internal/prompt"
	"harness/internal/session"
	"harness/internal/tool"
)

// chatRuntime stores the non-terminal dependencies for an interactive chat.
type chatRuntime struct {
	// cwd is the project directory used for tools, hooks, and prompt
	// context.
	cwd string

	// systemText is the loaded project system prompt for each turn.
	systemText string

	// modelClient streams provider events for chat turns.
	modelClient model.Client

	// hookRunner executes configured lifecycle hooks.
	hookRunner *hooks.Runner

	// registry contains built-in and configured plugin tools.
	registry *tool.Registry

	// closePlugins releases long-running plugin processes.
	closePlugins func()

	// sessionPath is the resolved JSONL session path when resuming.
	sessionPath string

	// resumeID is the resolved durable session identifier when resuming.
	resumeID string

	// initialStatus seeds the terminal footer with resumed-session
	// counters.
	initialStatus chatChromeStatus
}

// openChatRuntime builds provider, context, hook, tool, and session state.
func openChatRuntime(cfg cliConfig) (*chatRuntime, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	client, err := modelClient(cfg)
	if err != nil {
		return nil, err
	}
	projectContext, err := promptctx.LoadProjectContextWithOptions(
		cwd, projectContextOptions(cfg),
	)
	if err != nil {
		return nil, err
	}
	hookRunner, err := hooks.New(cfg.hooks, cwd)
	if err != nil {
		return nil, err
	}

	runtime := &chatRuntime{
		cwd:          cwd,
		systemText:   projectContext.SystemText,
		modelClient:  client,
		hookRunner:   hookRunner,
		closePlugins: func() {},
	}
	if cfg.sessionID != "" {
		entry, err := session.Resolve(cfg.sessionDir, cfg.sessionID)
		if err != nil {
			return nil, err
		}
		runtime.sessionPath = entry.Path
		runtime.resumeID = entry.ID
		runtime.initialStatus, err = chatSessionChromeStatus(entry.Path)
		if err != nil {
			return nil, err
		}
	}

	registry, closePlugins, err := configuredToolRegistry(
		context.Background(), cfg, cwd,
	)
	if err != nil {
		return nil, err
	}
	runtime.registry = registry
	runtime.closePlugins = closePlugins

	return runtime, nil
}

// Close releases runtime resources owned by the chat session.
func (r *chatRuntime) Close() {
	if r == nil || r.closePlugins == nil {
		return
	}
	r.closePlugins()
}
