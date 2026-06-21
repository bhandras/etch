// Package model defines the provider-neutral stream interface used by the core.
//
// Model clients stream text, reasoning summaries, tool calls, usage counters,
// transport metrics, and provider response identities through one small event
// protocol. The echo client remains useful for deterministic local tests, while
// OpenAI-compatible clients can add network, auth, and API-specific behavior
// behind the same interface.
package model
