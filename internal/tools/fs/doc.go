// Package fs implements dependency-free read-only filesystem operations.
//
// The package mirrors the shape of agent-friendly tools such as Pi's ls, find,
// and grep while keeping the core independent from external binaries. Each
// operation is bounded, deterministic, and suitable for later wrapping in the
// harness tool registry.
package fs
