// Package main provides the etch command-line entrypoint.
//
// The command exposes non-interactive turns, interactive chat, session
// inspection and resume, built-in tools, plugin tools, OpenAI-compatible auth,
// project context, hooks, compaction, and terminal rendering. It keeps CLI
// wiring separate from the core turn engine so provider, tool, and session
// behavior can remain testable without a terminal.
package main
