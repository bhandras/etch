// Package hooks runs external process hooks around the agent execution loop.
//
// Hooks are deliberately out-of-process. The harness sends a JSON event to the
// configured command on stdin, waits for optional JSON on stdout, and folds the
// result back into the lifecycle event that allowed it. This keeps the Go core
// dependency-free and language-neutral while still allowing policy, logging,
// and transformation nodes to compose around the agent loop.
package hooks
