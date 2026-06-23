package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	openaiauth "harness/internal/auth/openai"
	"harness/internal/provider/openai"
	"harness/internal/tool"
)

// parseFlags converts CLI arguments into the command configuration.
func parseFlags(args []string, stderr io.Writer) (cliConfig, error) {
	if len(args) > 0 {
		switch args[0] {
		case commandSessions:
			return parseSessionsFlags(args[1:], stderr)

		case commandShow:
			return parseShowFlags(args[1:], stderr)

		case commandResume:
			return parseResumeFlags(args[1:], stderr)

		case commandTool:
			return parseToolFlags(args[1:], stderr)

		case commandChat:
			return parseChatFlags(args[1:], stderr)

		case commandCompact:
			return parseCompactFlags(args[1:], stderr)

		case commandAuth:
			return parseAuthFlags(args[1:], stderr)

		case commandConfig:
			return parseConfigFlags(args[1:], stderr)
		}
		if !strings.HasPrefix(args[0], "-") {
			return cliConfig{}, fmt.Errorf("unknown command %q; "+
				"use \"harness help\" to list commands",
				args[0])
		}
	}

	return parseRunFlags(args, stderr)
}

// parseConfigFlags converts config subcommands into inspection config.
func parseConfigFlags(args []string, stderr io.Writer) (cliConfig, error) {
	if len(args) == 0 {
		return cliConfig{}, fmt.Errorf("config requires check, show, " +
			"or schema")
	}
	cfg := cliConfig{
		command:      commandConfig,
		configAction: args[0],
	}
	fs := flag.NewFlagSet(
		commandConfig+" "+cfg.configAction, flag.ContinueOnError,
	)
	fs.SetOutput(stderr)
	fs.BoolVar(
		&cfg.configEffective, "effective", false,
		"show compiled defaults merged with project config",
	)
	if err := fs.Parse(args[1:]); err != nil {
		return cliConfig{}, err
	}
	if fs.NArg() != 0 {
		return cliConfig{}, fmt.Errorf("config %s accepts no "+
			"positional arguments", cfg.configAction)
	}
	switch cfg.configAction {
	case "check", "show", "schema":
		if cfg.configEffective && cfg.configAction != "show" {
			return cliConfig{}, fmt.Errorf("--effective only " +
				"applies to config show")
		}

		return cfg, nil

	default:
		return cliConfig{}, fmt.Errorf("config requires check, show, " +
			"or schema")
	}
}

// parseAuthFlags converts auth subcommands into credential management config.
func parseAuthFlags(args []string, stderr io.Writer) (cliConfig, error) {
	if len(args) == 0 {
		return cliConfig{}, fmt.Errorf("auth requires login, status, " +
			"or logout")
	}
	cfg := cliConfig{
		command:           commandAuth,
		authAction:        args[0],
		authIssuer:        openaiauth.DefaultIssuer,
		authClientID:      openaiauth.DefaultClientID,
		authCodexBaseURL:  openaiauth.DefaultCodexBaseURL,
		baseURL:           openai.DefaultBaseURL,
		openaiAPI:         openai.APIResponses,
		openaiAPIExplicit: true,
	}
	fs := flag.NewFlagSet(
		commandAuth+" "+cfg.authAction, flag.ContinueOnError,
	)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.authPath, "auth-file", "", "OpenAI OAuth credential file",
	)
	fs.StringVar(
		&cfg.authIssuer, "issuer", cfg.authIssuer,
		"OpenAI OAuth issuer URL",
	)
	fs.StringVar(
		&cfg.authClientID, "client-id", cfg.authClientID,
		"OpenAI OAuth client id",
	)
	fs.StringVar(
		&cfg.authCodexBaseURL, "codex-base-url", cfg.authCodexBaseURL,
		"OpenAI Codex backend URL for OAuth tokens",
	)
	if err := fs.Parse(args[1:]); err != nil {
		return cliConfig{}, err
	}
	if fs.NArg() != 0 {
		return cliConfig{}, fmt.Errorf("auth %s accepts no positional "+
			"arguments", cfg.authAction)
	}
	switch cfg.authAction {
	case "login", "status", "logout":
		return cfg, nil

	default:
		return cliConfig{}, fmt.Errorf("auth requires login, status, " +
			"or logout")
	}
}

// parseToolFlags converts tool subcommand arguments into configuration.
func parseToolFlags(args []string, stderr io.Writer) (cliConfig, error) {
	if len(args) == 0 {
		return cliConfig{}, fmt.Errorf("provide a tool name")
	}
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	cfg := configCLIConfigDefaults(defaults)
	cfg.command = commandTool
	cfg.toolName = args[0]
	fs := flag.NewFlagSet(
		commandTool+" "+cfg.toolName, flag.ContinueOnError,
	)
	fs.SetOutput(stderr)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false, "print the tool result as JSON",
	)
	fs.IntVar(
		&cfg.toolLimit, "limit", 0,
		"maximum entries or lines for tools that support limits",
	)
	fs.IntVar(
		&cfg.toolOffset, "offset", 0,
		"1-indexed line offset for tools that support offsets",
	)
	fs.IntVar(
		&cfg.toolContext, "context", 0,
		"context lines around grep matches",
	)
	fs.IntVar(
		&cfg.toolTimeout, "timeout", 0,
		"timeout in seconds for tools that run commands",
	)
	fs.StringVar(
		&cfg.toolContent, "content", "",
		"complete file content for tools that write files",
	)
	fs.StringVar(
		&cfg.toolOldText, "old", "",
		"exact original text for tools that edit files",
	)
	fs.StringVar(
		&cfg.toolNewText, "new", "",
		"exact replacement text for tools that edit files",
	)
	fs.BoolVar(
		&cfg.toolIgnoreCase, "ignore-case", false,
		"case-insensitive matching for tools that search text",
	)
	fs.BoolVar(
		&cfg.toolRegex, "regex", false,
		"treat grep pattern as Go RE2 regular expression",
	)
	fs.BoolVar(
		&cfg.toolDryRun, "dry-run", false,
		"preview edit changes without modifying files",
	)
	fs.StringVar(
		&cfg.toolGlob, "glob", "",
		"slash-separated glob filter for find or grep",
	)
	fs.StringVar(
		&cfg.toolRawArguments, "args", "",
		"raw JSON object arguments for plugin tools",
	)
	if err := fs.Parse(args[1:]); err != nil {
		return cliConfig{}, err
	}

	switch cfg.toolName {
	case tool.NameLS:
		if fs.NArg() > 1 {
			return cliConfig{}, fmt.Errorf("ls accepts at most " +
				"one path")
		}
		if fs.NArg() == 1 {
			cfg.toolPath = fs.Arg(0)
		}

	case tool.NameRead:
		if fs.NArg() != 1 {
			return cliConfig{}, fmt.Errorf("read accepts exactly " +
				"one path")
		}
		cfg.toolPath = fs.Arg(0)

	case tool.NameFind:
		if fs.NArg() < 1 || fs.NArg() > 2 {
			return cliConfig{}, fmt.Errorf("find accepts a query " +
				"and optional path")
		}
		cfg.toolQuery = fs.Arg(0)
		if fs.NArg() == 2 {
			cfg.toolPath = fs.Arg(1)
		}

	case tool.NameGrep:
		if fs.NArg() < 1 || fs.NArg() > 2 {
			return cliConfig{}, fmt.Errorf("grep accepts a " +
				"pattern and optional path")
		}
		cfg.toolQuery = fs.Arg(0)
		if fs.NArg() == 2 {
			cfg.toolPath = fs.Arg(1)
		}

	case tool.NameWrite:
		if fs.NArg() != 1 {
			return cliConfig{}, fmt.Errorf("write accepts " +
				"exactly one path")
		}
		cfg.toolPath = fs.Arg(0)

	case tool.NameEdit:
		if fs.NArg() != 1 {
			return cliConfig{}, fmt.Errorf("edit accepts exactly " +
				"one path")
		}
		if cfg.toolOldText == "" {
			return cliConfig{}, fmt.Errorf("edit requires --old")
		}
		cfg.toolPath = fs.Arg(0)

	case tool.NameBash:
		if fs.NArg() == 0 {
			return cliConfig{}, fmt.Errorf("bash requires a " +
				"command")
		}
		cfg.toolCommand = strings.Join(fs.Args(), " ")

	default:
		if cfg.toolRawArguments != "" && fs.NArg() != 0 {
			return cliConfig{}, fmt.Errorf("plugin tool %s "+
				"accepts --args or one positional JSON "+
				"argument, not both", cfg.toolName)
		}
		switch fs.NArg() {
		case 0:
			if cfg.toolRawArguments == "" {
				cfg.toolRawArguments = "{}"
			}

		case 1:
			cfg.toolRawArguments = fs.Arg(0)

		default:
			return cliConfig{}, fmt.Errorf("plugin tool %s "+
				"accepts at most one positional JSON argument",
				cfg.toolName)
		}
	}

	return cfg, nil
}

// parseChatFlags converts chat subcommand flags into configuration.
func parseChatFlags(args []string, stderr io.Writer) (cliConfig, error) {
	cfg, fs, err := parseChatLikeFlags(commandChat, args, stderr)
	if err != nil {
		return cliConfig{}, err
	}
	if fs.NArg() != 0 {
		return cliConfig{}, fmt.Errorf("chat accepts no positional " +
			"arguments")
	}

	return cfg, nil
}

// parseResumeFlags converts resume subcommand flags into chat configuration.
func parseResumeFlags(args []string, stderr io.Writer) (cliConfig, error) {
	cfg, fs, err := parseChatLikeFlags(commandResume, args, stderr)
	if err != nil {
		return cliConfig{}, err
	}
	if cfg.sessionID != "" && fs.NArg() != 0 {
		return cliConfig{}, fmt.Errorf("resume accepts either " +
			"--session or a positional session id, not both")
	}
	if cfg.sessionID == "" {
		if fs.NArg() != 1 {
			return cliConfig{}, fmt.Errorf("resume requires " +
				"exactly one session id")
		}
		cfg.sessionID = fs.Arg(0)
	}

	return cfg, nil
}

// parseChatLikeFlags converts chat-style flags shared by chat and resume.
func parseChatLikeFlags(name string, args []string, stderr io.Writer) (
	cliConfig, *flag.FlagSet, error) {

	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, nil, err
	}
	cfg := configCLIConfigDefaults(defaults)
	cfg.command = commandChat
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", cfg.sessionDir,
		"session log directory",
	)
	fs.StringVar(
		&cfg.sessionID, "session", "",
		"existing session id or prefix to continue",
	)
	providerFlags := registerProviderFlags(fs, &cfg)
	fs.IntVar(
		&cfg.maxToolRounds, "max-tool-rounds", cfg.maxToolRounds,
		"maximum model/tool exchange rounds per user turn",
	)
	fs.IntVar(
		&cfg.keepMessages, "keep-messages", cfg.keepMessages,
		"fallback message count when token retention is disabled",
	)
	fs.IntVar(
		&cfg.keepRecentTokens, "keep-recent-tokens",
		cfg.keepRecentTokens,
		"approximate recent context tokens kept raw by compaction",
	)
	fs.BoolVar(
		&cfg.autoCompact, "auto-compact", cfg.autoCompact,
		"automatically compact large chat context before model calls",
	)
	fs.IntVar(
		&cfg.autoCompactLimit, "auto-compact-threshold-tokens",
		cfg.autoCompactLimit,
		"approximate token threshold for automatic compaction",
	)
	if name == commandResume {
		args = intersperseFlagArgs(fs, args)
	}
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, nil, err
	}
	if cfg.maxToolRounds < 1 {
		return cliConfig{}, nil, fmt.Errorf("max-tool-rounds must be " +
			"positive")
	}
	if cfg.autoCompact && cfg.autoCompactLimit < 1 {
		return cliConfig{}, nil, fmt.Errorf(
			"auto-compact-threshold-tokens must be positive")
	}
	if cfg.keepRecentTokens < 1 {
		return cliConfig{}, nil, fmt.Errorf("keep-recent-tokens must " +
			"be positive")
	}
	mergeExplicitProviderFlags(fs, &cfg, providerFlags)

	return cfg, fs, nil
}

// intersperseFlagArgs moves recognized flags before positional arguments so
// resume accepts the natural "resume <id> --flag value" form.
func intersperseFlagArgs(fs *flag.FlagSet, args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			positionals = append(positionals, args[index+1:]...)

			break
		}
		if !looksLikeFlag(arg) {
			positionals = append(positionals, arg)

			continue
		}

		name, hasInlineValue := flagName(arg)
		known := fs.Lookup(name)
		flags = append(flags, arg)
		if known == nil || hasInlineValue || flagIsBool(known) {
			continue
		}
		if index+1 < len(args) {
			index++
			flags = append(flags, args[index])
		}
	}

	return append(flags, positionals...)
}

// looksLikeFlag reports whether arg is syntactically a command-line flag.
func looksLikeFlag(arg string) bool {
	return strings.HasPrefix(arg, "-") && arg != "-"
}

// flagName returns the flag name without leading dashes or inline value text.
func flagName(arg string) (string, bool) {
	name := strings.TrimLeft(arg, "-")
	valueIndex := strings.Index(name, "=")
	if valueIndex < 0 {
		return name, false
	}

	return name[:valueIndex], true
}

// flagIsBool reports whether f can be specified without a value.
func flagIsBool(f *flag.Flag) bool {
	boolFlag, ok := f.Value.(interface {
		IsBoolFlag() bool
	})

	return ok && boolFlag.IsBoolFlag()
}

// parseCompactFlags converts compact subcommand flags into configuration.
func parseCompactFlags(args []string, stderr io.Writer) (cliConfig, error) {
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	cfg := configCLIConfigDefaults(defaults)
	cfg.command = commandCompact
	fs := flag.NewFlagSet(commandCompact, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", cfg.sessionDir,
		"session log directory",
	)
	fs.StringVar(
		&cfg.sessionID, "session", "",
		"existing session id or prefix to compact",
	)
	providerFlags := registerProviderFlags(fs, &cfg)
	fs.IntVar(
		&cfg.keepMessages, "keep-messages", cfg.keepMessages,
		"fallback message count when token retention is disabled",
	)
	fs.IntVar(
		&cfg.keepRecentTokens, "keep-recent-tokens",
		cfg.keepRecentTokens,
		"approximate recent context tokens kept raw by compaction",
	)
	fs.StringVar(
		&cfg.compactInstructions, "instructions", "",
		"optional focus instructions for the compaction summary",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.sessionID == "" {
		return cliConfig{}, fmt.Errorf("compact requires --session")
	}
	if cfg.keepRecentTokens < 1 {
		return cliConfig{}, fmt.Errorf("keep-recent-tokens must be " +
			"positive")
	}
	mergeExplicitProviderFlags(fs, &cfg, providerFlags)

	return cfg, nil
}

// parseRunFlags converts default command flags into a run configuration.
func parseRunFlags(args []string, stderr io.Writer) (cliConfig, error) {
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	cfg := configCLIConfigDefaults(defaults)
	cfg.command = commandRun
	fs := flag.NewFlagSet("harness", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.prompt, "p", "", "prompt to run non-interactively")
	fs.StringVar(
		&cfg.sessionDir, "session-dir", cfg.sessionDir,
		"session log directory",
	)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false, "print the turn result as JSON",
	)
	providerFlags := registerProviderFlags(fs, &cfg)
	fs.IntVar(
		&cfg.maxToolRounds, "max-tool-rounds", cfg.maxToolRounds,
		"maximum model/tool exchange rounds per user turn",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.maxToolRounds < 1 {
		return cliConfig{}, fmt.Errorf("max-tool-rounds must be " +
			"positive")
	}
	if cfg.prompt == "" {
		return cliConfig{}, fmt.Errorf("provide a prompt with -p")
	}
	mergeExplicitProviderFlags(fs, &cfg, providerFlags)

	return cfg, nil
}

// providerFlagValues stores values registered outside cliConfig directly.
type providerFlagValues struct {
	// apiKey stores the explicit API-key flag without exposing env
	// defaults.
	apiKey *string
}

// registerProviderFlags adds provider and OpenAI flags shared by run commands.
func registerProviderFlags(fs *flag.FlagSet,
	cfg *cliConfig) providerFlagValues {

	fs.StringVar(
		&cfg.provider, "provider", cfg.provider,
		"model provider: echo or openai",
	)
	fs.StringVar(&cfg.model, "model", cfg.model, "provider model name")
	addOpenAIFlags(fs, cfg)
	fs.StringVar(
		&cfg.baseURL, "base-url", cfg.baseURL,
		"OpenAI-compatible API base URL",
	)

	return providerFlagValues{
		apiKey: apiKeyFlagValue(fs),
	}
}

// mergeExplicitProviderFlags records provider flags supplied by the caller.
func mergeExplicitProviderFlags(fs *flag.FlagSet, cfg *cliConfig,
	values providerFlagValues) {

	cfg.providerExplicit = cfg.providerExplicit ||
		flagWasSet(fs, "provider")
	cfg.baseURLExplicit = cfg.baseURLExplicit || flagWasSet(fs, "base-url")
	cfg.openaiAPIExplicit = cfg.openaiAPIExplicit ||
		flagWasSet(fs, "openai-api")
	applyAPIKeyFlag(cfg, *values.apiKey)
}

// apiKeyFlagValue registers the API key flag without exposing env defaults.
func apiKeyFlagValue(fs *flag.FlagSet) *string {
	value := ""
	fs.StringVar(&value, "api-key", "", "OpenAI-compatible API key")

	return &value
}

// apiKeyFromEnv returns the configured OpenAI-compatible API key fallback.
func apiKeyFromEnv() string {
	if value := os.Getenv("OPENAI_API_KEY"); value != "" {
		return value
	}

	return os.Getenv("OPENROUTER_API_KEY")
}

// addOpenAIFlags registers provider-specific OpenAI controls.
func addOpenAIFlags(fs *flag.FlagSet, cfg *cliConfig) {
	fs.StringVar(
		&cfg.authPath, "auth-file", "", "OpenAI OAuth credential file",
	)
	fs.StringVar(
		&cfg.openaiAPI, "openai-api", cfg.openaiAPI,
		"OpenAI API shape: chat or responses",
	)
	fs.StringVar(
		&cfg.openaiTransport, "openai-transport", cfg.openaiTransport,
		"OpenAI Responses transport: http, websocket, or auto",
	)
	fs.StringVar(
		&cfg.reasoningEffort, "reasoning-effort", cfg.reasoningEffort,
		"OpenAI reasoning effort: none, minimal, low, medium, "+
			"high, or xhigh",
	)
	fs.StringVar(
		&cfg.reasoningSummary, "reasoning-summary",
		cfg.reasoningSummary,
		"OpenAI reasoning summary: auto, concise, or detailed",
	)
}

// applyAPIKeyFlag lets an explicit flag value override environment auth.
func applyAPIKeyFlag(cfg *cliConfig, apiKey string) {
	if apiKey != "" {
		cfg.apiKey = apiKey
		cfg.apiKeyExplicit = true
	}
}

// flagWasSet reports whether fs parsed a flag explicitly from the CLI.
func flagWasSet(fs *flag.FlagSet, name string) bool {
	wasSet := false
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			wasSet = true
		}
	})

	return wasSet
}

// parseSessionsFlags converts sessions subcommand flags into configuration.
func parseSessionsFlags(args []string, stderr io.Writer) (cliConfig, error) {
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	cfg := cliConfig{
		command:    commandSessions,
		sessionDir: configSessionDir(defaults),
	}
	fs := flag.NewFlagSet(commandSessions, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", cfg.sessionDir,
		"session log directory",
	)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false,
		"print the session list as JSON",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}

	return cfg, nil
}

// parseShowFlags converts show subcommand flags and arguments into config.
func parseShowFlags(args []string, stderr io.Writer) (cliConfig, error) {
	defaults, err := loadConfigDefaults()
	if err != nil {
		return cliConfig{}, err
	}
	cfg := cliConfig{
		command:    commandShow,
		sessionDir: configSessionDir(defaults),
	}
	fs := flag.NewFlagSet(commandShow, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", cfg.sessionDir,
		"session log directory",
	)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false,
		"print the raw session events as JSON",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if fs.NArg() != 1 {
		return cliConfig{}, fmt.Errorf("provide exactly one session " +
			"id or prefix")
	}
	cfg.sessionID = fs.Arg(0)

	return cfg, nil
}
