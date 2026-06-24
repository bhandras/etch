package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	openaiauth "etch/internal/auth/openai"
	harnessconfig "etch/internal/config"
	"etch/internal/core"
	"etch/internal/model"
	"etch/internal/provider/openai"
)

// modelClient creates the provider selected by run command configuration.
func modelClient(cfg cliConfig) (model.Client, error) {
	switch cfg.provider {
	case "", providerEcho:
		return model.EchoClient{}, nil

	case openai.ProviderName:
		if cfg.model == "" {
			return nil, fmt.Errorf("openai provider requires " +
				"--model or provider.model config")
		}
		if cfg.apiKeyExplicit && cfg.apiKey != "" {
			return openAIAPIKeyClient(cfg), nil
		}
		var oauthErr error
		token := ""
		baseURL := openaiauth.DefaultCodexBaseURL
		creds, err := loadOpenAIOAuthCredentials(cfg)
		if err == nil {
			token = creds.Tokens.AccessToken
			baseURL = creds.CodexBaseURL
		} else if errors.Is(err, openaiauth.ErrNotLoggedIn) {
			token = openaiauth.AccessTokenFromEnv()
		} else {
			oauthErr = err
		}

		if token == "" && cfg.apiKey != "" && oauthErr == nil {
			return openAIAPIKeyClient(cfg), nil
		}
		if oauthErr != nil {
			return nil, oauthErr
		}
		if token == "" {
			return nil, fmt.Errorf("openai provider requires " +
				"etch auth login, CODEX_ACCESS_TOKEN, " +
				"--api-key, OPENAI_API_KEY, or " +
				"OPENROUTER_API_KEY")
		}

		apiMode := cfg.openaiAPI
		if !cfg.openaiAPIExplicit {
			apiMode = openai.APIResponses
		}
		if cfg.baseURLExplicit {
			baseURL = cfg.baseURL
		}

		return &openai.Client{
			BaseURL:          baseURL,
			APIKey:           token,
			AccountID:        creds.Tokens.AccountID,
			Model:            cfg.model,
			API:              apiMode,
			Transport:        cfg.openaiTransport,
			ReasoningEffort:  cfg.reasoningEffort,
			ReasoningSummary: cfg.reasoningSummary,
			UserAgent:        etchUserAgent,
		}, nil

	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.provider)
	}
}

// openAIAPIKeyClient returns a provider client using explicit API-key mode.
func openAIAPIKeyClient(cfg cliConfig) model.Client {
	return &openai.Client{
		BaseURL:          cfg.baseURL,
		APIKey:           cfg.apiKey,
		Model:            cfg.model,
		API:              cfg.openaiAPI,
		Transport:        cfg.openaiTransport,
		ReasoningEffort:  cfg.reasoningEffort,
		ReasoningSummary: cfg.reasoningSummary,
		UserAgent:        etchUserAgent,
	}
}

// loadOpenAIOAuthCredentials loads and refreshes stored OAuth credentials.
func loadOpenAIOAuthCredentials(cfg cliConfig) (openaiauth.Credentials, error) {
	path, err := authStorePath(cfg)
	if err != nil {
		return openaiauth.Credentials{}, err
	}
	creds, err := openaiauth.EnsureAccessToken(
		context.Background(), path, authOptions(cfg),
	)
	if err != nil {
		return openaiauth.Credentials{}, err
	}

	return creds, nil
}

// loadConfigDefaults loads project TOML defaults for commands that honor them.
func loadConfigDefaults() (harnessconfig.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return harnessconfig.Config{}, fmt.Errorf("get working "+
			"directory: %w", err)
	}

	return harnessconfig.Load(cwd)
}

// configCLIConfigDefaults projects TOML defaults into shared CLI defaults.
func configCLIConfigDefaults(cfg harnessconfig.Config) cliConfig {
	return cliConfig{
		sessionDir:        configSessionDir(cfg),
		provider:          configProvider(cfg),
		providerExplicit:  cfg.Provider.Name != "",
		model:             cfg.Provider.Model,
		baseURL:           configOpenAIBaseURL(cfg),
		apiKey:            apiKeyFromEnv(),
		openaiAPI:         configOpenAIAPI(cfg),
		openaiAPIExplicit: cfg.OpenAI.API != "",
		openaiTransport:   configOpenAITransport(cfg),
		reasoningEffort:   cfg.OpenAI.ReasoningEffort,
		reasoningSummary:  cfg.OpenAI.ReasoningSummary,
		maxToolRounds:     configMaxToolRounds(cfg),
		autoCompact:       cfg.Context.AutoCompact,
		autoCompactLimit:  configAutoCompactThreshold(cfg),
		keepMessages:      configKeepMessages(cfg),
		keepRecentTokens:  configKeepRecentTokens(cfg),
		baseURLExplicit:   cfg.OpenAI.BaseURL != "",
		hooks:             cfg.Hooks,
		plugins:           cfg.Plugins,
		promptConfig:      cfg.Prompt,
		subagents:         cfg.Subagents,
		configPath:        cfg.Path,
	}
}

// configSessionDir returns the configured session directory or the CLI default.
func configSessionDir(cfg harnessconfig.Config) string {
	if cfg.Session.Dir != "" {
		return expandHomePath(cfg.Session.Dir)
	}
	if hasHomeConfigPath(cfg) {
		return expandHomePath("~/.etch/sessions")
	}

	return defaultSessionDir
}

// hasHomeConfigPath reports whether a merged config includes the home file.
func hasHomeConfigPath(cfg harnessconfig.Config) bool {
	if len(cfg.Paths) == 0 {
		return isHomeConfigPath(cfg.Path)
	}
	for _, path := range cfg.Paths {
		if isHomeConfigPath(path) {
			return true
		}
	}

	return false
}

// expandHomePath expands ~ and ~/ prefixes in user-facing config paths.
func expandHomePath(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}

	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}

// isHomeConfigPath reports whether path is the user's home-level config file.
func isHomeConfigPath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	want := filepath.Join(
		home, harnessconfig.ProjectConfigDir,
		harnessconfig.ConfigFileName,
	)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	return filepath.Clean(absPath) == filepath.Clean(want)
}

// configProvider returns the configured provider or the offline default.
func configProvider(cfg harnessconfig.Config) string {
	return configOrDefault(cfg.Provider.Name, providerEcho)
}

// configOpenAIBaseURL returns the configured OpenAI endpoint or the default.
func configOpenAIBaseURL(cfg harnessconfig.Config) string {
	return configOrDefault(cfg.OpenAI.BaseURL, openai.DefaultBaseURL)
}

// configOpenAIAPI returns the configured OpenAI API shape or the default.
func configOpenAIAPI(cfg harnessconfig.Config) string {
	return configOrDefault(cfg.OpenAI.API, openai.APIChatCompletions)
}

// configOpenAITransport returns the configured transport or the HTTP default.
func configOpenAITransport(cfg harnessconfig.Config) string {
	return configOrDefault(cfg.OpenAI.Transport, openai.TransportHTTP)
}

// configMaxToolRounds returns the configured tool-loop limit or the default.
func configMaxToolRounds(cfg harnessconfig.Config) int {
	return positiveConfigOrDefault(
		cfg.Session.MaxToolRounds, core.DefaultMaxToolRounds,
	)
}

// configKeepMessages returns the configured compaction retention or default.
func configKeepMessages(cfg harnessconfig.Config) int {
	return positiveConfigOrDefault(
		cfg.Session.KeepMessages, core.DefaultCompactKeepMessages,
	)
}

// configKeepRecentTokens returns the configured raw retention token budget.
func configKeepRecentTokens(cfg harnessconfig.Config) int {
	return positiveConfigOrDefault(
		cfg.Context.KeepRecentTokens,
		core.DefaultCompactKeepRecentTokens,
	)
}

// configAutoCompactThreshold returns the configured auto-compaction threshold.
func configAutoCompactThreshold(cfg harnessconfig.Config) int {
	return positiveConfigOrDefault(
		cfg.Context.AutoCompactThresholdTokens,
		core.DefaultAutoCompactThresholdTokens,
	)
}

// configOrDefault returns value unless it is the type zero value.
func configOrDefault[T comparable](value T, fallback T) T {
	var zero T
	if value != zero {
		return value
	}

	return fallback
}

// positiveConfigOrDefault returns value unless it is non-positive.
func positiveConfigOrDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}

	return fallback
}

// autoCompactThreshold returns the active auto-compaction threshold or zero.
func autoCompactThreshold(cfg cliConfig) int {
	if !cfg.autoCompact {
		return 0
	}

	return cfg.autoCompactLimit
}
