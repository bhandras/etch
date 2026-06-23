package config

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// valueString identifies quoted TOML string values in the config
	// schema.
	valueString valueKind = "string"

	// valuePositiveInt identifies positive integer values in the config
	// schema.
	valuePositiveInt valueKind = "positive integer"

	// valueBool identifies boolean values in the config schema.
	valueBool valueKind = "boolean"

	// valueStringList identifies a single-line TOML array of quoted string
	// values.
	valueStringList valueKind = "string list"
)

const (
	// tableNormal marks ordinary singleton TOML tables such as [session].
	tableNormal tableKind = iota

	// tableNamespace marks ordinary tables that group array table entries
	// but do not accept scalar assignments themselves.
	tableNamespace

	// tableArray marks repeatable array tables such as [[plugins]].
	tableArray

	// tablePrefixArray marks repeatable array tables that may include an
	// event suffix, such as [[hooks.PreToolUse]].
	tablePrefixArray
)

// FieldInfo describes one supported user-facing config setting.
type FieldInfo struct {
	// Table is the normalized config table family that owns this field.
	Table string

	// Key is the TOML assignment key under Table.
	Key string

	// Type is the human-readable scalar type accepted by this field.
	Type string

	// Description explains the field's purpose for generated references.
	Description string
}

// valueKind identifies the scalar parser used by one config field.
type valueKind string

// tableKind identifies how TOML headers instantiate a schema table.
type tableKind int

// parsedValue stores one TOML scalar after schema-directed parsing.
type parsedValue struct {
	text    string
	texts   []string
	integer int
	boolean bool
}

// configScope stores the active schema target for subsequent assignments.
type configScope struct {
	cfg     *Config
	table   string
	hook    *HookConfig
	plugin  *PluginConfig
	profile *SubagentProfileConfig
}

// assignmentTarget carries the config object currently receiving a field.
type assignmentTarget struct {
	cfg     *Config
	hook    *HookConfig
	plugin  *PluginConfig
	profile *SubagentProfileConfig
}

// configField describes one assignable scalar in the config schema.
type configField struct {
	table       string
	key         string
	kind        valueKind
	description string
	apply       func(*assignmentTarget, parsedValue)
}

// tableSchema stores all scalar fields accepted by one table family.
type tableSchema struct {
	name       string
	kind       tableKind
	fields     map[string]configField
	beginArray func(*Config, string) (configScope, error)
}

// schemaTables returns the complete supported scalar config surface.
func schemaTables() map[string]tableSchema {
	tables := map[string]tableSchema{}
	addSchemaTable(tables, "session", tableNormal, nil, []configField{
		{
			key:         "dir",
			kind:        valueString,
			description: "Directory where JSONL session logs are stored.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Session.Dir = value.text
			},
		},
		{
			key:  "max_tool_rounds",
			kind: valuePositiveInt,
			description: "Maximum model/tool exchange rounds allowed " +
				"for one user turn.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Session.MaxToolRounds = value.integer
			},
		},
		{
			key:  "keep_messages",
			kind: valuePositiveInt,
			description: "Fallback number of recent message events " +
				"preserved by manual compaction.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Session.KeepMessages = value.integer
			},
		},
	})
	addSchemaTable(tables, "context", tableNormal, nil, []configField{
		{
			key:  "auto_compact",
			kind: valueBool,
			description: "Whether chat automatically compacts large " +
				"context before model calls.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Context.AutoCompact = value.boolean
			},
		},
		{
			key:  "auto_compact_threshold_tokens",
			kind: valuePositiveInt,
			description: "Approximate context token threshold that " +
				"triggers automatic compaction.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Context.AutoCompactThresholdTokens =
					value.integer
			},
		},
		{
			key:  "keep_recent_tokens",
			kind: valuePositiveInt,
			description: "Approximate amount of recent raw context to " +
				"retain after compaction.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Context.KeepRecentTokens = value.integer
			},
		},
	})
	addSchemaTable(tables, "provider", tableNormal, nil, []configField{
		{
			key:         "name",
			kind:        valueString,
			description: "Model provider identifier, such as echo or openai.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Provider.Name = value.text
			},
		},
		{
			key:         "model",
			kind:        valueString,
			description: "Provider-specific model identifier.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Provider.Model = value.text
			},
		},
	})
	addSchemaTable(tables, "openai", tableNormal, nil, []configField{
		{
			key:  "base_url",
			kind: valueString,
			description: "OpenAI-compatible API base URL used by the " +
				"OpenAI provider.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.OpenAI.BaseURL = value.text
			},
		},
		{
			key:         "api",
			kind:        valueString,
			description: "OpenAI API shape, usually chat or responses.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.OpenAI.API = value.text
			},
		},
		{
			key:  "transport",
			kind: valueString,
			description: "OpenAI Responses transport: http, " +
				"websocket, or auto.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.OpenAI.Transport = value.text
			},
		},
		{
			key:  "reasoning_effort",
			kind: valueString,
			description: "Reasoning effort requested for models that " +
				"support it.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.OpenAI.ReasoningEffort = value.text
			},
		},
		{
			key:  "reasoning_summary",
			kind: valueString,
			description: "Reasoning summary detail requested for " +
				"models that support it.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.OpenAI.ReasoningSummary = value.text
			},
		},
	})
	addSchemaTable(
		tables, "hooks", tablePrefixArray, beginHookArray,
		[]configField{
			{
				key:  "event",
				kind: valueString,
				description: "Lifecycle event name used by generic " +
					"[[hooks]] entries.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.hook.Event = value.text
				},
			},
			{
				key:  "matcher",
				kind: valueString,
				description: "Optional event-specific matcher. Empty " +
					"means every event instance matches.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.hook.Matcher = value.text
				},
			},
			{
				key:  "command",
				kind: valueString,
				description: "Shell command that receives hook JSON on " +
					"stdin.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.hook.Command = value.text
				},
			},
			{
				key:         "timeout_seconds",
				kind:        valuePositiveInt,
				description: "Maximum hook runtime in seconds.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.hook.TimeoutSeconds = value.integer
				},
			},
			{
				key:         "disabled",
				kind:        valueBool,
				description: "Keep the hook configured without running it.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.hook.Disabled = value.boolean
				},
			},
		},
	)
	addSchemaTable(
		tables, "plugins", tableArray, beginPluginArray,
		[]configField{
			{
				key:         "name",
				kind:        valueString,
				description: "Human-readable plugin name for diagnostics.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.plugin.Name = value.text
				},
			},
			{
				key:         "command",
				kind:        valueString,
				description: "Shell command that starts the plugin process.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.plugin.Command = value.text
				},
			},
			{
				key:  "timeout_seconds",
				kind: valuePositiveInt,
				description: "Maximum plugin initialization and call " +
					"runtime in seconds.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.plugin.TimeoutSeconds = value.integer
				},
			},
			{
				key:  "env",
				kind: valueStringList,
				description: "Environment variable names to forward " +
					"into the otherwise sanitized plugin process " +
					"environment.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.plugin.Env = value.texts
				},
			},
			{
				key:         "disabled",
				kind:        valueBool,
				description: "Keep the plugin configured without starting it.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.plugin.Disabled = value.boolean
				},
			},
		},
	)
	addSchemaTable(tables, "subagents", tableNormal, nil, []configField{
		{
			key:  "enabled",
			kind: valueBool,
			description: "Whether configured subagent profiles are " +
				"available through the task tool.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Subagents.Enabled = value.boolean
			},
		},
		{
			key:  "max_per_turn",
			kind: valuePositiveInt,
			description: "Maximum subagent tasks the parent may " +
				"delegate during one turn.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Subagents.MaxPerTurn = value.integer
			},
		},
		{
			key:  "max_concurrent",
			kind: valuePositiveInt,
			description: "Maximum child agents that may run at the " +
				"same time.",
			apply: func(target *assignmentTarget,
				value parsedValue) {

				target.cfg.Subagents.MaxConcurrent = value.integer
			},
		},
	})
	addSchemaTable(
		tables, "subagents.profile", tableArray,
		beginSubagentProfileArray,
		[]configField{
			{
				key:         "name",
				kind:        valueString,
				description: "Model-facing profile name.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.Name = value.text
				},
			},
			{
				key:  "description",
				kind: valueString,
				description: "When the parent model should delegate " +
					"to this profile.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.Description = value.text
				},
			},
			{
				key:  "provider",
				kind: valueString,
				description: "Optional provider override for this " +
					"profile.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.Provider = value.text
				},
			},
			{
				key:  "model",
				kind: valueString,
				description: "Optional model override for this " +
					"profile.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.Model = value.text
				},
			},
			{
				key:  "base_url",
				kind: valueString,
				description: "Optional OpenAI-compatible base URL " +
					"override.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.BaseURL = value.text
				},
			},
			{
				key:         "openai_api",
				kind:        valueString,
				description: "Optional OpenAI API shape override.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.OpenAIAPI = value.text
				},
			},
			{
				key:         "reasoning_effort",
				kind:        valueString,
				description: "Optional reasoning effort override.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.ReasoningEffort = value.text
				},
			},
			{
				key:         "reasoning_summary",
				kind:        valueString,
				description: "Optional reasoning summary override.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.ReasoningSummary = value.text
				},
			},
			{
				key:         "system_prompt",
				kind:        valueString,
				description: "Inline child-agent instructions.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.SystemPrompt = value.text
				},
			},
			{
				key:         "system_prompt_file",
				kind:        valueString,
				description: "Path to child-agent instructions.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.SystemPromptFile = value.text
				},
			},
			{
				key:         "allowed_tools",
				kind:        valueStringList,
				description: "Tool names this profile may call.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.AllowedTools = value.texts
				},
			},
			{
				key:  "max_tool_rounds",
				kind: valuePositiveInt,
				description: "Maximum model/tool exchange rounds for " +
					"this child.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.MaxToolRounds = value.integer
				},
			},
			{
				key:         "auto_compact",
				kind:        valueBool,
				description: "Whether this child profile auto-compacts.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.AutoCompact = value.boolean
				},
			},
			{
				key:  "auto_compact_threshold_tokens",
				kind: valuePositiveInt,
				description: "Approximate child context token " +
					"threshold for auto compaction.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.AutoCompactThresholdTokens =
						value.integer
				},
			},
			{
				key:  "keep_messages",
				kind: valuePositiveInt,
				description: "Recent message events retained by child " +
					"compaction fallback.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.KeepMessages = value.integer
				},
			},
			{
				key:  "keep_recent_tokens",
				kind: valuePositiveInt,
				description: "Approximate recent raw context retained " +
					"after child compaction.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.KeepRecentTokens = value.integer
				},
			},
			{
				key:         "disabled",
				kind:        valueBool,
				description: "Keep the profile configured but hidden.",
				apply: func(target *assignmentTarget,
					value parsedValue) {

					target.profile.Disabled = value.boolean
				},
			},
		},
	)

	return tables
}

// addSchemaTable stores one schema table and annotates each field with table.
func addSchemaTable(tables map[string]tableSchema, name string, kind tableKind,
	beginArray func(*Config, string) (configScope, error),
	fields []configField) {

	schema := tableSchema{
		name:       name,
		kind:       kind,
		fields:     make(map[string]configField),
		beginArray: beginArray,
	}
	for _, field := range fields {
		field.table = name
		schema.fields[field.key] = field
	}
	tables[name] = schema
}

// beginNormalConfigTable returns the assignment scope for a normal table.
func beginNormalConfigTable(cfg *Config, name string) (configScope, error) {
	schema, ok := schemaForTable(name)
	if !ok {
		return configScope{}, fmt.Errorf("unknown table %q", name)
	}
	if schema.name != name {
		return configScope{}, fmt.Errorf("unknown table %q", name)
	}
	switch schema.kind {
	case tableNormal, tableNamespace, tableArray, tablePrefixArray:
		return configScope{cfg: cfg, table: schema.name}, nil

	default:
		return configScope{}, fmt.Errorf("unknown table %q", name)
	}
}

// beginArrayConfigTable returns the assignment scope for an array table.
func beginArrayConfigTable(cfg *Config, name string) (configScope, error) {
	schema, ok := schemaForTable(name)
	if !ok || schema.beginArray == nil {
		return configScope{}, fmt.Errorf("unknown array table %q", name)
	}

	return schema.beginArray(cfg, name)
}

// schemaForTable returns the normalized schema table for a parsed table name.
func schemaForTable(table string) (tableSchema, bool) {
	tables := schemaTables()
	if schema, ok := tables[table]; ok {
		return schema, true
	}
	for _, schema := range tables {
		if schema.kind == tablePrefixArray &&
			strings.HasPrefix(table, schema.name+".") {
			return schema, true
		}
	}

	return tableSchema{}, false
}

// applySchemaAssignment parses and stores one key/value pair through schema.
func applySchemaAssignment(scope configScope, key string, value string) error {
	if scope.table == "" {
		return fmt.Errorf("top-level key %q is not supported", key)
	}
	schema, ok := schemaForTable(scope.table)
	if !ok {
		return fmt.Errorf("unknown table %q", scope.table)
	}
	if schema.kind != tableNormal && scope.hook == nil &&
		scope.plugin == nil && scope.profile == nil {
		return fmt.Errorf("%s setting %q must be inside %s",
			schema.name, key, schema.assignmentHeader())
	}
	field, ok := schema.fields[key]
	if !ok {
		return unknownKeyError(schema, key)
	}
	parsed, err := parseSchemaValue(field.kind, value)
	if err != nil {
		return fmt.Errorf("%s.%s: %w", field.table, field.key, err)
	}
	field.apply(&assignmentTarget{
		cfg:     scope.cfg,
		hook:    scope.hook,
		plugin:  scope.plugin,
		profile: scope.profile,
	}, parsed)

	return nil
}

// beginHookArray appends the hook entry represented by name.
func beginHookArray(cfg *Config, name string) (configScope, error) {
	hook := HookConfig{}
	if suffix, ok := strings.CutPrefix(name, "hooks."); ok {
		if suffix == "" || strings.Contains(suffix, ".") {
			return configScope{}, fmt.Errorf("invalid hook event "+
				"table %q", name)
		}
		hook.Event = suffix
	} else if name != "hooks" {
		return configScope{}, fmt.Errorf("unknown array table %q", name)
	}
	cfg.Hooks = append(cfg.Hooks, hook)

	return configScope{
		cfg:   cfg,
		table: "hooks",
		hook:  &cfg.Hooks[len(cfg.Hooks)-1],
	}, nil
}

// beginPluginArray appends the plugin entry represented by name.
func beginPluginArray(cfg *Config, name string) (configScope, error) {
	if name != "plugins" {
		return configScope{}, fmt.Errorf("unknown array table %q", name)
	}
	cfg.Plugins = append(cfg.Plugins, PluginConfig{})

	return configScope{
		cfg:    cfg,
		table:  "plugins",
		plugin: &cfg.Plugins[len(cfg.Plugins)-1],
	}, nil
}

// beginSubagentProfileArray appends the subagent profile represented by name.
func beginSubagentProfileArray(cfg *Config, name string) (configScope, error) {
	if name != "subagents.profile" {
		return configScope{}, fmt.Errorf("unknown array table %q", name)
	}
	cfg.Subagents.Profiles = append(
		cfg.Subagents.Profiles, SubagentProfileConfig{},
	)

	return configScope{
		cfg:     cfg,
		table:   "subagents.profile",
		profile: &cfg.Subagents.Profiles[len(cfg.Subagents.Profiles)-1],
	}, nil
}

// assignmentHeader returns the TOML array-table form required for schema.
func (s tableSchema) assignmentHeader() string {
	switch s.kind {
	case tablePrefixArray:
		return "[[" + s.name + ".*]]"

	case tableArray:
		return "[[" + s.name + "]]"

	default:
		return "[" + s.name + "]"
	}
}

// parseSchemaValue parses raw TOML text according to kind.
func parseSchemaValue(kind valueKind, value string) (parsedValue, error) {
	switch kind {
	case valueString:
		text, err := parseString(value)

		return parsedValue{text: text}, err

	case valuePositiveInt:
		integer, err := parsePositiveInt(value)

		return parsedValue{integer: integer}, err

	case valueBool:
		boolean, err := parseBool(value)

		return parsedValue{boolean: boolean}, err

	case valueStringList:
		texts, err := parseStringList(value)

		return parsedValue{texts: texts}, err

	default:
		return parsedValue{}, fmt.Errorf("unsupported config "+
			"value type %q", kind)
	}
}

// unknownKeyError reports an unsupported key with nearby schema context.
func unknownKeyError(schema tableSchema, key string) error {
	return fmt.Errorf("unknown key %s.%s (known keys: %s)", schema.name,
		key, strings.Join(schema.keys(), ", "))
}

// keys returns the sorted keys supported by schema.
func (s tableSchema) keys() []string {
	keys := make([]string, 0, len(s.fields))
	for key := range s.fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return keys
}

// Fields returns the sorted config fields supported by the schema.
func Fields() []FieldInfo {
	tables := schemaTables()
	tableNames := make([]string, 0, len(tables))
	for name := range tables {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)

	var fields []FieldInfo
	for _, tableName := range tableNames {
		schema := tables[tableName]
		for _, key := range schema.keys() {
			field := schema.fields[key]
			fields = append(fields, FieldInfo{
				Table:       field.table,
				Key:         field.key,
				Type:        string(field.kind),
				Description: field.description,
			})
		}
	}

	return fields
}
