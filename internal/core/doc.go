// Package core coordinates one agent turn across sessions and model clients.
//
// The package owns the provider-neutral turn loop: it builds prompt context,
// streams model events, dispatches tool calls, applies hooks, records durable
// JSONL session events, performs compaction, accepts steering messages, and
// preserves provider response identities for continuation-capable APIs. CLI and
// terminal concerns live outside this package so the agent engine can be tested
// as ordinary Go code.
package core
