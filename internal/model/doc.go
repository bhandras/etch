// Package model defines the provider-neutral stream interface used by the core.
//
// The first implementation is intentionally an echo client. It lets the agent
// exercise turn handling and session persistence without network access,
// authentication, or provider-specific behavior.
package model
