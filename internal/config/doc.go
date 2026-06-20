// Package config loads the small project-local harness configuration file.
//
// The parser intentionally supports only the TOML subset the harness needs for
// first-party configuration: scalar assignments, normal tables, and hook array
// tables. Keeping this package dependency-free preserves the static-binary
// goal while still giving the CLI a durable config surface.
package config
