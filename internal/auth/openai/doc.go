// Package openai stores and refreshes OpenAI ChatGPT/Codex OAuth
// credentials.
//
// The package intentionally uses only the Go standard library. It implements
// the same device-code style flow used by Codex-compatible clients, persists a
// small JSON auth file with restrictive permissions, and exposes only bearer
// tokens to the provider layer.
package openai
