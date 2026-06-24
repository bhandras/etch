package main

import promptctx "etch/internal/prompt"

// projectContextOptions returns config-derived prompt extensions for context
// loading.
func projectContextOptions(cfg cliConfig) promptctx.ProjectContextOptions {
	return promptctx.ProjectContextOptions{
		ConfigPath:       cfg.configPath,
		SystemPrompt:     cfg.promptConfig.SystemPrompt,
		SystemPromptFile: cfg.promptConfig.SystemPromptFile,
	}
}
