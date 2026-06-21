package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"harness/internal/core"
	"harness/internal/hooks"
	"harness/internal/session"
)

// runCompact appends a model-written summary to an existing session.
func runCompact(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	entry, err := session.Resolve(cfg.sessionDir, cfg.sessionID)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	modelClient, err := modelClient(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "error: get working directory:", err)

		return 1
	}
	hookRunner, err := hooks.New(cfg.hooks, cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	result, err := core.CompactSession(context.Background(),
		core.CompactRequest{
			SessionPath:      entry.Path,
			Model:            modelClient,
			KeepMessages:     cfg.keepMessages,
			KeepRecentTokens: cfg.keepRecentTokens,
			ModelName:        cfg.model,
			Instructions:     cfg.compactInstructions,
			Hooks:            hookRunner,
		})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	fmt.Fprintf(
		stdout, "compacted session %s\nsummary event: %s\n",
		shortID(entry.ID), result.SummaryEventID,
	)

	return 0
}
