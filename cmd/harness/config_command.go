package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	harnessconfig "harness/internal/config"
)

// runConfig executes one config inspection subcommand.
func runConfig(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	switch cfg.configAction {
	case "check":
		loaded, err := loadConfigForInspection()
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		if loaded.Path == "" {
			fmt.Fprintln(stdout, "no config found")

			return 0
		}
		fmt.Fprintln(stdout, formatConfigCheck(loaded))

		return 0

	case "show":
		loaded, err := loadConfigForInspection()
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		if cfg.configEffective {
			fmt.Fprintln(stdout, formatEffectiveConfig(loaded))
		} else {
			fmt.Fprintln(stdout, formatLoadedConfig(loaded))
		}

		return 0

	case "schema":
		fmt.Fprintln(stdout, formatConfigSchema())

		return 0

	default:
		fmt.Fprintf(
			stderr, "error: unknown config action %q\n",
			cfg.configAction,
		)

		return 2
	}
}

// loadConfigForInspection loads merged config for config commands.
func loadConfigForInspection() (harnessconfig.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return harnessconfig.Config{}, fmt.Errorf("get working "+
			"directory: %w", err)
	}

	return harnessconfig.Load(cwd)
}

// formatLoadedConfig renders values present in loaded config files.
func formatLoadedConfig(cfg harnessconfig.Config) string {
	var out strings.Builder
	fmt.Fprintln(&out, "Config")
	writeConfigSources(&out, cfg)
	fmt.Fprintln(&out)
	writeRawConfigSections(&out, cfg)

	return strings.TrimRight(out.String(), "\n")
}

// formatEffectiveConfig renders loaded config after compiled defaults apply.
func formatEffectiveConfig(cfg harnessconfig.Config) string {
	defaults := configCLIConfigDefaults(cfg)
	var out strings.Builder
	fmt.Fprintln(&out, "Effective Config")
	writeConfigSources(&out, cfg)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "Session")
	writeEffectiveString(&out, "dir", defaults.sessionDir, cfg.Session.Dir)
	writeEffectiveInt(
		&out, "max tool rounds", defaults.maxToolRounds,
		cfg.Session.MaxToolRounds,
	)
	writeEffectiveInt(
		&out, "keep messages", defaults.keepMessages,
		cfg.Session.KeepMessages,
	)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "Context")
	writeEffectiveBool(
		&out, "auto compact", defaults.autoCompact,
		cfg.Context.AutoCompact,
	)
	writeEffectiveInt(
		&out, "auto compact threshold tokens",
		defaults.autoCompactLimit,
		cfg.Context.AutoCompactThresholdTokens,
	)
	writeEffectiveInt(
		&out, "keep recent tokens", defaults.keepRecentTokens,
		cfg.Context.KeepRecentTokens,
	)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "Prompt")
	writeEffectiveString(
		&out, "system prompt", defaults.promptConfig.SystemPrompt,
		cfg.Prompt.SystemPrompt,
	)
	writeEffectiveString(
		&out, "system prompt file",
		defaults.promptConfig.SystemPromptFile,
		cfg.Prompt.SystemPromptFile,
	)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "Provider")
	writeEffectiveString(&out, "name", defaults.provider, cfg.Provider.Name)
	writeEffectiveString(&out, "model", defaults.model, cfg.Provider.Model)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "OpenAI")
	writeEffectiveString(
		&out, "base url", defaults.baseURL, cfg.OpenAI.BaseURL,
	)
	writeEffectiveString(&out, "api", defaults.openaiAPI, cfg.OpenAI.API)
	writeEffectiveString(
		&out, "transport", defaults.openaiTransport,
		cfg.OpenAI.Transport,
	)
	writeEffectiveString(
		&out, "reasoning effort", defaults.reasoningEffort,
		cfg.OpenAI.ReasoningEffort,
	)
	writeEffectiveString(
		&out, "reasoning summary", defaults.reasoningSummary,
		cfg.OpenAI.ReasoningSummary,
	)
	fmt.Fprintln(&out)
	writeHooksAndPlugins(&out, cfg.Hooks, cfg.Plugins)
	fmt.Fprintln(&out)
	writeSubagents(&out, cfg.Subagents)

	return strings.TrimRight(out.String(), "\n")
}

// formatConfigCheck renders a concise successful validation summary.
func formatConfigCheck(cfg harnessconfig.Config) string {
	var out strings.Builder
	fmt.Fprintf(&out, "config ok: %s\n", configPathSummary(cfg))
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "Summary")
	fmt.Fprintf(
		&out, "- provider: %s\n",
		displayString(
			configProvider(cfg),
		),
	)
	fmt.Fprintf(&out, "- model: %s\n", displayString(cfg.Provider.Model))
	fmt.Fprintf(
		&out, "- openai api: %s\n",
		displayString(
			configOpenAIAPI(cfg),
		),
	)
	fmt.Fprintf(
		&out, "- hooks: %d enabled, %d disabled\n",
		enabledHooks(cfg.Hooks), disabledHooks(cfg.Hooks),
	)
	fmt.Fprintf(
		&out, "- plugins: %d enabled, %d disabled",
		enabledPlugins(cfg.Plugins), disabledPlugins(cfg.Plugins),
	)
	fmt.Fprintf(
		&out, "\n- subagents: %d enabled, %d disabled",
		enabledSubagentProfiles(cfg.Subagents),
		disabledSubagentProfiles(cfg.Subagents),
	)

	return out.String()
}

// writeRawConfigSections renders the loaded TOML values without defaults.
func writeRawConfigSections(out *strings.Builder, cfg harnessconfig.Config) {
	fmt.Fprintln(out, "Session")
	fmt.Fprintf(out, "- dir: %s\n", displayString(cfg.Session.Dir))
	fmt.Fprintf(
		out, "- max tool rounds: %s\n",
		displayInt(cfg.Session.MaxToolRounds),
	)
	fmt.Fprintf(
		out, "- keep messages: %s\n",
		displayInt(cfg.Session.KeepMessages),
	)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Context")
	fmt.Fprintf(out, "- auto compact: %t\n", cfg.Context.AutoCompact)
	fmt.Fprintf(
		out, "- auto compact threshold tokens: %s\n",
		displayInt(cfg.Context.AutoCompactThresholdTokens),
	)
	fmt.Fprintf(
		out, "- keep recent tokens: %s\n",
		displayInt(cfg.Context.KeepRecentTokens),
	)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Prompt")
	fmt.Fprintf(
		out, "- system prompt: %s\n",
		displayString(cfg.Prompt.SystemPrompt),
	)
	fmt.Fprintf(
		out, "- system prompt file: %s\n",
		displayString(cfg.Prompt.SystemPromptFile),
	)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Provider")
	fmt.Fprintf(out, "- name: %s\n", displayString(cfg.Provider.Name))
	fmt.Fprintf(out, "- model: %s\n", displayString(cfg.Provider.Model))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "OpenAI")
	fmt.Fprintf(out, "- base url: %s\n", displayString(cfg.OpenAI.BaseURL))
	fmt.Fprintf(out, "- api: %s\n", displayString(cfg.OpenAI.API))
	fmt.Fprintf(
		out, "- transport: %s\n", displayString(cfg.OpenAI.Transport),
	)
	fmt.Fprintf(
		out, "- reasoning effort: %s\n",
		displayString(cfg.OpenAI.ReasoningEffort),
	)
	fmt.Fprintf(
		out, "- reasoning summary: %s\n",
		displayString(cfg.OpenAI.ReasoningSummary),
	)
	fmt.Fprintln(out)
	writeHooksAndPlugins(out, cfg.Hooks, cfg.Plugins)
	fmt.Fprintln(out)
	writeSubagents(out, cfg.Subagents)
}

// writeConfigSources renders config source paths or an explicit missing marker.
func writeConfigSources(out *strings.Builder, cfg harnessconfig.Config) {
	if len(cfg.Paths) == 0 && cfg.Path == "" {
		fmt.Fprintln(out, "- path: (not found)")

		return
	}
	if len(cfg.Paths) == 0 {
		fmt.Fprintf(out, "- path: %s\n", cfg.Path)

		return
	}
	if len(cfg.Paths) == 1 {
		fmt.Fprintf(out, "- path: %s\n", cfg.Paths[0])

		return
	}
	fmt.Fprintln(out, "- paths:")
	for _, path := range cfg.Paths {
		fmt.Fprintf(out, "  - %s\n", path)
	}
}

// configPathSummary returns a compact source description for config check.
func configPathSummary(cfg harnessconfig.Config) string {
	if len(cfg.Paths) == 0 {
		if cfg.Path == "" {
			return "(not found)"
		}

		return cfg.Path
	}
	if len(cfg.Paths) == 1 {
		return cfg.Paths[0]
	}

	return strings.Join(cfg.Paths, " + ")
}

// writeHooksAndPlugins renders repeatable config sections with counts.
func writeHooksAndPlugins(out *strings.Builder,
	hooks []harnessconfig.HookConfig,
	plugins []harnessconfig.PluginConfig) {

	fmt.Fprintln(out, "Hooks")
	fmt.Fprintf(
		out, "- count: %d (%d enabled, %d disabled)\n", len(hooks),
		enabledHooks(hooks), disabledHooks(hooks),
	)
	for i, hook := range hooks {
		fmt.Fprintf(
			out, "- %d: %s %s%s\n", i+1, displayString(hook.Event),
			displayString(hook.Command),
			disabledSuffix(hook.Disabled),
		)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Plugins")
	fmt.Fprintf(
		out, "- count: %d (%d enabled, %d disabled)\n", len(plugins),
		enabledPlugins(plugins), disabledPlugins(plugins),
	)
	for i, plugin := range plugins {
		fmt.Fprintf(
			out, "- %d: %s %s%s\n", i+1, displayString(plugin.Name),
			displayString(plugin.Command),
			disabledSuffix(plugin.Disabled),
		)
		if len(plugin.Env) > 0 {
			fmt.Fprintf(
				out, "  env: %s\n",
				strings.Join(plugin.Env, ", "),
			)
		}
	}
}

// writeSubagents renders configured child-agent profiles.
func writeSubagents(out *strings.Builder,
	subagents harnessconfig.SubagentConfig) {

	fmt.Fprintln(out, "Subagents")
	fmt.Fprintf(out, "- enabled: %t\n", subagents.Enabled)
	fmt.Fprintf(
		out, "- max per turn: %s\n", displayInt(subagents.MaxPerTurn),
	)
	fmt.Fprintf(
		out, "- max concurrent: %s\n",
		displayInt(subagents.MaxConcurrent),
	)
	fmt.Fprintf(
		out, "- profiles: %d (%d enabled, %d disabled)\n",
		len(subagents.Profiles), enabledSubagentProfiles(subagents),
		disabledSubagentProfiles(subagents),
	)
	for i, profile := range subagents.Profiles {
		fmt.Fprintf(
			out, "- %d: %s %s%s\n", i+1,
			displayString(profile.Name),
			displayString(profile.Description),
			disabledSuffix(profile.Disabled),
		)
	}
}

// writeEffectiveString renders one effective string value with its source.
func writeEffectiveString(out *strings.Builder, label string, value string,
	configValue string) {

	fmt.Fprintf(
		out, "- %s: %s (%s)\n", label, displayString(value),
		valueSource(configValue != ""),
	)
}

// writeEffectiveInt renders one effective integer value with its source.
func writeEffectiveInt(out *strings.Builder, label string, value int,
	configValue int) {

	fmt.Fprintf(
		out, "- %s: %d (%s)\n", label, value,
		valueSource(configValue > 0),
	)
}

// writeEffectiveBool renders one effective boolean value with its source.
func writeEffectiveBool(out *strings.Builder, label string, value bool,
	configValue bool) {

	fmt.Fprintf(
		out, "- %s: %t (%s)\n", label, value, valueSource(configValue),
	)
}

// valueSource renders the source marker for one effective value.
func valueSource(configured bool) string {
	if configured {
		return "config"
	}

	return "default"
}

// disabledSuffix marks disabled extension rows in config output.
func disabledSuffix(disabled bool) string {
	if disabled {
		return " (disabled)"
	}

	return ""
}

// enabledHooks counts configured hooks that are not disabled.
func enabledHooks(hooks []harnessconfig.HookConfig) int {
	return len(hooks) - disabledHooks(hooks)
}

// disabledHooks counts disabled hook definitions.
func disabledHooks(hooks []harnessconfig.HookConfig) int {
	count := 0
	for _, hook := range hooks {
		if hook.Disabled {
			count++
		}
	}

	return count
}

// enabledPlugins counts configured plugins that are not disabled.
func enabledPlugins(plugins []harnessconfig.PluginConfig) int {
	return len(plugins) - disabledPlugins(plugins)
}

// disabledPlugins counts disabled plugin definitions.
func disabledPlugins(plugins []harnessconfig.PluginConfig) int {
	count := 0
	for _, plugin := range plugins {
		if plugin.Disabled {
			count++
		}
	}

	return count
}

// enabledSubagentProfiles counts configured profiles advertised to the model.
func enabledSubagentProfiles(subagents harnessconfig.SubagentConfig) int {
	if !subagents.Enabled {
		return 0
	}

	return len(subagents.Profiles) - disabledSubagentProfiles(subagents)
}

// disabledSubagentProfiles counts hidden subagent profiles.
func disabledSubagentProfiles(subagents harnessconfig.SubagentConfig) int {
	count := 0
	for _, profile := range subagents.Profiles {
		if profile.Disabled {
			count++
		}
	}

	return count
}

// displayString returns a printable value marker for optional strings.
func displayString(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(unset)"
	}

	return value
}

// displayInt returns a printable value marker for optional positive integers.
func displayInt(value int) string {
	if value == 0 {
		return "(unset)"
	}

	return fmt.Sprintf("%d", value)
}

// formatConfigSchema renders the supported config keys from schema metadata.
func formatConfigSchema() string {
	fields := fieldsByTable(harnessconfig.Fields())
	var out strings.Builder
	fmt.Fprintln(&out, "Config Schema")
	for _, table := range configSchemaTableOrder() {
		tableFields := fields[table]
		if len(tableFields) == 0 {
			continue
		}
		fmt.Fprintln(&out)
		fmt.Fprintf(&out, "%s\n", configSchemaHeader(table))
		for _, field := range tableFields {
			fmt.Fprintf(
				&out, "- %s (%s): %s\n", field.Key, field.Type,
				field.Description,
			)
		}
	}

	return strings.TrimRight(out.String(), "\n")
}

// fieldsByTable groups schema fields by table while preserving field order.
func fieldsByTable(
	fields []harnessconfig.FieldInfo) map[string][]harnessconfig.FieldInfo {

	grouped := make(map[string][]harnessconfig.FieldInfo)
	for _, field := range fields {
		grouped[field.Table] = append(grouped[field.Table], field)
	}

	return grouped
}

// configSchemaTableOrder returns the preferred human reading order.
func configSchemaTableOrder() []string {
	return []string{
		"session", "context", "provider", "openai", "hooks",
		"plugins", "subagents", "subagents.profile",
	}
}

// configSchemaHeader returns the TOML header representing table.
func configSchemaHeader(table string) string {
	switch table {
	case "hooks", "plugins":
		return "[[" + table + "]]"

	case "subagents.profile":
		return "[[" + table + "]]"

	default:
		return "[" + table + "]"
	}
}
