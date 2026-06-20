// Package sdk provides the small public API used by standalone Harness
// plugins.
//
// The package intentionally depends only on the Go standard library. Plugin
// modules can import it to serve the Harness JSONL plugin protocol without
// copying wire structs or request dispatch code.
package sdk
