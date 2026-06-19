// Package session stores agent runs as append-only JSONL event streams.
//
// The package keeps the durable representation intentionally plain. Each
// session is a single JSONL file, each line is one event, and parent IDs form
// the chain that later branching and replay features can build on.
package session
