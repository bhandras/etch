// Package tool defines the builtin tool registry used by the agent core.
//
// The registry is intentionally small. It exposes model-facing tool schemas and
// dispatches complete tool calls to pure-Go implementations without depending
// on shell commands or external binaries.
package tool
