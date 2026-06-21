package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	openaiauth "harness/internal/auth/openai"
	harnessconfig "harness/internal/config"
	"harness/internal/core"
	"harness/internal/model"
	"harness/internal/provider/openai"
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
				"harness auth login, CODEX_ACCESS_TOKEN, " +
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
			Model:            cfg.model,
			API:              apiMode,
			ReasoningEffort:  cfg.reasoningEffort,
			ReasoningSummary: cfg.reasoningSummary,
			UserAgent:        harnessUserAgent,
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
		ReasoningEffort:  cfg.reasoningEffort,
		ReasoningSummary: cfg.reasoningSummary,
		UserAgent:        harnessUserAgent,
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

// configSessionDir returns the configured session directory or the CLI default.
func configSessionDir(cfg harnessconfig.Config) string {
	if cfg.Session.Dir != "" {
		return cfg.Session.Dir
	}

	return defaultSessionDir
}

// configProvider returns the configured provider or the offline default.
func configProvider(cfg harnessconfig.Config) string {
	if cfg.Provider.Name != "" {
		return cfg.Provider.Name
	}

	return providerEcho
}

// configOpenAIBaseURL returns the configured OpenAI endpoint or the default.
func configOpenAIBaseURL(cfg harnessconfig.Config) string {
	if cfg.OpenAI.BaseURL != "" {
		return cfg.OpenAI.BaseURL
	}

	return openai.DefaultBaseURL
}

// configOpenAIAPI returns the configured OpenAI API shape or the default.
func configOpenAIAPI(cfg harnessconfig.Config) string {
	if cfg.OpenAI.API != "" {
		return cfg.OpenAI.API
	}

	return openai.APIChatCompletions
}

// configMaxToolRounds returns the configured tool-loop limit or the default.
func configMaxToolRounds(cfg harnessconfig.Config) int {
	if cfg.Session.MaxToolRounds > 0 {
		return cfg.Session.MaxToolRounds
	}

	return core.DefaultMaxToolRounds
}

// configKeepMessages returns the configured compaction retention or default.
func configKeepMessages(cfg harnessconfig.Config) int {
	if cfg.Session.KeepMessages > 0 {
		return cfg.Session.KeepMessages
	}

	return core.DefaultCompactKeepMessages
}

// configKeepRecentTokens returns the configured raw retention token budget.
func configKeepRecentTokens(cfg harnessconfig.Config) int {
	if cfg.Context.KeepRecentTokens > 0 {
		return cfg.Context.KeepRecentTokens
	}

	return core.DefaultCompactKeepRecentTokens
}

// configAutoCompactThreshold returns the configured auto-compaction threshold.
func configAutoCompactThreshold(cfg harnessconfig.Config) int {
	if cfg.Context.AutoCompactThresholdTokens > 0 {
		return cfg.Context.AutoCompactThresholdTokens
	}

	return core.DefaultAutoCompactThresholdTokens
}

// autoCompactThreshold returns the active auto-compaction threshold or zero.
func autoCompactThreshold(cfg cliConfig) int {
	if !cfg.autoCompact {
		return 0
	}

	return cfg.autoCompactLimit
}
