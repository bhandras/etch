// Package plugins adapts explicit out-of-process plugin commands into local
// model-callable tools.
//
// Plugins are configured, not discovered. Each plugin is a child process that
// speaks a small JSONL request/response protocol over stdin and stdout. The
// package keeps that protocol behind the existing tool registry interface so
// the core loop can execute builtin and plugin tools through one path.
package plugins
