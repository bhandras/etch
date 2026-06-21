package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	openaiauth "harness/internal/auth/openai"
)

// runAuth executes OpenAI OAuth credential management commands.
func runAuth(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	path, err := authStorePath(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	switch cfg.authAction {
	case "login":
		creds, err := openaiauth.LoginDevice(
			context.Background(), authOptions(cfg),
			func(event openaiauth.LoginProgress) {
				if event.DeviceCode.UserCode != "" {
					fmt.Fprintf(
						stdout, "Open %s\nEnter "+
							"code %s\n%s\n",
						event.DeviceCode.VerificationURL,
						event.DeviceCode.UserCode,
						"Waiting for authorization...",
					)

					return
				}
				if event.Message != "" {
					fmt.Fprintln(stdout, event.Message)
				}
			},
		)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		if err := openaiauth.Save(path, creds); err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		fmt.Fprintf(
			stdout, "saved OpenAI OAuth credentials to %s\n", path,
		)

		return 0

	case "status":
		return runAuthStatus(path, stdout, stderr)

	case "logout":
		removed, err := openaiauth.Logout(path)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		if removed {
			fmt.Fprintf(
				stdout, "removed OpenAI OAuth credentials "+
					"from %s\n", path,
			)
		} else {
			fmt.Fprintln(
				stdout, "no OpenAI OAuth credentials found",
			)
		}

		return 0

	default:
		fmt.Fprintf(
			stderr, "error: unknown auth action %q\n",
			cfg.authAction,
		)

		return 2
	}
}

// runAuthStatus renders non-secret OpenAI authentication state.
func runAuthStatus(path string, stdout io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stdout, "OpenAI Auth")
	if openaiauth.AccessTokenFromEnv() != "" {
		fmt.Fprintln(stdout, "- env token: CODEX_ACCESS_TOKEN")
	} else {
		fmt.Fprintln(stdout, "- env token: not set")
	}

	creds, err := openaiauth.Load(path)
	if err != nil {
		if errors.Is(err, openaiauth.ErrNotLoggedIn) {
			fmt.Fprintln(stdout, "- stored login: not found")

			return 0
		}
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	email, accountID := openaiauth.ParseChatGPTClaims(creds.Tokens.IDToken)
	fmt.Fprintln(stdout, "- stored login: ChatGPT/Codex OAuth")
	fmt.Fprintf(stdout, "- auth file: %s\n", path)
	fmt.Fprintf(stdout, "- backend: %s\n", creds.CodexBaseURL)
	if email != "" {
		fmt.Fprintf(stdout, "- email: %s\n", email)
	}
	if accountID == "" {
		accountID = creds.Tokens.AccountID
	}
	if accountID != "" {
		fmt.Fprintf(stdout, "- account: %s\n", accountID)
	}

	return 0
}

// authStorePath returns the active OpenAI OAuth credential file path.
func authStorePath(cfg cliConfig) (string, error) {
	if cfg.authPath != "" {
		return filepath.Abs(cfg.authPath)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	return openaiauth.DefaultStorePath(cwd)
}

// authOptions converts CLI auth flags into provider-specific OAuth options.
func authOptions(cfg cliConfig) openaiauth.Options {
	return openaiauth.Options{
		Issuer:       cfg.authIssuer,
		ClientID:     cfg.authClientID,
		CodexBaseURL: cfg.authCodexBaseURL,
	}
}
