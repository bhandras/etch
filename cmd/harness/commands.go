package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	harnessconfig "harness/internal/config"
	"harness/internal/tool"
)

const (
	// defaultSessionDir is the local directory used for JSONL session logs
	// when the caller does not provide one.
	defaultSessionDir = ".harness/sessions"

	// commandRun is the implicit command used when the caller passes -p.
	commandRun = "run"

	// commandSessions lists known local session index entries.
	commandSessions = "sessions"

	// commandShow renders one local session transcript.
	commandShow = "show"

	// commandTool executes a builtin tool directly for smoke testing.
	commandTool = "tool"

	// commandChat starts a minimal interactive line-oriented chat loop.
	commandChat = "chat"

	// commandCompact summarizes older session history into a context event.
	commandCompact = "compact"

	// commandAuth manages local OpenAI OAuth credentials.
	commandAuth = "auth"

	// commandHelp prints top-level or command-specific CLI help.
	commandHelp = "help"

	// providerEcho selects the dependency-free deterministic model client.
	providerEcho = "echo"

	// harnessUserAgent identifies this CLI on provider HTTP requests.
	harnessUserAgent = "harness"
)

// cliConfig stores parsed command-line options for one invocation.
type cliConfig struct {
	command             string
	prompt              string
	sessionDir          string
	jsonOutput          bool
	sessionID           string
	provider            string
	model               string
	baseURL             string
	apiKey              string
	openaiAPI           string
	openaiAPIExplicit   bool
	reasoningEffort     string
	reasoningSummary    string
	baseURLExplicit     bool
	toolName            string
	toolPath            string
	toolCommand         string
	toolContent         string
	toolOldText         string
	toolNewText         string
	toolQuery           string
	toolRawArguments    string
	toolOffset          int
	toolLimit           int
	toolTimeout         int
	toolIgnoreCase      bool
	toolDryRun          bool
	autoCompact         bool
	autoCompactLimit    int
	keepMessages        int
	keepRecentTokens    int
	compactInstructions string
	maxToolRounds       int
	authAction          string
	authPath            string
	authIssuer          string
	authClientID        string
	authCodexBaseURL    string
	hooks               []harnessconfig.HookConfig
	plugins             []harnessconfig.PluginConfig
}

// main runs the command and exits with the returned status code.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses flags, executes one command, and renders the result.
func run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		printTopLevelHelp(stdout)

		return 0
	}
	if isHelpArg(args[0]) {
		printTopLevelHelp(stdout)

		return 0
	}
	if args[0] == commandHelp {
		return runHelp(args[1:], stdout, stderr)
	}

	cfg, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "error:", err)

		return 2
	}

	switch cfg.command {
	case commandRun:
		return runPrompt(cfg, stdout, stderr)

	case commandSessions:
		return listSessions(cfg, stdout, stderr)

	case commandShow:
		return showSession(cfg, stdout, stderr)

	case commandTool:
		return runTool(cfg, stdout, stderr)

	case commandChat:
		return runChat(cfg, os.Stdin, stdout, stderr)

	case commandCompact:
		return runCompact(cfg, stdout, stderr)

	case commandAuth:
		return runAuth(cfg, stdout, stderr)

	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n", cfg.command)

		return 2
	}
}

// runHelp prints top-level or command-specific help text.
func runHelp(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		printTopLevelHelp(stdout)

		return 0
	}
	if len(args) > 2 {
		fmt.Fprintln(stderr, "error: help accepts at most two words")

		return 2
	}

	err := printCommandHelp(args, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 2
	}

	return 0
}

// printTopLevelHelp writes the command overview for the harness binary.
func printTopLevelHelp(stdout io.Writer) {
	fmt.Fprintln(stdout, "Harness is a minimal Go coding-agent harness.")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Usage:")
	fmt.Fprintln(stdout, `  harness -p "prompt" [flags]`)
	fmt.Fprintln(stdout, "  harness chat [flags]")
	fmt.Fprintln(stdout, "  harness auth <login|status|logout> [flags]")
	fmt.Fprintln(stdout, "  harness tool <name> [flags] [args]")
	fmt.Fprintln(stdout, "  harness sessions [flags]")
	fmt.Fprintln(stdout, "  harness show [flags] <session-id-prefix>")
	fmt.Fprintln(stdout, "  harness compact --session <id> [flags]")
	fmt.Fprintln(stdout, "  harness help [command]")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Commands:")
	fmt.Fprintln(stdout, "  chat      start an interactive chat session")
	fmt.Fprintln(stdout, "  auth      manage OpenAI OAuth credentials")
	fmt.Fprintln(stdout, "  tool      run a builtin tool directly")
	fmt.Fprintln(stdout, "  sessions  list local JSONL sessions")
	fmt.Fprintln(stdout, "  show      render one local session transcript")
	fmt.Fprintln(stdout, "  compact   summarize an existing session")
	fmt.Fprintln(stdout, "  help      show help for a command")
	fmt.Fprintln(stdout)
	fmt.Fprintln(
		stdout, "Use \"harness help <command>\" for command flags.",
	)
}

// printCommandHelp writes generated or custom help for one command.
func printCommandHelp(args []string, stdout io.Writer) error {
	command := args[0]
	switch command {
	case commandRun:
		_, err := parseRunFlags([]string{"-h"}, stdout)

		return ignoreHelpError(err)

	case commandChat:
		_, err := parseChatFlags([]string{"-h"}, stdout)

		return ignoreHelpError(err)

	case commandCompact:
		_, err := parseCompactFlags([]string{"-h"}, stdout)

		return ignoreHelpError(err)

	case commandSessions:
		_, err := parseSessionsFlags([]string{"-h"}, stdout)

		return ignoreHelpError(err)

	case commandShow:
		_, err := parseShowFlags([]string{"-h"}, stdout)

		return ignoreHelpError(err)

	case commandAuth:
		if len(args) == 2 {
			_, err := parseAuthFlags(
				[]string{args[1], "-h"}, stdout,
			)

			return ignoreHelpError(err)
		}
		printAuthHelp(stdout)

		return nil

	case commandTool:
		if len(args) == 2 {
			_, err := parseToolFlags(
				[]string{args[1], "-h"}, stdout,
			)

			return ignoreHelpError(err)
		}
		printToolHelp(stdout)

		return nil

	default:
		return fmt.Errorf("unknown help command %q", command)
	}
}

// printAuthHelp writes a compact auth command overview.
func printAuthHelp(stdout io.Writer) {
	fmt.Fprintln(stdout, "Usage:")
	fmt.Fprintln(stdout, "  harness auth login [flags]")
	fmt.Fprintln(stdout, "  harness auth status [flags]")
	fmt.Fprintln(stdout, "  harness auth logout [flags]")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Use \"harness help auth login\" for auth flags.")
}

// printToolHelp writes a compact direct-tool command overview.
func printToolHelp(stdout io.Writer) {
	fmt.Fprintln(stdout, "Usage:")
	fmt.Fprintln(stdout, "  harness tool <name> [flags] [args]")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Tools:")
	for _, spec := range tool.DefaultRegistry().Specs() {
		fmt.Fprintf(stdout, "  %s\n", spec.Name)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Use \"harness help tool <name>\" for tool flags.")
}

// ignoreHelpError treats flag package help as a successful help rendering.
func ignoreHelpError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}

	return err
}

// isHelpArg reports whether arg requests top-level help.
func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help"
}
