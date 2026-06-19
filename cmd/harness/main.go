package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"harness/internal/core"
	"harness/internal/model"
)

const (
	// defaultSessionDir is the local directory used for JSONL session logs
	// when the caller does not provide one.
	defaultSessionDir = ".harness/sessions"
)

// cliConfig stores parsed command-line options for one invocation.
type cliConfig struct {
	prompt     string
	sessionDir string
	jsonOutput bool
}

// main runs the command and exits with the returned status code.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses flags, executes one agent turn, and renders the result.
func run(args []string, stdout *os.File, stderr *os.File) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "error: get working directory:", err)

		return 1
	}

	result, err := core.RunTurn(context.Background(), core.TurnRequest{
		Prompt:     cfg.prompt,
		SessionDir: cfg.sessionDir,
		CWD:        cwd,
		Model:      model.EchoClient{},
	})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	if cfg.jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, "error: encode json output:", err)

			return 1
		}

		return 0
	}

	fmt.Fprintf(stdout, "assistant: %s\n", result.AssistantText)

	return 0
}

// parseFlags converts CLI arguments into the command configuration.
func parseFlags(args []string, stderr *os.File) (cliConfig, error) {
	var cfg cliConfig
	fs := flag.NewFlagSet("harness", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.prompt, "p", "", "prompt to run non-interactively")
	fs.StringVar(
		&cfg.sessionDir, "session-dir", defaultSessionDir,
		"session log directory",
	)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false, "print the turn result as JSON",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.prompt == "" {
		return cliConfig{}, fmt.Errorf("provide a prompt with -p")
	}

	return cfg, nil
}
