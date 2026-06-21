package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"harness/internal/core"
	"harness/internal/hooks"
	promptctx "harness/internal/prompt"
)

// runPrompt executes the default non-interactive prompt path.
func runPrompt(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	warnImplicitEchoProvider(cfg, stderr)

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "error: get working directory:", err)

		return 1
	}

	modelClient, err := modelClient(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	projectContext, err := promptctx.LoadProjectContext(cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	hookRunner, err := hooks.New(cfg.hooks, cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	registry, closePlugins, err := configuredToolRegistry(
		context.Background(), cfg, cwd,
	)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	defer closePlugins()

	result, err := core.RunTurn(context.Background(), core.TurnRequest{
		Prompt:        cfg.prompt,
		SessionDir:    cfg.sessionDir,
		CWD:           cwd,
		SystemText:    projectContext.SystemText,
		Model:         modelClient,
		Tools:         registry,
		MaxToolRounds: cfg.maxToolRounds,
		Hooks:         hookRunner,
	})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	if cfg.jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, "error: encode json output:", err)

			return 1
		}

		return 0
	}

	renderPromptAssistant(stdout, result.AssistantText)

	return 0
}

// warnImplicitEchoProvider tells users when the offline fixture is selected by
// default.
func warnImplicitEchoProvider(cfg cliConfig, stderr io.Writer) {
	if cfg.provider == providerEcho && !cfg.providerExplicit {
		fmt.Fprintln(
			stderr, "warning: running offline echo provider; "+
				"set --provider openai for a real model",
		)
	}
}

// renderPromptAssistant prints the non-JSON assistant response for prompt mode.
func renderPromptAssistant(stdout io.Writer, text string) {
	lines := markdownLines(text, terminalStyle{
		enabled: shouldStyle(stdout),
	})
	if len(lines) == 1 {
		fmt.Fprintf(stdout, "assistant: %s\n", lines[0])

		return
	}

	fmt.Fprintln(stdout, "assistant:")
	for _, line := range lines {
		fmt.Fprintln(stdout, line)
	}
}
