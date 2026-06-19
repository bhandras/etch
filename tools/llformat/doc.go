// Package llformat pins the source formatter used by repository Makefile
// targets.
//
// Runtime agent code must not import this package. It exists so Go's module
// system records the exact formatter version separately from in-repository
// command tools.
package llformat
