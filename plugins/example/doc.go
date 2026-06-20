// Package main implements a small standalone Harness plugin example.
//
// The plugin intentionally imports only the Go standard library. It speaks the
// Harness JSONL plugin protocol over stdin and stdout so it can be developed,
// built, and versioned independently from the core harness module.
package main
