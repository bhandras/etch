// Package main implements a standalone etch plugin for Go source-code
// intelligence.
//
// The plugin intentionally lives outside the harness core. It demonstrates how
// language-aware tools can use only the Go standard library plus etch/sdk to
// expose richer project inspection without adding parser dependencies to the
// agent kernel.
package main
