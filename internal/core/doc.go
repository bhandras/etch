// Package core coordinates one agent turn across sessions and model clients.
//
// The package is deliberately small in the first executable slice. It admits a
// user prompt, streams an echo model response, and persists both messages to
// the JSONL session log.
package core
