package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	harnessconfig "harness/internal/config"
)

// runConfig executes one project-config inspection subcommand.
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
		fmt.Fprintf(stdout, "config ok: %s\n", loaded.Path)

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

// loadConfigForInspection loads the nearest project config for config commands.
func loadConfigForInspection() (harnessconfig.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return harnessconfig.Config{}, fmt.Errorf("get working "+
			"directory: %w", err)
	}

	return harnessconfig.Load(cwd)
}

// formatLoadedConfig renders values explicitly present in project config.
func formatLoadedConfig(cfg harnessconfig.Config) string {
	var out strings.Builder
	fmt.Fprintln(&out, "Config")
	writeConfigPath(&out, cfg.Path)
	fmt.Fprintln(&out)
	writeRawConfigSections(&out, cfg)

	return strings.TrimRight(out.String(), "\n")
}

// formatEffectiveConfig renders project config after compiled defaults apply.
func formatEffectiveConfig(cfg harnessconfig.Config) string {
	defaults := configCLIConfigDefaults(cfg)
	var out strings.Builder
	fmt.Fprintln(&out, "Effective Config")
	writeConfigPath(&out, cfg.Path)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "Session")
	fmt.Fprintf(&out, "- dir: %s\n", defaults.sessionDir)
	fmt.Fprintf(&out, "- max tool rounds: %d\n", defaults.maxToolRounds)
	fmt.Fprintf(&out, "- keep messages: %d\n", defaults.keepMessages)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "Context")
	fmt.Fprintf(&out, "- auto compact: %t\n", defaults.autoCompact)
	fmt.Fprintf(
		&out, "- auto compact threshold tokens: %d\n",
		defaults.autoCompactLimit,
	)
	fmt.Fprintf(
		&out, "- keep recent tokens: %d\n", defaults.keepRecentTokens,
	)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "Provider")
	fmt.Fprintf(&out, "- name: %s\n", displayString(defaults.provider))
	fmt.Fprintf(&out, "- model: %s\n", displayString(defaults.model))
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "OpenAI")
	fmt.Fprintf(&out, "- base url: %s\n", displayString(defaults.baseURL))
	fmt.Fprintf(&out, "- api: %s\n", displayString(defaults.openaiAPI))
	fmt.Fprintf(
		&out, "- reasoning effort: %s\n",
		displayString(defaults.reasoningEffort),
	)
	fmt.Fprintf(
		&out, "- reasoning summary: %s\n",
		displayString(defaults.reasoningSummary),
	)
	fmt.Fprintln(&out)
	writeHooksAndPlugins(&out, cfg.Hooks, cfg.Plugins)

	return strings.TrimRight(out.String(), "\n")
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
	fmt.Fprintln(out, "Provider")
	fmt.Fprintf(out, "- name: %s\n", displayString(cfg.Provider.Name))
	fmt.Fprintf(out, "- model: %s\n", displayString(cfg.Provider.Model))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "OpenAI")
	fmt.Fprintf(out, "- base url: %s\n", displayString(cfg.OpenAI.BaseURL))
	fmt.Fprintf(out, "- api: %s\n", displayString(cfg.OpenAI.API))
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
}

// writeConfigPath renders the source path or an explicit missing marker.
func writeConfigPath(out *strings.Builder, path string) {
	if path == "" {
		fmt.Fprintln(out, "- path: (not found)")

		return
	}
	fmt.Fprintf(out, "- path: %s\n", path)
}

// writeHooksAndPlugins renders repeatable config sections with counts.
func writeHooksAndPlugins(out *strings.Builder,
	hooks []harnessconfig.HookConfig,
	plugins []harnessconfig.PluginConfig) {

	fmt.Fprintln(out, "Hooks")
	fmt.Fprintf(out, "- count: %d\n", len(hooks))
	for i, hook := range hooks {
		fmt.Fprintf(
			out, "- %d: %s %s\n", i+1, displayString(hook.Event),
			displayString(hook.Command),
		)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Plugins")
	fmt.Fprintf(out, "- count: %d\n", len(plugins))
	for i, plugin := range plugins {
		fmt.Fprintf(
			out, "- %d: %s %s\n", i+1, displayString(plugin.Name),
			displayString(plugin.Command),
		)
	}
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
		"plugins",
	}
}

// configSchemaHeader returns the TOML header representing table.
func configSchemaHeader(table string) string {
	switch table {
	case "hooks", "plugins":
		return "[[" + table + "]]"

	default:
		return "[" + table + "]"
	}
}
