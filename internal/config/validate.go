package config

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	// providerEchoName is the offline deterministic provider accepted by
	// config.
	providerEchoName = "echo"

	// providerOpenAIName is the OpenAI-compatible provider accepted by
	// config.
	providerOpenAIName = "openai"

	// openAIAPIChat is the Chat Completions API mode accepted by config.
	openAIAPIChat = "chat"

	// openAIAPIResponses is the Responses API mode accepted by config.
	openAIAPIResponses = "responses"
)

// Validate reports semantic config errors that require more than scalar
// parsing.
func Validate(cfg Config) error {
	var errors []string
	if value := strings.TrimSpace(cfg.Provider.Name); value != "" &&
		!stringIn(value, validProviders()) {

		errors = append(
			errors,
			fmt.Sprintf(
				"provider.name must be one of %s, got %q",
				joinOptions(
					validProviders(),
				),
				value,
			),
		)
	}
	if value := strings.TrimSpace(cfg.OpenAI.API); value != "" &&
		!stringIn(value, validOpenAIAPIs()) {

		errors = append(
			errors,
			fmt.Sprintf(
				"openai.api must be one of %s, got %q",
				joinOptions(
					validOpenAIAPIs(),
				),
				value,
			),
		)
	}
	if value := strings.TrimSpace(
		cfg.OpenAI.ReasoningEffort,
	); value != "" &&
		!stringIn(value, validReasoningEfforts()) {

		errors = append(
			errors,
			fmt.Sprintf(
				"openai.reasoning_effort must be one of %s, "+
					"got %q",
				joinOptions(
					validReasoningEfforts(),
				),
				value,
			),
		)
	}
	if value := strings.TrimSpace(
		cfg.OpenAI.ReasoningSummary,
	); value != "" &&
		!stringIn(value, validReasoningSummaries()) {

		errors = append(
			errors,
			fmt.Sprintf(
				"openai.reasoning_summary must be one of "+
					"%s, got %q",
				joinOptions(
					validReasoningSummaries(),
				),
				value,
			),
		)
	}
	errors = append(errors, validateHooks(cfg.Hooks)...)
	errors = append(errors, validatePlugins(cfg.Plugins)...)
	errors = append(errors, validateSubagents(cfg.Subagents)...)
	if len(errors) > 0 {
		return fmt.Errorf("invalid config: %s",
			strings.Join(errors, "; "))
	}

	return nil
}

// validateHooks reports semantic errors for enabled hook definitions.
func validateHooks(hooks []HookConfig) []string {
	var errors []string
	for i, hook := range hooks {
		if hook.Disabled {
			continue
		}
		prefix := fmt.Sprintf("hooks[%d]", i+1)
		event := strings.TrimSpace(hook.Event)
		if event == "" {
			errors = append(
				errors, prefix+".event must not be empty",
			)
		} else if !stringIn(event, validHookEvents()) {
			errors = append(
				errors,
				fmt.Sprintf(
					"%s.event must be one of %s, got %q",
					prefix,
					joinOptions(
						validHookEvents(),
					),
					event,
				),
			)
		}
		if strings.TrimSpace(hook.Command) == "" {
			errors = append(
				errors, prefix+".command must not be empty",
			)
		}
		if err := validateHookMatcher(hook.Matcher); err != nil {
			errors = append(
				errors, fmt.Sprintf("%s.matcher: %v", prefix,
					err),
			)
		}
	}

	return errors
}

// validatePlugins reports semantic errors for enabled plugin definitions.
func validatePlugins(plugins []PluginConfig) []string {
	var errors []string
	for i, plugin := range plugins {
		if plugin.Disabled {
			continue
		}
		prefix := fmt.Sprintf("plugins[%d]", i+1)
		if strings.TrimSpace(plugin.Name) == "" {
			errors = append(
				errors, prefix+".name must not be empty",
			)
		}
		if strings.TrimSpace(plugin.Command) == "" {
			errors = append(
				errors, prefix+".command must not be empty",
			)
		}
	}

	return errors
}

// validateSubagents reports semantic errors for child-agent profiles.
func validateSubagents(subagents SubagentConfig) []string {
	var errors []string
	names := map[string]bool{}
	for i, profile := range subagents.Profiles {
		prefix := fmt.Sprintf("subagents.profile[%d]", i+1)
		name := strings.TrimSpace(profile.Name)
		if name == "" {
			errors = append(
				errors, prefix+".name must not be empty",
			)
		} else if names[name] {
			errors = append(
				errors,
				prefix+".name duplicates "+strconvQuote(name),
			)
		}
		names[name] = true
		if profile.Disabled {
			continue
		}
		if strings.TrimSpace(profile.Description) == "" {
			errors = append(
				errors, prefix+".description must not be empty",
			)
		}
		if len(profile.AllowedTools) == 0 {
			errors = append(
				errors,
				prefix+".allowed_tools must not be empty",
			)
		}
		if strings.TrimSpace(profile.SystemPrompt) != "" &&
			strings.TrimSpace(profile.SystemPromptFile) != "" {

			errors = append(
				errors, prefix+" must set only one of "+
					"system_prompt or system_prompt_file",
			)
		}
		errors = append(
			errors, validateSubagentProvider(prefix, profile)...,
		)
	}

	return errors
}

// validateSubagentProvider reports semantic profile provider errors.
func validateSubagentProvider(prefix string,
	profile SubagentProfileConfig) []string {

	var errors []string
	if value := strings.TrimSpace(profile.Provider); value != "" &&
		!stringIn(value, validProviders()) {

		errors = append(
			errors,
			fmt.Sprintf(
				"%s.provider must be one of %s, got %q", prefix,
				joinOptions(
					validProviders(),
				),
				value,
			),
		)
	}
	if value := strings.TrimSpace(profile.OpenAIAPI); value != "" &&
		!stringIn(value, validOpenAIAPIs()) {

		errors = append(
			errors,
			fmt.Sprintf(
				"%s.openai_api must be one of %s, got %q",
				prefix,
				joinOptions(
					validOpenAIAPIs(),
				),
				value,
			),
		)
	}
	if value := strings.TrimSpace(profile.ReasoningEffort); value != "" &&
		!stringIn(value, validReasoningEfforts()) {

		errors = append(
			errors,
			fmt.Sprintf(
				"%s.reasoning_effort must be one of %s, got %q",
				prefix,
				joinOptions(
					validReasoningEfforts(),
				),
				value,
			),
		)
	}
	if value := strings.TrimSpace(profile.ReasoningSummary); value != "" &&
		!stringIn(value, validReasoningSummaries()) {

		errors = append(
			errors,
			fmt.Sprintf(
				"%s.reasoning_summary must be one of %s, "+
					"got %q", prefix,
				joinOptions(
					validReasoningSummaries(),
				),
				value,
			),
		)
	}

	return errors
}

// validateHookMatcher reports malformed hook matcher regular expressions.
func validateHookMatcher(matcher string) error {
	if matcher == "" || matcher == "*" {
		return nil
	}
	if _, err := regexp.Compile(matcher); err != nil {
		return err
	}

	return nil
}

// strconvQuote returns a quoted string without importing it at call sites.
func strconvQuote(value string) string {
	return fmt.Sprintf("%q", value)
}

// validProviders returns provider names accepted by project config.
func validProviders() []string {
	return []string{providerEchoName, providerOpenAIName}
}

// validOpenAIAPIs returns OpenAI API modes accepted by project config.
func validOpenAIAPIs() []string {
	return []string{openAIAPIChat, openAIAPIResponses}
}

// validReasoningEfforts returns reasoning effort values accepted by config.
func validReasoningEfforts() []string {
	return []string{"none", "minimal", "low", "medium", "high", "xhigh"}
}

// validReasoningSummaries returns reasoning summary values accepted by config.
func validReasoningSummaries() []string {
	return []string{"auto", "concise", "detailed"}
}

// validHookEvents returns lifecycle hook event names accepted by config.
func validHookEvents() []string {
	return []string{
		"SessionStart",
		"UserPromptSubmit",
		"TurnStart",
		"TurnComplete",
		"ContextBuild",
		"PreToolUse",
		"PostToolUse",
		"PreCompact",
		"PostCompact",
	}
}

// stringIn reports whether value appears in options.
func stringIn(value string, options []string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}

	return false
}

// joinOptions renders options for diagnostics.
func joinOptions(options []string) string {
	return strings.Join(options, ", ")
}
